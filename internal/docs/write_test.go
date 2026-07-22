package docs

import (
	"archive/zip"
	"image"
	"image/color"
	"image/png"
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

	var titles, headings, bullets, tables int
	var sawTableData bool
	for _, b := range blocks {
		switch b.Kind {
		case "title":
			titles++
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
	// The first top-level heading becomes the document Title; the "## Section"
	// becomes a Heading. That distinction is what makes a generated document
	// look like a document rather than a wall of same-sized bold text.
	if titles != 1 {
		t.Errorf("titles = %d, want 1 (the leading # becomes the title)", titles)
	}
	if headings != 1 {
		t.Errorf("headings = %d, want 1", headings)
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

// TestParseBlocksRecognisesImages covers the step that makes embedding
// reachable at all. The writer could always place a picture, but nothing turned
// what a model actually writes — markdown — into an image block, so the feature
// existed and could never be used.
func TestParseBlocksRecognisesImages(t *testing.T) {
	md := `# Report

![the rig](/home/akins/pictures/rig.png)

The bench is shown in ![this one](inline.png) above, mid-run.

[the datasheet](https://example.com/part.png)

![spaced](/home/akins/My Pictures/run 3.png)`

	var images, prose []string
	for _, b := range ParseBlocks(md) {
		switch b.Kind {
		case "image":
			images = append(images, b.Text)
		case "paragraph":
			if b.Text != "" {
				prose = append(prose, b.Text)
			}
		}
	}

	want := []string{
		"/home/akins/pictures/rig.png",
		"/home/akins/My Pictures/run 3.png", // spaces in a path are ordinary
	}
	if len(images) != len(want) {
		t.Fatalf("got %d image blocks %q, want %d", len(images), images, len(want))
	}
	for i, w := range want {
		if images[i] != w {
			t.Errorf("image %d = %q, want %q", i, images[i], w)
		}
	}

	// An image inside a sentence must leave the sentence alone: turning the
	// whole line into a picture would silently delete the prose around it.
	var sawSentence bool
	for _, p := range prose {
		if strings.Contains(p, "mid-run") {
			sawSentence = true
		}
	}
	if !sawSentence {
		t.Errorf("the sentence containing an inline image was swallowed: %q", prose)
	}
	// A link is not an image; a document cannot follow a hyperlink.
	for _, p := range prose {
		if strings.Contains(p, "example.com/part.png") {
			return
		}
	}
	t.Errorf("a plain link did not survive as prose: %q", prose)
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

// TestDOCXUsesNamedStyles is the difference between a document that looks like
// it has headings and one that actually has them. Without pStyle and outlineLvl,
// Word's navigation pane is empty and no table of contents can be generated.
func TestDOCXUsesNamedStyles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "styled.docx")
	if err := WriteDOCX(path, []Block{
		Title("Assignment"),
		Heading(1, "Introduction"),
		Bullet("a point"),
		Numbered("first step"),
	}); err != nil {
		t.Fatal(err)
	}

	parts := readParts(t, path)

	doc := parts["word/document.xml"]
	for _, want := range []string{
		`w:pStyle w:val="Title"`,
		`w:pStyle w:val="Heading1"`,
		`w:numId w:val="1"`, // real bullet list
		`w:numId w:val="2"`, // real numbered list
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("document.xml missing %s", want)
		}
	}
	// A literal bullet character means the list is fake.
	if strings.Contains(doc, "<w:t xml:space=\"preserve\">• ") {
		t.Error("bullets are typed characters, not list items")
	}

	styles, ok := parts["word/styles.xml"]
	if !ok {
		t.Fatal("no styles.xml — Word will fall back to defaults")
	}
	if !strings.Contains(styles, `w:outlineLvl`) {
		t.Error("headings have no outline level, so they will not appear in the navigation pane")
	}
	if !strings.Contains(styles, `w:name w:val="heading 1"`) {
		t.Error("style is not mapped to Word's built-in Heading 1 identity")
	}
	if _, ok := parts["word/numbering.xml"]; !ok {
		t.Error("no numbering.xml — list references will dangle")
	}
	if rels, ok := parts["word/_rels/document.xml.rels"]; !ok {
		t.Error("no document relationships — styles and numbering will be ignored")
	} else if !strings.Contains(rels, "styles.xml") || !strings.Contains(rels, "numbering.xml") {
		t.Error("relationships do not reference both parts")
	}
}

// TestXLSXIsStyled covers the things that make a spreadsheet readable rather
// than merely correct.
func TestXLSXIsStyled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "styled.xlsx")
	if err := WriteXLSX(path, []Sheet{{Name: "Data", Rows: [][]string{
		{"A Fairly Long Header Label", "Throughput"},
		{"Raft", "4500"},
		{"Paxos", "2100"},
	}}}); err != nil {
		t.Fatal(err)
	}

	parts := readParts(t, path)

	if _, ok := parts["xl/styles.xml"]; !ok {
		t.Fatal("no styles.xml — every format is ignored")
	}
	rels := parts["xl/_rels/workbook.xml.rels"]
	if !strings.Contains(rels, "styles.xml") {
		t.Error("styles part is not referenced, so Excel will not load it")
	}

	sheet := parts["xl/worksheets/sheet1.xml"]
	if !strings.Contains(sheet, "<cols>") {
		t.Error("no column widths — long labels will sit truncated on open")
	}
	if !strings.Contains(sheet, `state="frozen"`) {
		t.Error("header row is not frozen")
	}
	if !strings.Contains(sheet, `s="1"`) {
		t.Error("header cells carry no header style")
	}
	// Element order is fixed by the schema; out of order Excel rejects the file.
	viewsAt := strings.Index(sheet, "<sheetViews>")
	colsAt := strings.Index(sheet, "<cols>")
	dataAt := strings.Index(sheet, "<sheetData>")
	if !(viewsAt < colsAt && colsAt < dataAt) {
		t.Errorf("schema element order wrong: sheetViews=%d cols=%d sheetData=%d",
			viewsAt, colsAt, dataAt)
	}
}

