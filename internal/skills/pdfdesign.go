package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/browser"
	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/llm"
)

// The document engine she already had and nobody had pointed at documents.
//
// # Why not more OOXML
//
// The existing writers hand-build DOCX and XLSX, which is right for those
// formats and miserable for anything visual. A gradient fill or a drop shadow in
// WordprocessingML is a wall of XML for a result Word may still render its own
// way, and pdf_write goes markdown → DOCX → LibreOffice, so its ceiling is
// whatever Word styling survives a conversion.
//
// Meanwhile she drives Chrome, writes good CSS unprompted — the project audit
// came back as a styled dark dashboard nobody asked to be styled — and
// Page.printToPDF is already wired with printBackground and preferCSSPageSize,
// which are the two settings that decide whether a design survives printing at
// all. The best document engine on this machine was already running; nothing had
// ever aimed it at making a document.
//
// So: she writes HTML, Chrome renders it, the PDF is what she designed. Full
// CSS, real fonts, gradients, shadows, grid, and charts as inline SVG — no
// dependency, and nothing to hand-build.
//
// # Guest, not auth
//
// It renders in the isolated context. The page is her own markup and needs no
// session, and pointing the profile that carries the user's real cookies at
// locally-generated HTML would be an unnecessary risk for no gain.
//
// # Its own tab, closed afterwards
//
// A dedicated tab rather than whatever she happens to be driving, so rendering a
// report cannot navigate away from the page a task is working on — and closed
// when done, so a long session does not accumulate a browser full of documents.

// pdfRenderWait bounds how long to let a page settle before printing. Web fonts
// and any charting the page does for itself land after first paint, and printing
// early is how a document comes out with fallback type and empty boxes.
const pdfRenderWait = 6 * time.Second

// RegisterPDFDesign adds the HTML-rendered PDF writer.
func RegisterPDFDesign(r *Registry, g *guard.Guard) {
	if g == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "pdf_design",
			Description: "Produce a PDF from HTML and CSS you write, rendered by the " +
				"browser.\n\n" +
				"USE THIS when how it looks matters — a report, a proposal, a one-pager, " +
				"anything going in front of someone. You get real CSS: web fonts, " +
				"gradients, shadows, grid and flex layout, background colours, and " +
				"charts drawn as inline SVG. Write the whole document as one HTML " +
				"string, styling included.\n\n" +
				"pdf_write is the other one, and it is for plain prose from markdown. " +
				"docx_write is for when they need to edit it in Word. Reach for this " +
				"whenever the answer is 'it should look good'.\n\n" +
				"Page size and margins come from CSS: '@page { size: A4; margin: 18mm; }'. " +
				"Backgrounds and colours are printed, so a dark panel stays dark. Charts " +
				"have no library here — draw them as SVG, which is exact and prints " +
				"perfectly.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path": {Type: "string", Description: "Where to write the .pdf."},
				"html": {Type: "string", Description: "The complete HTML document, " +
					"including its <style>."},
				"reason":  {Type: "string", Description: "Why, shown when confirming."},
				"replace": {Type: "boolean", Description: "Required when a file already exists there."},
			}, "path", "html"),
		},
		Mutates: true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			path := expandIn(ctx, argString(args, "path"))
			html := argRaw(args, "html")
			if path == "" || strings.TrimSpace(html) == "" {
				return "", fmt.Errorf("path and html are required")
			}
			if !strings.HasSuffix(strings.ToLower(path), ".pdf") {
				path += ".pdf"
			}
			if err := mustMeanIt(path, argBool(args, "replace"),
				"write it under a name that is free, or pass replace=true"); err != nil {
				return "", err
			}

			action := guard.Action{Kind: guard.KindWrite, Paths: []string{path},
				Reason: argString(args, "reason")}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				data, note, err := renderHTMLToPDF(ctx, html)
				if err != nil {
					return "", err
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return "", err
				}
				if err := os.WriteFile(path, data, 0o644); err != nil {
					return "", err
				}
				return fmt.Sprintf("Wrote %s (%d KB, rendered from your HTML by the browser, "+
					"so it looks exactly as the markup describes).%s\n\nOpen it with "+
					"system_open if they should see it.", path, len(data)/1024, note), nil
			})
		},
	})
}

// renderHTMLToPDF puts the markup through Chrome and returns the PDF bytes.
//
// The temporary file is deliberate: a data: URL of any size runs into limits and
// makes relative references impossible, while a file on disk behaves like a page
// — an <img src="chart.png"> beside it resolves, which a data URL cannot do.
func renderHTMLToPDF(ctx context.Context, html string) ([]byte, string, error) {
	dir, err := os.MkdirTemp("", "freya-pdf-")
	if err != nil {
		return nil, "", fmt.Errorf("workspace for rendering: %w", err)
	}
	defer os.RemoveAll(dir)

	src := filepath.Join(dir, "document.html")
	if err := os.WriteFile(src, []byte(html), 0o644); err != nil {
		return nil, "", err
	}

	// Guest: this is her own markup and needs no session, so the profile carrying
	// the user's real cookies has no business rendering it.
	const bctx = browser.ContextGuest
	if err := browser.Launch(ctx, bctx); err != nil {
		return nil, "", fmt.Errorf("start the renderer: %w", err)
	}
	target, err := browser.NewTab(bctx, "about:blank")
	if err != nil {
		return nil, "", err
	}
	defer browser.CloseTab(bctx, target.ID)

	client, err := browser.Connect(bctx, target)
	if err != nil {
		return nil, "", err
	}
	defer client.Close()

	if err := client.Navigate(ctx, "file://"+src); err != nil {
		return nil, "", fmt.Errorf("render the page: %w", err)
	}
	// Navigate already waits for the content to settle; this is the second beat
	// for web fonts and anything the page draws for itself after first paint.
	client.WaitStable(ctx, pdfRenderWait)

	// Say so if the page broke on the way. A document with a script error in it
	// is usually a document with a missing chart, and the PDF will not show that.
	var note string
	if st := client.State(ctx); len(st.Errors) > 0 {
		note = fmt.Sprintf("\n\n[The page reported %d error(s) while rendering — anything "+
			"drawn by script may be missing: %s]", len(st.Errors), clip(st.Errors[0], 160))
	}

	data, err := client.PrintToPDF(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("print to PDF: %w", err)
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("the renderer produced an empty PDF")
	}
	return data, note, nil
}
