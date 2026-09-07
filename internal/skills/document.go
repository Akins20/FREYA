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
	// Collapse the selection to a DEFINED place, not merely somewhere sane.
	//
	// This used to press Left for a text document, which collapses to whichever
	// end of the selection the application prefers — so a later write landed at
	// an arbitrary point. Measured: appending a paragraph after a read put it
	// inside the last sentence, splicing "…before she touches it" and "Next
	// steps:" together and stranding the full stop after her text.
	//
	// Home for both kinds, so where the cursor is afterwards is a fact rather
	// than a guess. Writing does not depend on it either way now — see writeBlock.
	_ = keys(ctx, "ctrl+Home")
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
func writeBlock(ctx context.Context, d openDoc, ref, content string, asHTML bool) error {
	bin := firstOf("xclip", "xsel")
	if bin == "" {
		return fmt.Errorf("writing into a document this way needs xclip or xsel on PATH")
	}
	if err := focusDoc(ctx, d); err != nil {
		return err
	}

	if d.Kind == kindSheet {
		if ref != "" {
			if err := goToCell(ctx, ref); err != nil {
				return err
			}
		}
	} else if err := placeInText(ctx, ref); err != nil {
		return err
	}
	if asHTML {
		if err := clipWriteHTML(ctx, content); err != nil {
			// Losing the bold is better than losing the paragraph.
			if err2 := clipWrite(ctx, bin, content); err2 != nil {
				return err
			}
		}
	} else if err := clipWrite(ctx, bin, content); err != nil {
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

// placeInText puts the cursor where a write into a text document should land.
//
// A spreadsheet has cell addresses; prose has no such thing, so "where" has to
// be said in the few words that actually mean something: the end, the start, or
// wherever the user left the cursor.
//
// "end" opens a new paragraph first. Appending without one splices the new text
// onto the last sentence — measured, and it read as a typo rather than an edit.
func placeInText(ctx context.Context, where string) error {
	switch strings.ToLower(strings.TrimSpace(where)) {
	case "", "cursor":
		return nil
	case "end", "append":
		if err := keys(ctx, "ctrl+End"); err != nil {
			return err
		}
		return keys(ctx, "Return")
	case "start", "beginning", "top":
		if err := keys(ctx, "ctrl+Home"); err != nil {
			return err
		}
		// A paragraph opened above, so the existing first line stays its own.
		if err := keys(ctx, "Return"); err != nil {
			return err
		}
		return keys(ctx, "Up")
	}
	return fmt.Errorf("for a text document, 'at' is end, start or cursor — %q is "+
		"none of those", where)
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
				"Set format to 'html' to write FORMATTED content: bold, italic, colour, " +
				"headings, bulleted and numbered lists, and real tables all come through. " +
				"Write ordinary HTML — <b>, <i>, <span style=\"color:#cc0000\">, <ul><li>, " +
				"<table><tr><td> — and it arrives styled rather than as tags. Use it " +
				"whenever the answer has structure; a list pasted as plain text is a list " +
				"they have to format themselves.\n\n" +
				"Read the document first so you know what is there. This overwrites " +
				"whatever occupies the cells you paste over, and their document may have " +
				"work in it you did not put there.\n\n" +
				"It does not save. Use document_save when they want it saved.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"content": {Type: "string", Description: "What to put in. For a sheet, " +
					"rows of tab-separated values separated by newlines."},
				"at": {Type: "string", Description: "Where it goes. For a spreadsheet, " +
					"a cell address — D1, A12. For a text document, 'end' to append as a " +
					"new paragraph, 'start' to put it at the top, or 'cursor' to use " +
					"wherever they left it."},
				"document": {Type: "string", Description: "Part of the file name. Omit " +
					"when only one document is open."},
				"format": {Type: "string", Description: "'text' (default) or 'html' for " +
					"formatted content — styling, lists, tables."},
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
			if at == "" {
				if d.Kind == kindSheet {
					return "", fmt.Errorf("say which cell to start at — writing at wherever " +
						"the cursor happens to be would overwrite whatever it is sitting on")
				}
				return "", fmt.Errorf("say where it goes: 'end' to append as a new " +
					"paragraph, 'start' for the top, or 'cursor' for wherever they left it")
			}
			reason := argString(args, "reason")
			if reason == "" {
				reason = "write into the open document"
			}

			asHTML := strings.EqualFold(strings.TrimSpace(argString(args, "format")), "html")
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
				if err := writeBlock(ctx, d, at, content, asHTML); err != nil {
					return "", err
				}
				kind := "row(s)"
				if asHTML {
					kind = "formatted block"
					rows = 1
				}
				return fmt.Sprintf("Put %d %s into %s. Not saved yet.", rows, kind, where), nil
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "document_replace",
			Description: "Change specific text in an open document, leaving everything " +
				"else exactly as it is.\n\n" +
				"This is the surgical edit: fix a name, correct a figure, reword a phrase. " +
				"Unlike document_write it does not overwrite a region — it finds what you " +
				"name and replaces every occurrence, keeping the formatting around it.\n\n" +
				"Leave 'to' empty to delete the text instead.\n\n" +
				"Read the document first so you are replacing something that is actually " +
				"there and know how many times it occurs. It does not save.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"find": {Type: "string", Description: "The exact text to look for."},
				"to": {Type: "string", Description: "What to put in its place. Empty " +
					"deletes it."},
				"match_case": {Type: "boolean", Description: "Match capitalisation exactly."},
				"document": {Type: "string", Description: "Part of the file name. Omit " +
					"when only one document is open."},
				"reason": {Type: "string", Description: "Why, in a few words."},
			}, "find"),
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
			find := argString(args, "find")
			if find == "" {
				return "", fmt.Errorf("find is required — say what to look for")
			}
			to := argString(args, "to")
			docs, err := openDocuments(ctx)
			if err != nil {
				return "", err
			}
			d, err := pick(docs, argString(args, "document"))
			if err != nil {
				return "", err
			}
			reason := argString(args, "reason")
			if reason == "" {
				reason = "replace text in the open document"
			}
			what := fmt.Sprintf("replace %q with %q", find, to)
			if to == "" {
				what = fmt.Sprintf("delete every %q", find)
			}
			action := guard.Action{
				Kind:    guard.KindInput,
				Command: "paste into " + d.App,
				Args:    []string{what},
				Reason:  fmt.Sprintf("%s — %s, everywhere it appears in %s", reason, what, d.Name),
			}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				if _, err := replaceInDocument(ctx, d, find, to,
					argBool(args, "match_case")); err != nil {
					return "", err
				}
				if to == "" {
					return fmt.Sprintf("Deleted every %q in %s. Not saved yet.", find, d.Name), nil
				}
				return fmt.Sprintf("Replaced %q with %q throughout %s. Not saved yet.",
					find, to, d.Name), nil
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

