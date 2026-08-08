package browser

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Reading what the user has actually visited.
//
// # Why this matters more than it looks
//
// Without it, "open my school portal" is a web search, and a web search for a
// portal returns the wrong institution's login page. With it, the request is a
// lookup: the portal is somewhere you have been forty times, and the right
// answer is the one you keep going back to.
//
// The ranking below is built around that idea. A site visited repeatedly over
// months is a place in your life; a site visited once is a page you read. When
// you refer to somewhere by description, you almost always mean the former.
//
// # Which profile
//
// Freya drives Chrome through her own profile, because Chrome refuses a
// debugging port on the one you use day to day. But her profile has no history
// worth the name — yours does. So this reads the real browser's files, not
// hers, and treats them as strictly read-only.

// chromeEpochOffset converts Chrome's timestamps to Unix.
//
// Chrome counts microseconds from 1 January 1601, an epoch inherited from
// Windows FILETIME. This is the number of seconds between then and 1970.
const chromeEpochOffset = 11644473600

// chromeTime converts a Chrome timestamp to a Go time.
func chromeTime(micros int64) time.Time {
	if micros <= 0 {
		return time.Time{}
	}
	sec := micros/1_000_000 - chromeEpochOffset
	nsec := (micros % 1_000_000) * 1000
	return time.Unix(sec, nsec)
}

// Visit is one page in the history.
type Visit struct {
	URL        string
	Title      string
	VisitCount int
	TypedCount int
	LastVisit  time.Time
	// Score is the ranking used to order search results.
	Score float64
}

// Host returns the site the visit belongs to.
func (v Visit) Host() string {
	s := v.URL
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimPrefix(s, "www.")
}

// browserProfiles are the places a Chromium-family browser keeps its data,
// in the order they are worth trying.
var browserProfiles = []string{
	".config/google-chrome",
	".config/chromium",
	".config/BraveSoftware/Brave-Browser",
	".config/microsoft-edge",
	".config/vivaldi",
	"snap/chromium/common/chromium",
	".var/app/com.google.Chrome/config/google-chrome",
}

// HistoryFile locates the user's real browser history.
//
// When several browsers are installed the one with the most recently modified
// history wins, which is a decent proxy for "the one they actually use".
func HistoryFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	var best string
	var bestAt time.Time

	for _, base := range browserProfiles {
		root := filepath.Join(home, base)
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			// Chrome names profiles "Default", "Profile 1", "Profile 2"…
			name := e.Name()
			if name != "Default" && !strings.HasPrefix(name, "Profile ") {
				continue
			}
			path := filepath.Join(root, name, "History")
			info, err := os.Stat(path)
			if err != nil || info.Size() == 0 {
				continue
			}
			if info.ModTime().After(bestAt) {
				best, bestAt = path, info.ModTime()
			}
		}
	}

	if best == "" {
		return "", fmt.Errorf("no browser history found — looked in %s", strings.Join(browserProfiles, ", "))
	}
	return best, nil
}

// LoadHistory reads every visit from a history database.
func LoadHistory(path string) ([]Visit, error) {
	if path == "" {
		var err error
		if path, err = HistoryFile(); err != nil {
			return nil, err
		}
	}

	d, err := openSnapshot(path)
	if err != nil {
		return nil, err
	}
	rows, err := d.queryTable("urls")
	if err != nil {
		return nil, err
	}

	visits := make([]Visit, 0, len(rows))
	for _, r := range rows {
		// Chrome hides some rows from its own UI; respect that.
		if asInt(r["hidden"]) != 0 {
			continue
		}
		url := asText(r["url"])
		if url == "" {
			continue
		}
		visits = append(visits, Visit{
			URL:        url,
			Title:      asText(r["title"]),
			VisitCount: int(asInt(r["visit_count"])),
			TypedCount: int(asInt(r["typed_count"])),
			LastVisit:  chromeTime(asInt(r["last_visit_time"])),
		})
	}
	return visits, nil
}

