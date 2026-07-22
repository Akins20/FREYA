package docs

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
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

// Title builds a document title block.
func Title(text string) Block { return Block{Kind: "title", Text: text} }

// Numbered builds a numbered list item.
func Numbered(text string) Block { return Block{Kind: "numbered", Text: text} }

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
<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
<Override PartName="/word/numbering.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.numbering+xml"/>
</Types>`

// docxDocumentRels links the document to its styles and numbering parts.
// Without these relationships Word ignores both and silently falls back to
// defaults, which looks exactly like the styling never having been written.
const docxDocumentRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/>
</Relationships>`

const docxRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

// DocOptions carries page furniture: running header, footer and page numbers.
type DocOptions struct {
	PageSetup
	// Images maps a placeholder block's Text to a file path. Blocks of kind
	// "image" whose Text names a key here are embedded.
	Images map[string]string
}

// WriteDOCX creates a Word document from blocks.
func WriteDOCX(path string, blocks []Block) error {
	return WriteDOCXWith(path, blocks, DocOptions{})
}

// Image builds an image block. Text is the path to embed.
func Image(path string) Block { return Block{Kind: "image", Text: path} }

// WriteDOCXWith creates a Word document with page furniture and images.
func WriteDOCXWith(path string, blocks []Block, opts DocOptions) error {
	var body strings.Builder
	var imagePaths []string

	for _, b := range blocks {
		switch b.Kind {
		case "heading":
			level := b.Level
			if level < 1 || level > 3 {
				level = 1
			}
			// A named style, not bold-and-bigger. This is what puts the heading
			// in the navigation pane and lets Word build a table of contents.
			fmt.Fprintf(&body,
				`<w:p><w:pPr><w:pStyle w:val="Heading%d"/></w:pPr>`+
					`<w:r><w:t xml:space="preserve">%s</w:t></w:r></w:p>`,
				level, esc(b.Text))

		case "title":
			fmt.Fprintf(&body,
				`<w:p><w:pPr><w:pStyle w:val="Title"/></w:pPr>`+
					`<w:r><w:t xml:space="preserve">%s</w:t></w:r></w:p>`, esc(b.Text))

		case "bullet":
			// A real list item: numPr references the bullet definition in
			// numbering.xml, so Word treats it as a list rather than a
			// paragraph that happens to start with a dot character.
			fmt.Fprintf(&body,
				`<w:p><w:pPr><w:pStyle w:val="ListParagraph"/>`+
					`<w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr>`+
					`<w:r><w:t xml:space="preserve">%s</w:t></w:r></w:p>`,
				esc(b.Text))

		case "numbered":
			fmt.Fprintf(&body,
				`<w:p><w:pPr><w:pStyle w:val="ListParagraph"/>`+
					`<w:numPr><w:ilvl w:val="0"/><w:numId w:val="2"/></w:numPr></w:pPr>`+
					`<w:r><w:t xml:space="preserve">%s</w:t></w:r></w:p>`,
				esc(b.Text))

		case "image":
			// Handled after the loop, where the relationship ids are known.
			body.WriteString(fmt.Sprintf("<!--IMAGE:%d-->", len(imagePaths)))
			imagePaths = append(imagePaths, b.Text)

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

	parts := map[string]string{
		"_rels/.rels":        docxRels,
		"word/styles.xml":    docxStyles,
		"word/numbering.xml": docxNumbering,
	}
	binary := map[string][]byte{}

	// Relationship ids beyond the two fixed ones are allocated here, so the
	// document body, the rels part and the content types all agree.
	rels := []string{
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`,
		`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/>`,
	}
	nextRel := 3
	// The OPC schema requires every <Default> to precede every <Override>.
	// Appending both to one buffer put a Default after the Overrides and made
	// LibreOffice reject the file outright — so they are collected separately.
	var extraDefaults, extraOverrides strings.Builder
	seenExt := map[string]bool{}

	// Images.
	rendered := body.String()
	for i, imgPath := range imagePaths {
		placeholder := fmt.Sprintf("<!--IMAGE:%d-->", i)
		w, h, contentType, ext, err := imageInfo(imgPath)
		if err != nil {
			// A missing picture must not sink the whole document.
			rendered = strings.Replace(rendered, placeholder,
				fmt.Sprintf(`<w:p><w:r><w:rPr><w:i/><w:color w:val="999999"/></w:rPr>`+
					`<w:t xml:space="preserve">[image unavailable: %s]</w:t></w:r></w:p>`,
					esc(filepath.Base(imgPath))), 1)
			continue
		}
		data, err := os.ReadFile(imgPath)
		if err != nil {
			rendered = strings.Replace(rendered, placeholder, "", 1)
			continue
		}

		relID := fmt.Sprintf("rId%d", nextRel)
		mediaName := fmt.Sprintf("media/image%d.%s", i+1, ext)
		binary["word/"+mediaName] = data
		rels = append(rels, fmt.Sprintf(
			`<Relationship Id="%s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="%s"/>`,
			relID, mediaName))
		if !seenExt[ext] {
			seenExt[ext] = true
			fmt.Fprintf(&extraDefaults, `<Default Extension="%s" ContentType="%s"/>`, ext, contentType)
		}
		fw, fh := fitImage(w, h)
		rendered = strings.Replace(rendered, placeholder,
			imageDrawingXML(relID, nextRel, fw, fh, filepath.Base(imgPath)), 1)
		nextRel++
	}

	// Header and footer.
	var sectExtra strings.Builder
	if opts.Header != "" {
		relID := fmt.Sprintf("rId%d", nextRel)
		parts["word/header1.xml"] = headerXML(opts.Header)
		rels = append(rels, fmt.Sprintf(
			`<Relationship Id="%s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/header" Target="header1.xml"/>`, relID))
		fmt.Fprintf(&sectExtra, `<w:headerReference w:type="default" r:id="%s"/>`, relID)
		extraOverrides.WriteString(`<Override PartName="/word/header1.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.header+xml"/>`)
		nextRel++
	}
	if opts.Footer != "" || opts.PageNumbers {
		relID := fmt.Sprintf("rId%d", nextRel)
		parts["word/footer1.xml"] = footerXML(opts.Footer, opts.PageNumbers)
		rels = append(rels, fmt.Sprintf(
			`<Relationship Id="%s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/footer" Target="footer1.xml"/>`, relID))
		fmt.Fprintf(&sectExtra, `<w:footerReference w:type="default" r:id="%s"/>`, relID)
		extraOverrides.WriteString(`<Override PartName="/word/footer1.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.footer+xml"/>`)
		nextRel++
	}

	document := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"
 xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<w:body>` + rendered + `<w:sectPr>` + sectExtra.String() +
		`<w:pgSz w:w="11906" w:h="16838"/>` +
		`<w:pgMar w:top="1134" w:right="1134" w:bottom="1134" w:left="1134"` +
		` w:header="708" w:footer="708"/></w:sectPr>` +
		`</w:body></w:document>`

	parts["word/document.xml"] = document
	parts["word/_rels/document.xml.rels"] = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		strings.Join(rels, "") + `</Relationships>`
	contentTypes := docxContentTypes
	if extraDefaults.Len() > 0 {
		// Insert after the last existing Default, before the first Override.
		contentTypes = strings.Replace(contentTypes,
			`<Default Extension="xml" ContentType="application/xml"/>`,
			`<Default Extension="xml" ContentType="application/xml"/>`+extraDefaults.String(), 1)
	}
	if extraOverrides.Len() > 0 {
		contentTypes = strings.Replace(contentTypes,
			"</Types>", extraOverrides.String()+"</Types>", 1)
	}
	parts["[Content_Types].xml"] = contentTypes

	return writeZipMixed(path, parts, binary)
}

func docxTable(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(`<w:tbl><w:tblPr><w:tblStyle w:val="TableGrid"/>`)
	sb.WriteString(`<w:tblW w:w="5000" w:type="pct"/><w:tblBorders>`)
	for _, edge := range []string{"top", "left", "bottom", "right", "insideH", "insideV"} {
		fmt.Fprintf(&sb, `<w:%s w:val="single" w:sz="4" w:color="8496B0"/>`, edge)
	}
	sb.WriteString(`</w:tblBorders></w:tblPr>`)

	for i, row := range rows {
		// Repeat the header on every page rather than leaving later pages
		// with an unlabelled grid.
		if i == 0 {
			sb.WriteString(`<w:tr><w:trPr><w:tblHeader/></w:trPr>`)
		} else {
			sb.WriteString(`<w:tr>`)
		}
		for _, cell := range row {
			sb.WriteString(`<w:tc><w:tcPr><w:tcW w:w="0" w:type="auto"/>`)
			if i == 0 {
				sb.WriteString(`<w:shd w:val="clear" w:fill="2E5496"/>`)
			}
			sb.WriteString(`</w:tcPr><w:p><w:r>`)
			if i == 0 {
				sb.WriteString(`<w:rPr><w:b/><w:color w:val="FFFFFF"/></w:rPr>`)
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
<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
%s</Types>`

const xlsxRootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`

// Sheet is one worksheet of a spreadsheet.
type Sheet struct {
	Name string
	Rows [][]string
	// Chart, when set, draws a chart beside the data on this sheet.
	Chart *Chart
}

// WriteXLSX creates a spreadsheet from sheets of rows.
//
// A cell beginning with "=" is written as a formula, so a totals row is a live
// calculation rather than a number that silently goes stale.
//
// Values that parse as numbers are written as numbers, so the result is
// something you can sum and chart rather than a grid of text that merely looks
// numeric. Everything else is written as an inline string, which avoids
// maintaining a shared-string pool.
func WriteXLSX(path string, sheets []Sheet) error {
	if len(sheets) == 0 {
		return fmt.Errorf("xlsx: no sheets supplied")
	}

	parts := map[string]string{
		"_rels/.rels":   xlsxRootRels,
		"xl/styles.xml": xlsxStyles,
	}

	var overrides, workbookSheets, workbookRels strings.Builder
	for i, s := range sheets {
		n := i + 1

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

		sheetPath := fmt.Sprintf("xl/worksheets/sheet%d.xml", n)
		hasChart := s.Chart != nil && len(s.Chart.ValueColumns) > 0
		parts[sheetPath] = xlsxSheetWithDrawing(s.Rows, hasChart)

		// A chart needs four coordinated pieces: the chart part, a drawing that
		// anchors it, a relationship from the sheet to the drawing, and one from
		// the drawing to the chart. Miss any and the file opens with no chart.
		if hasChart {
			ch := *s.Chart
			ch.SheetName = name
			if ch.Rows == 0 {
				ch.Rows = len(s.Rows) - 1
			}
			parts["xl/charts/chart1.xml"] = chartXML(ch)
			parts["xl/drawings/drawing1.xml"] = chartDrawingXML()
			parts["xl/drawings/_rels/drawing1.xml.rels"] = chartRels()
			parts[fmt.Sprintf("xl/worksheets/_rels/sheet%d.xml.rels", n)] = sheetDrawingRels()

			overrides.WriteString(`<Override PartName="/xl/charts/chart1.xml" ContentType="application/vnd.openxmlformats-officedocument.drawingml.chart+xml"/>`)
			overrides.WriteString(`<Override PartName="/xl/drawings/drawing1.xml" ContentType="application/vnd.openxmlformats-officedocument.drawing+xml"/>`)
		}

		fmt.Fprintf(&overrides,
			`<Override PartName="/%s" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`,
			sheetPath)
		fmt.Fprintf(&workbookSheets,
			`<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, esc(name), n, n)
		fmt.Fprintf(&workbookRels,
			`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`,
			n, n)
		_ = n
	}

	parts["[Content_Types].xml"] = fmt.Sprintf(xlsxContentTypes, overrides.String())
	parts["xl/workbook.xml"] = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets>` + workbookSheets.String() + `</sheets></workbook>`
	// The styles part needs its own relationship or Excel ignores every format.
	fmt.Fprintf(&workbookRels,
		`<Relationship Id="rIdStyles" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`)
	parts["xl/_rels/workbook.xml.rels"] = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		workbookRels.String() + `</Relationships>`

	return writeZip(path, parts)
}

func xlsxSheet(rows [][]string) string {
	return xlsxSheetWithDrawing(rows, false)
}

func xlsxSheetWithDrawing(rows [][]string, drawing bool) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)

	// Element order is fixed by the schema: sheetViews, then cols, then
	// sheetData. Out of order, Excel rejects the file outright.
	if len(rows) > 1 {
		sb.WriteString(freezeHeaderXML)
	}
	sb.WriteString(colsXML(columnWidths(rows)))
	sb.WriteString(`<sheetData>`)

	for r, row := range rows {
		isHeader := r == 0 && len(rows) > 1
		fmt.Fprintf(&sb, `<row r="%d"`, r+1)
		if isHeader {
			sb.WriteString(` ht="20" customHeight="1"`)
		}
		sb.WriteString(`>`)

		for c, cell := range row {
			ref := columnName(c) + strconv.Itoa(r+1)
			style := cellStyle(cell, isHeader)

			// A value that is genuinely numeric is stored as a number, so the
			// spreadsheet can actually compute with it.
			if isFormula(cell) && !isHeader {
				sb.WriteString(formulaXML(ref, styleText, cell))
				continue
			}
			if isNumeric(cell) && !isHeader {
				fmt.Fprintf(&sb, `<c r="%s" s="%d"><v>%s</v></c>`,
					ref, style, strings.TrimSpace(cell))
				continue
			}
			fmt.Fprintf(&sb,
				`<c r="%s" s="%d" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`,
				ref, style, esc(cell))
		}
		sb.WriteString(`</row>`)
	}
	sb.WriteString(`</sheetData>`)
	// The drawing reference must follow sheetData; the schema is strict.
	if drawing {
		sb.WriteString(`<drawing r:id="rId1" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"/>`)
	}
	sb.WriteString(`</worksheet>`)
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

