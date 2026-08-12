// Package routes remembers where the user's things actually live, so the second
// time costs nothing.
//
// # The gap this closes
//
// She could reach any site, and knew none of them. "Check my email" meant
// guessing a provider, or searching, or asking, every single time, because
// nothing in her carried the one fact that would make it a single step: which
// mail this person uses. The same for a calendar, for whichever chat their
// friends are on, for the portal their course lives behind.
//
// The obvious fix is a tool per service, and it is the wrong shape. It means a
// Gmail tool, an Outlook tool, a WhatsApp tool, a Slack tool, each one a small
// integration to write and keep alive, and a user whose provider is not on the
// list gets nothing. What she needs is not fifty integrations. It is one memory:
// this is where your mail is, this is the address that works, and I checked it.
//
// # Found rather than assumed
//
// The route is discovered from evidence the user already generated. Their own
// browsing says which mail host they open every morning far more reliably than
// any default, and it is right about the person rather than about the average
// person. A short table of well-known hosts exists only to recognise a candidate
// once history has surfaced it; nothing is ever picked because it is popular.
//
// Anything can be learned by name, so a self-hosted mail server or a university
// portal is a first-class route rather than an unsupported case.
//
// # A remembered route is a claim with a date on it
//
// Sites move. A remembered address that quietly 404s is worse than no route at
// all, because it turns "I could not find your mail" into "your mail is empty".
// So a route records when it last worked, counts consecutive failures, and goes
// stale rather than being trusted forever. Whoever uses one is expected to check
// it landed where it said and report back, and the answer says how old the
// knowledge is.
//
// # What is deliberately not here
//
// No credentials, ever. The route is an address and nothing else. Signing in is
// the browser's auth context, which carries the session the user already has,
// and a password is never read, stored or typed by any of this.
package routes

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Route is one service and how to reach it.
type Route struct {
	// Service is what the user calls it: email, calendar, messages, or any name
	// they choose.
	Service string `json:"service"`
	// Host is the site it lives on, without a scheme, for checking that a page
	// which loaded is the page that was meant.
	Host string `json:"host"`
	// Entries are addresses within the service, keyed by what they are for:
	// "" is the way in, and "compose" or "today" are the places worth naming.
	Entries map[string]string `json:"entries"`
	// Found records how this was arrived at, because "you told me" and "I found
	// it in your history" deserve different amounts of confidence.
	Found string `json:"found"`
	// Learned and LastOK date the knowledge.
	Learned time.Time `json:"learned"`
	LastOK  time.Time `json:"last_ok,omitzero"`
	// Fails counts consecutive failures. Reset by any success.
	Fails int `json:"fails,omitempty"`
}

// URL returns the address for a capability, falling back to the way in.
func (r Route) URL(capability string) (string, bool) {
	capability = strings.ToLower(strings.TrimSpace(capability))
	if u, ok := r.Entries[capability]; ok && u != "" {
		return u, true
	}
	u, ok := r.Entries[""]
	return u, ok && u != ""
}

// Stale reports a route that has failed enough times to stop being asserted.
//
// Two rather than one: a single failure is as likely to be a dropped connection
// or a session that needed a sign-in as it is to be a route that has rotted, and
// forgetting a good address because the wifi blinked is its own kind of wrong.
func (r Route) Stale() bool { return r.Fails >= 2 }

// Age describes how old the knowledge is, for an answer that carries its own
// caveat rather than sounding equally certain at one day and at one year.
func (r Route) Age(now time.Time) string {
	when := r.LastOK
	what := "last worked"
	if when.IsZero() {
		when, what = r.Learned, "learned"
	}
	if when.IsZero() {
		return "never checked"
	}
	d := now.Sub(when)
	switch {
	case d < time.Hour:
		return what + " just now"
	case d < 24*time.Hour:
		return fmt.Sprintf("%s %d hours ago", what, int(d.Hours()))
	default:
		return fmt.Sprintf("%s %d days ago", what, int(d.Hours()/24))
	}
}

// Store holds the routes on disk.
type Store struct {
	mu     sync.Mutex
	path   string
	routes map[string]Route
}

// Open loads the store, creating it on first use.
//
// A file that will not parse is renamed rather than deleted or silently
// replaced: it is the only copy of what she knew, and a corrupt one is still
// evidence. Starting empty and saying so beats starting empty in silence.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("no data directory for the routes store")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "routes.json"), routes: map[string]Route{}}

	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	var list []Route
	if err := json.Unmarshal(b, &list); err != nil {
		lost := s.path + ".unreadable"
		if rerr := os.Rename(s.path, lost); rerr == nil {
			return s, fmt.Errorf("the routes file would not parse and was kept at %s; "+
				"starting with none", lost)
		}
		return s, fmt.Errorf("the routes file would not parse: %w", err)
	}
	for _, r := range list {
		if r.Service != "" {
			s.routes[key(r.Service)] = r
		}
	}
	return s, nil
}

