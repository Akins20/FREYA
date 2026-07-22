package docs

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Writing Office documents.
//
// The same insight that makes reading cheap makes writing cheap: docx and xlsx
// are ZIP containers of XML, so `archive/zip` plus careful string building
// produces a genuinely valid file with no LibreOffice round-trip.
//
// What is produced here is deliberately plain — paragraphs, headings, tables,
// cells and numbers. Anything richer (themes, styles, charts) means shipping a
// styles part and a theme part, and a document that merely *opens* while
// looking wrong is worse than one that is honestly simple.
//
// Both writers emit the minimal valid part set. Every part is required: omit
// [Content_Types].xml and Word reports the file as corrupt rather than
// degrading.

// Block is one element of a document.
type Block struct {
	// Kind is "heading", "paragraph", "bullet" or "table".
	Kind string
	// Level is the heading depth, 1 to 3.
	Level int
	// Text is the content for non-table blocks.
	Text string
	// Rows holds table data, first row treated as the header.
	Rows [][]string
}

// Heading builds a heading block.
func Heading(level int, text string) Block {
	return Block{Kind: "heading", Level: level, Text: text}
}

// Paragraph builds a body paragraph.
func Paragraph(text string) Block { return Block{Kind: "paragraph", Text: text} }

// Bullet builds a bulleted line.
func Bullet(text string) Block { return Block{Kind: "bullet", Text: text} }

// Table builds a table block.
func Table(rows [][]string) Block { return Block{Kind: "table", Rows: rows} }

// esc XML-escapes text for insertion into a document part.
func esc(s string) string {
	var buf bytes.Buffer
	// Strip control characters: OOXML rejects them and Word refuses the file.
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return -1
		}
		return r
	}, s)
	_ = xml.EscapeText(&buf, []byte(cleaned))
	return buf.String()
}

// --- docx -------------------------------------------------------------------

const docxContentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`

const docxRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

// headingSize maps a heading level to half-points, which is the unit OOXML
// uses for w:sz. 32 half-points is 16pt.
var headingSize = map[int]int{1: 32, 2: 28, 3: 24}

// WriteDOCX creates a Word document from blocks.
func WriteDOCX(path string, blocks []Block) error {
	var body strings.Builder

	for _, b := range blocks {
		switch b.Kind {
		case "heading":
			level := b.Level
			if level < 1 || level > 3 {
				level = 1
			}
			// Direct formatting rather than named styles: a styles part would
			// have to be shipped and kept consistent, and bold-and-larger reads
			// correctly everywhere without one.
			fmt.Fprintf(&body,
				`<w:p><w:pPr><w:spacing w:before="240" w:after="120"/></w:pPr>`+
					`<w:r><w:rPr><w:b/><w:sz w:val="%d"/></w:rPr>`+
					`<w:t xml:space="preserve">%s</w:t></w:r></w:p>`,
				headingSize[level], esc(b.Text))

		case "bullet":
			fmt.Fprintf(&body,
				`<w:p><w:pPr><w:ind w:left="720"/></w:pPr><w:r>`+
					`<w:t xml:space="preserve">• %s</w:t></w:r></w:p>`,
				esc(b.Text))

		case "table":
			body.WriteString(docxTable(b.Rows))

		default:
			// Blank paragraphs are how vertical space is expressed.
			if strings.TrimSpace(b.Text) == "" {
				body.WriteString(`<w:p/>`)
				continue
			}
			fmt.Fprintf(&body,
				`<w:p><w:r><w:t xml:space="preserve">%s</w:t></w:r></w:p>`, esc(b.Text))
		}
	}

	document := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:body>` + body.String() + `<w:sectPr><w:pgSz w:w="11906" w:h="16838"/>` +
		`<w:pgMar w:top="1134" w:right="1134" w:bottom="1134" w:left="1134"/></w:sectPr>` +
		`</w:body></w:document>`

	return writeZip(path, map[string]string{
		"[Content_Types].xml": docxContentTypes,
		"_rels/.rels":         docxRels,
		"word/document.xml":   document,
	})
}

func docxTable(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(`<w:tbl><w:tblPr><w:tblBorders>`)
	for _, edge := range []string{"top", "left", "bottom", "right", "insideH", "insideV"} {
		fmt.Fprintf(&sb, `<w:%s w:val="single" w:sz="4" w:color="999999"/>`, edge)
	}
	sb.WriteString(`</w:tblBorders></w:tblPr>`)

	for i, row := range rows {
		sb.WriteString(`<w:tr>`)
		for _, cell := range row {
			sb.WriteString(`<w:tc><w:tcPr><w:tcW w:w="0" w:type="auto"/></w:tcPr><w:p><w:r>`)
			if i == 0 {
				sb.WriteString(`<w:rPr><w:b/></w:rPr>`) // header row
			}
			fmt.Fprintf(&sb, `<w:t xml:space="preserve">%s</w:t></w:r></w:p></w:tc>`, esc(cell))
		}
		sb.WriteString(`</w:tr>`)
	}
	sb.WriteString(`</w:tbl><w:p/>`)
	return sb.String()
}

// --- xlsx -------------------------------------------------------------------

const xlsxContentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
%s</Types>`

const xlsxRootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`

// Sheet is one worksheet of a spreadsheet.
type Sheet struct {
	Name string
	Rows [][]string
}