// writeZip assembles a container from named text parts.
func writeZip(path string, parts map[string]string) error {
	return writeZipMixed(path, parts, nil)
}

// writeZipMixed assembles a container from text and binary parts.
func writeZipMixed(path string, parts map[string]string, binary map[string][]byte) error {
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

	for name, data := range binary {
		// Deflate, not Store. Storing an already-compressed PNG saves nothing
		// and made Go emit a data descriptor, which left readers unable to find
		// where the stream ended — LibreOffice refused the whole document.
		f, err := w.Create(name)
		if err != nil {
			return fmt.Errorf("zip media %s: %w", name, err)
		}
		if _, err := f.Write(data); err != nil {
			return fmt.Errorf("write media %s: %w", name, err)
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
	var seenTitle bool
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
			text := strings.TrimPrefix(trimmed, "# ")
			// The first top-level heading is the document title; later ones are
			// section headings. This is what people mean by markdown structure.
			if !seenTitle {
				seenTitle = true
				blocks = append(blocks, Title(text))
			} else {
				blocks = append(blocks, Heading(1, text))
			}
		case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "):
			blocks = append(blocks, Bullet(strings.TrimSpace(trimmed[2:])))
		case numberedItem.MatchString(trimmed):
			blocks = append(blocks, Numbered(numberedItem.ReplaceAllString(trimmed, "")))
		case markdownImage.MatchString(trimmed):
			blocks = append(blocks, Image(markdownImage.FindStringSubmatch(trimmed)[1]))
		default:
			// Strip emphasis markers: they would otherwise be read aloud as
			// asterisks and printed literally in the document.
			blocks = append(blocks, Paragraph(stripEmphasis(trimmed)))
		}
	}
	flushTable()
	return blocks
}