// SearchHistory ranks visits against a description.
//
// # How the ranking works
//
// Three signals, multiplied rather than added so that a miss on any one of them
// cannot be compensated for by the others:
//
//   - Match quality. A word in the title counts for more than a word in the
//     URL, and a match in the host counts for most of all — "gradebook" in the
//     domain is a stronger claim than "gradebook" buried in a query string.
//   - Habit. Visit count, dampened logarithmically: the fortieth visit says
//     less than the fourth did.
//   - Recency. A site from last week beats one from two years ago, with a long
//     half-life so that a summer break does not erase a school portal.
//
// Typed count gets its own bonus. Typing an address by hand rather than
// following a link is the strongest available signal that a site is somewhere
// you go deliberately.
func SearchHistory(visits []Visit, query string, limit int) []Visit {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return nil
	}
	now := time.Now()

	var out []Visit
	for _, v := range visits {
		title := strings.ToLower(v.Title)
		url := strings.ToLower(v.URL)
		host := strings.ToLower(v.Host())

		var match float64
		matched := 0
		for _, term := range terms {
			var best float64
			switch {
			case strings.Contains(host, term):
				best = 3.0
			case strings.Contains(title, term):
				best = 2.0
			case strings.Contains(url, term):
				best = 1.0
			}
			if best > 0 {
				matched++
				match += best
			}
		}
		if matched == 0 {
			continue
		}
		// Requiring every term would fail on "my school portal", where "my" is
		// filler; rewarding completeness without demanding it works better.
		match *= 1 + float64(matched-1)/float64(len(terms))

		habit := 1.0 + logish(float64(v.VisitCount))
		typed := 1.0 + 0.5*logish(float64(v.TypedCount))

		recency := 0.25
		if !v.LastVisit.IsZero() {
			days := now.Sub(v.LastVisit).Hours() / 24
			if days < 0 {
				days = 0
			}
			// Half-life of ninety days, floored so old-but-frequent survives.
			recency = 0.25 + 0.75*math.Pow(0.5, days/90)
		}

		v.Score = match * habit * typed * recency
		out = append(out, v)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	out = dedupeByURL(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// dedupeByURL collapses the same page reached with different query strings.
//
// History is full of near-duplicates — the same portal with a different session
// token each time. Showing ten of them is showing one result badly.
func dedupeByURL(in []Visit) []Visit {
	seen := map[string]bool{}
	var out []Visit
	for _, v := range in {
		key := v.Host() + pathOf(v.URL)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
	}
	return out
}

func pathOf(url string) string {
	s := url
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	i := strings.Index(s, "/")
	if i < 0 {
		return "/"
	}
	s = s[i:]
	if j := strings.IndexAny(s, "?#"); j >= 0 {
		s = s[:j]
	}
	return s
}

// TopSites ranks the places the user actually spends time, by host.
func TopSites(visits []Visit, limit int) []Visit {
	byHost := map[string]*Visit{}
	for i := range visits {
		v := visits[i]
		h := v.Host()
		if h == "" {
			continue
		}
		if cur, ok := byHost[h]; ok {
			cur.VisitCount += v.VisitCount
			cur.TypedCount += v.TypedCount
			if v.LastVisit.After(cur.LastVisit) {
				cur.LastVisit = v.LastVisit
				// Keep the title of the most recent page, which is usually more
				// descriptive than whichever one happened to be first.
				if v.Title != "" {
					cur.Title = v.Title
				}
			}
			continue
		}
		copied := v
		copied.URL = "https://" + h
		byHost[h] = &copied
	}

	out := make([]Visit, 0, len(byHost))
	for _, v := range byHost {
		out = append(out, *v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].VisitCount != out[j].VisitCount {
			return out[i].VisitCount > out[j].VisitCount
		}
		return out[i].LastVisit.After(out[j].LastVisit)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// RecentHistory returns the most recently visited pages.
func RecentHistory(visits []Visit, since time.Duration, limit int) []Visit {
	cutoff := time.Now().Add(-since)
	var out []Visit
	for _, v := range visits {
		if since > 0 && v.LastVisit.Before(cutoff) {
			continue
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastVisit.After(out[j].LastVisit) })
	out = dedupeByURL(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// PastDownload is a file the user's own Chrome fetched, read from its History
// database.
//
// Distinct from Download, which is the LIVE state of what this automation
// browser is fetching right now. Conflating the two is what made the question
// "did my download work?" unanswerable: this record belongs to a different
// profile, is written lazily, and only ever appears once a transfer is over.
type PastDownload struct {
	URL       string
	Path      string
	StartedAt time.Time
	Bytes     int64
	Done      bool
}

// LoadDownloads reads download history from the same database.
func LoadDownloads(path string) ([]PastDownload, error) {
	if path == "" {
		var err error
		if path, err = HistoryFile(); err != nil {
			return nil, err
		}
	}
	d, err := openSnapshot(path)
	if err != nil {
		return nil, err
	}
	rows, err := d.queryTable("downloads")
	if err != nil {
		return nil, err
	}

	out := make([]PastDownload, 0, len(rows))
	for _, r := range rows {
		out = append(out, PastDownload{
			URL:       asText(r["tab_url"]),
			Path:      asText(r["target_path"]),
			StartedAt: chromeTime(asInt(r["start_time"])),
			Bytes:     asInt(r["received_bytes"]),
			// Chrome's state enum: 1 means complete.
			Done: asInt(r["state"]) == 1,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

// Bookmark is a saved page.
type Bookmark struct {
	Name   string
	URL    string
	Folder string
}

// LoadBookmarks reads the bookmarks file, which is JSON rather than SQLite.
func LoadBookmarks() ([]Bookmark, error) {
	hist, err := HistoryFile()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(filepath.Dir(hist), "Bookmarks")

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading bookmarks: %w", err)
	}

	var file struct {
		Roots map[string]json.RawMessage `json:"roots"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}

	var out []Bookmark
	for name, raw := range file.Roots {
		collectBookmarks(raw, name, &out)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// collectBookmarks walks the bookmark tree.
func collectBookmarks(raw json.RawMessage, folder string, out *[]Bookmark) {
	var node struct {
		Type     string            `json:"type"`
		Name     string            `json:"name"`
		URL      string            `json:"url"`
		Children []json.RawMessage `json:"children"`
	}
	if err := json.Unmarshal(raw, &node); err != nil {
		return
	}

	switch node.Type {
	case "url":
		*out = append(*out, Bookmark{Name: node.Name, URL: node.URL, Folder: folder})
	case "folder":
		next := folder
		if node.Name != "" {
			next = folder + "/" + node.Name
		}
		for _, child := range node.Children {
			collectBookmarks(child, next, out)
		}
	default:
		for _, child := range node.Children {
			collectBookmarks(child, folder, out)
		}
	}
}

// SearchBookmarks finds saved pages matching a description.
func SearchBookmarks(marks []Bookmark, query string, limit int) []Bookmark {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return nil
	}
	type scored struct {
		b Bookmark
		n int
	}
	var hits []scored
	for _, b := range marks {
		hay := strings.ToLower(b.Name + " " + b.URL + " " + b.Folder)
		n := 0
		for _, t := range terms {
			if strings.Contains(hay, t) {
				n++
			}
		}
		if n > 0 {
			hits = append(hits, scored{b, n})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].n > hits[j].n })

	var out []Bookmark
	for i, h := range hits {
		if limit > 0 && i >= limit {
			break
		}
		out = append(out, h.b)
	}
	return out
}

// Resolve finds the single best URL for a description, across bookmarks and
// history.
//
// Bookmarks win ties: saving a page is an explicit statement that it matters,
// where a visit count is only an inference.
func Resolve(query string) (Visit, error) {
	if marks, err := LoadBookmarks(); err == nil {
		if hits := SearchBookmarks(marks, query, 1); len(hits) > 0 {
			return Visit{URL: hits[0].URL, Title: hits[0].Name}, nil
		}
	}

	visits, err := LoadHistory("")
	if err != nil {
		return Visit{}, err
	}
	hits := SearchHistory(visits, query, 1)
	if len(hits) == 0 {
		return Visit{}, fmt.Errorf("nothing in history or bookmarks matches %q", query)
	}
	return hits[0], nil
}

// FormatVisits renders results for the model to read.
func FormatVisits(visits []Visit) string {
	if len(visits) == 0 {
		return "No matching pages."
	}
	var b strings.Builder
	for _, v := range visits {
		title := v.Title
		if title == "" {
			title = "(untitled)"
		}
		when := "unknown"
		if !v.LastVisit.IsZero() {
			when = humanAgo(time.Since(v.LastVisit))
		}
		fmt.Fprintf(&b, "%s\n  %s\n  %d visits, last %s\n", title, v.URL, v.VisitCount, when)
	}
	return strings.TrimRight(b.String(), "\n")
}

func humanAgo(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%d months ago", int(d.Hours()/24/30))
	}
}

// logish gives strongly diminishing returns: zero at zero, never negative.
//
// The fortieth visit to a site says much less than the fourth did, and a linear
// count would let one obsessively-refreshed page outrank everything else.
func logish(x float64) float64 {
	if x <= 0 {
		return 0
	}
	return math.Log1p(x)
}