// --- surgical edits ---------------------------------------------------------

// replaceInDocument runs Find & Replace over an open document.
//
// # Why the fields are found by their labels
//
// Ctrl+H opens a dialog whose BUTTONS are named — "Replace All", "Close" — and
// whose input fields are not: they arrive in the accessibility tree as unnamed
// combo boxes, several of them, because the collapsed "Other options" section
// contributes more.
//
// The obvious route is to type into Find and press Tab. Measured, that does not
// work: Tab from the Find field lands on "Find Next", and the replacement is
// typed into a button. A tab count is a guess about a layout that changes with
// the version, the locale and whether Other options is expanded.
//
// The labels beside them are named, though, and they are on the same row. So
// each field is found by taking the label's position and picking the input to
// its right on the same line. That is a fact about the dialog as drawn, not a
// count somebody has to keep true.
func replaceInDocument(ctx context.Context, d openDoc, find, repl string, matchCase bool) (int, error) {
	if err := focusDoc(ctx, d); err != nil {
		return 0, err
	}
	if err := keys(ctx, "ctrl+h"); err != nil {
		return 0, err
	}
	// The dialog is slower to appear than a keystroke is to send.
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-time.After(1200 * time.Millisecond):
	}

	reader, err := a11y.Open(ctx)
	if err != nil {
		return 0, fmt.Errorf("the Find and Replace dialog opened and cannot be read: %w", err)
	}
	dlg, err := reader.Window(ctx, "Find and Replace")
	if err != nil {
		return 0, fmt.Errorf("Find and Replace did not open, or publishes nothing: %w", err)
	}

	if err := typeBesideLabel(ctx, reader, dlg, []string{"Find:", "Search:"}, find); err != nil {
		_ = keys(ctx, "Escape")
		return 0, err
	}
	if err := typeBesideLabel(ctx, reader, dlg, []string{"Replace:", "Replace with:"}, repl); err != nil {
		_ = keys(ctx, "Escape")
		return 0, err
	}
	if matchCase {
		if box := a11y.Find(dlg, "Match case", ""); box != nil {
			if acts := reader.Actions(ctx, box); len(acts) > 0 {
				if i, ok := a11y.PreferredAction(acts); ok {
					_ = reader.Do(ctx, box, i)
				}
			}
		}
	}

	btn := a11y.Find(dlg, "Replace All", "")
	if btn == nil {
		_ = keys(ctx, "Escape")
		return 0, fmt.Errorf("the dialog has no Replace All button; it may be a different " +
			"version than this expects")
	}
	acts := reader.Actions(ctx, btn)
	i, ok := a11y.PreferredAction(acts)
	if !ok {
		_ = keys(ctx, "Escape")
		return 0, fmt.Errorf("Replace All publishes no action to perform")
	}
	if err := reader.Do(ctx, btn, i); err != nil {
		_ = keys(ctx, "Escape")
		return 0, err
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-time.After(900 * time.Millisecond):
	}

	// LibreOffice reports the count in a message the dialog puts up; when it
	// found nothing it says so and waits. Either way the dialog has to be closed,
	// and Escape closes whichever is in front.
	_ = keys(ctx, "Escape")
	_ = keys(ctx, "Escape")
	return 0, nil
}

