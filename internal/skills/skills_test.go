package skills

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/akins/jarvis/internal/llm"
)

func TestArgCoercion(t *testing.T) {
	// Providers return JSON, so every number arrives as float64 and any field
	// may be missing entirely. Coercion must never panic.
	args := map[string]any{
		"str":   "  hello  ",
		"num":   float64(42),
		"numS":  "17",
		"boolT": true,
		"boolS": "true",
		"null":  nil,
	}

	if got := argString(args, "str"); got != "hello" {
		t.Errorf("argString did not trim: %q", got)
	}
	if got := argString(args, "num"); got != "42" {
		t.Errorf("argString(float64) = %q, want \"42\"", got)
	}
	if got := argString(args, "missing"); got != "" {
		t.Errorf("missing key returned %q", got)
	}
	if got := argString(args, "null"); got != "" {
		t.Errorf("null value returned %q", got)
	}
	if got := argInt(args, "num", 0); got != 42 {
		t.Errorf("argInt(float64) = %d", got)
	}
	if got := argInt(args, "numS", 0); got != 17 {
		t.Errorf("argInt(string) = %d", got)
	}
	if got := argInt(args, "missing", 9); got != 9 {
		t.Errorf("argInt fallback = %d, want 9", got)
	}
	if !argBool(args, "boolT") || !argBool(args, "boolS") {
		t.Error("argBool failed on bool or string form")
	}
	if argBool(args, "missing") {
		t.Error("argBool on missing key should be false")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Prefers Go":        "prefers-go",
		"  main drive!!  ":  "main-drive",
		"already-slugged":   "already-slugged",
		"UPPER_Case__Mixed": "upper-case-mixed",
		"":                  "",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseDue(t *testing.T) {
	if _, err := parseDue("2026-07-23T09:00:00Z"); err != nil {
		t.Errorf("RFC3339 rejected: %v", err)
	}
	if _, err := parseDue("2026-07-23 09:00"); err != nil {
		t.Errorf("datetime rejected: %v", err)
	}

	got, err := parseDue("2h")
	if err != nil {
		t.Fatalf("duration rejected: %v", err)
	}
	if delta := time.Until(got); delta < 90*time.Minute || delta > 150*time.Minute {
		t.Errorf("'2h' resolved to %v away", delta)
	}

	// time.ParseDuration has no day unit, so days need explicit handling.
	got, err = parseDue("3d")
	if err != nil {
		t.Fatalf("day offset rejected: %v", err)
	}
	if delta := time.Until(got); delta < 70*time.Hour || delta > 74*time.Hour {
		t.Errorf("'3d' resolved to %v away", delta)
	}

	if _, err := parseDue("next tuesday-ish"); err == nil {
		t.Error("nonsense time should be rejected")
	}
}

func TestRegistryDispatch(t *testing.T) {
	r := New()
	r.Register(Skill{
		Tool: llm.Tool{
			Name:   "echo",
			Params: llm.ObjectSchema(map[string]llm.Property{"v": {Type: "string"}}, "v"),
		},
		Handler: func(_ context.Context, a map[string]any) (string, error) {
			return "got " + argString(a, "v"), nil
		},
	})

	out, err := r.Execute(context.Background(), "echo", map[string]any{"v": "x"})
	if err != nil || out != "got x" {
		t.Fatalf("execute = %q, %v", out, err)
	}
	if _, err := r.Execute(context.Background(), "nope", nil); err == nil {
		t.Fatal("unknown skill should error")
	}
}

func TestRegistryToolsAreSorted(t *testing.T) {
	// Tool order must be deterministic: reshuffling the declarations between
	// runs would invalidate the model's cached prompt prefix every time.
	r := New()
	for _, n := range []string{"zebra", "alpha", "middle"} {
		r.Register(Skill{Tool: llm.Tool{Name: n}})
	}
	tools := r.Tools()
	if len(tools) != 3 || tools[0].Name != "alpha" || tools[2].Name != "zebra" {
		t.Fatalf("tools not sorted: %+v", tools)
	}
}

func TestDevPathEscapeIsRefused(t *testing.T) {
	root := t.TempDir()
	d := &devSkills{root: root}

	for _, bad := range []string{"../../../etc/passwd", "../../etc", "/etc/passwd"} {
		got, err := d.resolve(bad)
		// Absolute and traversal paths are clamped into root, never outside it.
		if err == nil && !strings.HasPrefix(got, root) {
			t.Errorf("resolve(%q) escaped root: %q", bad, got)
		}
	}

	if _, err := d.resolve("subdir/file.go"); err != nil {
		t.Errorf("legitimate relative path rejected: %v", err)
	}
}

func TestWebSkillsWithoutKeyFailClearly(t *testing.T) {
	r := New()
	RegisterWeb(r, "") // no API key configured

	out, err := r.Execute(context.Background(), "web_search", map[string]any{"query": "test"})
	if err == nil {
		t.Fatalf("expected an error without a key, got %q", out)
	}
	if !strings.Contains(err.Error(), "SERPER_API_KEY") {
		t.Errorf("error should name the missing variable, got: %v", err)
	}
}

func TestNotesLifecycle(t *testing.T) {
	dir := t.TempDir()
	r := New()
	if err := RegisterNotes(r, dir); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := r.Execute(ctx, "note_add", map[string]any{"text": "buy cmake"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Execute(ctx, "note_add", map[string]any{
		"text": "call the bank", "due": "2h",
	}); err != nil {
		t.Fatal(err)
	}

	out, err := r.Execute(ctx, "note_list", map[string]any{"filter": "all"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "buy cmake") || !strings.Contains(out, "call the bank") {
		t.Fatalf("note_list missing entries: %s", out)
	}

	// Reminders must sort ahead of undated notes.
	if strings.Index(out, "call the bank") > strings.Index(out, "buy cmake") {
		t.Errorf("dated reminder should sort first:\n%s", out)
	}

	if out, err = r.Execute(ctx, "note_list", map[string]any{"filter": "reminders"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "buy cmake") {
		t.Errorf("undated note leaked into the reminders filter: %s", out)
	}

	if _, err := r.Execute(ctx, "note_done", map[string]any{"id": "nonexistent"}); err == nil {
		t.Error("marking an unknown id done should error")
	}
}

func TestNotesPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()

	r1 := New()
	if err := RegisterNotes(r1, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := r1.Execute(context.Background(), "note_add",
		map[string]any{"text": "survives restart"}); err != nil {
		t.Fatal(err)
	}

	r2 := New()
	if err := RegisterNotes(r2, dir); err != nil {
		t.Fatal(err)
	}
	out, err := r2.Execute(context.Background(), "note_list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "survives restart") {
		t.Fatalf("note lost across reopen: %s", out)
	}
}

func TestSystemStatusReportsSomething(t *testing.T) {
	out, err := systemStatus(context.Background(), nil)
	if err != nil {
		t.Skipf("system tools unavailable in this environment: %v", err)
	}
	if !strings.Contains(out, "Disk") && !strings.Contains(out, "Memory") {
		t.Errorf("status reported neither disk nor memory:\n%s", out)
	}
}
