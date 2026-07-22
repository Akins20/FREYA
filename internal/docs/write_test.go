package docs

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The decisive test for a document writer is not "did it produce bytes" but
// "can the file be read back, and does a real Office implementation accept it".
// Round-tripping through our own reader proves structure; LibreOffice proves
// validity.

func TestWriteDOCXRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.docx")

	blocks := []Block{
		Heading(1, "Consensus in Distributed Systems"),
		Paragraph("Raft decomposes consensus into leader election and log replication."),
		Paragraph(""),
		Heading(2, "Findings"),
		Bullet("Quorum size must exceed half the cluster"),
		Bullet("Paxos is harder to reason about"),
		Table([][]string{
			{"Component", "Latency", "Throughput"},
			{"Raft", "12", "4500"},
			{"Gossip", "8", "9000"},
		}),
	}
	if err := WriteDOCX(path, blocks); err != nil {
		t.Fatalf("write: %v", err)
	}

	// It must be detected as a docx, not merely as a zip.
	format, err := Detect(path)
	if err != nil {
		t.Fatal(err)
	}
	if format != FormatDOCX {
		t.Fatalf("written file detected as %s", format)
	}

	doc, err := Extract(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	t.Logf("%s", doc.Summary())

	for _, want := range []string{
		"Consensus in Distributed Systems", "Raft decomposes", "Findings",
		"Quorum size", "Paxos", "Component", "Gossip", "9000",
	} {
		if !strings.Contains(doc.Text, want) {
			t.Errorf("round-trip lost %q\ngot: %s", want, clip(doc.Text, 400))
		}
	}
}

func TestWriteXLSXRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.xlsx")

	sheets := []Sheet{
		{Name: "Results", Rows: [][]string{
			{"Component", "Latency ms", "Throughput", "Notes"},
			{"Raft leader", "12", "4500", "baseline"},
			{"Paxos", "31", "2100", "harder"},
			{"Gossip", "8", "9000", "eventual"},
		}},
		{Name: "Summary", Rows: [][]string{
			{"Metric", "Value"},
			{"Mean latency", "17"},
			{"Best throughput", "9000"},
		}},
	}
	if err := WriteXLSX(path, sheets); err != nil {
		t.Fatalf("write: %v", err)
	}

	format, err := Detect(path)
	if err != nil {
		t.Fatal(err)
	}
	if format != FormatXLSX {
		t.Fatalf("written file detected as %s", format)
	}

	doc, err := Extract(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	t.Logf("%s", doc.Summary())

	if len(doc.Sections) != 2 {
		t.Errorf("got %d sheets, want 2", len(doc.Sections))
	}
	// Sheet names must survive into the workbook part.
	if len(doc.Sections) > 0 && doc.Sections[0].Name != "Results" {
		t.Errorf("first sheet named %q, want Results", doc.Sections[0].Name)
	}
	for _, want := range []string{"Raft leader", "Gossip", "4500", "9000", "Mean latency"} {
		if !strings.Contains(doc.Text, want) {
			t.Errorf("round-trip lost %q\ngot: %s", want, clip(doc.Text, 400))
		}
	}
}

// TestNumbersStoredAsNumbers matters because a spreadsheet of numeric-looking
// strings cannot be summed or charted, which defeats the point of xlsx.
func TestNumbersStoredAsNumbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "n.xlsx")
	if err := WriteXLSX(path, []Sheet{{Name: "S", Rows: [][]string{
		{"label", "42", "3.14", "007", "not a number", "-5"},
	}}}); err != nil {
		t.Fatal(err)
	}

	// The parts are compressed inside the container, so the raw file bytes
	// contain no readable XML — the sheet part has to be decompressed first.
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	var content string
	for _, f := range zr.File {
		if f.Name == "xl/worksheets/sheet1.xml" {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatal(err)
			}
			content = string(b)
		}
	}
	if content == "" {
		t.Fatal("sheet1.xml not found in the container")
	}
	t.Logf("sheet xml: %s", clip(content, 300))

	// Numeric cells carry no t attribute; text cells are inline strings.
	if !strings.Contains(content, "<v>42</v>") {
		t.Error("42 was not stored as a number")
	}
	if !strings.Contains(content, "<v>3.14</v>") {
		t.Error("3.14 was not stored as a number")
	}
	if !strings.Contains(content, "<v>-5</v>") {
		t.Error("-5 was not stored as a number")
	}
	// A leading zero signals an identifier, not a quantity.
	if strings.Contains(content, "<v>007</v>") {
		t.Error(`"007" was coerced to a number, losing the leading zeros`)
	}
}

