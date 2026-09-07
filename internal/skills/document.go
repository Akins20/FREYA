package skills

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/a11y"
	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/llm"
)

// Working in the document the user already has open.
//
// # Why this is not the docs package
//
// internal/docs writes files. That covers "make me a spreadsheet" and covers
// nothing about the spreadsheet already on screen with unsaved edits in it —
// which is where a person actually works. Asked to add a column to the sheet
// they were looking at, the best she could do was write a second file beside it
// and hope, and a second file is not the answer to a question about the first.
//
// # Why the clipboard rather than the accessibility tree
//
// This was measured, not assumed. LibreOffice does expose its grid over AT-SPI,
// down to Table and TableCell interfaces — and it is unusable for the job:
//
//   - the sheet reports its dimensions as 1,048,576 x 16,384, the whole
//     addressable grid rather than the used range, so there is nothing to
//     enumerate against
//   - cell objects are realised only while on screen, so anything scrolled out
//     of view does not exist to read
//   - Text.GetText on a cell holding "region" came back empty
//
// The clipboard route was tried against the same live document and returned the
// whole used range as tab-separated text, first go, including 360 for a Total
// cell holding =SUM(B2:B4) — the value LibreOffice had computed, which is the
// number the user can see and the number the file on disk does not contain.
//
// So this drives the application the way a person does: select, copy, read what
// landed. It needs no bridge, no extension and no package the machine does not
// already have, and it works the same for Writer as for Calc.
//
// # What it costs
//
// The clipboard is the user's. Reading a document replaces whatever was on it,
// and there is no way to put the old contents back that does not risk being
// wrong about what they were. That is stated in every tool description here
// rather than hidden, because someone mid-copy-paste deserves to know.

// A document window is identified by the application in its title. LibreOffice
// writes "name.xlsx — LibreOffice Calc"; the separator is an em dash.
const officeMark = "LibreOffice"

// docKind is what sort of document a window holds, which decides how to select
// all of it.
type docKind int

const (
	kindUnknown docKind = iota
	kindSheet           // Calc: a grid, read as tab-separated values
	kindText            // Writer: prose
	kindOther           // Impress, Draw: select-all still works, meaning less
)

func (k docKind) String() string {
	switch k {
	case kindSheet:
		return "spreadsheet"
	case kindText:
		return "document"
	case kindOther:
		return "presentation or drawing"
	}
	return "unknown"
}

// openDoc is one document currently on screen.
type openDoc struct {
	Title string // the full window title
	Name  string // the file name part
	App   string // "LibreOffice Calc"
	Kind  docKind
}

// classify reads a window title into a document, or reports that it is not one.
func classify(title string) (openDoc, bool) {
	if !strings.Contains(title, officeMark) {
		return openDoc{}, false
	}
	d := openDoc{Title: title}
	// The em dash is what LibreOffice uses; a hyphen is accepted too, because
	// builds and locales differ and being wrong about the separator would make
	// every document invisible.
	//
	// The width comes from the separator that matched, not a constant. An em dash
	// is three BYTES in UTF-8, so " — " is five and " - " is three — assuming one
	// figure for both left a stray byte on the front of every application name.
	sep, width := -1, 0
	for _, cand := range []string{" — ", " - ", " – "} {
		if i := strings.LastIndex(title, cand); i > sep {
			sep, width = i, len(cand)
		}
	}
	if sep < 0 {
		return openDoc{}, false
	}
	d.Name = strings.TrimSpace(title[:sep])
	d.App = strings.TrimSpace(title[sep+width:])
	switch {
	case strings.Contains(d.App, "Calc"):
		d.Kind = kindSheet
	case strings.Contains(d.App, "Writer"):
		d.Kind = kindText
	case strings.Contains(d.App, "Impress"), strings.Contains(d.App, "Draw"):
		d.Kind = kindOther
	default:
		d.Kind = kindUnknown
	}
	return d, true
}

// openDocuments lists the documents currently on screen.
func openDocuments(ctx context.Context) ([]openDoc, error) {
	titles, err := onScreenTitles(ctx)
	if err != nil {
		return nil, err
	}
	var out []openDoc
	for _, t := range titles {
		if d, ok := classify(t); ok {
			out = append(out, d)
		}
	}
	return out, nil
}

