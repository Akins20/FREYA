package skills

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/browser"
	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/llm"
)

// The rest of the hand.
//
// Every click she had was a plain left click. That is one gesture out of the
// handful a person uses without thinking, and the gap only showed up when a real
// task needed the others — two files to download from Google Drive, and no way to
// right-click, no way to ctrl-click a second one, no way to double-click to open.
// She tried text that was not there, then a guessed shortcut, then F10, and ran
// out of rounds still standing in front of them.
//
// These tools are deliberately named for the human action rather than the
// mechanism. She reaches for them by thinking "I want the menu for this", not by
// remembering that a menu is button:right.

// RegisterBrowserGestures adds the interactions that are not a left click.
func RegisterBrowserGestures(r *Registry, g *guard.Guard, tabs *Tabs) {
	if g == nil || tabs == nil {
		return
	}

	// act runs a gesture against either a selector or visible text, and reports
	// what happened outside the page as well as inside it.
	act := func(ctx context.Context, args map[string]any, what string,
		ges browser.Gesture) (Outcome, error) {

		tab, tabNote, err := tabNoted(tabs, args)
		if err != nil {
			return Outcome{}, err
		}
		sel, text := argString(args, "selector"), argString(args, "text")
		if sel == "" && text == "" {
			return Outcome{}, fmt.Errorf("say what to %s: either text (what it reads on "+
				"screen, preferred) or an exact selector from browser_inspect", what)
		}

		action := guard.Action{Kind: guard.KindBrowser,
			Command: what + " " + firstNonBlank(text, sel),
			Reason: fmt.Sprintf("%s %q in tab %q (%s context)", what,
				firstNonBlank(text, sel), tab.name, tab.ctx)}

		started := time.Now()
		// The baseline, for the same reason browser_click takes one. Ctrl-click is
		// literally "open in a new tab", so a gesture is the LAST place that should
		// have to notice a tab appearing by itself.
		before := pageIDs(tab.ctx)
		out, rerr := g.Run(ctx, action, func(ctx context.Context) (string, error) {
			var landed string
			var gerr error
			if text != "" {
				landed, gerr = tab.client.GestureAtText(ctx, text, ges)
			} else {
				gerr = tab.client.GestureAtSelector(ctx, sel, ges)
			}
			if gerr != nil {
				tab.missed()
				return "", clickHint(ctx, tab, gerr)
			}
			tab.sawReality()

			url, _ := tab.client.URL(ctx)
			title, _ := tab.client.Title(ctx)
			msg := fmt.Sprintf("Did that. Now on %q\n%s", title, url)
			// Fuzzy matching means what she named and what she hit are not always
			// the same thing, and acting on the wrong row is worth knowing before
			// the next step assumes otherwise.
			if landed != "" && !strings.EqualFold(strings.TrimSpace(landed), strings.TrimSpace(text)) {
				msg += fmt.Sprintf("\n(That landed on %q, not exactly %q.)", landed, text)
			}
			return msg, nil
		})
		if rerr != nil {
			return Outcome{}, rerr
		}
		// The whole point of the event log: a gesture that started a download or
		// opened a dialog says so, instead of looking identical to one that did
		// nothing at all.
		return Outcome{Text: out + tabNote + sideEffects(tab, started, before)}, nil
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_right_click",
			Description: "Right-click something to open its context menu — where the " +
				"commands live that an app does not put on screen: Download in Google " +
				"Drive, Rename, Copy link address, Save image as, Open in new tab. Reach " +
				"for this whenever you can SEE a thing but cannot find a button for what " +
				"you want to do to it.\n\n" +
				"Then READ THE PAGE. Apps like Drive draw their menu into the page and you " +
				"can click an item by its text. If the page looks unchanged, the browser " +
				"drew its own menu — that one is not page content and nothing can click " +
				"it, so press escape and find another route rather than repeating this.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":     {Type: "string", Description: "Tab name."},
				"text":     {Type: "string", Description: "What it reads on screen, e.g. a file name. Preferred."},
				"selector": {Type: "string", Description: "Or an exact CSS selector from browser_inspect."},
			}),
		},
		Mutates: true, Observe: tabs.observe, Affordances: tabs.affordances,
		Act: func(ctx context.Context, args map[string]any) (Outcome, error) {
			return act(ctx, args, "right-click", browser.Gesture{Button: browser.Right, Clicks: 1})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_double_click",
			Description: "Double-click something. This is how you OPEN a thing in a file " +
				"list, a grid or a tree — a folder in Drive, a file in a manager, a row " +
				"that expands. A single click usually only selects it, which is why one " +
				"click often looks like it did nothing.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":     {Type: "string", Description: "Tab name."},
				"text":     {Type: "string", Description: "What it reads on screen. Preferred."},
				"selector": {Type: "string", Description: "Or an exact CSS selector."},
			}),
		},
		Mutates: true, Observe: tabs.observe, Affordances: tabs.affordances,
		Act: func(ctx context.Context, args map[string]any) (Outcome, error) {
			return act(ctx, args, "double-click", browser.Gesture{Button: browser.Left, Clicks: 2})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_select_also",
			Description: "Ctrl+click, to ADD something to what is already selected — the " +
				"way you pick out three files before acting on all of them at once. Click " +
				"the first normally, then use this for each of the others. Use " +
				"extend=true for shift+click instead, which selects everything between " +
				"the first and this one.\n\n" +
				"This is what makes \"download these two\" or \"delete those\" one action " +
				"rather than several, and it is usually the only way an app will offer a " +
				"bulk command at all.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":     {Type: "string", Description: "Tab name."},
				"text":     {Type: "string", Description: "What it reads on screen. Preferred."},
				"selector": {Type: "string", Description: "Or an exact CSS selector."},
				"extend":   {Type: "boolean", Description: "Shift+click: select the whole range from the last one."},
			}),
		},
		Mutates: true, Observe: tabs.observe, Affordances: tabs.affordances,
		Act: func(ctx context.Context, args map[string]any) (Outcome, error) {
			mod := "ctrl"
			what := "ctrl-click"
			if argBool(args, "extend") {
				mod, what = "shift", "shift-click"
			}
			return act(ctx, args, what,
				browser.Gesture{Button: browser.Left, Clicks: 1, Modifiers: []string{mod}})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_drag",
			Description: "Drag one thing onto another: a file into a folder or an upload " +
				"zone, a card to another column, a slider handle to a value, a row into a " +
				"new order. Nothing else can do this — a drag is the movement between two " +
				"points, and clicking each of them in turn does not produce it.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "Tab name."},
				"from": {Type: "string", Description: "Exact CSS selector of what to drag."},
				"to":   {Type: "string", Description: "Exact CSS selector of where to drop it."},
			}, "from", "to"),
		},
		Mutates: true, Observe: tabs.observe, Affordances: tabs.affordances,
		Act: func(ctx context.Context, args map[string]any) (Outcome, error) {
			tab, tabNote, err := tabNoted(tabs, args)
			if err != nil {
				return Outcome{}, err
			}
			from, to := argString(args, "from"), argString(args, "to")
			action := guard.Action{Kind: guard.KindBrowser,
				Command: "drag " + from + " to " + to,
				Reason:  fmt.Sprintf("drag %q onto %q in tab %q", from, to, tab.name)}

			started := time.Now()
			before := pageIDs(tab.ctx)
			out, rerr := g.Run(ctx, action, func(ctx context.Context) (string, error) {
				if err := tab.client.Drag(ctx, from, to); err != nil {
					tab.missed()
					return "", clickHint(ctx, tab, err)
				}
				tab.sawReality()
				title, _ := tab.client.Title(ctx)
				return fmt.Sprintf("Dragged it. Now on %q — read the page to confirm it "+
					"landed where you meant.", title), nil
			})
			if rerr != nil {
				return Outcome{}, rerr
			}
			return Outcome{Text: out + tabNote + sideEffects(tab, started, before)}, nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_upload",
			Description: "Attach local files to a file input, without the operating " +
				"system's file chooser ever opening. Use it wherever a page wants a file: " +
				"an upload button, an attach control, a drop zone.\n\n" +
				"Point it at the input[type=file] itself, which browser_inspect will show " +
				"even when the page hides it behind a styled button — the hidden input is " +
				"the real target and its invisibility does not matter here. Clicking the " +
				"pretty button instead opens a chooser nothing can drive.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":     {Type: "string", Description: "Tab name."},
				"selector": {Type: "string", Description: "CSS selector of the input[type=file]."},
				"files":    {Type: "string", Description: "Path to upload. Separate several with a comma."},
			}, "selector", "files"),
		},
		Mutates: true, Observe: tabs.observe, Affordances: tabs.affordances,
		Act: func(ctx context.Context, args map[string]any) (Outcome, error) {
			tab, tabNote, err := tabNoted(tabs, args)
			if err != nil {
				return Outcome{}, err
			}
			sel := argString(args, "selector")
			var paths []string
			for _, p := range strings.Split(argString(args, "files"), ",") {
				if p = strings.TrimSpace(p); p != "" {
					paths = append(paths, expandIn(ctx, p))
				}
			}
			action := guard.Action{Kind: guard.KindBrowser,
				Command: "upload " + strings.Join(paths, ", "),
				Reason: fmt.Sprintf("attach %d file(s) to %q in tab %q",
					len(paths), sel, tab.name)}

			out, rerr := g.Run(ctx, action, func(ctx context.Context) (string, error) {
				if err := tab.client.UploadFiles(ctx, sel, paths); err != nil {
					return "", err
				}
				return fmt.Sprintf("Attached %s. The page has the file(s) now; submit the "+
					"form or click its upload button to send them.",
					strings.Join(paths, ", ")), nil
			})
			if rerr != nil {
				return Outcome{}, rerr
			}
			return Outcome{Text: out + tabNote}, nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_clipboard",
			Description: "Read or write the system clipboard. This is how something moves " +
				"between two places with no other connection: use \"Copy link\" on a page, " +
				"read it here, and paste it into a message somewhere else. Omit text to " +
				"read; pass text to write.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "Tab name."},
				"text": {Type: "string", Description: "Text to put on the clipboard. Omit to read instead."},
			}),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, _, err := tabNoted(tabs, args)
			if err != nil {
				return "", err
			}
			if text := argRaw(args, "text"); strings.TrimSpace(text) != "" {
				if err := tab.client.WriteClipboard(ctx, text); err != nil {
					return "", err
				}
				return "Copied to the clipboard.", nil
			}
			got, err := tab.client.ReadClipboard(ctx)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(got) == "" {
				return "The clipboard is empty.", nil
			}
			return "On the clipboard:\n" + got, nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_scroll_within",
			Description: "Scroll INSIDE a panel rather than the whole page — a chat " +
				"thread, a file grid, a long dialog, a code viewer. These keep their own " +
				"scrollbar, so scrolling the page does nothing to them and you keep " +
				"reading the same rows and conclude that is all there is. Negative " +
				"amounts scroll back up.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":     {Type: "string", Description: "Tab name."},
				"selector": {Type: "string", Description: "CSS selector of the scrolling panel."},
				"amount":   {Type: "number", Description: "Pixels to scroll. Default 600."},
			}, "selector"),
		},
		Mutates: true, Observe: tabs.observe, Affordances: tabs.affordances,
		Act: func(ctx context.Context, args map[string]any) (Outcome, error) {
			tab, tabNote, err := tabNoted(tabs, args)
			if err != nil {
				return Outcome{}, err
			}
			sel := argString(args, "selector")
			if err := tab.client.ScrollWithin(ctx, sel, argInt(args, "amount", 600)); err != nil {
				return Outcome{}, err
			}
			return Outcome{Text: "Scrolled inside " + sel + ". Read the page for what is now visible." + tabNote}, nil
		},
	})
}