func TestColumnWidthsTrackContent(t *testing.T) {
	widths := columnWidths([][]string{
		{"short", "a much longer header than the other one"},
		{"x", "y"},
	})
	if len(widths) != 2 {
		t.Fatalf("got %d widths", len(widths))
	}
	if widths[1] <= widths[0] {
		t.Errorf("wider content did not produce a wider column: %v", widths)
	}
	// Bounded at both ends: unbounded widths push later columns off screen.
	for _, w := range widths {
		if w < 9 || w > 55 {
			t.Errorf("width %.1f outside sensible bounds", w)
		}
	}
}

// readParts unzips a container into a name->content map.
func readParts(t *testing.T, path string) map[string]string {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	out := map[string]string{}
	for _, f := range r.File {
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

// TestContentTypesOrdering is a regression test. The OPC schema requires every
// <Default> to precede every <Override>; appending both to one buffer put a
// Default last and LibreOffice refused to open the file at all.
func TestContentTypesOrdering(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "pic.png")
	writeTinyPNG(t, img)

	path := filepath.Join(dir, "withimage.docx")
	if err := WriteDOCXWith(path, []Block{Paragraph("text"), Image(img)},
		DocOptions{PageSetup: PageSetup{Header: "H", Footer: "F", PageNumbers: true}}); err != nil {
		t.Fatal(err)
	}

	parts := readParts(t, path)
	ct := parts["[Content_Types].xml"]

	lastDefault := strings.LastIndex(ct, "<Default ")
	firstOverride := strings.Index(ct, "<Override ")
	if lastDefault == -1 || firstOverride == -1 {
		t.Fatalf("content types malformed: %s", ct)
	}
	if lastDefault > firstOverride {
		t.Errorf("a <Default> follows an <Override>, which the OPC schema forbids:\n%s", ct)
	}
	if !strings.Contains(ct, `Extension="png"`) {
		t.Error("image content type not declared")
	}
	for _, want := range []string{"header1.xml", "footer1.xml"} {
		if !strings.Contains(ct, want) {
			t.Errorf("%s not declared in content types", want)
		}
	}
}

// TestDocumentFurniture covers header, footer, page-number fields and images.
func TestDocumentFurniture(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "pic.png")
	writeTinyPNG(t, img)

	path := filepath.Join(dir, "furnished.docx")
	if err := WriteDOCXWith(path, []Block{Paragraph("body"), Image(img)},
		DocOptions{PageSetup: PageSetup{
			Header: "CS401", Footer: "A. Akins", PageNumbers: true,
		}}); err != nil {
		t.Fatal(err)
	}
	parts := readParts(t, path)

	if !strings.Contains(parts["word/header1.xml"], "CS401") {
		t.Error("header text missing")
	}
	footer := parts["word/footer1.xml"]
	// Page numbers must be field codes, not literal text, so they stay correct
	// as the document is edited.
	if !strings.Contains(footer, "PAGE") || !strings.Contains(footer, "NUMPAGES") {
		t.Error("page numbers are not field codes")
	}
	doc := parts["word/document.xml"]
	if !strings.Contains(doc, "headerReference") || !strings.Contains(doc, "footerReference") {
		t.Error("sectPr does not reference the header and footer parts")
	}
	if !strings.Contains(doc, "<w:drawing>") || !strings.Contains(doc, "r:embed=") {
		t.Error("image drawing missing from the document body")
	}
	if _, ok := parts["word/media/image1.png"]; !ok {
		t.Error("image bytes not embedded")
	}
}

