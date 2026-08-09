package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

			if len(problems) == 0 {
				return fmt.Sprintf("%d page(s) checked — every link, anchor, image and "+
					"form target resolves. Nothing leads nowhere.", len(pages)), nil
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "%d dead end(s) across %d page(s). Each is either something to "+
				"build or a reference to take out:\n\n", len(problems), len(pages))
			for _, p := range problems {
				sb.WriteString("  \u00b7 " + p + "\n")
			}
			return strings.TrimRight(sb.String(), "\n"), nil
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
