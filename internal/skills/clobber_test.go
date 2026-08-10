package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/guard"
)

func writeSkills(t *testing.T) *Registry {
	t.Helper()
	r := New()
	g := guard.New(func(context.Context, guard.Action, guard.Assessment) bool { return true }, nil)
	places, err := RegisterPlaces(r, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	RegisterFiles(r, g, places)
	return r
}

// "Do NOT rewrite the whole file with file_write" was prose, and the result read
// "Wrote N bytes" whether it created something new or flattened four hundred
// lines of the user's work down to fifty. Invisible from both directions: nothing
// checked before, nothing reported after.
func TestReplacingMostOfAFileSaysSoAndKeepsACopy(t *testing.T) {
	r := writeSkills(t)
	path := filepath.Join(t.TempDir(), "notes.md")

	var big strings.Builder
	for i := 0; i < 200; i++ {
		big.WriteString("a line of the user's actual work\n")
	}
	if err := os.WriteFile(path, []byte(big.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := r.Execute(context.Background(), "file_write",
		map[string]any{"path": path, "content": "just this one line\n", "replace": true})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out, "200 lines") {
		t.Errorf("the result does not say what was there before: %s", out)
	}
	if !strings.Contains(out, "removed most of the file") {
		t.Errorf("a drastic replacement was not flagged: %s", out)
	}
	if !strings.Contains(out, "file_edit") {
		t.Errorf("the result does not point at the safer tool: %s", out)
	}

	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("no copy was kept of 200 lines of work: %v", err)
	}
	if !strings.Contains(string(backup), "the user's actual work") {
		t.Error("the backup does not contain what was replaced")
	}
}

// A new file must not be described as a replacement, and must not leave a .bak.
func TestCreatingAFileIsNotReportedAsDestruction(t *testing.T) {
	r := writeSkills(t)
	path := filepath.Join(t.TempDir(), "fresh.txt")

	out, err := r.Execute(context.Background(), "file_write",
		map[string]any{"path": path, "content": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Created") {
		t.Errorf("a new file was not reported as created: %s", out)
	}
	if strings.Contains(out, "Replaced") || strings.Contains(out, "removed most") {
		t.Errorf("a new file was described as destroying something: %s", out)
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("a new file left a backup behind")
	}
}

// An ordinary edit-by-rewrite must not be treated as a catastrophe. A .bak beside
// every file she touches is litter, and litter gets ignored — which is how a
// safety net stops being one.
func TestAnOrdinaryRewriteIsReportedButNotBackedUp(t *testing.T) {
	r := writeSkills(t)
	path := filepath.Join(t.TempDir(), "cfg.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("setting = value\n", 40)), 0o644); err != nil {
		t.Fatal(err)
	}

	// Same size, different content — a legitimate whole-file rewrite.
	out, err := r.Execute(context.Background(), "file_write",
		map[string]any{"path": path, "content": strings.Repeat("setting = other\n", 40), "replace": true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Replaced") {
		t.Errorf("the replacement was not reported at all: %s", out)
	}
	if strings.Contains(out, "removed most") {
		t.Errorf("a same-size rewrite was flagged as destructive: %s", out)
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("an ordinary rewrite left litter behind")
	}
}

// A short file shrinking is normal — a one-line note becoming a shorter one is
// not the failure this is about.
func TestAShortFileIsNotProtected(t *testing.T) {
	r := writeSkills(t)
	path := filepath.Join(t.TempDir(), "todo.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Execute(context.Background(), "file_write",
		map[string]any{"path": path, "content": "one\n", "replace": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("a three-line file produced a backup")
	}
}

// The stronger half: replacing something that exists must be asked for, not
// merely reported afterwards. By the time the report is written the other
// version is gone.
func TestOverwritingAnExistingFileMustBeMeant(t *testing.T) {
	r := writeSkills(t)
	path := filepath.Join(t.TempDir(), "work.md")
	if err := os.WriteFile(path, []byte("the user's work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := r.Execute(context.Background(), "file_write",
		map[string]any{"path": path, "content": "oops"})
	if err == nil {
		t.Fatal("an existing file was overwritten without being asked for")
	}
	if !strings.Contains(err.Error(), "file_edit") {
		t.Errorf("the refusal does not offer the surgical alternative: %v", err)
	}

	// The file is untouched.
	kept, _ := os.ReadFile(path)
	if string(kept) != "the user's work\n" {
		t.Errorf("the refusal still altered the file: %q", kept)
	}

	// And saying so lets it through.
	if _, err := r.Execute(context.Background(), "file_write",
		map[string]any{"path": path, "content": "deliberate", "replace": true}); err != nil {
		t.Errorf("an explicit replace was still refused: %v", err)
	}
}

// Rename onto an existing path is os.Rename, which destroys the destination
// without a word. Same shape, same cure.
func TestMovingOntoAnExistingFileMustBeMeant(t *testing.T) {
	r := writeSkills(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "new.txt")
	dst := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(src, []byte("incoming"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("do not lose me"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Execute(context.Background(), "file_move",
		map[string]any{"source": src, "destination": dst}); err == nil {
		t.Fatal("a move silently destroyed the destination")
	}
	kept, _ := os.ReadFile(dst)
	if string(kept) != "do not lose me" {
		t.Errorf("the destination was altered anyway: %q", kept)
	}

	if _, err := r.Execute(context.Background(), "file_move",
		map[string]any{"source": src, "destination": dst, "replace": true}); err != nil {
		t.Errorf("an explicit replace was refused: %v", err)
	}
}

// Copy has the same hole.
func TestCopyingOntoAnExistingFileMustBeMeant(t *testing.T) {
	r := writeSkills(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	dst := filepath.Join(dir, "b.txt")
	os.WriteFile(src, []byte("source"), 0o644)
	os.WriteFile(dst, []byte("precious"), 0o644)

	if _, err := r.Execute(context.Background(), "file_copy",
		map[string]any{"source": src, "destination": dst}); err == nil {
		t.Fatal("a copy silently destroyed the destination")
	}
	kept, _ := os.ReadFile(dst)
	if string(kept) != "precious" {
		t.Errorf("the destination was altered anyway: %q", kept)
	}
}

// Moving to a free name is ordinary work and must not need a flag.
func TestMovingToAFreeNameIsUnaffected(t *testing.T) {
	r := writeSkills(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	os.WriteFile(src, []byte("x"), 0o644)
	if _, err := r.Execute(context.Background(), "file_move",
		map[string]any{"source": src, "destination": filepath.Join(dir, "free.txt")}); err != nil {
		t.Errorf("an ordinary rename was refused: %v", err)
	}
}

// A document writer replaces the file at that path as completely as file_write
// does, and had none of its guards — so "write up my notes into a document"
// twice, with the same name, silently destroyed the first one.
func TestADocumentWriterWillNotSilentlyReplaceADocument(t *testing.T) {
	r := New()
	g := guard.New(func(context.Context, guard.Action, guard.Assessment) bool { return true }, nil)
	RegisterDocWriting(r, g)

	path := filepath.Join(t.TempDir(), "report.docx")
	if err := os.WriteFile(path, []byte("the first version"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := r.Execute(context.Background(), "docx_write",
		map[string]any{"path": path, "content": "# a second go"})
	if err == nil {
		t.Fatal("an existing document was replaced without being asked for")
	}
	kept, _ := os.ReadFile(path)
	if string(kept) != "the first version" {
		t.Errorf("the original was altered anyway: %q", kept)
	}

	if _, err := r.Execute(context.Background(), "docx_write",
		map[string]any{"path": path, "content": "# a second go", "replace": true}); err != nil {
		t.Errorf("an explicit replace was refused: %v", err)
	}
}

// A surgical edit on a binary is not surgical, it is destruction.
//
// file_edit reads a file, replaces a run of bytes and writes it back. On text
// that is exactly right. On a .docx or .xlsx — zip archives with checksums and a
// central directory — changing any byte invalidates the container and the file
// no longer opens. The occurrence count protected against editing the WRONG
// text; nothing protected against editing something that was never text.
func TestASurgicalEditRefusesBinaryDocuments(t *testing.T) {
	r := writeSkills(t)
	dir := t.TempDir()

	// A real zip container, as every Office document is.
	docx := filepath.Join(dir, "report.docx")
	if err := os.WriteFile(docx, []byte("PK\x03\x04\x00\x00binary\x00content"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := r.Execute(context.Background(), "file_edit",
		map[string]any{"path": docx, "old_text": "binary", "new_text": "text"})
	if err == nil {
		t.Fatal("a .docx was edited as text — that corrupts it beyond opening")
	}
	if !strings.Contains(err.Error(), "docx_write") {
		t.Errorf("the refusal does not say how to do it properly: %v", err)
	}

	// Content-sniffed too, for anything without a telltale extension.
	blob := filepath.Join(dir, "data.bin")
	os.WriteFile(blob, []byte("some\x00thing"), 0o644)
	if _, err := r.Execute(context.Background(), "file_edit",
		map[string]any{"path": blob, "old_text": "some", "new_text": "any"}); err == nil {
		t.Error("a file containing NUL bytes was edited as text")
	}

	// And ordinary text is untouched by any of this.
	txt := filepath.Join(dir, "notes.md")
	os.WriteFile(txt, []byte("hello world\n"), 0o644)
	if _, err := r.Execute(context.Background(), "file_edit",
		map[string]any{"path": txt, "old_text": "world", "new_text": "there"}); err != nil {
		t.Errorf("an ordinary text edit was refused: %v", err)
	}
}