// pick finds the document a request names, or the only one if it names none.
//
// Refusing to guess between two open spreadsheets is deliberate: the cost of
// writing into the wrong one is a person's work, and "which one" is a question
// with a cheap answer.
func pick(docs []openDoc, want string) (openDoc, error) {
	if len(docs) == 0 {
		return openDoc{}, fmt.Errorf("no document is open — nothing matched a " +
			"LibreOffice window. Open the file first, or write a new one with the " +
			"file tools instead")
	}
	if want == "" {
		if len(docs) == 1 {
			return docs[0], nil
		}
		var names []string
		for _, d := range docs {
			names = append(names, d.Name)
		}
		return openDoc{}, fmt.Errorf("%d documents are open (%s) — say which one",
			len(docs), strings.Join(names, ", "))
	}
	lower := strings.ToLower(want)
	var hits []openDoc
	for _, d := range docs {
		if strings.Contains(strings.ToLower(d.Name), lower) {
			hits = append(hits, d)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		var names []string
		for _, d := range docs {
			names = append(names, d.Name)
		}
		return openDoc{}, fmt.Errorf("no open document matches %q; open now: %s",
			want, strings.Join(names, ", "))
	default:
		var names []string
		for _, d := range hits {
			names = append(names, d.Name)
		}
		return openDoc{}, fmt.Errorf("%q matches %d documents (%s) — be more specific",
			want, len(hits), strings.Join(names, ", "))
	}
}

// focusDoc brings a document to the front and waits for it to actually be there.
//
// The wait is not politeness. Keystrokes go to whatever holds focus at the
// instant they are sent, so sending them before the window manager has finished
// raising the window types them into whatever was in front — which is the one
// failure mode of synthetic input that cannot be undone by trying again.
func focusDoc(ctx context.Context, d openDoc) error {
	if !have("wmctrl") {
		return fmt.Errorf("wmctrl is not installed, so windows cannot be raised")
	}
	if _, err := run(ctx, 10*time.Second, "wmctrl", "-a", d.Title); err != nil {
		return fmt.Errorf("could not bring %q to the front: %w", d.Name, err)
	}
	for i := 0; i < 20; i++ {
		if strings.Contains(focusedWindowName(ctx), d.Name) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return fmt.Errorf("%q would not come to the front; it may be minimised on "+
		"another workspace", d.Name)
}

// keys sends one key combination and gives the application a moment to act.
func keys(ctx context.Context, combo string) error {
	if _, err := run(ctx, 15*time.Second, "xdotool", "key", "--clearmodifiers", "--", combo); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(250 * time.Millisecond):
	}
	return nil
}

// typeInto types literal text, at a rate the application can keep up with.
func typeInto(ctx context.Context, text string) error {
	_, err := run(ctx, 60*time.Second, "xdotool", "type", "--delay", "18", "--", text)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(200 * time.Millisecond):
	}
	return nil
}

// selectAll selects the whole document, by the route its kind needs.
//
// Ctrl+A in Calc selects every cell of the addressable grid, a million rows of
// mostly nothing, and copying that is slow enough to look like a hang. Ctrl+End
// jumps to the last cell that has anything in it, and selecting back to A1 from
// there is exactly the used range.
func selectAll(ctx context.Context, k docKind) error {
	if k == kindSheet {
		if err := keys(ctx, "ctrl+End"); err != nil {
			return err
		}
		return keys(ctx, "ctrl+shift+Home")
	}
	return keys(ctx, "ctrl+a")
}

