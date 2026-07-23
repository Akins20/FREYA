package browser

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Reading which accounts the user has saved, so a sign-in can ask the right
// question instead of guessing.
//
// # What this reads and, more importantly, what it does not
//
// Chrome stores saved logins in a SQLite file next to the history. Each row has
// an origin, a username, and a password. This reads the origin and the username.
// It never reads the password, and it could not use it if it did: the value is
// encrypted against the system keyring, and decrypting it is precisely the thing
// that must not happen here. Chrome fills the password itself when a credential
// is chosen; the assistant's job is only to know which usernames exist for a
// site so that, when there is more than one, it can ask which to use rather than
// silently signing in as whoever happens to be first.

// SavedLogin is one stored credential, minus the secret.
type SavedLogin struct {
	Origin   string // the URL the login is for
	Username string
	Realm    string // Chrome's signon_realm, the grouping key it uses
	LastUsed time.Time
}

// Host returns the site the login belongs to.
func (s SavedLogin) Host() string {
	return (Visit{URL: s.Origin}).Host()
}

// LoadLogins reads saved usernames from Chrome's login store.
func LoadLogins(path string) ([]SavedLogin, error) {
	if path == "" {
		hist, err := HistoryFile()
		if err != nil {
			return nil, err
		}
		// The login store sits beside the history, under the same profile.
		path = filepath.Join(filepath.Dir(hist), "Login Data")
	}

	d, err := openSnapshot(path)
	if err != nil {
		return nil, err
	}
	rows, err := d.queryTable("logins")
	if err != nil {
		return nil, err
	}

	out := make([]SavedLogin, 0, len(rows))
	for _, r := range rows {
		origin := asText(r["origin_url"])
		if origin == "" {
			continue
		}
		out = append(out, SavedLogin{
			Origin:   origin,
			Username: asText(r["username_value"]),
			Realm:    asText(r["signon_realm"]),
			LastUsed: chromeTime(asInt(r["date_last_used"])),
		})
	}
	return out, nil
}

// AccountsFor returns the saved usernames for a site, most recently used first.
//
// Matching is by host substring so that "google.com" finds accounts saved under
// "accounts.google.com". Blank usernames — passkeys, some SSO rows — are kept
// but labelled, because their presence still means "there is more than one thing
// to choose here".
func AccountsFor(logins []SavedLogin, site string) []SavedLogin {
	want := strings.ToLower(strings.TrimSpace(site))
	// Reduce a full URL to its host, so callers can pass either.
	if h := (Visit{URL: want}).Host(); h != "" && strings.Contains(want, "://") {
		want = h
	}

	var hits []SavedLogin
	seen := map[string]bool{}
	for _, l := range logins {
		host := strings.ToLower(l.Host())
		if !strings.Contains(host, want) && !strings.Contains(strings.ToLower(l.Realm), want) {
			continue
		}
		// One row per username: Chrome often stores the same account twice under
		// slightly different origins, and offering it twice helps nobody choose.
		key := host + "\x00" + l.Username
		if seen[key] {
			continue
		}
		seen[key] = true
		hits = append(hits, l)
	}

	sort.SliceStable(hits, func(i, j int) bool { return hits[i].LastUsed.After(hits[j].LastUsed) })
	return hits
}

// FormatAccounts renders saved accounts for a site, for the model to read and
// decide whether it must ask.
func FormatAccounts(site string, accounts []SavedLogin) string {
	if len(accounts) == 0 {
		return fmt.Sprintf("No saved logins for %q. Chrome may still autofill from a "+
			"broader match, or the site may already be signed in.", site)
	}
	if len(accounts) == 1 {
		u := accounts[0].Username
		if u == "" {
			u = "a saved login with no username (a passkey or SSO entry)"
		}
		return fmt.Sprintf("One saved login for %s: %s. Safe to sign in with it without asking.",
			accounts[0].Host(), u)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d saved logins for %s — ASK the user which to use before signing in:\n",
		len(accounts), accounts[0].Host())
	for i, a := range accounts {
		name := a.Username
		if name == "" {
			name = "(no username — passkey or SSO)"
		}
		when := "unknown"
		if !a.LastUsed.IsZero() {
			when = humanAgo(time.Since(a.LastUsed))
		}
		fmt.Fprintf(&b, "  %d. %s  (last used %s)\n", i+1, name, when)
	}
	return strings.TrimRight(b.String(), "\n")
}
