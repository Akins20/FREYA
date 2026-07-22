// Package docs reads text out of the file formats people actually keep work in.
//
// # Why most of this is pure Go
//
// docx, xlsx, pptx, odt and ods are all ZIP containers holding XML. That means
// `archive/zip` plus `encoding/xml` — both standard library — can read them
// directly, with no LibreOffice invocation, no temp files and no 400ms process
// start per document. On a 2013 laptop that difference is the difference
// between "she read my dissertation" and "she hung for a minute".
//
// PDF is the exception. Its text layer is a content stream with its own
// operator language and font-specific encodings; a correct pure-Go extractor is
// a project, not a file. So PDF shells out to pdftotext, which is already
// installed and excellent at exactly this.
//
// # What this deliberately does not do
//
// It extracts *text*, not formatting, images or layout. An assistant asked to
// read a document needs the words. Anything that needs fidelity should open the
// file in the application that owns it.
package docs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Format identifies how a file should be read.
type Format string

const (
	FormatText    Format = "text"
	FormatDOCX    Format = "docx"
	FormatXLSX    Format = "xlsx"
	FormatPPTX    Format = "pptx"
	FormatODT     Format = "odt"
	FormatODS     Format = "ods"
	FormatPDF     Format = "pdf"
	FormatZIP     Format = "zip"
	FormatBinary  Format = "binary"
	FormatUnknown Format = "unknown"
)

// Document is extracted content plus what was learned along the way.
type Document struct {
	Path   string
	Format Format
	Text   string
	// Pages or sheets, when the format has them.
	Sections []Section
	// Truncated reports that Text was cut to fit a limit.
	Truncated bool
	// Note carries anything the caller should know: a missing helper binary,
	// an encrypted file, a format read only partially.
	Note string
}

// Section is a page, sheet or slide.
type Section struct {
	Name string
	Text string
}

// maxExtract caps how much text is returned. Handing a model an entire book
// wastes the context budget that the memory architecture works to protect.
const maxExtract = 400_000

// Detect identifies a file's format from its extension and magic bytes.
//
// Extension alone is unreliable (a .docx that is really a .doc, a .txt that is
// really a JPEG), so the container signature is checked too.
func Detect(path string) (Format, error) {
	ext := strings.ToLower(filepath.Ext(path))

	f, err := os.Open(path)
	if err != nil {
		return FormatUnknown, err
	}
	defer f.Close()

	head := make([]byte, 512)
	n, _ := f.Read(head)
	head = head[:n]

	switch {
	case len(head) >= 4 && string(head[:4]) == "%PDF":
		return FormatPDF, nil
	case len(head) >= 2 && head[0] == 'P' && head[1] == 'K':
		// A ZIP container: which kind depends on the extension, since the
		// signature is identical for every Office format.
		switch ext {
		case ".docx", ".docm":
			return FormatDOCX, nil
		case ".xlsx", ".xlsm":
			return FormatXLSX, nil
		case ".pptx", ".pptm":
			return FormatPPTX, nil
		case ".odt":
			return FormatODT, nil
		case ".ods":
			return FormatODS, nil
		default:
			return FormatZIP, nil
		}
	}

	if isProbablyText(head) {
		return FormatText, nil
	}
	return FormatBinary, nil
}

// isProbablyText reports whether a byte sample looks like readable text.
//
// A NUL byte is decisive — no text encoding in normal use emits one — and
// beyond that the ratio of control characters separates prose from data.
func isProbablyText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	control := 0
	for _, c := range b {
		if c == 0 {
			return false
		}
		if c < 0x09 || (c > 0x0d && c < 0x20) {
			control++
		}
	}
	return float64(control)/float64(len(b)) < 0.05
}

// Extract reads a file and returns its text, whatever the format.
func Extract(path string) (*Document, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("docs: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("docs: %s is a directory", path)
	}

	format, err := Detect(path)
	if err != nil {
		return nil, err
	}

	doc := &Document{Path: path, Format: format}

	switch format {
	case FormatText:
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("docs: read: %w", err)
		}
		doc.Text = string(raw)

	case FormatDOCX:
		doc.Text, err = extractDOCX(path)
	case FormatXLSX:
		doc.Sections, err = extractXLSX(path)
		doc.Text = joinSections(doc.Sections)
	case FormatPPTX:
		doc.Sections, err = extractPPTX(path)
		doc.Text = joinSections(doc.Sections)
	case FormatODT, FormatODS:
		doc.Text, err = extractOpenDocument(path)
	case FormatPDF:
		doc.Text, doc.Note, err = extractPDF(path)
	case FormatZIP:
		doc.Text, err = listArchive(path)
	case FormatBinary:
		return &Document{
			Path: path, Format: FormatBinary,
			Note: fmt.Sprintf("binary file, %s — not text", humanBytes(info.Size())),
		}, nil
	}
	if err != nil {
		return nil, err
	}

	if len(doc.Text) > maxExtract {
		doc.Text = doc.Text[:maxExtract]
		doc.Truncated = true
	}
	return doc, nil
}

func joinSections(sections []Section) string {
	var sb strings.Builder
	for _, s := range sections {
		if s.Text == "" {
			continue
		}
		fmt.Fprintf(&sb, "── %s ──\n%s\n\n", s.Name, s.Text)
	}
	return strings.TrimSpace(sb.String())
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTP"[exp])
}

// Summary describes a document for display.
func (d *Document) Summary() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s (%s)", filepath.Base(d.Path), d.Format)
	if len(d.Sections) > 0 {
		fmt.Fprintf(&sb, ", %d sections", len(d.Sections))
	}
	if d.Text != "" {
		words := len(strings.Fields(d.Text))
		fmt.Fprintf(&sb, ", ~%d words", words)
	}
	if d.Truncated {
		sb.WriteString(", truncated")
	}
	if d.Note != "" {
		fmt.Fprintf(&sb, " — %s", d.Note)
	}
	return sb.String()
}