// readDocument copies a document's content out through the clipboard.
func readDocument(ctx context.Context, d openDoc) (string, error) {
	if err := focusDoc(ctx, d); err != nil {
		return "", err
	}
	if err := selectAll(ctx, d.Kind); err != nil {
		return "", err
	}
	if err := keys(ctx, "ctrl+c"); err != nil {
		return "", err
	}
	// Give the application time to own the selection before asking for it. A
	// clipboard read that races the copy returns whatever was there before,
	// which is the previous document's contents and looks entirely plausible.
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(600 * time.Millisecond):
	}

	bin := firstOf("xclip", "xsel")
	if bin == "" {
		return "", fmt.Errorf("reading a document this way needs xclip or xsel on " +
			"PATH and neither is installed")
	}
	text, err := run(ctx, 10*time.Second, bin, clipReadArgs(bin)...)
	if err != nil {
		if strings.Contains(err.Error(), "target STRING not available") {
			return "", fmt.Errorf("nothing was copied — %s may be empty, or the "+
				"selection did not take", d.Name)
		}
		return "", err
	}
	// Leave the cursor somewhere sane rather than holding a selection over the
	// user's whole document.
	if d.Kind == kindSheet {
		_ = keys(ctx, "ctrl+Home")
	} else {
		_ = keys(ctx, "Left")
	}
	return text, nil
}

// goToCell moves the cursor to a cell address in Calc.
//
// Through the Name Box (Ctrl+Shift+T), not by counting arrow keys: an address is
// exact and a hundred Rights is a hundred chances to be off by one. Verified
// against a live Calc window — the box takes focus, an address and Return, and
// the cursor is there.
func goToCell(ctx context.Context, ref string) error {
	if err := keys(ctx, "ctrl+shift+t"); err != nil {
		return err
	}
	if err := typeInto(ctx, ref); err != nil {
		return err
	}
	return keys(ctx, "Return")
}

// writeBlock pastes a block of tab-separated rows into a sheet at ref.
//
// Pasted rather than typed. Typing a grid means sending Tab and Return between
// every value and trusting the application's autocomplete, autocorrect and
// autoinput not to help — and Calc's autoinput will happily finish "Nor" as
// "North" from the column above. A paste is one operation and lands exactly what
// was on the clipboard.
func writeBlock(ctx context.Context, d openDoc, ref, tsv string) error {
	bin := firstOf("xclip", "xsel")
	if bin == "" {
		return fmt.Errorf("writing into a document this way needs xclip or xsel on PATH")
	}
	if err := focusDoc(ctx, d); err != nil {
		return err
	}
	if ref != "" {
		if err := goToCell(ctx, ref); err != nil {
			return err
		}
	}
	if err := clipWrite(ctx, bin, tsv); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(300 * time.Millisecond):
	}
	// Unformatted paste, so a block copied from anywhere does not drag fonts and
	// cell colours in with it. Ctrl+Shift+V opens a dialog; this is the direct
	// one Calc gives for text.
	return keys(ctx, "ctrl+v")
}

// saveDoc saves, and answers the format question it provokes.
//
// Saving anything that is not ODF puts up "Non-standard file format" with two
// buttons, and the wrong one silently rewrites a .xlsx as .ods. Enter happens to
// pick keep-the-format on this build, and that is not a thing to rely on across
// versions and locales, so the button is found by its label and clicked. Enter
// is the fallback, not the plan.
func saveDoc(ctx context.Context, d openDoc) (string, error) {
	if err := focusDoc(ctx, d); err != nil {
		return "", err
	}
	if err := keys(ctx, "ctrl+s"); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(1200 * time.Millisecond):
	}

	if answered, how := answerFormatDialog(ctx, d); answered {
		return fmt.Sprintf("Saved %s, %s.", d.Name, how), nil
	}
	return fmt.Sprintf("Saved %s.", d.Name), nil
}

