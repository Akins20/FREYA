package docs

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Office formats, read directly from their ZIP containers.
//
// Each is a package of XML parts. The parts that matter:
//
//	docx  word/document.xml            <w:t> text runs, <w:p> paragraphs
//	xlsx  xl/sharedStrings.xml         the string pool
//	      xl/worksheets/sheet*.xml     cells, t="s" meaning "index into pool"
//	pptx  ppt/slides/slide*.xml        <a:t> text runs
//	odt   content.xml                  text:p paragraphs
//
// The XML is walked as a token stream rather than unmarshalled into structs.
// These schemas are enormous and mostly irrelevant to reading text, and a
// tolerant streaming walk survives the variations different producers emit.

// maxZipEntry bounds any single decompressed part, so a zip bomb cannot
// exhaust memory.
const maxZipEntry = 64 << 20

// openZipPart finds a file inside a ZIP container and reads it.
func openZipPart(r *zip.ReadCloser, name string) ([]byte, error) {
	for _, f := range r.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, maxZipEntry))
		}
	}
	return nil, fmt.Errorf("part %q not found", name)
}

// textFromXML walks an XML stream and collects the character data inside the
// named elements, inserting a break wherever a paragraph boundary occurs.
func textFromXML(data []byte, textElems, breakElems map[string]bool) string {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	// Office XML occasionally carries entities the strict parser rejects;
	// tolerate them rather than failing the whole document.
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity

	var sb strings.Builder
	var capture int

	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if textElems[t.Name.Local] {
				capture++
			}
		case xml.EndElement:
			if textElems[t.Name.Local] && capture > 0 {
				capture--
			}
			if breakElems[t.Name.Local] {
				sb.WriteString("\n")
			}
		case xml.CharData:
			if capture > 0 {
				sb.Write(t)
			}
		}
	}
	return tidyText(sb.String())
}

// tidyText collapses the runs of blank lines that paragraph breaks leave behind.
func tidyText(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(l, " \t\r")
		if strings.TrimSpace(l) == "" {
			blank++
			if blank > 1 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// --- docx -------------------------------------------------------------------

func extractDOCX(path string) (string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("docx: open: %w", err)
	}
	defer r.Close()

	data, err := openZipPart(r, "word/document.xml")
	if err != nil {
		return "", fmt.Errorf("docx: %w", err)
	}
	// w:t holds the text; w:p ends a paragraph; w:br and w:tab are within-line.
	return textFromXML(data,
		map[string]bool{"t": true},
		map[string]bool{"p": true, "br": true},
	), nil
}

// --- xlsx -------------------------------------------------------------------

func extractXLSX(path string) ([]Section, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("xlsx: open: %w", err)
	}
	defer r.Close()

	// The string pool: most cell text lives here rather than inline, which is
	// why a naive reader of sheet XML alone returns nothing but numbers.
	var shared []string
	if data, err := openZipPart(r, "xl/sharedStrings.xml"); err == nil {
		shared = parseSharedStrings(data)
	}

	sheetNames := parseSheetNames(r)

	type sheetFile struct {
		name string
		file *zip.File
	}
	var sheets []sheetFile
	for _, f := range r.File {
		if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") {
			sheets = append(sheets, sheetFile{name: f.Name, file: f})
		}
	}
	sort.Slice(sheets, func(i, j int) bool { return sheets[i].name < sheets[j].name })

	var out []Section
	for i, s := range sheets {
		rc, err := s.file.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxZipEntry))
		rc.Close()
		if err != nil {
			continue
		}

		label := fmt.Sprintf("Sheet%d", i+1)
		if i < len(sheetNames) {
			label = sheetNames[i]
		}
		out = append(out, Section{Name: label, Text: parseSheet(data, shared)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("xlsx: no worksheets found")
	}
	return out, nil
}

// parseSharedStrings reads the workbook string pool.
func parseSharedStrings(data []byte) []string {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	decoder.Strict = false

	var out []string
	var current strings.Builder
	inSI, inT := false, false

	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI, current = true, strings.Builder{}
			case "t":
				inT = true
			}
		case xml.CharData:
			if inSI && inT {
				current.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "si":
				out = append(out, current.String())
				inSI = false
			}
		}
	}
	return out
}

// parseSheetNames reads the human-facing tab names from the workbook part.
func parseSheetNames(r *zip.ReadCloser) []string {
	data, err := openZipPart(r, "xl/workbook.xml")
	if err != nil {
		return nil
	}
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	decoder.Strict = false

	var names []string
	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "sheet" {
			for _, a := range se.Attr {
				if a.Name.Local == "name" {
					names = append(names, a.Value)
				}
			}
		}
	}
	return names
}

