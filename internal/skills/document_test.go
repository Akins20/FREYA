package skills

import (
	"os"
	"strings"
	"testing"
)

// A window title is the only thing identifying a document, so reading it wrong
// means either missing documents that are open or offering to type into Firefox.
func TestAWindowTitleIsReadIntoADocument(t *testing.T) {
	for _, c := range []struct {
		title string
		name  string
		app   string
		kind  docKind
		is    bool
	}{
		{"probe.xlsx — LibreOffice Calc", "probe.xlsx", "LibreOffice Calc", kindSheet, true},
		{"report.docx — LibreOffice Writer", "report.docx", "LibreOffice Writer", kindText, true},
		{"deck.odp — LibreOffice Impress", "deck.odp", "LibreOffice Impress", kindOther, true},
		// Some builds and locales use a hyphen rather than an em dash. Being wrong
		// about the separator would make every document invisible.
		{"notes.ods - LibreOffice Calc", "notes.ods", "LibreOffice Calc", kindSheet, true},
		// Everything else on the desktop is not a document.
		{"Peer Replies — Mozilla Firefox", "", "", kindUnknown, false},
		{"xfce4-terminal", "", "", kindUnknown, false},
		{"", "", "", kindUnknown, false},
	} {
		got, ok := classify(c.title)
		if ok != c.is {
			t.Errorf("classify(%q) recognised=%v, want %v", c.title, ok, c.is)
			continue
		}
		if !ok {
			continue
		}
		if got.Name != c.name || got.App != c.app || got.Kind != c.kind {
			t.Errorf("classify(%q) = name %q app %q kind %v; want %q %q %v",
				c.title, got.Name, got.App, got.Kind, c.name, c.app, c.kind)
		}
	}
}

// Choosing between two open spreadsheets is not a guess worth making: the cost
// of writing into the wrong one is somebody's unsaved work.
func TestItRefusesToGuessWhichDocument(t *testing.T) {
	two := []openDoc{
		{Name: "sales.xlsx", App: "LibreOffice Calc", Kind: kindSheet},
		{Name: "costs.xlsx", App: "LibreOffice Calc", Kind: kindSheet},
	}

	if _, err := pick(two, ""); err == nil {
		t.Error("picked one of two open documents with nothing to go on")
	} else if !strings.Contains(err.Error(), "sales.xlsx") ||
		!strings.Contains(err.Error(), "costs.xlsx") {
		t.Errorf("the refusal does not say what the choices are: %v", err)
	}

	got, err := pick(two, "sales")
	if err != nil || got.Name != "sales.xlsx" {
		t.Errorf("pick(sales) = %q, %v", got.Name, err)
	}

	// A single open document needs no naming.
	one := two[:1]
	if got, err := pick(one, ""); err != nil || got.Name != "sales.xlsx" {
		t.Errorf("pick of the only document = %q, %v", got.Name, err)
	}

	// An ambiguous name is as bad as no name.
	if _, err := pick(two, ".xlsx"); err == nil {
		t.Error("a name matching both documents was accepted")
	}

	// And nothing open says so plainly, rather than failing somewhere later.
	if _, err := pick(nil, "anything"); err == nil {
		t.Error("picked a document with none open")
	} else if !strings.Contains(err.Error(), "no document is open") {
		t.Errorf("unhelpful: %v", err)
	}
}

// Ctrl+A in Calc selects the whole addressable grid — a million rows of nothing,
// which copies slowly enough to look like a hang. The used range is what is
// wanted, and Ctrl+End reaches it.
func TestASpreadsheetIsNotSelectedWithCtrlA(t *testing.T) {
	src, err := readSource("document.go")
	if err != nil {
		t.Fatal(err)
	}
	fn := between(src, "func selectAll(", "\n}")
	if fn == "" {
		t.Fatal("selectAll has moved")
	}
	if !strings.Contains(fn, "ctrl+End") || !strings.Contains(fn, "ctrl+shift+Home") {
		t.Error("a sheet is no longer selected by its used range")
	}
	// The text path may use ctrl+a; the sheet path must not reach it.
	sheetPath := between(fn, "kindSheet", "return keys")
	if strings.Contains(sheetPath, "ctrl+a") {
		t.Error("a spreadsheet is being selected with ctrl+a")
	}
}

// Saving a .xlsx puts up a two-button dialog and the wrong button rewrites it as
// .ods. The button is chosen by its label, not by pressing Enter and hoping.
func TestTheSaveFormatIsChosenByLabelNotByEnter(t *testing.T) {
	src, err := readSource("document.go")
	if err != nil {
		t.Fatal(err)
	}
	fn := between(src, "func answerFormatDialog(", "\n// findButton")
	if fn == "" {
		t.Fatal("answerFormatDialog has moved")
	}
	if !strings.Contains(fn, "ODF") {
		t.Error("the dialog is answered without distinguishing ODF from the file's own format")
	}
	if strings.Contains(fn, `"Return"`) {
		t.Error("the format dialog is answered by pressing Return, which is a fact " +
			"about one build of LibreOffice and not a thing to rest a user's file on")
	}
}

