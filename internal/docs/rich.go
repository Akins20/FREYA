package docs

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The parts that turn a generated file into something submittable: formulas and
// charts in a spreadsheet, page numbers and images in a document.
//
// Each of these needs additional parts *and* relationships. Omitting the
// relationship is the classic failure: the part is present, the file opens, and
// the feature is silently absent — which looks exactly like never having
// written it.

// --- spreadsheet formulas ---------------------------------------------------

// Formula marks a cell value as a formula rather than literal text.
//
// Written as a prefix rather than a separate type because the value arrives
// from a model as a string in a CSV row, and "=SUM(B2:B4)" is how a person
// writes one.
const FormulaPrefix = "="

// isFormula reports whether a cell should be emitted as a formula.
func isFormula(s string) bool {
	t := strings.TrimSpace(s)
	return len(t) > 1 && strings.HasPrefix(t, FormulaPrefix)
}

// formulaXML renders a formula cell.
//
// No cached <v> is written. Excel and LibreOffice recalculate on open, and a
// stale cached value shown before recalculation is worse than none.
func formulaXML(ref string, style int, formula string) string {
	expr := strings.TrimPrefix(strings.TrimSpace(formula), FormulaPrefix)
	return fmt.Sprintf(`<c r="%s" s="%d"><f>%s</f></c>`, ref, style, esc(expr))
}

// --- spreadsheet charts -----------------------------------------------------

// Chart describes a chart to draw beside the data.
type Chart struct {
	// Kind is "bar", "line" or "pie".
	Kind  string
	Title string
	// CategoryColumn is the zero-based column holding the labels.
	CategoryColumn int
	// ValueColumns are the zero-based columns holding the series.
	ValueColumns []int
	// Rows is how many data rows follow the header.
	Rows int
	// SheetName is the sheet the data lives on.
	SheetName string
}

// chartTypeTag maps a chart kind to its OOXML element name.
func chartTypeTag(kind string) (tag string, grouping string) {
	switch strings.ToLower(kind) {
	case "line":
		return "lineChart", ""
	case "pie":
		return "pieChart", ""
	default:
		return "barChart", `<c:barDir val="col"/><c:grouping val="clustered"/>`
	}
}