// unattached lists pages the browser has open that she is not driving.
//
// A click on "Open in new tab", a sign-in that pops a window, a download that
// opens a viewer: each produces a real page she has no handle on. Until these
// were listed, such a click was indistinguishable from one that did nothing, and
// the page she actually wanted sat there unreachable for the rest of the session.
func unattached(tabs *Tabs) []browser.Target {
	driving := map[string]bool{}
	for _, t := range tabs.list() {
		driving[t.target.ID] = true
	}
	var out []browser.Target
	for _, ctx := range []browser.Context{browser.ContextAuth, browser.ContextGuest} {
		targets, err := browser.Targets(ctx)
		if err != nil {
			continue // that context simply is not running
		}
		for _, t := range targets {
			if driving[t.ID] || t.URL == "about:blank" {
				continue
			}
			out = append(out, t)
		}
	}
	return out
}

// RegisterBrowserAttach lets her take over a page the browser opened.
func RegisterBrowserAttach(r *Registry, g *guard.Guard, tabs *Tabs) {
	if g == nil || tabs == nil {
		return
	}
	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_attach",
			Description: "Take over a tab the browser opened by itself, so you can read " +
				"and click it. Run browser_tabs first to see what is unattached. Match on " +
				"part of the title or the url.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"match": {Type: "string", Description: "Part of the tab's title or url."},
				"name":  {Type: "string", Description: "What to call it from now on."},
			}, "match"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			match := strings.ToLower(argString(args, "match"))
			if match == "" {
				return "", fmt.Errorf("say which tab: part of its title or url")
			}
			candidates := unattached(tabs)
			for _, t := range candidates {
				if !strings.Contains(strings.ToLower(t.Title), match) &&
					!strings.Contains(strings.ToLower(t.URL), match) {
					continue
				}
				bctx := browser.ContextAuth
				if !strings.Contains(t.WS, fmt.Sprint(browser.AuthPort)) {
					bctx = browser.ContextGuest
				}
				client, err := browser.Connect(bctx, &t)
				if err != nil {
					return "", err
				}
				client.Watch(ctx)
				name := argString(args, "name")
				if name == "" {
					name = "adopted"
				}
				tabs.put(&openTab{name: name, ctx: bctx, target: &t, client: client,
					opened: time.Now(), lastURL: t.URL})
				return fmt.Sprintf("Attached to %q as tab %q.\n%s", clip(t.Title, 60), name, t.URL), nil
			}
			if len(candidates) == 0 {
				return "", fmt.Errorf("nothing is open that you are not already driving")
			}
			var have []string
			for _, t := range candidates {
				have = append(have, clip(t.Title, 40))
			}
			return "", withOptions(fmt.Errorf("no unattached tab matches %q", match), have)
		},
	})
}
