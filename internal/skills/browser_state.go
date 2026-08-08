package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/akins/jarvis/internal/browser"
	"github.com/akins/jarvis/internal/guard"
	"github.com/akins/jarvis/internal/llm"
)

// Knowing what the browser is doing, as opposed to what the page says.
//
// A page is only part of the browser, and the rest of it was invisible to her.
// She could not tell whether a download was running, finished or refused;
// whether the page she was reading was the site or Chrome's own warning about
// it; whether a click had opened a window she was not driving. Every one of
// those looks, through the DOM, exactly like an ordinary page that simply did
// not change — which is the same shape as failure, so failure is what she
// concluded.

// RegisterBrowserState adds the tools that report the browser rather than the page.
func RegisterBrowserState(r *Registry, g *guard.Guard, tabs *Tabs) {
	if g == nil || tabs == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_downloads",
			Description: "What is downloading right now, what has finished, and what " +
				"failed — with progress and where each file landed.\n\n" +
				"ALWAYS use this after triggering a download instead of clicking again. A " +
				"download changes nothing about the page, so a working one and a broken " +
				"one look identical from the page alone. 'preparing' means the server has " +
				"not started sending yet and is normal for anything it has to build, like " +
				"a zip — wait, do not retry. This also checks the folder on disk, so a " +
				"file that arrived without the browser telling us still shows up.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "Tab name."},
			}),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, _, err := tabNoted(tabs, args)
			if err != nil {
				return "", err
			}
			var sb strings.Builder

			live := tab.client.Downloads()
			if len(live) > 0 {
				sb.WriteString("This session:\n")
				for _, d := range live {
					fmt.Fprintf(&sb, "  · %s", d.Describe())
					// The browser saying "completed" and a file existing are
					// different claims, and the gap is where a blocked or
					// quarantined download disappears.
					if d.State == browser.Complete {
						if size, ok := browser.VerifyOnDisk(d); ok {
							fmt.Fprintf(&sb, "  [verified on disk, %d bytes]", size)
						} else {
							sb.WriteString("  [WARNING: the browser says it finished but the " +
								"file is not there — it may have been blocked or renamed]")
						}
					}
					sb.WriteByte('\n')
				}
			}

			// Whatever the browser reported, a file either exists or it does not.
			// This catches the transfers no event ever described.
			recent, ferr := browser.RecentFiles(2*time.Hour, 12)
			if ferr == nil && len(recent) > 0 {
				fmt.Fprintf(&sb, "\nIn %s, changed in the last 2 hours:\n", browser.DownloadDir())
				for _, f := range recent {
					fmt.Fprintf(&sb, "  · %s\n", f)
				}
			}

			if sb.Len() == 0 {
				return fmt.Sprintf("Nothing has downloaded this session, and nothing new is "+
					"in %s. If you expected a download, it never started — the click may "+
					"not have reached the right control.", browser.DownloadDir()), nil
			}
			if n := tab.client.ActiveDownloads(); n > 0 {
				fmt.Fprintf(&sb, "\n%d still in progress — wait and check again rather than "+
					"triggering another.", n)
			}
			return strings.TrimRight(sb.String(), "\n"), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_find",
			Description: "Count and locate a phrase on the page without reading the whole " +
				"thing back. Use it to answer 'is X here?' on a long page — reading the " +
				"page costs thousands of tokens and often truncates before reaching the " +
				"answer, so you get 'no' when the truth is 'not in the part I read'. " +
				"Searches shadow DOM and frames.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":   {Type: "string", Description: "Tab name."},
				"phrase": {Type: "string", Description: "What to look for."},
			}, "phrase"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, _, err := tabNoted(tabs, args)
			if err != nil {
				return "", err
			}
			phrase := argString(args, "phrase")
			n, where, err := tab.client.FindInPage(ctx, phrase)
			if err != nil {
				return "", err
			}
			if n == 0 {
				return fmt.Sprintf("%q is not on this page. It may be behind a scroll "+
					"container (browser_scroll_within), on another page of a list, or the "+
					"page may still be loading.", phrase), nil
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "%q appears %d time(s):\n", phrase, n)
			for _, w := range where {
				fmt.Fprintf(&sb, "  · %s\n", w)
			}
			sb.WriteString("Click one by its text with browser_click_text.")
			return sb.String(), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_status",
			Description: "What the BROWSER is doing, as opposed to what the page says: " +
				"whether this is really the site or one of Chrome's own warning pages, " +
				"whether it is still loading, what is downloading, and which tabs are " +
				"open including ones you are not driving.\n\n" +
				"Use it when something is not behaving and you cannot see why — " +
				"especially when a page seems empty, a click seemed to do nothing, or you " +
				"are about to tell the user something did not work.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "Tab name."},
			}),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, _, err := tabNoted(tabs, args)
			if err != nil {
				return "", err
			}
			st := tab.client.State(ctx)

			var sb strings.Builder
			fmt.Fprintf(&sb, "Tab %q [%s context]\n  %s\n  %s\n",
				tab.name, tab.ctx, clip(st.Title, 80), clip(st.URL, 100))
			if d := st.Describe(); d != "" {
				sb.WriteString(d + "\n")
			} else {
				sb.WriteString("  the page is loaded and is the site's own, not a browser warning\n")
			}

			if n := tab.client.ActiveDownloads(); n > 0 {
				fmt.Fprintf(&sb, "\n%d download(s) in progress — browser_downloads for detail.\n", n)
			}
			if extra := unattached(tabs); len(extra) > 0 {
				fmt.Fprintf(&sb, "\n%d page(s) open that you are NOT driving:\n", len(extra))
				for _, t := range extra {
					fmt.Fprintf(&sb, "  · %q %s\n", clip(t.Title, 50), clip(t.URL, 70))
				}
				sb.WriteString("browser_attach to take one over.\n")
			}
			return strings.TrimRight(sb.String(), "\n"), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_save_pdf",
			Description: "Save the current page as a PDF. This is how you keep something " +
				"that is not a file to begin with — a receipt, a confirmation, a result " +
				"page, an article that needs the user's session to read. Works where " +
				"there is no download link at all.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "Tab name."},
				"path": {Type: "string", Description: "Where to save it. Defaults to the download folder."},
			}),
		},
		Mutates: true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, _, err := tabNoted(tabs, args)
			if err != nil {
				return "", err
			}
			out := argString(args, "path")
			if out == "" {
				title, _ := tab.client.Title(ctx)
				out = filepath.Join(browser.DownloadDir(), safeFilename(title)+".pdf")
			} else {
				out = expandIn(ctx, out)
			}

			action := guard.Action{Kind: guard.KindWrite, Command: "save pdf " + out,
				Reason: "save the current page as a PDF"}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				data, err := tab.client.PrintToPDF(ctx)
				if err != nil {
					return "", err
				}
				if err := os.WriteFile(out, data, 0o644); err != nil {
					return "", err
				}
				return fmt.Sprintf("Saved the page as %s (%d KB).", out, len(data)/1024), nil
			})
		},
	})
}

// safeFilename turns a page title into something that can be a filename.
func safeFilename(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "page"
	}
	var b strings.Builder
	for _, r := range title {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
		if b.Len() > 60 {
			break
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		return "page"
	}
	return name
}
