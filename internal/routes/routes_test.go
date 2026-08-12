package routes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A route that has failed twice stops being asserted as fact.
//
// This is the whole reason the memory is safe to trust. An address that has
// quietly rotted is worse than not knowing where something is, because "I could
// not reach your mail" becomes "your mail is empty", and the second one is a
// confident wrong answer of exactly the kind everything else here is built to
// prevent.
//
// Two rather than one, because a single failure is as likely to be a dropped
// connection or a sign-in wall as a moved site, and forgetting a good address
// because the wifi blinked is its own kind of wrong.
func TestARouteGoesStaleOnlyAfterItKeepsFailing(t *testing.T) {
	s := openTemp(t)
	put(t, s, "email", "https://mail.example.com/inbox")

	r, _ := s.Get("email")
	if r.Stale() {
		t.Fatal("a freshly learned route was already stale")
	}

	if n, err := s.Failed("email"); err != nil || n != 1 {
		t.Fatalf("first failure counted as %d (%v)", n, err)
	}
	if r, _ := s.Get("email"); r.Stale() {
		t.Error("one failure made a route stale, so a dropped connection forgets an address")
	}
	if n, err := s.Failed("email"); err != nil || n != 2 {
		t.Fatalf("second failure counted as %d (%v)", n, err)
	}
	if r, _ := s.Get("email"); !r.Stale() {
		t.Error("two failures in a row and the route is still asserted as fact")
	}

	// And working again clears it, or a site that was briefly down stays
	// distrusted forever.
	if err := s.Worked("email"); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Get("email")
	if r.Stale() || r.Fails != 0 {
		t.Errorf("a route that worked again is still marked stale (fails=%d)", r.Fails)
	}
	if r.LastOK.IsZero() {
		t.Error("a route that worked has no record of when")
	}
}

// Learning a place inside a service must not throw away the way in.
//
// The addresses are learned at different moments: the way in on the first visit,
// "compose" later, once she has worked out where it is. A put that replaced the
// map would mean learning the second fact costs her the first.
func TestLearningOnePlaceKeepsTheOthers(t *testing.T) {
	s := openTemp(t)
	put(t, s, "email", "https://mail.example.com/inbox")

	if err := s.Put(Route{
		Service: "email",
		Host:    "mail.example.com",
		Entries: map[string]string{"compose": "https://mail.example.com/compose"},
	}); err != nil {
		t.Fatal(err)
	}

	r, ok := s.Get("email")
	if !ok {
		t.Fatal("the route vanished")
	}
	if u, _ := r.URL(""); u != "https://mail.example.com/inbox" {
		t.Errorf("the way in became %q after learning where compose is", u)
	}
	if u, _ := r.URL("compose"); u != "https://mail.example.com/compose" {
		t.Errorf("compose is %q", u)
	}
	// An unknown capability falls back to the way in rather than failing, since
	// the front door is usually a reasonable answer.
	if u, ok := r.URL("archive"); !ok || u != "https://mail.example.com/inbox" {
		t.Errorf("an unknown place gave %q (ok=%v), want the way in", u, ok)
	}
}

// The store survives a restart, which is the entire point of writing it down.
func TestRoutesSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	put(t, s, "calendar", "https://calendar.example.com/today")

	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := again.Get("calendar")
	if !ok {
		t.Fatal("the route did not survive a reopen")
	}
	if r.Host != "calendar.example.com" {
		t.Errorf("host came back as %q", r.Host)
	}
	// Case should not matter: the user says "Calendar" and "calendar" on
	// different days.
	if _, ok := again.Get("CALENDAR"); !ok {
		t.Error("a service name is case sensitive, so asking differently loses it")
	}
}

// A file that will not parse is kept, not deleted, and the caller is told.
//
// It is the only record of what she knew. Starting empty is survivable;
// starting empty in silence, having destroyed the evidence, is not.
func TestAnUnreadableStoreIsKeptAndReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.json")
	if err := os.WriteFile(path, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dir)
	if err == nil {
		t.Fatal("a corrupt store loaded without complaint")
	}
	if s == nil {
		t.Fatal("a corrupt store left her with nothing at all, rather than starting empty")
	}
	if len(s.List()) != 0 {
		t.Error("a corrupt store produced routes")
	}
	if _, statErr := os.Stat(path + ".unreadable"); statErr != nil {
		t.Error("the unparseable file was not kept for anyone to look at")
	}
}

// The host is what a page is checked against later, so it has to come out of
// any shape of URL.
func TestHostIsTakenOutOfWhateverShapeTheUrlIs(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"https://mail.google.com/mail/u/0/#inbox", "mail.google.com"},
		{"https://www.fastmail.com/", "fastmail.com"},
		{"http://localhost:8080/webmail", "localhost:8080"},
		{"mail.proton.me/u/0", "mail.proton.me"},
		{"https://user@mail.example.com/inbox", "mail.example.com"},
		{"", ""},
	} {
		if got := HostOf(c.in); got != c.want {
			t.Errorf("HostOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Recognising a kind is a label for something history already surfaced, never a
// reason to pick it.
func TestKnownHostsAreRecognisedAndNothingElseIs(t *testing.T) {
	for url, want := range map[string]string{
		"https://mail.google.com/mail":     "email",
		"https://calendar.google.com/":     "calendar",
		"https://web.whatsapp.com/":        "messages",
		"https://drive.google.com/drive/0": "files",
		"https://github.com/Akins20":       "code",
	} {
		got, ok := KindOf(url)
		if !ok || got != want {
			t.Errorf("KindOf(%q) = %q (%v), want %q", url, got, ok, want)
		}
	}
	if k, ok := KindOf("https://news.ycombinator.com/"); ok {
		t.Errorf("an ordinary site was labelled %q", k)
	}
}

// Age carries the caveat, so an answer does not sound as certain at a year as
// it does at an hour.
func TestAgeSaysHowOldTheKnowledgeIs(t *testing.T) {
	now := time.Now()
	fresh := Route{Learned: now.Add(-30 * time.Minute)}
	if got := fresh.Age(now); !strings.Contains(got, "just now") {
		t.Errorf("half an hour old reads as %q", got)
	}
	old := Route{Learned: now.Add(-72 * time.Hour), LastOK: now.Add(-48 * time.Hour)}
	if got := old.Age(now); !strings.Contains(got, "2 days") || !strings.Contains(got, "worked") {
		t.Errorf("two days since it last worked reads as %q", got)
	}
	if got := (Route{}).Age(now); got != "never checked" {
		t.Errorf("a route with no dates reads as %q", got)
	}
}

// A route with no address is not a route, and would produce an answer that
// names a service and cannot open it.
func TestARouteNeedsAnAddress(t *testing.T) {
	s := openTemp(t)
	if err := s.Put(Route{Service: "email", Host: "x.example.com"}); err == nil {
		t.Error("a route with no addresses was accepted")
	}
	if err := s.Put(Route{Entries: map[string]string{"": "https://x.example.com"}}); err == nil {
		t.Error("a route with no service name was accepted")
	}
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func put(t *testing.T, s *Store, service, url string) {
	t.Helper()
	if err := s.Put(Route{
		Service: service,
		Host:    HostOf(url),
		Entries: map[string]string{"": url},
		Found:   "test",
	}); err != nil {
		t.Fatal(err)
	}
}