// numberedItem matches "1. ", "2) " and similar list markers.
var numberedItem = regexp.MustCompile(`^\d+[.)]\s+`)

// markdownImage matches ![alt](path) occupying a whole line.
//
// Anchored at both ends deliberately. An image mentioned inside a sentence
// cannot become a block without throwing away the sentence around it, and
// silently losing a paragraph of prose is a far worse failure than rendering
// one picture as text. The leading bang is the whole difference from a link:
// [text](url) stays prose, because a document cannot follow a hyperlink.
//
// The path is captured lazily so a trailing ")" closes the form rather than
// being absorbed, while paths containing spaces or their own parentheses still
// survive intact.
var markdownImage = regexp.MustCompile(`^!\[[^\]]*\]\(\s*(.+?)\s*\)$`)

func stripEmphasis(s string) string {
	for _, marker := range []string{"**", "__", "*", "_", "`"} {
		s = strings.ReplaceAll(s, marker, "")
	}
	return s
}

// --- pdf --------------------------------------------------------------------

// WritePDF produces a PDF by rendering a Word document through LibreOffice.
//
// Writing PDF directly would mean implementing font metrics, text layout and
// the content-stream operators — and the result would still look worse than
// what a real layout engine produces from the same content. Building the docx
// first and converting reuses every style already defined, so the PDF and the
// Word file are genuinely the same document.
func WritePDF(path string, blocks []Block) error {
	if _, err := exec.LookPath("soffice"); err != nil {
		return fmt.Errorf("PDF export needs LibreOffice (soffice) on PATH")
	}

	outDir := filepath.Dir(path)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	// Stage the intermediate in a private directory so a concurrent conversion
	// cannot collide on the name, and so a failure leaves nothing behind.
	tmpDir, err := os.MkdirTemp("", "freya-pdf-")
	if err != nil {
		return fmt.Errorf("temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	intermediate := filepath.Join(tmpDir, base+".docx")
	if err := WriteDOCX(intermediate, blocks); err != nil {
		return fmt.Errorf("build intermediate document: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "soffice", "--headless", "--norestore",
		"--convert-to", "pdf", intermediate, "--outdir", tmpDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("PDF conversion timed out")
		}
		return fmt.Errorf("PDF conversion failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	produced := filepath.Join(tmpDir, base+".pdf")
	if _, err := os.Stat(produced); err != nil {
		return fmt.Errorf("LibreOffice reported success but produced no PDF")
	}

	data, err := os.ReadFile(produced)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
