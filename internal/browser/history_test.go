package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadVarint(t *testing.T) {
	cases := []struct {
		bytes []byte
		want  int64
		n     int
	}{
		{[]byte{0x00}, 0, 1},
		{[]byte{0x7F}, 127, 1},
		{[]byte{0x81, 0x00}, 128, 2},
		{[]byte{0x82, 0x00}, 256, 2},
		{[]byte{0xFF, 0x7F}, 16383, 2},
		{[]byte{0x81, 0x80, 0x00}, 16384, 3},
		// Nine bytes: the first eight contribute seven bits each, the ninth all
		// eight. 1<<49 shifted a further eight places is 1<<57.
		{[]byte{0x81, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x00}, 1 << 57, 9},
	}
	for _, c := range cases {
		got, n := readVarint(c.bytes)
		if got != c.want || n != c.n {
			t.Errorf("readVarint(% x) = %d,%d want %d,%d", c.bytes, got, n, c.want, c.n)
		}
	}

	// A truncated varint must report failure rather than a plausible number.
	if _, n := readVarint([]byte{0x81}); n != 0 {
		t.Error("truncated varint should return n=0")
	}
	if _, n := readVarint(nil); n != 0 {
		t.Error("empty varint should return n=0")
	}
}

func TestBEIntSignExtension(t *testing.T) {
	cases := []struct {
		raw  []byte
		want int64
	}{
		{[]byte{0x01}, 1},
		{[]byte{0xFF}, -1},
		{[]byte{0x80}, -128},
		{[]byte{0x7F}, 127},
		{[]byte{0xFF, 0xFF}, -1},
		{[]byte{0x01, 0x00}, 256},
		// Six-byte integers are a SQLite-specific width and a likely place to
		// get sign extension wrong.
		{[]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, -1},
		{[]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x01}, 1},
	}
	for _, c := range cases {
		if got := beInt(c.raw); got != c.want {
			t.Errorf("beInt(% x) = %d want %d", c.raw, got, c.want)
		}
	}
}

func TestSerialSize(t *testing.T) {
	cases := map[int64]int{
		0: 0, 1: 1, 2: 2, 3: 3, 4: 4, 5: 6, 6: 8, 7: 8, 8: 0, 9: 0,
		12: 0, 14: 1, 16: 2, // blobs
		13: 0, 15: 1, 17: 2, // text
	}
	for serial, want := range cases {
		if got := serialSize(serial); got != want {
			t.Errorf("serialSize(%d) = %d want %d", serial, got, want)
		}
	}
}

func TestParseColumns(t *testing.T) {
	// The real Chrome schema, which is the case that has to work.
	sql := `CREATE TABLE urls(id INTEGER PRIMARY KEY AUTOINCREMENT,url LONGVARCHAR,` +
		`title LONGVARCHAR,visit_count INTEGER DEFAULT 0 NOT NULL,` +
		`typed_count INTEGER DEFAULT 0 NOT NULL,last_visit_time INTEGER NOT NULL,` +
		`hidden INTEGER DEFAULT 0 NOT NULL)`

	cols, pk := parseColumns(sql)
	want := []string{"id", "url", "title", "visit_count", "typed_count", "last_visit_time", "hidden"}
	if len(cols) != len(want) {
		t.Fatalf("got %d columns %v, want %d", len(cols), cols, len(want))
	}
	for i := range want {
		if cols[i] != want[i] {
			t.Errorf("column %d = %q want %q", i, cols[i], want[i])
		}
	}
	if pk != 0 {
		t.Errorf("rowid column = %d, want 0 (id is INTEGER PRIMARY KEY)", pk)
	}
}

func TestParseColumnsSkipsTableConstraints(t *testing.T) {
	// A trailing PRIMARY KEY clause is not a column, and counting it as one
	// shifts every value one place to the right.
	sql := `CREATE TABLE t(a INTEGER, b TEXT, PRIMARY KEY(a, b))`
	cols, _ := parseColumns(sql)
	if len(cols) != 2 || cols[0] != "a" || cols[1] != "b" {
		t.Errorf("got %v, want [a b]", cols)
	}

	// A type with its own parentheses must not split the column list.
	sql2 := `CREATE TABLE t(a VARCHAR(255), b DECIMAL(10, 2))`
	cols2, _ := parseColumns(sql2)
	if len(cols2) != 2 {
		t.Errorf("got %v, want two columns — commas inside a type broke the split", cols2)
	}
}

func TestChromeTime(t *testing.T) {
	// Chrome's epoch is 1601-01-01; zero must not become a date in 1601.
	if !chromeTime(0).IsZero() {
		t.Error("zero timestamp should give a zero time, not the year 1601")
	}
	// A known value: the Unix epoch in Chrome time.
	unixEpoch := chromeTime(chromeEpochOffset * 1_000_000)
	if unixEpoch.Unix() != 0 {
		t.Errorf("Chrome epoch offset wrong: got Unix %d, want 0", unixEpoch.Unix())
	}
	// Something in the recent past should land in a plausible decade.
	recent := chromeTime(13422799795449963)
	if recent.Year() < 2020 || recent.Year() > 2030 {
		t.Errorf("timestamp decoded to %v, which is not a plausible date", recent)
	}
}