func TestMissingImageDegradesGracefully(t *testing.T) {
	// A missing picture must not sink the whole document.
	path := filepath.Join(t.TempDir(), "broken.docx")
	if err := WriteDOCXWith(path,
		[]Block{Paragraph("before"), Image("/nonexistent/nope.png"), Paragraph("after")},
		DocOptions{}); err != nil {
		t.Fatalf("a missing image failed the whole write: %v", err)
	}
	doc, err := Extract(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"before", "after", "unavailable"} {
		if !strings.Contains(doc.Text, want) {
			t.Errorf("expected %q in output, got: %s", want, doc.Text)
		}
	}
}

func TestFormulasAndCharts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calc.xlsx")
	if err := WriteXLSX(path, []Sheet{{
		Name: "Data",
		Rows: [][]string{
			{"Item", "Value"},
			{"a", "10"},
			{"b", "20"},
			{"total", "=SUM(B2:B3)"},
		},
		Chart: &Chart{Kind: "bar", Title: "Values", CategoryColumn: 0, ValueColumns: []int{1}},
	}}); err != nil {
		t.Fatal(err)
	}
	parts := readParts(t, path)

	sheet := parts["xl/worksheets/sheet1.xml"]
	if !strings.Contains(sheet, "<f>SUM(B2:B3)</f>") {
		t.Error("formula not written as a formula")
	}
	// A cached value would be shown stale before recalculation.
	if strings.Contains(sheet, "<f>SUM(B2:B3)</f><v>") {
		t.Error("a cached value was written alongside the formula")
	}
	if !strings.Contains(sheet, "<drawing") {
		t.Error("sheet does not reference the drawing")
	}

	// All four chart pieces must be present, or the file opens with no chart.
	for _, part := range []string{
		"xl/charts/chart1.xml",
		"xl/drawings/drawing1.xml",
		"xl/drawings/_rels/drawing1.xml.rels",
		"xl/worksheets/_rels/sheet1.xml.rels",
	} {
		if _, ok := parts[part]; !ok {
			t.Errorf("missing chart part: %s", part)
		}
	}
	if !strings.Contains(parts["[Content_Types].xml"], "chart+xml") {
		t.Error("chart content type not declared")
	}
	if !strings.Contains(parts["xl/charts/chart1.xml"], "Values") {
		t.Error("chart title missing")
	}
}

// writeTinyPNG creates a minimal valid PNG for embedding tests.
func writeTinyPNG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 20, 10))
	for y := range 10 {
		for x := range 20 {
			img.Set(x, y, color.RGBA{100, 150, 200, 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// TestEmbeddedMediaIsDeflated is a regression test for a subtle container bug.
// Storing already-compressed images uncompressed seemed like a free
// optimisation, but Go emits a data descriptor for stored entries, leaving
// strict readers unable to find where the stream ends. LibreOffice refused the
// entire document — while our own reader, and every structural assertion,
// passed happily.
func TestEmbeddedMediaIsDeflated(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "pic.png")
	writeTinyPNG(t, img)

	path := filepath.Join(dir, "withimage.docx")
	if err := WriteDOCXWith(path, []Block{Image(img)}, DocOptions{}); err != nil {
		t.Fatal(err)
	}

	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	found := false
	for _, f := range r.File {
		if !strings.Contains(f.Name, "media/") {
			continue
		}
		found = true
		if f.Method != zip.Deflate {
			t.Errorf("%s uses compression method %d; must be Deflate (%d) so readers "+
				"can locate the end of the stream", f.Name, f.Method, zip.Deflate)
		}
		// A data descriptor is signalled by bit 3 of the general purpose flag.
		if f.Flags&0x8 != 0 && f.Method == zip.Store {
			t.Errorf("%s is stored with a data descriptor, which strict readers reject", f.Name)
		}
	}
	if !found {
		t.Fatal("no media part was embedded")
	}
}