// answerFormatDialog deals with "Non-standard file format" if it appeared.
//
// Saving anything that is not ODF puts up a dialog with two buttons — "Use ODF
// Format" and "Use Excel 2007 Format" — and picking the wrong one rewrites a
// .xlsx as .ods without saying so. Enter picks keep-the-format on the build this
// was written against, which is a fact about one build and not a thing to rest a
// user's file on, so the button is found by its label.
//
// The dialog publishes no window title at all, so it cannot be looked up by
// name; it is found by walking LibreOffice's own windows for a button that says
// what it does.
func answerFormatDialog(ctx context.Context, d openDoc) (bool, string) {
	reader, err := a11y.Open(ctx)
	if err != nil {
		return false, ""
	}
	apps, err := reader.Applications(ctx)
	if err != nil {
		return false, ""
	}
	keepODF := strings.HasSuffix(strings.ToLower(d.Name), ".ods") ||
		strings.HasSuffix(strings.ToLower(d.Name), ".odt")

	for _, app := range apps {
		if !strings.Contains(strings.ToLower(app.Name), "soffice") &&
			!strings.Contains(strings.ToLower(app.Name), "libreoffice") {
			continue
		}
		for _, w := range app.Children {
			// Only the dialog is nameless; walking every document window looking
			// for a Format button would be slow and would find nothing.
			if strings.TrimSpace(w.Name) != "" {
				continue
			}
			reader.Walk(ctx, w)
			btn := findButton(w, func(name string) bool {
				if !strings.HasPrefix(name, "Use ") || !strings.Contains(name, "Format") {
					return false
				}
				isODF := strings.Contains(name, "ODF")
				return isODF == keepODF
			})
			if btn == nil {
				continue
			}
			acts := reader.Actions(ctx, btn)
			if i, ok := a11y.PreferredAction(acts); ok {
				if err := reader.Do(ctx, btn, i); err == nil {
					time.Sleep(500 * time.Millisecond)
					return true, "keeping its " + formatWord(d.Name) + " format"
				}
			}
		}
	}
	return false, ""
}

// findButton walks a subtree for the first push button whose label satisfies
// want.
func findButton(n *a11y.Node, want func(string) bool) *a11y.Node {
	if n == nil {
		return nil
	}
	if strings.Contains(n.Role, "button") && want(strings.TrimSpace(n.Name)) {
		return n
	}
	for _, c := range n.Children {
		if got := findButton(c, want); got != nil {
			return got
		}
	}
	return nil
}

// formatWord names the format a file is in, for saying so afterwards.
func formatWord(name string) string {
	switch {
	case strings.HasSuffix(strings.ToLower(name), ".xlsx"):
		return "Excel"
	case strings.HasSuffix(strings.ToLower(name), ".docx"):
		return "Word"
	case strings.HasSuffix(strings.ToLower(name), ".csv"):
		return "CSV"
	}
	return "current"
}