func key(service string) string { return strings.ToLower(strings.TrimSpace(service)) }

// Put records a route, keeping the date it was first learned.
func (s *Store) Put(r Route) error {
	if strings.TrimSpace(r.Service) == "" {
		return fmt.Errorf("a route needs a service name")
	}
	if len(r.Entries) == 0 {
		return fmt.Errorf("a route needs at least one address")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	k := key(r.Service)
	if old, ok := s.routes[k]; ok && !old.Learned.IsZero() {
		r.Learned = old.Learned
		// Merge rather than replace, so recording where "compose" lives does not
		// throw away the way in.
		merged := map[string]string{}
		for c, u := range old.Entries {
			merged[c] = u
		}
		for c, u := range r.Entries {
			merged[c] = u
		}
		r.Entries = merged
	}
	if r.Learned.IsZero() {
		r.Learned = time.Now()
	}
	s.routes[k] = r
	return s.save()
}

// Get returns a route by service name.
func (s *Store) Get(service string) (Route, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.routes[key(service)]
	return r, ok
}

// List returns every route, most recently useful first.
func (s *Store) List() []Route {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Route, 0, len(s.routes))
	for _, r := range s.routes {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].LastOK, out[j].LastOK
		if a.Equal(b) {
			return out[i].Service < out[j].Service
		}
		return a.After(b)
	})
	return out
}

// Forget removes a route.
func (s *Store) Forget(service string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(service)
	if _, ok := s.routes[k]; !ok {
		return false, nil
	}
	delete(s.routes, k)
	return true, s.save()
}

// Worked records that a route led where it said it would.
func (s *Store) Worked(service string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.routes[key(service)]
	if !ok {
		return fmt.Errorf("no route for %q", service)
	}
	r.LastOK = time.Now()
	r.Fails = 0
	s.routes[key(service)] = r
	return s.save()
}

// Failed records that it did not, and returns the running count.
func (s *Store) Failed(service string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.routes[key(service)]
	if !ok {
		return 0, fmt.Errorf("no route for %q", service)
	}
	r.Fails++
	s.routes[key(service)] = r
	return r.Fails, s.save()
}

// save writes the whole file. Called with the lock held.
func (s *Store) save() error {
	list := make([]Route, 0, len(s.routes))
	for _, r := range s.routes {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Service < list[j].Service })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	// Written beside and renamed, so an interrupted write cannot leave her with
	// a half-file and no memory of where anything is.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// HostOf strips a URL down to its host, for comparing where a page landed
// against where it was meant to land.
func HostOf(url string) string {
	s := strings.TrimSpace(url)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	return strings.ToLower(strings.TrimPrefix(s, "www."))
}

// Kinds are the service kinds with well-known hosts worth recognising.
//
// This is not a list of what she supports. It is a way of recognising a
// candidate that the user's own history has already surfaced, so that "you open
// this every day and it is a mail host" can be said with some confidence.
// Anything absent from here is still learnable by name, and a host being on the
// list is never a reason to pick it if the user does not actually go there.
var Kinds = map[string][]string{
	"email": {
		"mail.google.com", "gmail.com", "outlook.office.com", "outlook.office365.com",
		"outlook.live.com", "mail.yahoo.com", "mail.proton.me", "protonmail.com",
		"mail.zoho.com", "fastmail.com", "roundcube", "webmail", "mail.",
	},
	"calendar": {
		"calendar.google.com", "outlook.office.com/calendar", "calendar.yahoo.com",
		"calendar.proton.me", "fantastical", "cal.com", "calendly.com", "calendar.",
	},
	"messages": {
		"web.whatsapp.com", "app.slack.com", "slack.com", "web.telegram.org",
		"discord.com/app", "discord.com/channels", "teams.microsoft.com",
		"messenger.com", "web.skype.com", "signal.org", "chat.google.com",
	},
	"files": {
		"drive.google.com", "onedrive.live.com", "dropbox.com", "box.com",
		"nextcloud", "mega.nz",
	},
	"code": {
		"github.com", "gitlab.com", "bitbucket.org", "codeberg.org",
	},
}

// KindOf reports which kind a host looks like, and whether anything matched.
func KindOf(url string) (string, bool) {
	h := strings.ToLower(url)
	for kind, hosts := range Kinds {
		for _, want := range hosts {
			if strings.Contains(h, want) {
				return kind, true
			}
		}
	}
	return "", false
}

// KindNames lists the recognised kinds, sorted, for a message that has to name
// them.
func KindNames() []string {
	out := make([]string, 0, len(Kinds))
	for k := range Kinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