func TestColumnNaming(t *testing.T) {
	cases := map[int]string{0: "A", 1: "B", 25: "Z", 26: "AA", 27: "AB", 51: "AZ", 52: "BA"}
	for in, want := range cases {
		if got := columnName(in); got != want {
			t.Errorf("columnName(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestXMLEscaping guards against content that would otherwise produce a file
// Word declares corrupt.
func TestXMLEscaping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "esc.docx")
	nasty := `Tom & Jerry <script>alert("x")</script> 'quoted' "double"`

	if err := WriteDOCX(path, []Block{Paragraph(nasty)}); err != nil {
		t.Fatal(err)
	}
	doc, err := Extract(path)
	if err != nil {
		t.Fatalf("a document with XML metacharacters could not be read back: %v", err)
	}
	for _, want := range []string{"Tom & Jerry", "script", "quoted"} {
		if !strings.Contains(doc.Text, want) {
			t.Errorf("escaping lost %q: %s", want, doc.Text)
		}
	}
}

func TestControlCharactersStripped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ctl.docx")
	// OOXML rejects control characters; Word refuses the whole file.
	if err := WriteDOCX(path, []Block{Paragraph("before\x07\x00after")}); err != nil {
		t.Fatal(err)
	}
	doc, err := Extract(path)
	if err != nil {
		t.Fatalf("control characters produced an unreadable file: %v", err)
	}
	if !strings.Contains(doc.Text, "before") || !strings.Contains(doc.Text, "after") {
		t.Errorf("surrounding text lost: %q", doc.Text)
	}
}

func TestParseBlocksFromMarkdown(t *testing.T) {
	md := `# Title

Some prose here.

## Section

- first bullet
- second bullet

| Name | Value |
|------|-------|
| a    | 1     |
| b    | 2     |

Closing **bold** paragraph.`

	blocks := ParseBlocks(md)

	var headings, bullets, tables int
	var sawTableData bool
	for _, b := range blocks {
		switch b.Kind {
		case "heading":
			headings++
		case "bullet":
			bullets++
		case "table":
			tables++
			for _, row := range b.Rows {
				for _, c := range row {
					if c == "a" || c == "1" {
						sawTableData = true
					}
				}
			}
			// The |---| separator must not become a data row.
			for _, row := range b.Rows {
				for _, c := range row {
					if strings.Contains(c, "---") {
						t.Errorf("separator row leaked into table data: %v", row)
					}
				}
			}
		}
	}
	if headings != 2 {
		t.Errorf("headings = %d, want 2", headings)
	}
	if bullets != 2 {
		t.Errorf("bullets = %d, want 2", bullets)
	}
	if tables != 1 {
		t.Errorf("tables = %d, want 1", tables)
	}
	if !sawTableData {
		t.Error("table data was lost")
	}

	// Emphasis markers must not survive into the document.
	for _, b := range blocks {
		if strings.Contains(b.Text, "**") {
			t.Errorf("emphasis markers left in text: %q", b.Text)
		}
	}
}

func TestWriteToNestedPathCreatesParents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "c", "deep.docx")
	if err := WriteDOCX(path, []Block{Paragraph("hi")}); err != nil {
		t.Fatalf("nested write failed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
