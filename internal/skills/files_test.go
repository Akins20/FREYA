package skills

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/guard"
)

// approveAll is a guard that says yes, so these tests exercise the file
// operations themselves rather than re-testing the permission layer.
func approveAll() *guard.Guard {
	return guard.New(func(context.Context, guard.Action, guard.Assessment) bool { return true }, nil)
}

func fileReg(t *testing.T) *Registry {
	t.Helper()
	r := New()
	RegisterFiles(r, approveAll(), nil)
	return r
}

func TestWriteReadRoundTrip(t *testing.T) {
	r := fileReg(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "note.txt")

	if _, err := r.Execute(ctx, "file_write", map[string]any{
		"path": path, "content": "line one\nline two\nline three\n",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := r.Execute(ctx, "file_read", map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"line one", "line two", "line three"} {
		if !strings.Contains(out, want) {
			t.Errorf("read output missing %q:\n%s", want, out)
		}
	}
	// Line numbers make later surgical edits possible.
	if !strings.Contains(out, "     1  ") {
		t.Errorf("read output lacks line numbers:\n%s", out)
	}
}

// TestSurgicalEditRefusesAmbiguity is the important one: several matches means
// several possible meanings, and guessing corrupts files silently.
func TestSurgicalEditRefusesAmbiguity(t *testing.T) {
	r := fileReg(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "code.go")

	content := "foo := 1\nbar := 2\nfoo := 3\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := r.Execute(ctx, "file_edit", map[string]any{
		"path": path, "old_text": "foo", "new_text": "baz",
	})
	if err == nil {
		t.Fatal("ambiguous edit was accepted")
	}
	if !strings.Contains(err.Error(), "appears 2 times") {
		t.Errorf("error should name the ambiguity, got: %v", err)
	}

	// The file must be untouched after a refusal.
	after, _ := os.ReadFile(path)
	if string(after) != content {
		t.Error("file was modified despite the edit being refused")
	}
}

