package skills

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/akins/jarvis/internal/browser"
	"github.com/akins/jarvis/internal/llm"
)

// Skills that read where the user has been.
//
// # Why this changes what the browser skills can do
//
// Everything else in this package needs a URL. Getting one meant a web search,
// which answers "what is a school portal" rather than "what is *my* school
// portal". History answers the second question, and it is nearly always the one
// being asked. "Open my portal", "that recipe I looked at", "the tracking page
// for my order" — all of these are lookups into somewhere already visited.
//
// # On privacy
//
// This reads the user's full browsing history from their own machine, at their
// own request. Two things follow. First, it is read-only by construction: the
// SQLite layer underneath cannot write. Second, results are never volunteered
// — a skill runs when it is called, and history should be summarised into an
// answer rather than recited, because the user asked where their portal is, not
// for a list of everywhere they have been.

// historyCache avoids re-reading and re-parsing an 8MB file on every call
// within a conversation.
//
// Guarded: tool calls inside one round run concurrently, so two history skills
// requested together read and write this at the same time. It was a real data
// race, not a theoretical one — the fix is a mutex rather than an atomic because
// the slice and its timestamp have to move as a pair.
var historyCache struct {
	mu     sync.Mutex
	visits []browser.Visit
	loaded time.Time
}

// historyTTL is how long a loaded snapshot stays fresh.
//
// Long enough that a burst of related questions costs one read; short enough
// that a page visited during the conversation shows up if asked about later.
const historyTTL = 2 * time.Minute

func loadVisits() ([]browser.Visit, error) {
	historyCache.mu.Lock()
	defer historyCache.mu.Unlock()

	if time.Since(historyCache.loaded) < historyTTL && historyCache.visits != nil {
		return historyCache.visits, nil
	}
	// The read happens under the lock on purpose. Two concurrent callers finding
	// the cache cold would otherwise both parse the 8MB file; holding it means the
	// second waits and then gets the first one's result.
	visits, err := browser.LoadHistory("")
	if err != nil {
		return nil, err
	}
	historyCache.visits, historyCache.loaded = visits, time.Now()
	return visits, nil
}

// RegisterBrowserHistory adds the history, bookmark and download skills.
func RegisterBrowserHistory(r *Registry) {
	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_history",
			Description: "Search the user's real browsing history by description to find a " +
				"site they have visited before. Use this BEFORE searching the web when the " +
				"user refers to a site as theirs — 'my school portal', 'that bank site', " +
				"'the course page' — because they mean a specific place they already go. " +
				"Results are ranked by how often and how recently they visit.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"query": {Type: "string", Description: "What to look for, e.g. 'school portal' or 'bank'."},
				"limit": {Type: "integer", Description: "How many results (default 8)."},
			}, "query"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			visits, err := loadVisits()
			if err != nil {
				return "", err
			}
			limit := argInt(args, "limit", 8)
			hits := browser.SearchHistory(visits, argString(args, "query"), limit)
			if len(hits) == 0 {
				return fmt.Sprintf("Nothing in %d pages of history matches %q. "+
					"Worth asking the user for the address, or searching the web.",
					len(visits), argString(args, "query")), nil
			}
			return browser.FormatVisits(hits), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_recent",
			Description: "List pages the user visited recently. Useful for 'what was I just " +
				"looking at' or picking up something they were part-way through.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"hours": {Type: "integer", Description: "How far back to look (default 24)."},
				"limit": {Type: "integer", Description: "How many results (default 15)."},
			}),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			visits, err := loadVisits()
			if err != nil {
				return "", err
			}
			hours := argInt(args, "hours", 24)
			hits := browser.RecentHistory(visits, time.Duration(hours)*time.Hour, argInt(args, "limit", 15))
			if len(hits) == 0 {
				return fmt.Sprintf("Nothing visited in the last %d hours.", hours), nil
			}
			return browser.FormatVisits(hits), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_top_sites",
			Description: "The sites the user visits most. Good for understanding their habits, " +
				"or finding the right site when a description is vague.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"limit": {Type: "integer", Description: "How many sites (default 20)."},
			}),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			visits, err := loadVisits()
			if err != nil {
				return "", err
			}
			top := browser.TopSites(visits, argInt(args, "limit", 20))
			var b strings.Builder
			for i, v := range top {
				fmt.Fprintf(&b, "%2d. %-40s %d visits\n", i+1, v.Host(), v.VisitCount)
			}
			if b.Len() == 0 {
				return "No history to summarise.", nil
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_bookmarks",
			Description: "Search the user's saved bookmarks. A bookmark is a stronger signal " +
				"than a visit — they chose to keep it — so check here first when looking " +
				"for somewhere they consider important.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"query": {Type: "string", Description: "What to look for. Omit to list everything."},
				"limit": {Type: "integer", Description: "How many results (default 20)."},
			}),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			marks, err := browser.LoadBookmarks()
			if err != nil {
				return "", err
			}
			query := argString(args, "query")
			limit := argInt(args, "limit", 20)
			if query != "" {
				marks = browser.SearchBookmarks(marks, query, limit)
			} else if len(marks) > limit {
				marks = marks[:limit]
			}
			if len(marks) == 0 {
				return "No matching bookmarks.", nil
			}
			var b strings.Builder
			for _, m := range marks {
				fmt.Fprintf(&b, "%s\n  %s\n  in %s\n", m.Name, m.URL, m.Folder)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_downloads",
			Description: "List files the user has downloaded through the browser, most recent " +
				"first, with where each was saved. Useful for finding a file they mention " +
				"having downloaded.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"limit": {Type: "integer", Description: "How many (default 20)."},
			}),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			downloads, err := browser.LoadDownloads("")
			if err != nil {
				return "", err
			}
			limit := argInt(args, "limit", 20)
			if len(downloads) > limit {
				downloads = downloads[:limit]
			}
			if len(downloads) == 0 {
				return "No downloads recorded.", nil
			}
			var b strings.Builder
			for _, d := range downloads {
				state := "incomplete"
				if d.Done {
					state = "complete"
				}
				fmt.Fprintf(&b, "%s\n  %s, %s, %s\n", d.Path,
					humanBytes(d.Bytes), state, d.StartedAt.Format("2 Jan 2006 15:04"))
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_find_site",
			Description: "Resolve a description of a site to a single best URL, checking " +
				"bookmarks then history. Use this to turn 'my school portal' into an " +
				"address that browser_open can take.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"query": {Type: "string", Description: "Description of the site."},
			}, "query"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			v, err := browser.Resolve(argString(args, "query"))
			if err != nil {
				return "", err
			}
			if v.VisitCount > 0 {
				return fmt.Sprintf("%s\n%s\n(visited %d times)", v.Title, v.URL, v.VisitCount), nil
			}
			return fmt.Sprintf("%s\n%s\n(from bookmarks)", v.Title, v.URL), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_accounts",
			Description: "List the usernames the user has saved in Chrome for a site — NOT the " +
				"passwords, which are never read. Check this BEFORE signing in: if a site has " +
				"more than one saved account, ask the user which to use rather than picking " +
				"one. If it has exactly one, sign in with it. Passwords are filled by Chrome " +
				"itself when you choose a login; you only ever need the username to know " +
				"whether there is a choice to make.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"site": {Type: "string", Description: "The site or domain, e.g. 'learn.uopeople.com' or a full URL."},
			}, "site"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			logins, err := browser.LoadLogins("")
			if err != nil {
				return "", err
			}
			site := argString(args, "site")
			return browser.FormatAccounts(site, browser.AccountsFor(logins, site)), nil
		},
	})
}