// chartXML builds a chart part from a data range.
func chartXML(ch Chart) string {
	tag, grouping := chartTypeTag(ch.Kind)
	sheet := ch.SheetName
	if sheet == "" {
		sheet = "Sheet1"
	}
	// Sheet names containing spaces must be quoted in a formula reference.
	ref := sheet
	if strings.ContainsAny(sheet, " '") {
		ref = "'" + strings.ReplaceAll(sheet, "'", "''") + "'"
	}

	catCol := columnName(ch.CategoryColumn)
	firstRow, lastRow := 2, ch.Rows+1

	var series strings.Builder
	for i, vc := range ch.ValueColumns {
		valCol := columnName(vc)
		fmt.Fprintf(&series, `<c:ser>
<c:idx val="%d"/><c:order val="%d"/>
<c:tx><c:strRef><c:f>%s!$%s$1</c:f></c:strRef></c:tx>
<c:cat><c:strRef><c:f>%s!$%s$%d:$%s$%d</c:f></c:strRef></c:cat>
<c:val><c:numRef><c:f>%s!$%s$%d:$%s$%d</c:f></c:numRef></c:val>
</c:ser>`,
			i, i,
			ref, valCol,
			ref, catCol, firstRow, catCol, lastRow,
			ref, valCol, firstRow, valCol, lastRow)
	}

	axes := `<c:axId val="111111111"/><c:axId val="222222222"/>`
	axisParts := `<c:catAx><c:axId val="111111111"/><c:scaling><c:orientation val="minMax"/></c:scaling>
<c:delete val="0"/><c:axPos val="b"/><c:crossAx val="222222222"/></c:catAx>
<c:valAx><c:axId val="222222222"/><c:scaling><c:orientation val="minMax"/></c:scaling>
<c:delete val="0"/><c:axPos val="l"/><c:crossAx val="111111111"/></c:valAx>`
	if tag == "pieChart" {
		axes, axisParts = "", ""
	}

	title := ""
	if ch.Title != "" {
		title = fmt.Sprintf(`<c:title><c:tx><c:rich>
<a:bodyPr xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"/>
<a:p xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:r><a:t>%s</a:t></a:r></a:p>
</c:rich></c:tx><c:overlay val="0"/></c:title><c:autoTitleDeleted val="0"/>`, esc(ch.Title))
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<c:chartSpace xmlns:c="http://schemas.openxmlformats.org/drawingml/2006/chart"
 xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
 xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<c:chart>%s<c:plotArea><c:layout/>
<c:%s>%s%s%s</c:%s>
%s</c:plotArea><c:legend><c:legendPos val="b"/><c:overlay val="0"/></c:legend>
<c:plotVisOnly val="1"/></c:chart></c:chartSpace>`,
		title, tag, grouping, series.String(), axes, tag, axisParts)
}

// chartDrawingXML anchors a chart onto a worksheet.
func chartDrawingXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<xdr:wsDr xmlns:xdr="http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing"
 xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
 xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<xdr:twoCellAnchor>
<xdr:from><xdr:col>6</xdr:col><xdr:colOff>0</xdr:colOff><xdr:row>1</xdr:row><xdr:rowOff>0</xdr:rowOff></xdr:from>
<xdr:to><xdr:col>15</xdr:col><xdr:colOff>0</xdr:colOff><xdr:row>20</xdr:row><xdr:rowOff>0</xdr:rowOff></xdr:to>
<xdr:graphicFrame macro="">
<xdr:nvGraphicFramePr><xdr:cNvPr id="2" name="Chart 1"/><xdr:cNvGraphicFramePr/></xdr:nvGraphicFramePr>
<xdr:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/></xdr:xfrm>
<a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/chart">
<c:chart xmlns:c="http://schemas.openxmlformats.org/drawingml/2006/chart" r:id="rId1"/>
</a:graphicData></a:graphic>
</xdr:graphicFrame><xdr:clientData/>
</xdr:twoCellAnchor></xdr:wsDr>`
}

// --- document headers, footers and page numbers -----------------------------

// PageSetup controls the running header and footer of a document.
type PageSetup struct {
	Header string
	Footer string
	// PageNumbers adds "Page N of M" to the footer.
	PageNumbers bool
}

// headerXML builds a running header part.
func headerXML(text string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:hdr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:p><w:pPr><w:jc w:val="right"/></w:pPr>
<w:r><w:rPr><w:sz w:val="18"/><w:color w:val="666666"/></w:rPr>
<w:t xml:space="preserve">%s</w:t></w:r></w:p></w:hdr>`, esc(text))
}

// footerXML builds a running footer, optionally with page numbering.
//
// Page numbers are field codes, not text: PAGE and NUMPAGES are evaluated by
// the word processor, so they stay correct when the document is edited.
func footerXML(text string, pageNumbers bool) string {
	var body strings.Builder
	body.WriteString(`<w:p><w:pPr><w:jc w:val="center"/></w:pPr>`)

	if text != "" {
		fmt.Fprintf(&body,
			`<w:r><w:rPr><w:sz w:val="18"/><w:color w:val="666666"/></w:rPr>`+
				`<w:t xml:space="preserve">%s</w:t></w:r>`, esc(text))
		if pageNumbers {
			body.WriteString(`<w:r><w:rPr><w:sz w:val="18"/><w:color w:val="666666"/></w:rPr>` +
				`<w:t xml:space="preserve">  ·  </w:t></w:r>`)
		}
	}

	if pageNumbers {
		body.WriteString(
			`<w:r><w:rPr><w:sz w:val="18"/><w:color w:val="666666"/></w:rPr><w:t>Page </w:t></w:r>` +
				`<w:r><w:fldChar w:fldCharType="begin"/></w:r>` +
				`<w:r><w:instrText xml:space="preserve"> PAGE </w:instrText></w:r>` +
				`<w:r><w:fldChar w:fldCharType="end"/></w:r>` +
				`<w:r><w:rPr><w:sz w:val="18"/><w:color w:val="666666"/></w:rPr><w:t> of </w:t></w:r>` +
				`<w:r><w:fldChar w:fldCharType="begin"/></w:r>` +
				`<w:r><w:instrText xml:space="preserve"> NUMPAGES </w:instrText></w:r>` +
				`<w:r><w:fldChar w:fldCharType="end"/></w:r>`)
	}
	body.WriteString(`</w:p>`)

	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:ftr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
		body.String() + `</w:ftr>`
}

// --- document images --------------------------------------------------------

// emuPerPixel converts pixels to English Metric Units, the unit OOXML uses for
// drawing dimensions. 914400 EMU per inch at 96 DPI.
const emuPerPixel = 9525

// imageDrawingXML places an image inline in a paragraph.
func imageDrawingXML(relID string, id int, widthPx, heightPx int, name string) string {
	cx := widthPx * emuPerPixel
	cy := heightPx * emuPerPixel
	return fmt.Sprintf(`<w:p><w:pPr><w:jc w:val="center"/></w:pPr><w:r><w:drawing>
<wp:inline distT="0" distB="0" distL="0" distR="0"
 xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing">
<wp:extent cx="%d" cy="%d"/><wp:docPr id="%d" name="%s"/>
<a:graphic xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
<a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture">
<pic:pic xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture">
<pic:nvPicPr><pic:cNvPr id="%d" name="%s"/><pic:cNvPicPr/></pic:nvPicPr>
<pic:blipFill><a:blip xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" r:embed="%s"/>
<a:stretch><a:fillRect/></a:stretch></pic:blipFill>
<pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="%d" cy="%d"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr>
</pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing></w:r></w:p>`,
		cx, cy, id, esc(name), id, esc(name), relID, cx, cy)
}

// imageInfo reads an image's dimensions and content type.
//
// Dimensions come from the file header rather than a decode: only the first few
// bytes are needed, and decoding a large photograph purely to learn its size
// would be wasteful.
func imageInfo(path string) (width, height int, contentType, ext string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, "", "", err
	}
	switch {
	case len(data) > 24 && string(data[1:4]) == "PNG":
		// IHDR width and height are big-endian at fixed offsets.
		width = int(data[16])<<24 | int(data[17])<<16 | int(data[18])<<8 | int(data[19])
		height = int(data[20])<<24 | int(data[21])<<16 | int(data[22])<<8 | int(data[23])
		return width, height, "image/png", "png", nil

	case len(data) > 4 && data[0] == 0xFF && data[1] == 0xD8:
		// Walk the JPEG segment chain to the start-of-frame marker.
		for i := 2; i+9 < len(data); {
			if data[i] != 0xFF {
				i++
				continue
			}
			marker := data[i+1]
			if marker >= 0xC0 && marker <= 0xCF &&
				marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
				height = int(data[i+5])<<8 | int(data[i+6])
				width = int(data[i+7])<<8 | int(data[i+8])
				return width, height, "image/jpeg", "jpeg", nil
			}
			segLen := int(data[i+2])<<8 | int(data[i+3])
			if segLen < 2 {
				break
			}
			i += 2 + segLen
		}
		return 0, 0, "", "", fmt.Errorf("could not read JPEG dimensions")
	}
	return 0, 0, "", "", fmt.Errorf("unsupported image format for embedding: %s",
		filepath.Ext(path))
}

// fitImage scales dimensions to fit the printable width of an A4 page.
//
// Without this a phone photograph is embedded at several thousand pixels wide
// and Word renders it running off the page.
func fitImage(w, h int) (int, int) {
	const maxWidthPx = 620 // A4 minus the 2cm margins, at 96 DPI
	if w <= maxWidthPx {
		return w, h
	}
	return maxWidthPx, int(float64(h) * float64(maxWidthPx) / float64(w))
}

func chartRels() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/chart" Target="../charts/chart1.xml"/>
</Relationships>`
}

func sheetDrawingRels() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/drawing" Target="../drawings/drawing1.xml"/>
</Relationships>`
}

var _ = strconv.Itoa
