package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// probeDir holds documents produced by LibreOffice, so the parsers are tested
// against files real software actually emits rather than hand-built fixtures
// that only exercise the happy path.
const probeDir = "/tmp/claude-1000/-run-media-akins-Akins-Drive1-Development-JARVIS/efee1d9b-e792-4ccf-adc7-86ba3beaeb7c/scratchpad/docs"

func probe(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(probeDir, name)
	if _, err := os.Stat(p); err != nil {
		t.Skipf("probe document %s not present", name)
	}
	return p
}

func TestDetectFormats(t *testing.T) {
	cases := map[string]Format{
		"source.txt":  FormatText,
		"source.docx": FormatDOCX,
		"source.pdf":  FormatPDF,
		"data.xlsx":   FormatXLSX,
		"source.odt":  FormatODT,
		"archive.zip": FormatZIP,
	}
	for name, want := range cases {
		p := probe(t, name)
		got, err := Detect(p)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("%s detected as %s, want %s", name, got, want)
		}
	}
}

func TestExtractDOCX(t *testing.T) {
	doc, err := Extract(probe(t, "source.docx"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s", doc.Summary())
	for _, want := range []string{"Raft", "consensus", "quorum", "Paxos"} {
		if !strings.Contains(doc.Text, want) {
			t.Errorf("docx text missing %q\ngot: %s", want, clip(doc.Text, 300))
		}
	}
	// Paragraph structure must survive, not collapse into one line.
	if strings.Count(doc.Text, "\n") < 2 {
		t.Errorf("docx lost paragraph breaks: %q", clip(doc.Text, 200))
	}
}

func TestExtractPDF(t *testing.T) {
	doc, err := Extract(probe(t, "source.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s", doc.Summary())
	for _, want := range []string{"Raft", "consensus", "quorum"} {
		if !strings.Contains(doc.Text, want) {
			t.Errorf("pdf text missing %q\ngot: %s", want, clip(doc.Text, 300))
		}
	}
	if doc.Note == "" {
		t.Error("pdf extraction reported no page count")
	}
}

func TestExtractXLSX(t *testing.T) {
	doc, err := Extract(probe(t, "data.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s", doc.Summary())
	t.Logf("content:\n%s", clip(doc.Text, 400))

	// Shared strings are the part a naive reader misses entirely.
	for _, want := range []string{"Raft leader", "Paxos", "Gossip", "Throughput"} {
		if !strings.Contains(doc.Text, want) {
			t.Errorf("xlsx missing cell text %q — shared strings likely unresolved", want)
		}
	}
	// Numbers must come through too.
	for _, want := range []string{"4500", "2100", "9000"} {
		if !strings.Contains(doc.Text, want) {
			t.Errorf("xlsx missing numeric cell %q", want)
		}
	}
	if len(doc.Sections) == 0 {
		t.Error("xlsx produced no sheet sections")
	}
	// Rows must stay rows.
	if !strings.Contains(doc.Text, "\t") {
		t.Error("xlsx lost column separation")
	}
}

func TestExtractODT(t *testing.T) {
	doc, err := Extract(probe(t, "source.odt"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s", doc.Summary())
	for _, want := range []string{"Raft", "consensus"} {
		if !strings.Contains(doc.Text, want) {
			t.Errorf("odt text missing %q\ngot: %s", want, clip(doc.Text, 300))
		}
	}
}

func TestListZIP(t *testing.T) {
	doc, err := Extract(probe(t, "archive.zip"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s", clip(doc.Text, 300))
	for _, want := range []string{"source.txt", "data.csv", "entries"} {
		if !strings.Contains(doc.Text, want) {
			t.Errorf("zip listing missing %q", want)
		}
	}
}

func TestExtractPlainText(t *testing.T) {
	doc, err := Extract(probe(t, "source.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Text, "Raft algorithm") {
		t.Errorf("plain text mangled: %q", clip(doc.Text, 200))
	}
}

func TestBinaryFileIsRefusedGracefully(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "blob.bin")
	// NUL bytes are decisive: no text encoding in normal use emits one.
	if err := os.WriteFile(p, []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := Extract(p)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Format != FormatBinary {
		t.Errorf("format = %s, want binary", doc.Format)
	}
	if doc.Note == "" {
		t.Error("binary file gave no explanation")
	}
}

func TestMissingFileErrors(t *testing.T) {
	if _, err := Extract("/nonexistent/path/file.txt"); err == nil {
		t.Fatal("missing file did not error")
	}
}

func TestDirectoryIsRefused(t *testing.T) {
	if _, err := Extract(t.TempDir()); err == nil {
		t.Fatal("directory was accepted as a document")
	}
}

func TestIsProbablyText(t *testing.T) {
	if !isProbablyText([]byte("hello world\nsecond line\t tabbed")) {
		t.Error("plain prose rejected as binary")
	}
	if isProbablyText([]byte{'a', 'b', 0x00, 'c'}) {
		t.Error("NUL byte accepted as text")
	}
	if !isProbablyText(nil) {
		t.Error("empty input should be treated as text")
	}
}

func TestTidyTextCollapsesBlankRuns(t *testing.T) {
	got := tidyText("a\n\n\n\n\nb\n   \n\n c ")
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("blank runs survived: %q", got)
	}
	if !strings.Contains(got, "a") || !strings.Contains(got, "b") {
		t.Errorf("content lost: %q", got)
	}
}

func clip(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
