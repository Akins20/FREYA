package skills

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/browser"
	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/llm"
	"github.com/Akins20/FREYA/internal/routes"
)

// Knowing where the user's things are.
//
// # Why this is not a Gmail tool
//
// The obvious way to make "check my email" work is to write an email tool, and
// then a calendar tool, and then one per chat application, and to keep all of
// them alive against APIs that change. It is a lot of work that ends with a
// fixed list, and a user whose provider is not on it gets nothing.
//
// She does not need integrations. She needs one fact per service and a memory
// for it: this is where your mail is, this address works, I checked. Everything
// after that is the browser, which already drives any site well and carries the
// user's real session.
//
// # The discovery is evidence, not a default
//
// service_find reads the user's own browsing. Which mail host they open every
// morning is a fact about them, sitting in their history with a visit count on
// it, and it beats any default. The candidates are ranked by how much they
// actually use the site, the recognised-host table only labels what history has
// already surfaced, and nothing is ever chosen because it is popular in general.
//
// So the answer to "which email do I use" is derived from evidence and then
// confirmed by going there, rather than guessed and asserted.
//
// # A remembered address is a claim with a date on it
//
// Sites move. An address that quietly fails is worse than not knowing, because
// it turns "I could not reach your mail" into "your mail is empty". So every
// answer carries how old the knowledge is, using a route is expected to be
// followed by saying whether it landed, and two failures in a row make it stale
// rather than trusted forever.
//
// # No credentials, at any point
//
// A route is an address. Signing in is the browser's auth context, which carries
// the session the user already has. Nothing here reads, stores or types a
// password, and there is deliberately no field it could be put in.
func RegisterServices(r *Registry, g *guard.Guard, tabs *Tabs, store *routes.Store) {
	if store == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "service_where",
			Description: "Where the user's email, calendar, messages or any other service " +
				"lives, if she has learned it.\n\n" +
				"ASK THIS FIRST whenever a request names one of their things — 'check my " +
				"email', 'what's on my calendar', 'message them' — before searching or " +
				"guessing an address. One call replaces a search and several wrong turns.\n\n" +
				"It returns an address to open with browser_open in the 'auth' context, " +
				"where they are already signed in. It also says how old the knowledge is: " +
				"say so if it is stale, and check the page you land on is the one it " +
				"promised, because sites move and a remembered address that quietly fails " +
				"looks exactly like an empty inbox.\n\n" +
				"If she has not learned it, use service_find.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"service": {Type: "string", Description: "email, calendar, messages, or " +
					"whatever the user calls it."},
				"capability": {Type: "string", Description: "Optional, a named place inside " +
					"it such as compose or today. Omit for the way in."},
			}, "service"),
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			service := strings.TrimSpace(argString(args, "service"))
			if service == "" {
				return "", fmt.Errorf("which service?")
			}
			route, ok := store.Get(service)
			if !ok {
				known := store.List()
				if len(known) == 0 {
					return "", fmt.Errorf("she has not learned where %q is, and knows no "+
						"services yet. service_find works it out from the user's own "+
						"browsing", service)
				}
				var names []string
				for _, k := range known {
					names = append(names, k.Service)
				}
				return "", fmt.Errorf("she has not learned where %q is. She does know: %s. "+
					"service_find works out a new one from the user's own browsing",
					service, strings.Join(names, ", "))
			}

			capability := argString(args, "capability")
			url, has := route.URL(capability)
			if !has {
				var named []string
				for c := range route.Entries {
					if c != "" {
						named = append(named, c)
					}
				}
				sort.Strings(named)
				if len(named) == 0 {
					return "", fmt.Errorf("%q is known but has no address recorded, which "+
						"should not happen; relearn it with service_learn", service)
				}
				return "", fmt.Errorf("%q has no address for %q. It has: %s",
					service, capability, strings.Join(named, ", "))
			}

			note := ""
			if route.Stale() {
				note = fmt.Sprintf(" This route has failed %d times in a row, so treat it as "+
					"a guess: if it does not land where it should, use service_find and "+
					"service_learn rather than trying it again.", route.Fails)
			}
			return fmt.Sprintf("%s: %s\n\nOpen it with browser_open in the 'auth' context. "+
				"Known from %s, %s.%s\n\n[Check the page you land on is really %s, and say "+
				"so if it is not. A remembered address that has rotted looks like an empty "+
				"inbox rather than an error.]",
				service, url, route.Found, route.Age(time.Now()), note, route.Host), nil
		},
	})

	// One call rather than two, and the verdict comes from the page rather than
	// from her. See the doc on the handler.
	if g != nil && tabs != nil {
		registerServiceOpen(r, g, tabs, store)
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "service_find",
			Description: "Work out which site the user actually uses for something, from " +
				"their own browsing history, bookmarks and saved sign-ins.\n\n" +
				"Use it the first time a service comes up and she does not know it, or " +
				"when a known one has gone stale. Kinds she can recognise: email, " +
				"calendar, messages, files, code. Any other word searches their history " +
				"for that term instead.\n\n" +
				"It ranks candidates by how much they actually go there, so the answer is " +
				"about this person rather than about what is popular. Confirm by opening " +
				"the top candidate, then record it with service_learn so it is one step " +
				"from then on.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"kind": {Type: "string", Description: "email, calendar, messages, files, " +
					"code, or a word to look for."},
			}, "kind"),
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			kind := strings.ToLower(strings.TrimSpace(argString(args, "kind")))
			if kind == "" {
				return "", fmt.Errorf("what kind of service? %s, or a word to look for",
					strings.Join(routes.KindNames(), ", "))
			}

			path, err := browser.HistoryFile()
			if err != nil {
				return "", fmt.Errorf("their browsing history is where this is worked out "+
					"from and it could not be found: %w. Ask them which they use, then "+
					"record it with service_learn", err)
			}
			visits, err := browser.LoadHistory(path)
			if err != nil {
				return "", fmt.Errorf("their history could not be read: %w", err)
			}
			// By host, because the question is which site rather than which page.
			sites := browser.TopSites(visits, 400)

			type candidate struct {
				host  string
				url   string
				title string
				count int
			}
			var found []candidate
			for _, v := range sites {
				host := v.Host()
				if host == "" {
					continue
				}
				match := false
				if hosts, ok := routes.Kinds[kind]; ok {
					for _, want := range hosts {
						if strings.Contains(strings.ToLower(v.URL), want) {
							match = true
							break
						}
					}
				} else {
					// An unrecognised kind is treated as a word to look for, which is
					// how a university portal or a self-hosted thing gets found.
					match = strings.Contains(strings.ToLower(host), kind) ||
						strings.Contains(strings.ToLower(v.Title), kind)
				}
				if match {
					found = append(found, candidate{host, v.URL, v.Title, v.VisitCount})
				}
			}
			sort.SliceStable(found, func(i, j int) bool { return found[i].count > found[j].count })

			if len(found) == 0 {
				if _, known := routes.Kinds[kind]; known {
					return "", fmt.Errorf("nothing in their browsing history looks like %s. "+
						"Either they use something she does not recognise, or they do not "+
						"use it in this browser. Ask them, then record it with service_learn",
						kind)
				}
				return "", fmt.Errorf("nothing in their browsing history matches %q. Ask "+
					"them, then record it with service_learn", kind)
			}

			var b strings.Builder
			fmt.Fprintf(&b, "Candidates for %s, by how much they actually go there:\n", kind)
			for i, c := range found {
				if i >= 5 {
					fmt.Fprintf(&b, "  (%d more, less used)\n", len(found)-5)
					break
				}
				title := c.title
				if title == "" {
					title = "(no title recorded)"
				}
				fmt.Fprintf(&b, "  %-32s %4d visits  %s\n", c.host, c.count, clip(title, 48))
			}
			b.WriteString("\n[This is their history, not a recommendation. Open the top one " +
				"in the 'auth' context to confirm it is theirs and signed in, then " +
				"service_learn it so this is one step next time.]")
			return b.String(), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "service_learn",
			Description: "Remember where one of the user's services lives, so it is one " +
				"step from now on.\n\n" +
				"Record it once you have OPENED the address and seen that it is really " +
				"theirs. Recording a guess is worse than recording nothing, because a " +
				"wrong address is then asserted with confidence.\n\n" +
				"Use 'capability' to remember a place inside a service you had to work " +
				"out: where compose is, where today's agenda is, which tab their course " +
				"sits behind. That is the part worth keeping, because it is the part that " +
				"took several steps to find.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"service": {Type: "string", Description: "What the user calls it: email, " +
					"calendar, messages, or their own name for it."},
				"url": {Type: "string", Description: "The address that worked."},
				"capability": {Type: "string", Description: "Optional. A named place inside " +
					"the service, such as compose or today. Omit for the way in."},
			}, "service", "url"),
		},
		Mutates: true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			service := strings.TrimSpace(argString(args, "service"))
			url := strings.TrimSpace(argString(args, "url"))
			if service == "" || url == "" {
				return "", fmt.Errorf("both a service name and the address that worked")
			}
			// The same check browser_open makes, so an address she never actually
			// loaded cannot be written down as one that works.
			if _, err := CheckURL(ctx, url); err != nil {
				return "", err
			}
			host := routes.HostOf(url)
			if host == "" {
				return "", fmt.Errorf("%q has no host in it, so there is nothing to check a "+
					"page against later", url)
			}

			capability := strings.ToLower(strings.TrimSpace(argString(args, "capability")))

			// What is recorded here is how the address was arrived at, and it can
			// only claim what this tool can see: that the host is one it
			// recognises for this kind of service. Whether anybody opened it is
			// not knowable from here, and the first version of this line said
			// "confirmed by opening it" on the strength of a string match, which
			// is an assertion about an event that may never have happened.
			found := "recorded from what the user or the page said"
			if k, ok := routes.KindOf(url); ok && k == strings.ToLower(service) {
				found = "a recognised " + k + " host"
			}

			existing, had := store.Get(service)
			route := routes.Route{
				Service: service,
				Host:    host,
				Entries: map[string]string{capability: url},
				Found:   found,
				// LastOK is deliberately not set. Learning an address is not the
				// same as having used it, and stamping it here would make every
				// brand-new route claim to have worked moments ago. service_used
				// is what turns it into a route with a success behind it, and
				// until then the age reads "learned", which is the truth.
			}
			if had {
				route.Found = existing.Found
			}
			if err := store.Put(route); err != nil {
				return "", err
			}

			where := "the way in"
			if capability != "" {
				where = fmt.Sprintf("%q", capability)
			}
			verb := "Learned"
			if had {
				verb = "Updated"
			}
			// Recorded rather than gated, exactly as skill_learn is. This writes into
			// her own store, so Run would ask a permission nobody can give in the
			// daemon — but where the user's services live is persistent state they
			// cannot see change anywhere else. See Guard.Note.
			g.Note(guard.Action{Kind: guard.KindWrite,
				Command: "learn service " + service, Reason: url}, "ok", nil)
			return fmt.Sprintf("%s %s: %s is %s. service_where finds it from now on.",
				verb, service, where, url), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "service_used",
			Description: "Report whether a remembered address actually led where it said.\n\n" +
				"Call it after opening one. A route that keeps failing goes stale and stops " +
				"being asserted, which is the whole reason the memory is safe to trust: it " +
				"knows when it is wrong.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"service": {Type: "string", Description: "Which service."},
				"worked": {Type: "boolean", Description: "True if the page was really that " +
					"service, false if it was a sign-in wall she could not pass, an error, " +
					"or something else entirely."},
			}, "service", "worked"),
		},
		Mutates: true,
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			// Deliberately not recorded, unlike learn and forget. This is a
			// high-frequency feedback signal that only ever marks a route stale — it
			// creates nothing and destroys nothing — so an entry per call would bury
			// the two that matter in an audit log the user reads by eye.
			service := strings.TrimSpace(argString(args, "service"))
			if service == "" {
				return "", fmt.Errorf("which service?")
			}
			if argBool(args, "worked") {
				if err := store.Worked(service); err != nil {
					return "", err
				}
				return fmt.Sprintf("Noted: the route to %s still works.", service), nil
			}
			fails, err := store.Failed(service)
			if err != nil {
				return "", err
			}
			if fails >= 2 {
				return fmt.Sprintf("Noted: %s has now failed %d times in a row, so it is "+
					"marked stale and will be offered as a guess rather than as fact. Use "+
					"service_find to work out the current address.", service, fails), nil
			}
			return fmt.Sprintf("Noted: %s did not work this time (%d in a row). One failure "+
				"is as likely to be a dropped connection as a moved site, so it is still "+
				"trusted for now.", service, fails), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "service_list",
			Description: "Everything she has learned about where the user's things live, " +
				"and how current each one is.",
			Params: llm.ObjectSchema(nil),
		},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			known := store.List()
			if len(known) == 0 {
				return "She has not learned where anything lives yet. service_find works " +
					"one out from the user's own browsing.", nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%d service(s) she knows how to reach:\n", len(known))
			now := time.Now()
			for _, r := range known {
				mark := " "
				if r.Stale() {
					mark = "!"
				}
				url, _ := r.URL("")
				fmt.Fprintf(&b, "%s %-12s %-40s %s\n", mark, r.Service, clip(url, 40), r.Age(now))
				var extra []string
				for c, u := range r.Entries {
					if c != "" {
						extra = append(extra, fmt.Sprintf("%s: %s", c, u))
					}
				}
				sort.Strings(extra)
				for _, e := range extra {
					fmt.Fprintf(&b, "               %s\n", clip(e, 68))
				}
			}
			if strings.Contains(b.String(), "\n! ") {
				b.WriteString("\n! marks a route that has failed twice and is no longer " +
					"asserted as fact.")
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "service_forget",
			Description: "Forget where a service lives, when the user has moved to a " +
				"different provider or she learned the wrong thing.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"service": {Type: "string", Description: "Which service to forget."},
			}, "service"),
		},
		Mutates: true,
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			service := strings.TrimSpace(argString(args, "service"))
			if service == "" {
				return "", fmt.Errorf("which service?")
			}
			had, err := store.Forget(service)
			if err != nil {
				return "", err
			}
			if !had {
				return "", fmt.Errorf("she did not know where %q was, so there is nothing "+
					"to forget", service)
			}
			// Forgetting is the one of the three that destroys something, which makes
			// it the one most worth having in the record.
			g.Note(guard.Action{Kind: guard.KindWrite,
				Command: "forget service " + service}, "ok", nil)
			return fmt.Sprintf("Forgotten where %s lives.", service), nil
		},
	})
}