func readSource(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i:], end)
	if j < 0 {
		return ""
	}
	return s[i : i+j]
}

// Prose needs a notion of where, and "wherever the cursor is" is not one.
//
// Measured: after document_read collapsed the selection with Left, appending a
// paragraph spliced it into the last sentence — "…before she touches itNext
// steps:" with the original full stop stranded after her text. It read as a typo
// rather than an edit. A spreadsheet has cell addresses; prose had nothing, so
// the paste landed wherever reading happened to leave the cursor.
func TestWritingProseHasToSayWhere(t *testing.T) {
	src, err := readSource("document.go")
	if err != nil {
		t.Fatal(err)
	}

	place := between(src, "func placeInText(", "\n}\n")
	if place == "" {
		t.Fatal("placeInText has moved")
	}
	// Appending must open a paragraph of its own, or it splices.
	appendPath := between(place, `case "end", "append":`, "case \"start\"")
	if !strings.Contains(appendPath, "ctrl+End") || !strings.Contains(appendPath, "Return") {
		t.Error("appending does not go to the end and open a new paragraph, so it " +
			"joins the last sentence")
	}
	// An unrecognised placement is refused rather than silently meaning "here".
	if !strings.Contains(place, "none of those") {
		t.Error("an unknown placement does not refuse; it would land at the cursor")
	}

	// And reading has to leave the cursor somewhere defined, or the next write
	// inherits an arbitrary position.
	read := between(src, "func readDocument(", "\n}\n")
	if strings.Contains(read, `keys(ctx, "Left")`) {
		t.Error("reading collapses the selection with Left, which lands at whichever " +
			"end the application prefers")
	}
	if !strings.Contains(read, "ctrl+Home") {
		t.Error("reading does not leave the cursor at a known place")
	}
}

// The Find and Replace fields are located by their labels, never by tabbing.
//
// Measured on the real dialog: typing into Find and pressing Tab lands on the
// "Find Next" button, so the replacement gets typed into a button and Replace
// stays empty. The dialog's inputs arrive in the accessibility tree unnamed —
// several unnamed combo boxes, because the collapsed "Other options" section
// contributes more of them — so there is nothing to match on but position.
//
// The labels beside them ARE named and sit on the same row, which makes "the
// input to the right of this label" a fact about the dialog as drawn rather than
// a tab count somebody has to keep true across versions and locales.
func TestFindAndReplaceFieldsAreFoundByLabel(t *testing.T) {
	src, err := readSource("document.go")
	if err != nil {
		t.Fatal(err)
	}
	fn := between(src, "func replaceInDocument(", "\n// typeBesideLabel")
	if fn == "" {
		t.Fatal("replaceInDocument has moved")
	}

	if !strings.Contains(fn, "typeBesideLabel") {
		t.Error("the fields are not located by their labels")
	}
	if strings.Contains(fn, `"Tab"`) {
		t.Error("the dialog is driven by pressing Tab, which lands on Find Next")
	}
	// Both label spellings, because LibreOffice has used each.
	for _, want := range []string{"Find:", "Replace:"} {
		if !strings.Contains(fn, want) {
			t.Errorf("the %q label is not looked for", want)
		}
	}
	// Replace All is a named button and is clicked through its own action, not
	// aimed at with the pointer.
	if !strings.Contains(fn, `"Replace All"`) {
		t.Error("Replace All is not found by name")
	}

	// An empty replacement is a deletion, and select-all followed by typing
	// nothing leaves the old text selected rather than removing it.
	place := between(src, "func typeBesideLabel(", "\n// inputRightOf")
	if !strings.Contains(place, `keys(ctx, "Delete")`) {
		t.Error("an empty replacement does not delete; it would leave the field unchanged")
	}
}

// Formatted content goes on the clipboard as HTML, which is what makes styling,
// lists and tables possible at all — and it falls back to plain text rather than
// failing, because losing the bold is better than losing the paragraph.
func TestFormattedContentFallsBackToPlainText(t *testing.T) {
	src, err := readSource("document.go")
	if err != nil {
		t.Fatal(err)
	}
	fn := between(src, "func writeBlock(", "\n// placeInText")
	if fn == "" {
		t.Fatal("writeBlock has moved")
	}
	if !strings.Contains(fn, "clipWriteHTML") {
		t.Error("formatted content is not written as HTML, so nothing is styled")
	}
	if !strings.Contains(fn, "clipWrite(ctx, bin, content)") {
		t.Error("there is no plain-text fallback; a machine without xclip loses the write entirely")
	}
}
