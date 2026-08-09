package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/akins/jarvis/internal/llm"
	"github.com/akins/jarvis/internal/wiring"
)

// Whether the thing she built is actually joined up.
//
// # The measurement
//
// A flower shop site, built well: no cards, no stray borders, no emoji, a
// palette that suited it. Fifteen links, of which five went nowhere — one
// href="#", and four pointing at #weddings and #contact, which do not exist. The
// nav offered two pages that were never built.
//
// Nothing failed. code_check passed all three files, because the HTML parses
// perfectly: a link to nowhere is valid HTML. The page looked finished and was
// not, and the only way to find out was to click.
//
// # Why a checker rather than an instruction
//
// Measured today: telling her to stop emitting a named thing works, and telling
// her to do something well does not. Cards, borders, emoji and em dashes went to
// zero when named; padding, gradient angles and heading style did not move at
// all. Completeness is squarely in the second category — "make sure everything
// leads somewhere" is exactly the kind of guidance that reads well and changes
// nothing.
//
// So it is counted instead. A number she can run before saying she is finished,
// and which says the same thing to her as it would to the person clicking.

// RegisterSiteCheck adds the wiring check.
func RegisterSiteCheck(r *Registry) {
	r.Register(Skill{
		Tool: llm.Tool{
			Name: "site_check",
			Description: "Find the dead ends in a page or site you built — links that go " +
				"nowhere, menu items pointing at sections that do not exist, images and " +
				"stylesheets that are not on disk, forms that submit to nothing.\n\n" +
				"Run it before you say a site is finished. code_check tells you the HTML " +
				"parses; this tells you it is joined up, and they are different " +
				"questions — a link to nowhere is perfectly valid HTML. A nav offering " +
				"four pages when two exist looks complete and is not, and the only other " +
				"way to find out is for them to click it.\n\n" +
				"Anything it reports is either something to build or a reference to " +
				"remove. Both are fine; leaving it is not.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path": {Type: "string", Description: "An .html file, or a folder to check " +
					"every page in."},
			}, "path"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			root := expandIn(ctx, argString(args, "path"))
			if root == "" {
				return "", fmt.Errorf("path is required")
			}
			pages, err := htmlFilesUnder(root)
			if err != nil {
				return "", err
			}
			if len(pages) == 0 {
				return "", fmt.Errorf("no .html files at %s", root)
			}

			var problems []string
			for _, page := range pages {
				problems = append(problems, wiring.Page(page)...)
			}
			sort.Strings(problems)

			// Then the assets she does not own. A pottery site passed everything
			// above — four pages, fifty-seven links, none dead — and rendered with
			// two blank tiles, because two of its six background images were
			// Unsplash photo IDs that do not exist. Invented, well-formed, and in a
			// stylesheet, so nothing local could ever have caught them.
			dir := root
			if info, err := os.Stat(root); err == nil && !info.IsDir() {
				dir = filepath.Dir(root)
			}
			remote, unknown := wiring.Remote(dir, 8*time.Second, 40)
			problems = append(problems, remote...)
			unreachable := ""
			if unknown > 0 {
				unreachable = fmt.Sprintf("\n\n[%d external asset(s) did not answer in time. "+
					"That is not proof they are broken — it is proof they were not checked.]", unknown)
			}

			// Clean is the moment to hand it to the reviewer, not the moment to stop.
			// The rule saying so has been in the design playbook since it was pushed
			// at project start, and the next build ran site_check, served the site and
			// handed it over without ever calling review. Attaching it to a call she
			// already makes is the only thing that has ever worked.
			if len(problems) == 0 {
				return fmt.Sprintf("%d page(s) checked — every link, anchor, image and "+
					"form target resolves, including the external ones. Nothing leads "+
					"nowhere.%s\n\n[That is the half a regex can answer. It says nothing "+
					"about whether the page is any good — whether the copy says anything, "+
					"whether the spacing has a rhythm, whether the eye knows where to go. "+
					"Run review on this folder now: it shows the rendered page to somebody "+
					"who has never seen your work and asks for the three weakest things. "+
					"Expect it to find them, and fix them before you hand this over.]",
					len(pages), unreachable), nil
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "%d dead end(s) across %d page(s). Each is either something to "+
				"build or a reference to take out:\n\n", len(problems), len(pages))
			for _, p := range problems {
				sb.WriteString("  \u00b7 " + p + "\n")
			}
			return strings.TrimRight(sb.String(), "\n") + unreachable, nil
		},
	})
}

// htmlFilesUnder collects the pages to check, from a file or a folder.
func htmlFilesUnder(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", root, err)
	}
	if !info.IsDir() {
		return []string{root}, nil
	}
	var out []string
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if wiring.IsHTML(p) {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}