// parseSheet renders one worksheet as tab-separated rows.
func parseSheet(data []byte, shared []string) string {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	decoder.Strict = false

	var sb strings.Builder
	var cell strings.Builder
	var cellType string
	inV, inRow, firstCell := false, false, true

	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				inRow, firstCell = true, true
			case "c":
				cellType = ""
				for _, a := range t.Attr {
					if a.Name.Local == "t" {
						cellType = a.Value
					}
				}
			case "v", "t":
				inV, cell = true, strings.Builder{}
			}
		case xml.CharData:
			if inV {
				cell.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v", "t":
				if inV && inRow {
					value := cell.String()
					// t="s" means the value is an index into the string pool.
					if cellType == "s" {
						if idx, err := strconv.Atoi(value); err == nil && idx >= 0 && idx < len(shared) {
							value = shared[idx]
						}
					}
					if !firstCell {
						sb.WriteString("\t")
					}
					sb.WriteString(value)
					firstCell = false
				}
				inV = false
			case "row":
				sb.WriteString("\n")
				inRow = false
			}
		}
	}
	return tidyText(sb.String())
}

// --- pptx -------------------------------------------------------------------

func extractPPTX(path string) ([]Section, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("pptx: open: %w", err)
	}
	defer r.Close()

	var slides []*zip.File
	for _, f := range r.File {
		if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			slides = append(slides, f)
		}
	}
	// slide2 must not sort before slide10 by string order alone.
	sort.Slice(slides, func(i, j int) bool {
		return slideNumber(slides[i].Name) < slideNumber(slides[j].Name)
	})

	var out []Section
	for i, f := range slides {
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxZipEntry))
		rc.Close()
		if err != nil {
			continue
		}
		text := textFromXML(data,
			map[string]bool{"t": true},
			map[string]bool{"p": true},
		)
		out = append(out, Section{Name: fmt.Sprintf("Slide %d", i+1), Text: text})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("pptx: no slides found")
	}
	return out, nil
}

func slideNumber(name string) int {
	base := filepath.Base(name)
	base = strings.TrimPrefix(base, "slide")
	base = strings.TrimSuffix(base, ".xml")
	n, _ := strconv.Atoi(base)
	return n
}

// --- OpenDocument -----------------------------------------------------------

func extractOpenDocument(path string) (string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("opendocument: open: %w", err)
	}
	defer r.Close()

	data, err := openZipPart(r, "content.xml")
	if err != nil {
		return "", fmt.Errorf("opendocument: %w", err)
	}
	return textFromXML(data,
		map[string]bool{"p": true, "h": true, "span": true, "a": true},
		map[string]bool{"p": true, "h": true, "table-row": true},
	), nil
}

// --- pdf --------------------------------------------------------------------

// extractPDF shells out to pdftotext.
//
// PDF text lives in a content stream with its own operator language and
// font-specific encodings; correct pure-Go extraction is a project rather than
// a file, and poppler already does it well.
func extractPDF(path string) (text, note string, err error) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		return "", "", fmt.Errorf("pdftotext not found — install poppler-utils to read PDFs")
	}

	// -layout preserves column structure, which matters for tables and papers.
	out, err := exec.Command("pdftotext", "-layout", "-q", path, "-").Output()
	if err != nil {
		return "", "", fmt.Errorf("pdftotext: %w", err)
	}
	text = tidyText(string(out))

	if strings.TrimSpace(text) == "" {
		note = "no text layer — this is probably a scanned image and would need OCR"
	}
	if info, err := exec.Command("pdfinfo", path).Output(); err == nil {
		for _, line := range strings.Split(string(info), "\n") {
			if strings.HasPrefix(line, "Pages:") {
				note = strings.TrimSpace(strings.TrimPrefix(line, "Pages:")) + " pages"
				if strings.TrimSpace(text) == "" {
					note += ", no text layer (scanned?)"
				}
			}
		}
	}
	return text, note, nil
}

// --- archives ---------------------------------------------------------------

// listArchive describes what a ZIP holds without extracting it.
func listArchive(path string) (string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("zip: open: %w", err)
	}
	defer r.Close()

	var sb strings.Builder
	var total uint64
	fmt.Fprintf(&sb, "Archive with %d entries:\n", len(r.File))
	for i, f := range r.File {
		if i >= 200 {
			fmt.Fprintf(&sb, "  ... and %d more\n", len(r.File)-i)
			break
		}
		total += f.UncompressedSize64
		kind := ""
		if f.FileInfo().IsDir() {
			kind = "/"
		}
		fmt.Fprintf(&sb, "  %s%s (%s)\n", f.Name, kind, humanBytes(int64(f.UncompressedSize64)))
	}
	fmt.Fprintf(&sb, "Total uncompressed: %s", humanBytes(int64(total)))
	return sb.String(), nil
}