func TestVisitHost(t *testing.T) {
	cases := map[string]string{
		"https://www.example.com/a/b?c=d": "example.com",
		"http://portal.school.edu/login":  "portal.school.edu",
		"https://example.com":             "example.com",
		"https://example.com#frag":        "example.com",
	}
	for url, want := range cases {
		if got := (Visit{URL: url}).Host(); got != want {
			t.Errorf("Host(%q) = %q want %q", url, got, want)
		}
	}
}

// TestSearchRankingPrefersHabit is the behaviour the feature exists for: when
// the user says "my portal", the place they go every week must outrank the page
// they opened once.
func TestSearchRankingPrefersHabit(t *testing.T) {
	now := time.Now()
	visits := []Visit{
		{URL: "https://random.blog/what-is-a-portal", Title: "What is a portal",
			VisitCount: 1, LastVisit: now.Add(-2 * time.Hour)},
		{URL: "https://portal.myschool.edu/dashboard", Title: "Student Portal",
			VisitCount: 87, TypedCount: 12, LastVisit: now.Add(-24 * time.Hour)},
	}

	hits := SearchHistory(visits, "school portal", 5)
	if len(hits) == 0 {
		t.Fatal("no results")
	}
	if !strings.Contains(hits[0].URL, "myschool.edu") {
		t.Errorf("ranked %q first; the frequently-visited portal should win", hits[0].URL)
	}
}

// TestSearchRecencyBreaksTies checks the other half: with habit equal, the more
// recent page wins.
func TestSearchRecencyBreaksTies(t *testing.T) {
	now := time.Now()
	visits := []Visit{
		{URL: "https://old.example.com/notes", Title: "Course notes",
			VisitCount: 10, LastVisit: now.AddDate(-2, 0, 0)},
		{URL: "https://new.example.com/notes", Title: "Course notes",
			VisitCount: 10, LastVisit: now.Add(-time.Hour)},
	}
	hits := SearchHistory(visits, "course notes", 5)
	if len(hits) < 2 {
		t.Fatalf("expected both results, got %d", len(hits))
	}
	if !strings.Contains(hits[0].URL, "new.example.com") {
		t.Errorf("ranked %q first; the recent one should win a tie", hits[0].URL)
	}
}

// TestSearchDoesNotRequireEveryTerm covers the filler-word case: "my school
// portal" contains a word that will never appear in a URL.
func TestSearchDoesNotRequireEveryTerm(t *testing.T) {
	visits := []Visit{
		{URL: "https://portal.school.edu/", Title: "Login", VisitCount: 30,
			LastVisit: time.Now()},
	}
	if hits := SearchHistory(visits, "my school portal", 5); len(hits) == 0 {
		t.Error("filler word 'my' eliminated the only correct answer")
	}
}

func TestSearchDedupesQueryStrings(t *testing.T) {
	now := time.Now()
	var visits []Visit
	// The same page reached with ten different session tokens.
	for i := range 10 {
		visits = append(visits, Visit{
			URL:        "https://portal.edu/dash?session=" + strings.Repeat("x", i+1),
			Title:      "Dashboard",
			VisitCount: 5, LastVisit: now,
		})
	}
	hits := SearchHistory(visits, "dashboard", 10)
	if len(hits) != 1 {
		t.Errorf("got %d results for the same page with different tokens, want 1", len(hits))
	}
}

func TestTopSitesAggregatesByHost(t *testing.T) {
	visits := []Visit{
		{URL: "https://a.com/1", VisitCount: 5, LastVisit: time.Now()},
		{URL: "https://a.com/2", VisitCount: 7, LastVisit: time.Now()},
		{URL: "https://b.com/1", VisitCount: 4, LastVisit: time.Now()},
	}
	top := TopSites(visits, 10)
	if len(top) != 2 {
		t.Fatalf("got %d hosts, want 2", len(top))
	}
	if top[0].Host() != "a.com" || top[0].VisitCount != 12 {
		t.Errorf("got %s with %d visits, want a.com with 12", top[0].Host(), top[0].VisitCount)
	}
}

func TestOpenDBRejectsRubbish(t *testing.T) {
	if _, err := openDB([]byte("not a database at all")); err == nil {
		t.Error("accepted a non-database")
	}
	if _, err := openDB(nil); err == nil {
		t.Error("accepted empty input")
	}

	// A valid magic string with an absurd page size must still be rejected: a
	// corrupt header should not become an out-of-range slice later on.
	bad := make([]byte, 100)
	copy(bad, sqliteMagic)
	bad[16], bad[17] = 0x00, 0x03 // 768, not a power of two
	if _, err := openDB(bad); err == nil {
		t.Error("accepted an implausible page size")
	}
}

