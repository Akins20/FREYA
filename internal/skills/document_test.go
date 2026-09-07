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
