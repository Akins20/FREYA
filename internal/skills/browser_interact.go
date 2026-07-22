package skills

import (
	"context"
	"fmt"
	"time"

	"github.com/akins/jarvis/internal/guard"
	"github.com/akins/jarvis/internal/llm"
)

// Skills for driving a page: dropdowns, scrolling, keys, waiting.
//
// # What guards what
//
// Reading and scrolling change nothing and are not gated. Clicking, typing and
// submitting are, because in the auth context they act as the user on their own
// accounts — a click can place an order or send a message. The guard sees the
// context in the reason string so a confirmation prompt can say which browser
// the action lands in.

// RegisterBrowserInteract adds the page-interaction skills.
func RegisterBrowserInteract(r *Registry, g *guard.Guard, tabs *Tabs) {
	// tabFor is the same lookup every handler needs.
	tabFor := func(args map[string]any) (*openTab, error) {
		name := argString(args, "name")
		tab, ok := tabs.get(name)
		if !ok {
			return nil, fmt.Errorf("no tab named %q — open one with browser_open first", name)
		}
		return tab, nil
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_inspect",
			Description: "List the interactive elements on a page — fields, buttons, dropdowns, " +
				"checkboxes — with the CSS selector and label for each. Call this before " +
				"filling or clicking anything: it gives the exact selectors the other " +
				"browser skills need, which cannot be guessed from the page text.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":  {Type: "string", Description: "Tab name."},
				"limit": {Type: "integer", Description: "Maximum elements to list (default 60)."},
			}, "name"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, err := tabFor(args)
			if err != nil {
				return "", err
			}
			return tab.client.Inspect(ctx, argInt(args, "limit", 60))
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_select",
			Description: "Choose an option from a native <select> dropdown, by its visible " +
				"label. If the element turns out to be a custom dropdown rather than a real " +
				"<select>, this says so — click it instead.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":     {Type: "string", Description: "Tab name."},
				"selector": {Type: "string", Description: "CSS selector for the dropdown."},
				"option":   {Type: "string", Description: "The option's visible text."},
			}, "name", "selector", "option"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, err := tabFor(args)
			if err != nil {
				return "", err
			}
			sel, opt := argString(args, "selector"), argString(args, "option")
			action := guard.Action{Kind: guard.KindInput, Command: "select " + opt,
				Reason: fmt.Sprintf("choose %q in %q, tab %q (%s context)", opt, sel, tab.name, tab.ctx)}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				return tab.client.SelectOption(ctx, sel, opt)
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "browser_check",
			Description: "Tick or untick a checkbox, or choose a radio button.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":     {Type: "string", Description: "Tab name."},
				"selector": {Type: "string", Description: "CSS selector for the checkbox or radio."},
				"checked":  {Type: "boolean", Description: "True to tick, false to untick. Default true."},
			}, "name", "selector"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, err := tabFor(args)
			if err != nil {
				return "", err
			}
			want := true
			if v, ok := args["checked"].(bool); ok {
				want = v
			}
			sel := argString(args, "selector")
			action := guard.Action{Kind: guard.KindInput, Command: "check " + sel,
				Reason: fmt.Sprintf("set %q in tab %q (%s context)", sel, tab.name, tab.ctx)}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				return tab.client.SetChecked(ctx, sel, want)
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_scroll",
			Description: "Scroll the page. Give a direction and amount, or a target of 'top', " +
				"'bottom', or a CSS selector to bring into view. Needed for pages that load " +
				"more content as you scroll.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":   {Type: "string", Description: "Tab name."},
				"to":     {Type: "string", Description: "'top', 'bottom', or a CSS selector to scroll to."},
				"pixels": {Type: "integer", Description: "Pixels to scroll down; negative for up. Ignored if 'to' is given."},
				"inside": {Type: "string", Description: "CSS selector of a scrollable panel, to scroll that instead of the page."},
			}, "name"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, err := tabFor(args)
			if err != nil {
				return "", err
			}
			if to := argString(args, "to"); to != "" {
				if err := tab.client.ScrollTo(ctx, to); err != nil {
					return "", err
				}
				return "Scrolled to " + to + ".", nil
			}
			pixels := argInt(args, "pixels", 600)
			if err := tab.client.Scroll(ctx, argString(args, "inside"), 0, float64(pixels)); err != nil {
				return "", err
			}
			return fmt.Sprintf("Scrolled %d pixels.", pixels), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_press",
			Description: "Press a key: enter, tab, escape, up, down, pageup, pagedown, or a " +
				"combination like 'ctrl+a'. Use enter to submit a search box, escape to " +
				"dismiss a popup, tab to move between fields.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "Tab name."},
				"key":  {Type: "string", Description: "The key, e.g. 'enter' or 'ctrl+a'."},
				"selector": {Type: "string",
					Description: "Optional: focus this element first."},
			}, "name", "key"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, err := tabFor(args)
			if err != nil {
				return "", err
			}
			key := argString(args, "key")
			action := guard.Action{Kind: guard.KindInput, Command: "press " + key,
				Reason: fmt.Sprintf("press %s in tab %q (%s context)", key, tab.name, tab.ctx)}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				if sel := argString(args, "selector"); sel != "" {
					if err := tab.client.Focus(ctx, sel); err != nil {
						return "", err
					}
				}
				if err := tab.client.PressKey(ctx, key); err != nil {
					return "", err
				}
				// Enter usually navigates or submits; give it time to land so the
				// reported URL is the new one.
				time.Sleep(700 * time.Millisecond)
				url, _ := tab.client.URL(ctx)
				title, _ := tab.client.Title(ctx)
				return fmt.Sprintf("Pressed %s. Now on %q\n%s", key, title, url), nil
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_type",
			Description: "Type text with real keystrokes, rather than setting a field's value. " +
				"Slower than browser_fill but necessary where a page reacts as you type — " +
				"search suggestions, live validation, a submit button that only enables " +
				"after a keypress.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":     {Type: "string", Description: "Tab name."},
				"selector": {Type: "string", Description: "CSS selector for the field."},
				"text":     {Type: "string", Description: "What to type."},
			}, "name", "selector", "text"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, err := tabFor(args)
			if err != nil {
				return "", err
			}
			sel := argString(args, "selector")
			action := guard.Action{Kind: guard.KindInput, Command: "type into " + sel,
				Reason: fmt.Sprintf("type into %q in tab %q (%s context)", sel, tab.name, tab.ctx)}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				if err := tab.client.TypeText(ctx, sel, argRaw(args, "text")); err != nil {
					return "", err
				}
				return fmt.Sprintf("Typed into %q.", sel), nil
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_click_real",
			Description: "Click using genuine mouse events rather than a scripted click. Try " +
				"this when browser_click appeared to work but the page did not respond — " +
				"custom dropdowns, drag handles and some login buttons only accept real " +
				"mouse input.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":     {Type: "string", Description: "Tab name."},
				"selector": {Type: "string", Description: "CSS selector."},
			}, "name", "selector"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, err := tabFor(args)
			if err != nil {
				return "", err
			}
			sel := argString(args, "selector")
			action := guard.Action{Kind: guard.KindInput, Command: "click " + sel,
				Reason: fmt.Sprintf("click %q with real mouse events in tab %q (%s context)",
					sel, tab.name, tab.ctx)}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				if err := tab.client.ClickReal(ctx, sel); err != nil {
					return "", err
				}
				time.Sleep(900 * time.Millisecond)
				url, _ := tab.client.URL(ctx)
				title, _ := tab.client.Title(ctx)
				return fmt.Sprintf("Clicked. Now on %q\n%s", title, url), nil
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "browser_hover",
			Description: "Move the pointer over an element, to open a menu that appears on hover.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":     {Type: "string", Description: "Tab name."},
				"selector": {Type: "string", Description: "CSS selector."},
			}, "name", "selector"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, err := tabFor(args)
			if err != nil {
				return "", err
			}
			sel := argString(args, "selector")
			if err := tab.client.Hover(ctx, sel); err != nil {
				return "", err
			}
			return fmt.Sprintf("Hovering over %q.", sel), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_wait",
			Description: "Wait for an element or a phrase to appear. Use after any action that " +
				"triggers loading, instead of assuming the page is ready.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":     {Type: "string", Description: "Tab name."},
				"selector": {Type: "string", Description: "CSS selector to wait for."},
				"text":     {Type: "string", Description: "Or a phrase to wait for."},
				"seconds":  {Type: "integer", Description: "How long to wait (default 15)."},
			}, "name"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, err := tabFor(args)
			if err != nil {
				return "", err
			}
			timeout := time.Duration(argInt(args, "seconds", 15)) * time.Second
			if sel := argString(args, "selector"); sel != "" {
				if err := tab.client.WaitFor(ctx, sel, timeout); err != nil {
					return "", err
				}
				return fmt.Sprintf("%q appeared.", sel), nil
			}
			if text := argString(args, "text"); text != "" {
				if err := tab.client.WaitForText(ctx, text, timeout); err != nil {
					return "", err
				}
				return fmt.Sprintf("The page now contains %q.", text), nil
			}
			return "", fmt.Errorf("give either a selector or some text to wait for")
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_submit",
			Description: "Submit a form. Give any element inside it, or the form itself. " +
				"Runs the page's own validation, unlike clicking a button that may be disabled.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":     {Type: "string", Description: "Tab name."},
				"selector": {Type: "string", Description: "A selector inside the form. Defaults to the first form."},
			}, "name"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, err := tabFor(args)
			if err != nil {
				return "", err
			}
			sel := argString(args, "selector")
			// Submitting is the irreversible one: it sends whatever is in the form.
			action := guard.Action{Kind: guard.KindInput, Command: "submit form",
				Reason: fmt.Sprintf("submit the form in tab %q (%s context) — this sends its contents",
					tab.name, tab.ctx)}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				if err := tab.client.Submit(ctx, sel); err != nil {
					return "", err
				}
				url, _ := tab.client.URL(ctx)
				title, _ := tab.client.Title(ctx)
				return fmt.Sprintf("Submitted. Now on %q\n%s", title, url), nil
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "browser_element",
			Description: "Read the text of one element, when the whole page would be too much.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":     {Type: "string", Description: "Tab name."},
				"selector": {Type: "string", Description: "CSS selector."},
			}, "name", "selector"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, err := tabFor(args)
			if err != nil {
				return "", err
			}
			text, err := tab.client.ReadElement(ctx, argString(args, "selector"))
			if err != nil {
				return "", err
			}
			if text == "" {
				return "That element is empty.", nil
			}
			return text, nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "browser_back",
			Description: "Go back, forward, or reload the current page.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":      {Type: "string", Description: "Tab name."},
				"direction": {Type: "string", Description: "'back', 'forward' or 'reload'. Default 'back'."},
			}, "name"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, err := tabFor(args)
			if err != nil {
				return "", err
			}
			switch argString(args, "direction") {
			case "forward":
				err = tab.client.Forward(ctx)
			case "reload":
				err = tab.client.Reload(ctx)
			default:
				err = tab.client.Back(ctx)
			}
			if err != nil {
				return "", err
			}
			url, _ := tab.client.URL(ctx)
			title, _ := tab.client.Title(ctx)
			return fmt.Sprintf("Now on %q\n%s", title, url), nil
		},
	})
}