func TestSurgicalEditWithUniqueAnchor(t *testing.T) {
	r := fileReg(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "code.go")
	if err := os.WriteFile(path, []byte("foo := 1\nbar := 2\nfoo := 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// More context makes the target unique.
	if _, err := r.Execute(ctx, "file_edit", map[string]any{
		"path": path, "old_text": "bar := 2\nfoo := 3", "new_text": "bar := 2\nfoo := 99",
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !strings.Contains(string(after), "foo := 99") {
		t.Errorf("edit did not apply: %q", after)
	}
	if !strings.Contains(string(after), "foo := 1") {
		t.Errorf("edit hit the wrong occurrence: %q", after)
	}
}

func TestEditAllOccurrences(t *testing.T) {
	r := fileReg(t)
	path := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(path, []byte("a\na\na\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Execute(context.Background(), "file_edit", map[string]any{
		"path": path, "old_text": "a", "new_text": "b", "all": true,
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if strings.Contains(string(after), "a") {
		t.Errorf("all=true left occurrences behind: %q", after)
	}
}

func TestEditMissingTextErrorsClearly(t *testing.T) {
	r := fileReg(t)
	path := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := r.Execute(context.Background(), "file_edit", map[string]any{
		"path": path, "old_text": "goodbye", "new_text": "x",
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected a clear not-found error, got %v", err)
	}
}

func TestEditPreservesFileMode(t *testing.T) {
	r := fileReg(t)
	path := filepath.Join(t.TempDir(), "script.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Execute(context.Background(), "file_edit", map[string]any{
		"path": path, "old_text": "echo hi", "new_text": "echo bye",
	}); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode became %o, want 755 — an edited script must stay executable",
			info.Mode().Perm())
	}
}

func TestLargeFileEdit(t *testing.T) {
	r := fileReg(t)
	path := filepath.Join(t.TempDir(), "big.txt")

	var sb strings.Builder
	for i := range 50000 {
		sb.WriteString("line ")
		sb.WriteString(strings.Repeat("x", 20))
		if i == 25000 {
			sb.WriteString(" UNIQUE_ANCHOR_HERE")
		}
		sb.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Execute(context.Background(), "file_edit", map[string]any{
		"path": path, "old_text": "UNIQUE_ANCHOR_HERE", "new_text": "REPLACED",
	}); err != nil {
		t.Fatalf("edit on a 50k-line file failed: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !strings.Contains(string(after), "REPLACED") {
		t.Error("large-file edit did not apply")
	}
	if strings.Contains(string(after), "UNIQUE_ANCHOR_HERE") {
		t.Error("original anchor survived")
	}
	if n := strings.Count(string(after), "\n"); n != 50000 {
		t.Errorf("line count changed to %d, want 50000 — the edit was not surgical", n)
	}
}

func TestFolderListAndRecursion(t *testing.T) {
	r := fileReg(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a.go", "b.txt", "sub/c.go", "sub/deep/d.go"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()

	flat, err := r.Execute(ctx, "folder_list", map[string]any{"path": dir})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(flat, "deep/d.go") {
		t.Error("non-recursive listing descended into subdirectories")
	}

	deep, err := r.Execute(ctx, "folder_list", map[string]any{
		"path": dir, "recursive": true, "pattern": "*.go",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"a.go", "c.go", "d.go"} {
		if !strings.Contains(deep, want) {
			t.Errorf("recursive listing missing %q:\n%s", want, deep)
		}
	}
	if strings.Contains(deep, "b.txt") {
		t.Error("pattern filter did not exclude b.txt")
	}
}

func TestDeleteRefusesNonEmptyDirWithoutRecursive(t *testing.T) {
	r := fileReg(t)
	dir := t.TempDir()
	sub := filepath.Join(dir, "full")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := r.Execute(context.Background(), "file_delete", map[string]any{"path": sub})
	if err == nil {
		t.Fatal("deleted a non-empty directory without recursive=true")
	}
	if _, statErr := os.Stat(sub); statErr != nil {
		t.Error("directory was removed despite the refusal")
	}
}

func TestDeleteGlobExpands(t *testing.T) {
	r := fileReg(t)
	dir := t.TempDir()
	for _, n := range []string{"a.log", "b.log", "keep.md"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Execute(context.Background(), "file_delete", map[string]any{
		"path": filepath.Join(dir, "*.log"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.md")); err != nil {
		t.Error("glob deletion removed a non-matching file")
	}
	if _, err := os.Stat(filepath.Join(dir, "a.log")); err == nil {
		t.Error("glob deletion did not remove matches")
	}
}

func TestCopyMoveDirectory(t *testing.T) {
	r := fileReg(t)
	ctx := context.Background()
	base := t.TempDir()
	src := filepath.Join(base, "src")
	if err := os.MkdirAll(filepath.Join(src, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "nested", "f.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(base, "copy")
	if _, err := r.Execute(ctx, "file_copy", map[string]any{
		"source": src, "destination": dst,
	}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "nested", "f.txt")); err != nil || string(b) != "data" {
		t.Errorf("nested file not copied: %v", err)
	}

	moved := filepath.Join(base, "moved")
	if _, err := r.Execute(ctx, "file_move", map[string]any{
		"source": dst, "destination": moved,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("source still present after a move")
	}
	if _, err := os.Stat(filepath.Join(moved, "nested", "f.txt")); err != nil {
		t.Error("moved tree is incomplete")
	}
}

func TestArchiveRoundTrip(t *testing.T) {
	r := fileReg(t)
	ctx := context.Background()
	base := t.TempDir()
	src := filepath.Join(base, "project")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"one.txt", "sub/two.txt"} {
		if err := os.WriteFile(filepath.Join(src, p), []byte("content of "+p), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	zipPath := filepath.Join(base, "out.zip")
	if _, err := r.Execute(ctx, "archive_create", map[string]any{
		"source": src, "destination": zipPath,
	}); err != nil {
		t.Fatal(err)
	}

	// Reading the archive should describe it without extracting.
	listing, err := r.Execute(ctx, "file_read", map[string]any{"path": zipPath})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listing, "one.txt") {
		t.Errorf("archive listing incomplete:\n%s", listing)
	}

	out := filepath.Join(base, "extracted")
	if _, err := r.Execute(ctx, "archive_extract", map[string]any{
		"path": zipPath, "destination": out,
	}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(out, "project", "sub", "two.txt"))
	if err != nil {
		t.Fatalf("extracted tree incomplete: %v", err)
	}
	if !strings.Contains(string(b), "two.txt") {
		t.Errorf("extracted content wrong: %q", b)
	}
}

// TestZipSlipIsRefused covers the classic archive attack: an entry named
// "../../etc/passwd" that writes outside the destination when extracted.
func TestZipSlipIsRefused(t *testing.T) {
	base := t.TempDir()
	malicious := filepath.Join(base, "evil.zip")
	if err := writeEvilZip(malicious); err != nil {
		t.Fatal(err)
	}

	r := fileReg(t)
	dest := filepath.Join(base, "out")
	_, err := r.Execute(context.Background(), "archive_extract", map[string]any{
		"path": malicious, "destination": dest,
	})
	if err == nil {
		t.Fatal("zip-slip entry was extracted without complaint")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("error should name the escape, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(base, "escaped.txt")); statErr == nil {
		t.Fatal("ESCAPED: file was written outside the destination directory")
	}
}

func TestReadHandlesMissingFile(t *testing.T) {
	r := fileReg(t)
	if _, err := r.Execute(context.Background(), "file_read",
		map[string]any{"path": "/nonexistent/nope.txt"}); err == nil {
		t.Fatal("reading a missing file did not error")
	}
}

func TestAppendCreatesAndExtends(t *testing.T) {
	r := fileReg(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "log.txt")

	for _, s := range []string{"first\n", "second\n"} {
		if _, err := r.Execute(ctx, "file_append", map[string]any{
			"path": path, "content": s,
		}); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(path)
	if string(b) != "first\nsecond\n" {
		t.Errorf("append produced %q", b)
	}
}

// TestContentIsNotTrimmed is a regression test. argString trims whitespace,
// which silently destroyed trailing newlines on write/append and — far worse —
// stripped leading indentation from surgical-edit anchors, so an edit could
// miss or match the wrong line in indented code.
func TestContentIsNotTrimmed(t *testing.T) {
	r := fileReg(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "indented.go")

	original := "func main() {\n    if x {\n        doThing()\n    }\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	// The anchor's leading indentation is load-bearing.
	if _, err := r.Execute(ctx, "file_edit", map[string]any{
		"path":     path,
		"old_text": "        doThing()",
		"new_text": "        doOtherThing()",
	}); err != nil {
		t.Fatalf("indented edit failed: %v", err)
	}

	after, _ := os.ReadFile(path)
	if !strings.Contains(string(after), "        doOtherThing()") {
		t.Errorf("indentation was not preserved:\n%q", after)
	}
	if strings.Contains(string(after), "\ndoOtherThing()") {
		t.Error("indentation was stripped from the replacement")
	}

	// Trailing newlines must survive a write verbatim.
	p2 := filepath.Join(t.TempDir(), "trailing.txt")
	if _, err := r.Execute(ctx, "file_write", map[string]any{
		"path": p2, "content": "line\n\n",
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p2)
	if string(b) != "line\n\n" {
		t.Errorf("trailing newlines lost: %q", b)
	}
}

// TestDocxWriteEmbedsImageAndFurniture is the end-to-end proof that the writer's
// richer features are actually reachable. Both existed, both were tested inside
// the docs package, and neither could be triggered from a conversation because
// the skill only ever called the plain writer with no options.
func TestDocxWriteEmbedsImageAndFurniture(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "bench.png")
	pixels := writeTestPNG(t, img)

	r := New()
	RegisterDocWriting(r, approveAll())
	path := filepath.Join(dir, "report.docx")

	out, err := r.Execute(context.Background(), "docx_write", map[string]any{
		"path": path,
		"content": "# Findings\n\n![the bench](" + img +
			")\n\nThe run completed in twelve seconds.",
		"header":       "CS401 Coursework",
		"footer":       "A. Akins",
		"page_numbers": true,
	})
	if err != nil {
		t.Fatalf("docx_write failed: %v", err)
	}
	t.Logf("%s", out)

	parts := unzip(t, path)

	// The picture must arrive byte-identical: a media part that merely exists
	// can still be a truncated or re-encoded file that Word refuses to draw.
	var media string
	for name := range parts {
		if strings.HasPrefix(name, "word/media/") {
			media = name
		}
	}
	if media == "" {
		t.Fatal("no word/media entry — the markdown image never reached the writer")
	}
	if parts[media] != string(pixels) {
		t.Errorf("%s is %d bytes, want the original %d", media, len(parts[media]), len(pixels))
	}
	// Without the Default the package is malformed and Word calls it corrupt.
	if !strings.Contains(parts["[Content_Types].xml"], `Extension="png"`) {
		t.Errorf("png content type not declared:\n%s", parts["[Content_Types].xml"])
	}
	if !strings.Contains(parts["word/document.xml"], "r:embed=") {
		t.Error("document body has no picture reference")
	}

	if !strings.Contains(parts["word/header1.xml"], "CS401 Coursework") {
		t.Error("header text did not reach the header part")
	}
	footer := parts["word/footer1.xml"]
	if !strings.Contains(footer, "A. Akins") {
		t.Error("footer text did not reach the footer part")
	}
	// Field codes, not literal text, so the numbering survives editing.
	if !strings.Contains(footer, "PAGE") || !strings.Contains(footer, "NUMPAGES") {
		t.Errorf("page numbers are not field codes:\n%s", footer)
	}
}

// TestDocxWriteWithoutOptionsIsUnchanged pins the compatibility half of the
// change: adding the furniture arguments must not start emitting header and
// footer parts for every document that never asked for them.
func TestDocxWriteWithoutOptionsIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.docx")

	r := New()
	RegisterDocWriting(r, approveAll())
	if _, err := r.Execute(context.Background(), "docx_write", map[string]any{
		"path": path, "content": "# Plain\n\nNothing special here.",
	}); err != nil {
		t.Fatal(err)
	}

	parts := unzip(t, path)
	for _, unwanted := range []string{"word/header1.xml", "word/footer1.xml"} {
		if _, ok := parts[unwanted]; ok {
			t.Errorf("%s written for a document that requested no furniture", unwanted)
		}
	}
	if strings.Contains(parts["word/document.xml"], "headerReference") {
		t.Error("sectPr references a header that was never asked for")
	}
}

// writeTestPNG puts a real image on disk and returns its bytes, so an embedding
// test can assert the file arrived intact rather than merely that something
// image-shaped was zipped.
func writeTestPNG(t *testing.T, path string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 9))
	for y := range 9 {
		for x := range 16 {
			img.Set(x, y, color.RGBA{200, 80, 40, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// unzip reads a container into a name->content map. The parts are compressed,
// so the raw file bytes contain nothing readable.
func unzip(t *testing.T, path string) map[string]string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = string(b)
	}
	return out
}
