package docs

import (
	"fmt"
	"strconv"
	"strings"
)

// Styling for generated documents.
//
// # Why named styles rather than direct formatting
//
// A heading rendered as "bold, 16pt" *looks* like a heading and is not one.
// Word's navigation pane stays empty, a table of contents cannot be generated,
// and accessibility tools read it as ordinary text. The difference is
// `w:outlineLvl` and a style definition — a styles.xml part that has to be
// shipped, which is the only reason the first version went without it.
//
// # Why a spreadsheet needs styling at all
//
// A submitted spreadsheet with an unbolded header row and default column widths
// that truncate every label is data, not a document. Column widths in
// particular: Excel does not auto-fit on open, so text sits clipped until
// someone drags the column, and most people conclude the file is broken.

// --- docx styles ------------------------------------------------------------

// docxStyles defines real Heading 1-3, Normal and a list style.
//
// The w:name values matter: they map onto Word's built-in style identities, so
// Heading 1 here is the *same* Heading 1 the navigation pane and TOC look for.
const docxStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:docDefaults><w:rPrDefault><w:rPr>
<w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:cs="Calibri"/><w:sz w:val="22"/>
</w:rPr></w:rPrDefault>
<w:pPrDefault><w:pPr><w:spacing w:after="120" w:line="276" w:lineRule="auto"/></w:pPr></w:pPrDefault>
</w:docDefaults>
<w:style w:type="paragraph" w:default="1" w:styleId="Normal">
<w:name w:val="Normal"/><w:qFormat/>
</w:style>
<w:style w:type="paragraph" w:styleId="Title">
<w:name w:val="Title"/><w:basedOn w:val="Normal"/><w:qFormat/>
<w:pPr><w:spacing w:before="240" w:after="240"/><w:jc w:val="center"/></w:pPr>
<w:rPr><w:b/><w:sz w:val="52"/><w:color w:val="1F3864"/></w:rPr>
</w:style>
<w:style w:type="paragraph" w:styleId="Heading1">
<w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/>
<w:pPr><w:keepNext/><w:outlineLvl w:val="0"/><w:spacing w:before="360" w:after="120"/></w:pPr>
<w:rPr><w:b/><w:sz w:val="32"/><w:color w:val="1F3864"/></w:rPr>
</w:style>
<w:style w:type="paragraph" w:styleId="Heading2">
<w:name w:val="heading 2"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/>
<w:pPr><w:keepNext/><w:outlineLvl w:val="1"/><w:spacing w:before="280" w:after="120"/></w:pPr>
<w:rPr><w:b/><w:sz w:val="26"/><w:color w:val="2E5496"/></w:rPr>
</w:style>
<w:style w:type="paragraph" w:styleId="Heading3">
<w:name w:val="heading 3"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/>
<w:pPr><w:keepNext/><w:outlineLvl w:val="2"/><w:spacing w:before="240" w:after="100"/></w:pPr>
<w:rPr><w:b/><w:i/><w:sz w:val="24"/><w:color w:val="2E5496"/></w:rPr>
</w:style>
<w:style w:type="paragraph" w:styleId="ListParagraph">
<w:name w:val="List Paragraph"/><w:basedOn w:val="Normal"/><w:qFormat/>
<w:pPr><w:ind w:left="720"/><w:contextualSpacing/></w:pPr>
</w:style>
<w:style w:type="table" w:styleId="TableGrid">
<w:name w:val="Table Grid"/>
<w:tblPr><w:tblBorders>
<w:top w:val="single" w:sz="4" w:color="8496B0"/><w:left w:val="single" w:sz="4" w:color="8496B0"/>
<w:bottom w:val="single" w:sz="4" w:color="8496B0"/><w:right w:val="single" w:sz="4" w:color="8496B0"/>
<w:insideH w:val="single" w:sz="4" w:color="8496B0"/><w:insideV w:val="single" w:sz="4" w:color="8496B0"/>
</w:tblBorders></w:tblPr>
</w:style>
</w:styles>`

// docxNumbering defines a real bulleted list, so bullets are list items rather
// than a literal "•" typed into the paragraph.
const docxNumbering = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:abstractNum w:abstractNumId="0">
<w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="bullet"/><w:lvlText w:val="•"/>
<w:lvlJc w:val="left"/><w:pPr><w:ind w:left="720" w:hanging="360"/></w:pPr>
<w:rPr><w:rFonts w:ascii="Symbol" w:hAnsi="Symbol"/></w:rPr></w:lvl>
</w:abstractNum>
<w:abstractNum w:abstractNumId="1">
<w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="decimal"/><w:lvlText w:val="%1."/>
<w:lvlJc w:val="left"/><w:pPr><w:ind w:left="720" w:hanging="360"/></w:pPr></w:lvl>
</w:abstractNum>
<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>
<w:num w:numId="2"><w:abstractNumId w:val="1"/></w:num>
</w:numbering>`

