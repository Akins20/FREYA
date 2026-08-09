package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/akins/jarvis/internal/guard"
	"github.com/akins/jarvis/internal/llm"
)

// Slides, on the engine that already renders documents.
//
// A deck is a PDF whose pages are landscape and whose content fills them, so
// almost none of this is new: the same browser, the same printToPDF, the same
// CSS. What it adds is the part that is fiddly to get right and boring to get
// wrong every time — 16:9 page geometry, one slide per page, and no margin, so
// a background actually reaches the edge instead of floating in a white border.
//
// # Why the geometry is injected rather than left to her
//
// Because the failure mode is silent and ugly. Get @page slightly wrong and the
// deck still renders: it simply has a white gutter down one side, or slides that
// split across two pages, and neither is visible until someone opens it. The
// numbers below are the ones that produce a clean 16:9 page at print scale, and
// they are not interesting enough to re-derive per deck.
//
// Her stylesheet comes after this one, so anything here can be overridden — a
// deck that wants 4:3, or A4 portrait for a handout, only has to say so.

// slideBase is the geometry every deck needs and none should have to work out.
//
// 13.333in × 7.5in is 16:9 at the size PowerPoint itself uses, so a PDF made
// here and a deck made there are the same shape on a projector. Margin zero
// because a slide's background is meant to bleed to the edge; padding belongs to
// the slide, not the page.
const slideBase = `<style id="freya-slide-base">
  @page { size: 13.333in 7.5in; margin: 0; }
  html, body { margin: 0; padding: 0; }
  .slide {
    position: relative;
    width: 13.333in; height: 7.5in;
    box-sizing: border-box;
    overflow: hidden;
    page-break-after: always;
    break-after: page;
    display: flex; flex-direction: column; justify-content: center;
    padding: 0.8in;
  }
  .slide:last-child { page-break-after: auto; break-after: auto; }
</style>
`

// RegisterSlides adds the deck writer.
func RegisterSlides(r *Registry, g *guard.Guard) {
	if g == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "slides_design",
			Description: "Build a slide deck as a PDF, from HTML and CSS you write.\n\n" +
				"Each slide is one '<div class=\"slide\">…</div>'. The page geometry is " +
				"handled for you — 16:9, full bleed, one slide per page — so you write " +
				"the content and the look, not the plumbing. Everything pdf_design can " +
				"do works here: gradients, web fonts, shadows, grid and flex layout, " +
				"backgrounds that print, and charts as inline SVG.\n\n" +
				"Make them look like slides rather than a document cut into pieces: " +
				"large type, one idea per slide, plenty of space, a consistent accent " +
				"colour. A slide with a paragraph on it is a slide nobody reads.\n\n" +
				"Your own <style> is applied after the base, so override anything you " +
				"want — a 4:3 deck or a portrait handout only has to say so.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path": {Type: "string", Description: "Where to write the .pdf."},
				"html": {Type: "string", Description: "The slides: a sequence of " +
					"<div class=\"slide\"> blocks, plus your own <style>."},
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

			slides := strings.Count(html, `class="slide"`) + strings.Count(html, "class='slide'")
			if slides == 0 {
				return "", fmt.Errorf("no slides found — each one is a <div class=\"slide\">…</div>, " +
					"and without them this is a single long page rather than a deck")
			}

			action := guard.Action{Kind: guard.KindWrite, Paths: []string{path},
				Reason: argString(args, "reason")}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				data, note, err := renderHTMLToPDF(ctx, withSlideBase(html))
				if err != nil {
					return "", err
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return "", err
				}
				if err := os.WriteFile(path, data, 0o644); err != nil {
					return "", err
				}
				return fmt.Sprintf("Wrote %s — %d slide(s), %d KB, 16:9.%s\n\nOpen it with "+
					"system_open if they should see it.", path, slides, len(data)/1024, note), nil
			})
		},
	})
}

// withSlideBase puts the geometry in front of her markup.
//
// Inserted after <head> when there is one so the document stays well-formed, and
// prepended otherwise — Chrome will parse a fragment either way, but a stylesheet
// stranded after <body> is a stylesheet that has already lost to anything inline.
// Either way it goes BEFORE her own styles, so hers win.
func withSlideBase(html string) string {
	lower := strings.ToLower(html)
	if i := strings.Index(lower, "<head>"); i >= 0 {
		at := i + len("<head>")
		return html[:at] + "\n" + slideBase + html[at:]
	}
	if i := strings.Index(lower, "<html"); i >= 0 {
		if j := strings.Index(lower[i:], ">"); j >= 0 {
			at := i + j + 1
			return html[:at] + "\n<head>" + slideBase + "</head>" + html[at:]
		}
	}
	return slideBase + html
}