// registerServiceOpen is the fast path: resolve a remembered address and go
// there, in one call.
//
// # Why this exists when service_where already returns the address
//
// service_where hands back an address for browser_open to load, which costs a
// round every time she checks the mail. That is the wrong shape for the thing
// she does most often, and the fix was not to duplicate the tab bookkeeping here
// but to factor it out of browser_open, where it now lives as Tabs.OpenAt.
//
// # The verdict is taken from the page, not from her
//
// service_used exists so a route that has rotted stops being asserted, and it
// depends on somebody remembering to call it. Here nobody has to: the address
// was opened by this tool, so this tool can compare where it landed against the
// host it promised and record the answer itself. That closes the loop that made
// the memory trustworthy without asking the model to close it.
//
// A redirect to a sign-in page on the same host counts as landing: it is the
// right place, asking for a session. A redirect somewhere else does not.
func registerServiceOpen(r *Registry, g *guard.Guard, tabs *Tabs, store *routes.Store) {
	r.Register(Skill{
		Tool: llm.Tool{
			Name: "service_open",
			Description: "Open one of the user's services and land on it, in one call: " +
				"their email, calendar, messages, or anything she has learned.\n\n" +
				"This is the tool for 'check my email' and 'what's on my calendar'. It " +
				"looks up the address she learned, opens it in the browser context that " +
				"carries their real session, checks the page is really that service, and " +
				"records whether the route still works. Read it afterwards with " +
				"browser_read.\n\n" +
				"Use service_find first if she has not learned the service yet.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"service": {Type: "string", Description: "email, calendar, messages, or " +
					"whatever the user calls it."},
				"capability": {Type: "string", Description: "Optional, a named place inside " +
					"it such as compose or today. Omit for the way in."},
				"tab": {Type: "string", Description: "What to call the tab. Defaults to the " +
					"service name."},
			}, "service"),
		},
		Mutates: true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			service := strings.TrimSpace(argString(args, "service"))
			if service == "" {
				return "", fmt.Errorf("which service?")
			}
			route, ok := store.Get(service)
			if !ok {
				return "", fmt.Errorf("she has not learned where %q is. service_find works "+
					"it out from the user's own browsing, then service_learn keeps it", service)
			}
			url, has := route.URL(argString(args, "capability"))
			if !has {
				return "", fmt.Errorf("%q is known but has no address for that", service)
			}
			tab := strings.TrimSpace(argString(args, "tab"))
			if tab == "" {
				tab = service
			}

			action := guard.Action{
				Kind:    guard.KindBrowser,
				Command: "open " + url,
				Reason: fmt.Sprintf("open the user's %s at %s, signed in as them "+
					"(auth context, real cookies)", service, url),
			}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				title, landed, err := tabs.OpenAt(ctx, tab, browser.ContextAuth, url)
				if err != nil {
					// A route that would not open is exactly what the failure count is
					// for, so it is recorded here rather than left to be reported.
					if n, ferr := store.Failed(service); ferr == nil && n >= 2 {
						return "", fmt.Errorf("%w — and that is %d failures in a row for %s, "+
							"so the address is now treated as a guess. service_find will "+
							"work out the current one", err, n, service)
					}
					return "", err
				}

				// Landing is judged on the host. A redirect to a sign-in page on the
				// same site is the right place asking for a session; a redirect
				// somewhere else entirely is a route that has moved.
				got := routes.HostOf(landed)
				if got != route.Host && !strings.HasSuffix(got, "."+route.Host) {
					n, _ := store.Failed(service)
					stale := ""
					if n >= 2 {
						stale = " That is two in a row, so the address is now treated as a " +
							"guess rather than fact."
					}
					return fmt.Sprintf("Opened %s and it did NOT land on %s: the tab is on %s "+
						"(%q). The remembered address has probably moved.%s\n\nUse "+
						"service_find to work out the current one, then service_learn it.",
						service, route.Host, got, title, stale), nil
				}

				_ = store.Worked(service)
				return fmt.Sprintf("Tab %q is open on the user's %s: %q\n%s\n\n"+
					"[Landed on %s as expected, so the route is confirmed working. Read it "+
					"with browser_read.]", tab, service, title, landed, got), nil
			})
		},
	})
}