// TestRealChromeDatabase reads the actual browser history if there is one.
//
// It asserts structure rather than content — the point is that a real file, with
// real overflow pages and real interior b-tree nodes, parses without loss.
func TestRealChromeDatabase(t *testing.T) {
	path, err := HistoryFile()
	if err != nil {
		t.Skip("no browser history on this machine")
	}
	if _, err := os.Stat(path); err != nil {
		t.Skip("history file unreadable")
	}

	visits, err := LoadHistory(path)
	if err != nil {
		t.Fatalf("loading real history: %v", err)
	}
	if len(visits) == 0 {
		t.Skip("history is empty")
	}
	t.Logf("parsed %d visits", len(visits))

	// Every row must have come out coherent: a URL, and a date that is not
	// absurd. A parser that loses sync produces garbage in exactly these fields.
	bad := 0
	for _, v := range visits {
		if !strings.HasPrefix(v.URL, "http") && !strings.HasPrefix(v.URL, "file") &&
			!strings.HasPrefix(v.URL, "chrome") && !strings.HasPrefix(v.URL, "about") &&
			!strings.HasPrefix(v.URL, "javascript") && !strings.HasPrefix(v.URL, "data") &&
			!strings.HasPrefix(v.URL, "ftp") && !strings.HasPrefix(v.URL, "blob") {
			bad++
			if bad < 3 {
				t.Logf("unexpected scheme in row: %.40q", v.URL)
			}
		}
		if !v.LastVisit.IsZero() && (v.LastVisit.Year() < 1990 || v.LastVisit.Year() > 2100) {
			t.Errorf("implausible date %v — timestamps are being misread", v.LastVisit)
			break
		}
	}
	if bad > len(visits)/20 {
		t.Errorf("%d of %d rows had an unrecognisable URL scheme — the parser is losing sync",
			bad, len(visits))
	}

	// Long URLs prove the overflow-page path ran.
	long := 0
	for _, v := range visits {
		if len(v.URL) > 300 {
			long++
		}
	}
	t.Logf("%d URLs over 300 bytes (these require overflow pages)", long)

	// Searching must be fast enough to sit inside a conversation turn.
	start := time.Now()
	SearchHistory(visits, "portal login", 10)
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Errorf("search took %s over %d visits — too slow to use per turn", elapsed, len(visits))
	}
}

func TestKeyParsing(t *testing.T) {
	// Modifier parsing is worth checking directly: "ctrl+a" selecting all is a
	// different outcome from typing the letter a.
	cases := []struct {
		input   string
		wantMod int
		wantKey string
		wantErr bool
	}{
		{"enter", 0, "Enter", false},
		{"ctrl+a", 2, "a", false},
		{"shift+tab", 8, "Tab", false},
		{"ctrl+shift+k", 10, "k", false},
		{"hyper+x", 0, "", true},
		{"nonsensekey", 0, "", true},
	}
	for _, c := range cases {
		parts := strings.Split(strings.ToLower(c.input), "+")
		mods := 0
		failed := false
		for _, p := range parts[:len(parts)-1] {
			bit, ok := modifierBits[p]
			if !ok {
				failed = true
				break
			}
			mods |= bit
		}
		keyName := parts[len(parts)-1]
		spec, known := keyCodes[keyName]
		if !known && len([]rune(keyName)) != 1 {
			failed = true
		}

		if failed != c.wantErr {
			t.Errorf("%q: error = %v, want %v", c.input, failed, c.wantErr)
			continue
		}
		if c.wantErr {
			continue
		}
		if mods != c.wantMod {
			t.Errorf("%q: modifiers = %d want %d", c.input, mods, c.wantMod)
		}
		got := spec.key
		if !known {
			got = keyName
		}
		if got != c.wantKey {
			t.Errorf("%q: key = %q want %q", c.input, got, c.wantKey)
		}
	}
}

func TestLoadLoginsReadsUsernamesNotPasswords(t *testing.T) {
	hist, err := HistoryFile()
	if err != nil {
		t.Skip("no browser profile")
	}
	path := filepath.Join(filepath.Dir(hist), "Login Data")
	if _, err := os.Stat(path); err != nil {
		t.Skip("no Login Data file")
	}
	logins, err := LoadLogins(path)
	if err != nil {
		t.Fatalf("load logins: %v", err)
	}
	if len(logins) == 0 {
		t.Skip("no saved logins")
	}
	t.Logf("read %d saved logins", len(logins))

	// The struct has no password field at all; this asserts the shape rather
	// than a value, which is the real guarantee — the secret is never carried.
	for _, l := range logins {
		if l.Origin == "" {
			t.Error("a login came back with no origin")
		}
	}

	// A site with multiple accounts must trigger the ask-path formatting.
	byHost := map[string]int{}
	for _, l := range logins {
		byHost[l.Host()]++
	}
	var multiSite string
	for h, n := range byHost {
		if n > 1 {
			multiSite = h
			break
		}
	}
	if multiSite != "" {
		out := FormatAccounts(multiSite, AccountsFor(logins, multiSite))
		if !strings.Contains(strings.ToUpper(out), "ASK") {
			t.Errorf("a multi-account site did not produce an ASK prompt:\n%s", out)
		}
	}
}