// RegisterDocuments adds skills for working in a document that is already open.
func RegisterDocuments(r *Registry, g *guard.Guard) {
	if g == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "document_list",
			Description: "List the documents the user currently has open — " +
				"spreadsheets, text documents, presentations — with the application " +
				"each is in.\n\n" +
				"Check here first whenever they talk about a document as though it is in " +
				"front of them: 'this sheet', 'the file I have open', 'add a column to " +
				"that'. Writing a new file beside the one they are looking at is not an " +
				"answer to a question about the one they are looking at.",
			Params: llm.ObjectSchema(nil),
		},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			if err := requireX11(); err != nil {
				return "", err
			}
			docs, err := openDocuments(ctx)
			if err != nil {
				return "", err
			}
			if len(docs) == 0 {
				return "No document is open. Nothing on screen is a LibreOffice window.", nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%d open:\n", len(docs))
			for _, d := range docs {
				fmt.Fprintf(&b, "  %s — %s (%s)\n", d.Name, d.App, d.Kind)
			}
			return b.String(), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "document_read",
			Description: "Read what is in an open document, as it stands right now — " +
				"including edits that have not been saved.\n\n" +
				"A spreadsheet comes back as tab-separated rows covering the used range, " +
				"with formulas shown as the values they have computed to. That is the " +
				"number the user can see, and it is often not in the file on disk.\n\n" +
				"Use this rather than file_read whenever the document is open: the file " +
				"on disk is the last save, which may be an hour ago.\n\n" +
				"It works by selecting and copying, so it replaces what is on their " +
				"clipboard and briefly moves their cursor. Say that you have done it.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"document": {Type: "string", Description: "Part of the file name. Omit " +
					"when only one document is open."},
			}),
		},
		Serial: true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if err := requireX11(); err != nil {
				return "", err
			}
			if !have("xdotool") {
				return "", fmt.Errorf("xdotool is not installed, so nothing here can " +
					"drive the application")
			}
			docs, err := openDocuments(ctx)
			if err != nil {
				return "", err
			}
			d, err := pick(docs, argString(args, "document"))
			if err != nil {
				return "", err
			}
			text, err := readDocument(ctx, d)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(text) == "" {
				return fmt.Sprintf("%s is open and empty.", d.Name), nil
			}
			return fmt.Sprintf("%s (%s), as it stands on screen:\n\n%s",
				d.Name, d.Kind, text), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "document_write",
			Description: "Put values into an open spreadsheet, or text into an open " +
				"document, without closing it or writing a second file.\n\n" +
				"For a spreadsheet give 'at' as a cell address (D1, A12) and 'content' as " +
				"rows of tab-separated values — one line per row. The block is pasted, so " +
				"a whole table lands in one go.\n\n" +
				"Read the document first so you know what is there. This overwrites " +
				"whatever occupies the cells you paste over, and their document may have " +
				"work in it you did not put there.\n\n" +
				"It does not save. Use document_save when they want it saved.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"content": {Type: "string", Description: "What to put in. For a sheet, " +
					"rows of tab-separated values separated by newlines."},
				"at": {Type: "string", Description: "Cell address to start at, for a " +
					"spreadsheet — D1, A12. Omit for a text document, where it goes at " +
					"the cursor."},
				"document": {Type: "string", Description: "Part of the file name. Omit " +
					"when only one document is open."},
				"reason": {Type: "string", Description: "Why, in a few words."},
			}, "content"),
		},
		Mutates: true,
		Serial:  true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if err := requireX11(); err != nil {
				return "", err
			}
			if !have("xdotool") {
				return "", fmt.Errorf("xdotool is not installed")
			}
			content := argString(args, "content")
			if content == "" {
				return "", fmt.Errorf("content is required")
			}
			docs, err := openDocuments(ctx)
			if err != nil {
				return "", err
			}
			d, err := pick(docs, argString(args, "document"))
			if err != nil {
				return "", err
			}
			at := strings.TrimSpace(argString(args, "at"))
			if d.Kind == kindSheet && at == "" {
				return "", fmt.Errorf("say which cell to start at — writing at wherever " +
					"the cursor happens to be would overwrite whatever it is sitting on")
			}
			reason := argString(args, "reason")
			if reason == "" {
				reason = "write into the open document"
			}

			rows := strings.Count(strings.TrimRight(content, "\n"), "\n") + 1
			where := d.Name
			if at != "" {
				where = d.Name + " at " + at
			}
			// Guarded like every other synthetic input, and for the sharper reason
			// that this one lands in a document the user has open and may not have
			// saved: an overwrite here costs work that exists nowhere else.
			action := guard.Action{
				Kind:    guard.KindInput,
				Command: "paste into " + d.App,
				Args:    []string{clip(content, 300)},
				Reason: fmt.Sprintf("%s — %d row(s) into %s, over whatever is there now",
					reason, rows, where),
			}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				if err := writeBlock(ctx, d, at, content); err != nil {
					return "", err
				}
				return fmt.Sprintf("Put %d row(s) into %s. Not saved yet.", rows, where), nil
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "document_save",
			Description: "Save an open document, keeping the format it is already in.\n\n" +
				"Saving a .xlsx or .docx makes LibreOffice ask whether to keep that " +
				"format or switch to ODF; this answers it by keeping theirs, so a " +
				"spreadsheet does not quietly become a .ods.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"document": {Type: "string", Description: "Part of the file name. Omit " +
					"when only one document is open."},
			}),
		},
		Mutates: true,
		Serial:  true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if err := requireX11(); err != nil {
				return "", err
			}
			if !have("xdotool") {
				return "", fmt.Errorf("xdotool is not installed")
			}
			docs, err := openDocuments(ctx)
			if err != nil {
				return "", err
			}
			d, err := pick(docs, argString(args, "document"))
			if err != nil {
				return "", err
			}
			action := guard.Action{
				Kind:    guard.KindInput,
				Command: "save " + d.Name,
				Reason:  "write the open document to disk, keeping its current format",
			}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				return saveDoc(ctx, d)
			})
		},
	})
}