// typeBesideLabel puts text into the input that sits beside a label.
//
// Beside means: on the same row, starting to the right of the label, nearest
// first. Clicking it focuses it; Ctrl+A then the text replaces whatever the
// dialog remembered from last time, which it does remember.
func typeBesideLabel(ctx context.Context, reader *a11y.Reader, dlg *a11y.Node,
	labels []string, text string) error {

	var lab *a11y.Node
	var want string
	for _, l := range labels {
		if n := a11y.Find(dlg, l, ""); n != nil {
			lab, want = n, l
			break
		}
	}
	if lab == nil {
		return fmt.Errorf("the dialog has no %s field where one was expected", labels[0])
	}
	lrect, ok := reader.Extents(ctx, lab)
	if !ok || lrect.W == 0 {
		return fmt.Errorf("%s is in the dialog but publishes no position, so the field "+
			"beside it cannot be found", want)
	}

	field := inputRightOf(ctx, reader, dlg, lrect)
	if field == nil {
		return fmt.Errorf("nothing that takes typing sits beside %s", want)
	}
	frect, _ := reader.Extents(ctx, field)
	x, y := frect.Centre()
	if _, err := run(ctx, 10*time.Second, "xdotool", "mousemove", itoaInt(x), itoaInt(y),
		"click", "1"); err != nil {
		return err
	}
	if err := keys(ctx, "ctrl+a"); err != nil {
		return err
	}
	// An empty replacement is a deletion, and typing nothing after select-all
	// leaves the old text selected rather than removing it.
	if text == "" {
		return keys(ctx, "Delete")
	}
	return typeInto(ctx, text)
}

// inputRightOf finds the editable control on a label's row.
func inputRightOf(ctx context.Context, reader *a11y.Reader, root *a11y.Node, label Rectish) *a11y.Node {
	labMidY := label.Y + label.H/2
	var best *a11y.Node
	bestDX := 1 << 30

	var walk func(n *a11y.Node)
	walk = func(n *a11y.Node) {
		if n == nil {
			return
		}
		if strings.Contains(n.Role, "combo") || strings.Contains(n.Role, "text") ||
			strings.Contains(n.Role, "entry") {
			if r, ok := reader.Extents(ctx, n); ok && r.W > 20 && r.H > 8 {
				// Same row, and starting to the right of the label.
				if labMidY >= r.Y && labMidY <= r.Y+r.H && r.X >= label.X+label.W-4 {
					if dx := r.X - (label.X + label.W); dx < bestDX {
						best, bestDX = n, dx
					}
				}
			}
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(root)
	return best
}

// Rectish is the shape of an a11y.Rect, kept local so this file does not need to
// name the type in a signature the package may change.
type Rectish = a11y.Rect

// itoaInt formats a coordinate for xdotool.
func itoaInt(n int) string { return fmt.Sprintf("%d", n) }