// WriteXLSX creates a spreadsheet from sheets of rows.
//
// Values that parse as numbers are written as numbers, so the result is
// something you can sum and chart rather than a grid of text that merely looks
// numeric. Everything else is written as an inline string, which avoids
// maintaining a shared-string pool.
func WriteXLSX(path string, sheets []Sheet) error {
	if len(sheets) == 0 {
		return fmt.Errorf("xlsx: no sheets supplied")
	}

	parts := map[string]string{"_rels/.rels": xlsxRootRels}

	var overrides, workbookSheets, workbookRels strings.Builder
	for i, s := range sheets {
		n := i + 1
		sheetPath := fmt.Sprintf("xl/worksheets/sheet%d.xml", n)
		parts[sheetPath] = xlsxSheet(s.Rows)

		fmt.Fprintf(&overrides,
			`<Override PartName="/%s" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`,
			sheetPath)

		name := s.Name
		if name == "" {
			name = fmt.Sprintf("Sheet%d", n)
		}
		// Excel rejects these characters in a tab name and refuses the file.
		name = strings.Map(func(r rune) rune {
			if strings.ContainsRune(`\/?*[]:`, r) {
				return '-'
			}
			return r
		}, name)
		if len(name) > 31 {
			name = name[:31]
		}

		fmt.Fprintf(&workbookSheets,
			`<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, esc(name), n, n)
		fmt.Fprintf(&workbookRels,
			`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`,
			n, n)
	}

	parts["[Content_Types].xml"] = fmt.Sprintf(xlsxContentTypes, overrides.String())
	parts["xl/workbook.xml"] = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets>` + workbookSheets.String() + `</sheets></workbook>`
	parts["xl/_rels/workbook.xml.rels"] = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		workbookRels.String() + `</Relationships>`

	return writeZip(path, parts)
}

func xlsxSheet(rows [][]string) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)

	for r, row := range rows {
		fmt.Fprintf(&sb, `<row r="%d">`, r+1)
		for c, cell := range row {
			ref := columnName(c) + strconv.Itoa(r+1)

			// A value that is genuinely numeric is stored as a number, so the
			// spreadsheet can actually compute with it.
			if isNumeric(cell) {
				fmt.Fprintf(&sb, `<c r="%s"><v>%s</v></c>`, ref, strings.TrimSpace(cell))
				continue
			}
			fmt.Fprintf(&sb,
				`<c r="%s" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`,
				ref, esc(cell))
		}
		sb.WriteString(`</row>`)
	}
	sb.WriteString(`</sheetData></worksheet>`)
	return sb.String()
}

// isNumeric reports whether a cell should be stored as a number.
//
// Deliberately strict: a leading zero usually means an identifier rather than a
// quantity, and storing "007" as 7 loses information the user cared about.
func isNumeric(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	if len(t) > 1 && t[0] == '0' && t[1] != '.' {
		return false
	}
	if _, err := strconv.ParseFloat(t, 64); err != nil {
		return false
	}
	return true
}

// columnName converts a zero-based index to a spreadsheet column: 0→A, 26→AA.
func columnName(n int) string {
	name := ""
	for n >= 0 {
		name = string(rune('A'+(n%26))) + name
		n = n/26 - 1
	}
	return name
}

// --- shared -----------------------------------------------------------------

// writeZip assembles a container from named parts.
func writeZip(path string, parts map[string]string) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create parent: %w", err)
		}
	}

	// Build in memory, then write once: a half-written Office file is not a
	// document, and truncating the target before the content is ready would
	// destroy an existing file on any error.
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	// [Content_Types].xml must come first; some readers assume it.
	order := []string{"[Content_Types].xml"}
	for name := range parts {
		if name != "[Content_Types].xml" {
			order = append(order, name)
		}
	}

	for _, name := range order {
		content, ok := parts[name]
		if !ok {
			continue
		}
		f, err := w.Create(name)
		if err != nil {
			return fmt.Errorf("zip part %s: %w", name, err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			return fmt.Errorf("write part %s: %w", name, err)
		}
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("finalise container: %w", err)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// ParseBlocks turns lightweight markdown into document blocks.
//
// This is the bridge from what a language model naturally writes to what a
// document needs: models emit markdown by default, and asking one to produce
// structured JSON for every paragraph wastes tokens and invites malformed
// output.
func ParseBlocks(md string) []Block {
	var blocks []Block
	lines := strings.Split(md, "\n")

	var tableRows [][]string
	flushTable := func() {
		if len(tableRows) > 0 {
			blocks = append(blocks, Table(tableRows))
			tableRows = nil
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Pipe tables.
		if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") {
			cells := strings.Split(strings.Trim(trimmed, "|"), "|")
			// The |---|---| separator row carries no data.
			isSeparator := true
			for _, c := range cells {
				if strings.Trim(strings.TrimSpace(c), "-: ") != "" {
					isSeparator = false
					break
				}
			}
			if !isSeparator {
				row := make([]string, 0, len(cells))
				for _, c := range cells {
					row = append(row, strings.TrimSpace(c))
				}
				tableRows = append(tableRows, row)
			}
			continue
		}
		flushTable()

		switch {
		case trimmed == "":
			blocks = append(blocks, Paragraph(""))
		case strings.HasPrefix(trimmed, "### "):
			blocks = append(blocks, Heading(3, strings.TrimPrefix(trimmed, "### ")))
		case strings.HasPrefix(trimmed, "## "):
			blocks = append(blocks, Heading(2, strings.TrimPrefix(trimmed, "## ")))
		case strings.HasPrefix(trimmed, "# "):
			blocks = append(blocks, Heading(1, strings.TrimPrefix(trimmed, "# ")))
		case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "):
			blocks = append(blocks, Bullet(strings.TrimSpace(trimmed[2:])))
		default:
			// Strip emphasis markers: they would otherwise be read aloud as
			// asterisks and printed literally in the document.
			blocks = append(blocks, Paragraph(stripEmphasis(trimmed)))
		}
	}
	flushTable()
	return blocks
}

func stripEmphasis(s string) string {
	for _, marker := range []string{"**", "__", "*", "_", "`"} {
		s = strings.ReplaceAll(s, marker, "")
	}
	return s
}