// --- xlsx styles ------------------------------------------------------------

// xlsxStyles defines the cell formats a readable spreadsheet needs.
//
// Index order is load-bearing and Excel is unforgiving about it: fill ids 0 and
// 1 are reserved (none and gray125) and must be present even though nothing
// uses them, or the file is rejected.
const xlsxStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<numFmts count="3">
<numFmt numFmtId="164" formatCode="#,##0"/>
<numFmt numFmtId="165" formatCode="#,##0.00"/>
<numFmt numFmtId="166" formatCode="0.0%"/>
</numFmts>
<fonts count="3">
<font><sz val="11"/><name val="Calibri"/></font>
<font><b/><sz val="11"/><color rgb="FFFFFFFF"/><name val="Calibri"/></font>
<font><b/><sz val="11"/><name val="Calibri"/></font>
</fonts>
<fills count="3">
<fill><patternFill patternType="none"/></fill>
<fill><patternFill patternType="gray125"/></fill>
<fill><patternFill patternType="solid"><fgColor rgb="FF2E5496"/><bgColor indexed="64"/></patternFill></fill>
</fills>
<borders count="2">
<border><left/><right/><top/><bottom/><diagonal/></border>
<border>
<left style="thin"><color rgb="FFB4C6E7"/></left><right style="thin"><color rgb="FFB4C6E7"/></right>
<top style="thin"><color rgb="FFB4C6E7"/></top><bottom style="thin"><color rgb="FFB4C6E7"/></bottom>
<diagonal/></border>
</borders>
<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
<cellXfs count="6">
<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>
<xf numFmtId="0" fontId="1" fillId="2" borderId="1" xfId="0" applyFont="1" applyFill="1" applyBorder="1" applyAlignment="1"><alignment horizontal="center" vertical="center" wrapText="1"/></xf>
<xf numFmtId="0" fontId="0" fillId="0" borderId="1" xfId="0" applyBorder="1"/>
<xf numFmtId="164" fontId="0" fillId="0" borderId="1" xfId="0" applyNumberFormat="1" applyBorder="1"/>
<xf numFmtId="165" fontId="0" fillId="0" borderId="1" xfId="0" applyNumberFormat="1" applyBorder="1"/>
<xf numFmtId="0" fontId="2" fillId="0" borderId="1" xfId="0" applyFont="1" applyBorder="1"/>
</cellXfs>
</styleSheet>`

// Style indices into cellXfs above.
const (
	styleDefault = 0
	styleHeader  = 1
	styleText    = 2
	styleInt     = 3
	styleDecimal = 4
	styleBold    = 5
)

// columnWidths sizes each column to its widest cell.
//
// Excel does not auto-fit on open, so without this every label sits clipped and
// the file reads as broken. Bounded at both ends: too narrow truncates, too
// wide pushes later columns off the screen.
func columnWidths(rows [][]string) []float64 {
	const (
		minWidth = 9.0
		maxWidth = 55.0
		padding  = 2.5
	)

	var widest []float64
	for _, row := range rows {
		for c, cell := range row {
			for len(widest) <= c {
				widest = append(widest, 0)
			}
			// Measure the longest line, since wrapped headers span several.
			for _, line := range strings.Split(cell, "\n") {
				if w := float64(len([]rune(line))); w > widest[c] {
					widest[c] = w
				}
			}
		}
	}

	out := make([]float64, len(widest))
	for i, w := range widest {
		w += padding
		if w < minWidth {
			w = minWidth
		}
		if w > maxWidth {
			w = maxWidth
		}
		out[i] = w
	}
	return out
}

// cellStyle picks a format for a value: thousands separators for whole numbers,
// two decimals for fractions, plain text otherwise.
func cellStyle(value string, isHeader bool) int {
	if isHeader {
		return styleHeader
	}
	if !isNumeric(value) {
		return styleText
	}
	if strings.Contains(value, ".") {
		return styleDecimal
	}
	// Small integers read better without a thousands separator.
	if n, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && n < 1000 && n > -1000 {
		return styleText
	}
	return styleInt
}

// colsXML renders the column-width definitions.
func colsXML(widths []float64) string {
	if len(widths) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<cols>")
	for i, w := range widths {
		fmt.Fprintf(&sb, `<col min="%d" max="%d" width="%.2f" customWidth="1"/>`, i+1, i+1, w)
	}
	sb.WriteString("</cols>")
	return sb.String()
}

// freezeHeaderXML keeps the header row visible while scrolling, which is the
// single change that makes a long sheet usable.
const freezeHeaderXML = `<sheetViews><sheetView workbookViewId="0">` +
	`<pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/>` +
	`<selection pane="bottomLeft" activeCell="A2" sqref="A2"/>` +
	`</sheetView></sheetViews>`
