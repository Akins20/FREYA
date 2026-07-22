package skills

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/akins/jarvis/internal/browser"
	"github.com/akins/jarvis/internal/guard"
	"github.com/akins/jarvis/internal/llm"
)

// Browser control.
//
// Two contexts, and which one is used is the security decision. The auth
// context carries the user's real cookies — anything driven there is acting as
// them, on their accounts. The guest context has nothing, so an unknown page
// can be opened in it with nothing to lose. Auth is therefore never the default:
// it must be asked for.

// Tabs holds open browser connections so a page survives between turns.
type Tabs struct {
	mu     sync.Mutex
	byName map[string]*openTab
}

type openTab struct {
	name    string
	ctx     browser.Context
	target  *browser.Target
	client  *browser.Client
	opened  time.Time
	lastURL string
}

// NewTabs creates a tab registry.
func NewTabs() *Tabs { return &Tabs{byName: map[string]*openTab{}} }

// CloseAll shuts every connection, for shutdown.
func (t *Tabs) CloseAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for name, tab := range t.byName {
		_ = tab.client.Close()
		_ = browser.CloseTab(tab.ctx, tab.target.ID)
		delete(t.byName, name)
	}
}

func (t *Tabs) get(name string) (*openTab, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tab, ok := t.byName[name]
	return tab, ok
}

func (t *Tabs) put(tab *openTab) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.byName[tab.name] = tab
}

func (t *Tabs) remove(name string) (*openTab, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tab, ok := t.byName[name]
	if ok {
		delete(t.byName, name)
	}
	return tab, ok
}

func (t *Tabs) list() []*openTab {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*openTab, 0, len(t.byName))
	for _, tab := range t.byName {
		out = append(out, tab)
	}
	return out
}

// RegisterBrowser adds browser skills.
func RegisterBrowser(r *Registry, g *guard.Guard, tabs *Tabs) {
	// History and page interaction live in their own files; they are part of the
	// same capability and are registered together so a caller cannot get one
	// without the other.
	RegisterBrowserHistory(r)
	RegisterBrowserInteract(r, g, tabs)

	if g == nil || tabs == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_open",
			Description: "Open a browser tab and load a page. Two contexts:\n\n" +
				"'guest' (default) is isolated and signed into nothing — use it for " +
				"reading any ordinary page, and for anything you do not fully trust.\n\n" +
				"'auth' carries the user's real cookies and logins. Use it only when the " +
				"page genuinely requires being signed in — a portal, an account page, a " +
				"dashboard. Everything done there acts as the user on their accounts.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "A name to refer to this tab by."},
				"url":  {Type: "string", Description: "Page to load."},
				"context": {Type: "string", Description: "Which browser to use.",
					Enum: []string{"guest", "auth"}},
			}, "name", "url"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			name := argString(args, "name")
			url := argString(args, "url")
			bctx := browser.ContextGuest
			if strings.EqualFold(argString(args, "context"), "auth") {
				bctx = browser.ContextAuth
			}

			action := guard.Action{
				Kind:    guard.KindBrowser,
				Command: "open " + url,
				Reason:  fmt.Sprintf("open %s in the %s browser context", url, bctx),
			}
			// Acting with the user's live sessions is a different thing from
			// reading a page anonymously, and is never silent.
			if bctx == browser.ContextAuth {
				action.Kind = guard.KindSystem
				action.Reason = fmt.Sprintf(
					"open %s SIGNED IN AS THE USER (auth context, real cookies)", url)
			}

			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				if err := browser.Launch(ctx, bctx); err != nil {
					return "", err
				}
				if old, ok := tabs.remove(name); ok {
					_ = old.client.Close()
					_ = browser.CloseTab(old.ctx, old.target.ID)
				}
				target, err := browser.NewTab(bctx, "about:blank")
				if err != nil {
					return "", err
				}
				client, err := browser.Connect(bctx, target)
				if err != nil {
					return "", err
				}
				if err := client.Navigate(ctx, url); err != nil {
					client.Close()
					return "", err
				}
				title, _ := client.Title(ctx)
				current, _ := client.URL(ctx)

				tabs.put(&openTab{name: name, ctx: bctx, target: target,
					client: client, opened: time.Now(), lastURL: current})
				return fmt.Sprintf("Tab %q open in %s context: %q\n%s",
					name, bctx, title, current), nil
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "browser_read",
			Description: "Read the readable text of a page already open in a tab.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":  {Type: "string", Description: "Tab name."},
				"limit": {Type: "number", Description: "Maximum characters, default 12000."},
			}, "name"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, ok := tabs.get(argString(args, "name"))
			if !ok {
				return "", fmt.Errorf("no tab named %q — open one with browser_open", argString(args, "name"))
			}
			text, err := tab.client.Text(ctx)
			if err != nil {
				return "", err
			}
			title, _ := tab.client.Title(ctx)
			url, _ := tab.client.URL(ctx)
			limit := clamp(argInt(args, "limit", 12000), 200, 100000)
			return fmt.Sprintf("%s\n%s\n\n%s", title, url, clipText(text, limit)), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "browser_navigate",
			Description: "Send an already-open tab to a different URL, keeping its context.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "Tab name."},
				"url":  {Type: "string", Description: "Where to go."},
			}, "name", "url"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, ok := tabs.get(argString(args, "name"))
			if !ok {
				return "", fmt.Errorf("no tab named %q", argString(args, "name"))
			}
			url := argString(args, "url")
			action := guard.Action{Kind: guard.KindBrowser, Command: "navigate " + url,
				Reason: fmt.Sprintf("navigate tab %q (%s context)", tab.name, tab.ctx)}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				if err := tab.client.Navigate(ctx, url); err != nil {
					return "", err
				}
				title, _ := tab.client.Title(ctx)
				current, _ := tab.client.URL(ctx)
				tab.lastURL = current
				return fmt.Sprintf("%q\n%s", title, current), nil
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_click",
			Description: "Click an element by CSS selector. Use browser_links or " +
				"browser_read first to find what is on the page.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":     {Type: "string", Description: "Tab name."},
				"selector": {Type: "string", Description: "CSS selector, e.g. 'button.submit' or '#login'."},
			}, "name", "selector"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, ok := tabs.get(argString(args, "name"))
			if !ok {
				return "", fmt.Errorf("no tab named %q", argString(args, "name"))
			}
			sel := argString(args, "selector")
			action := guard.Action{Kind: guard.KindInput, Command: "click " + sel,
				Reason: fmt.Sprintf("click %q in tab %q (%s context)", sel, tab.name, tab.ctx)}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				if err := tab.client.Click(ctx, sel); err != nil {
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
			Name: "browser_fill",
			Description: "Type a value into a form field. NEVER use this for passwords, " +
				"card numbers, or any other credential — the browser's own saved " +
				"passwords handle sign-in, and secrets must not pass through here.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":     {Type: "string", Description: "Tab name."},
				"selector": {Type: "string", Description: "CSS selector for the field."},
				"value":    {Type: "string", Description: "Text to enter. Never a secret."},
			}, "name", "selector", "value"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, ok := tabs.get(argString(args, "name"))
			if !ok {
				return "", fmt.Errorf("no tab named %q", argString(args, "name"))
			}
			sel := argString(args, "selector")

			// A refusal in the tool, not only in the description. A model that
			// has talked itself into "just this once" should still fail here.
			if looksLikeSecretField(sel) {
				return "", fmt.Errorf("refusing to type into %q: it looks like a "+
					"credential field. Let the browser's saved passwords sign in, or "+
					"ask the user to type it themselves", sel)
			}

			action := guard.Action{Kind: guard.KindInput,
				Command: "fill " + sel,
				Reason: fmt.Sprintf("enter text into %q on tab %q (%s context)",
					sel, tab.name, tab.ctx)}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				if err := tab.client.Fill(ctx, sel, argRaw(args, "value")); err != nil {
					return "", err
				}
				return "Filled " + sel + ".", nil
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "browser_links",
			Description: "List the links on a page, with their text and destinations.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":  {Type: "string", Description: "Tab name."},
				"limit": {Type: "number", Description: "Maximum links, default 60."},
			}, "name"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, ok := tabs.get(argString(args, "name"))
			if !ok {
				return "", fmt.Errorf("no tab named %q", argString(args, "name"))
			}
			out, err := tab.client.Links(ctx, clamp(argInt(args, "limit", 60), 1, 300))
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(out) == "" {
				return "No links found.", nil
			}
			return clipText(out, 12000), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_screenshot",
			Description: "Capture what the page looks like, saving a PNG. Pass the " +
				"returned path to image_view to actually read it — useful when a page " +
				"renders as images or the layout matters.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "Tab name."},
			}, "name"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, ok := tabs.get(argString(args, "name"))
			if !ok {
				return "", fmt.Errorf("no tab named %q", argString(args, "name"))
			}
			b64, err := tab.client.Screenshot(ctx)
			if err != nil {
				return "", err
			}
			data, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return "", err
			}
			path := filepath.Join(os.TempDir(),
				fmt.Sprintf("freya-page-%d.png", time.Now().UnixNano()))
			if err := os.WriteFile(path, data, 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("Saved %s (%s). Use image_view to read it.",
				path, humanBytes(int64(len(data)))), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "browser_tabs",
			Description: "List open tabs and which browser context each is in.",
			Params:      llm.ObjectSchema(nil),
		},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			open := tabs.list()
			if len(open) == 0 {
				return "No tabs open.", nil
			}
			var sb strings.Builder
			for _, t := range open {
				url, _ := t.client.URL(ctx)
				fmt.Fprintf(&sb, "- %s [%s] %s\n", t.name, t.ctx, clip(url, 80))
			}
			return strings.TrimSpace(sb.String()), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "browser_close",
			Description: "Close a tab.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "Tab name."},
			}, "name"),
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			name := argString(args, "name")
			tab, ok := tabs.remove(name)
			if !ok {
				return "", fmt.Errorf("no tab named %q", name)
			}
			_ = tab.client.Close()
			_ = browser.CloseTab(tab.ctx, tab.target.ID)
			return fmt.Sprintf("Closed %q.", name), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_sync_logins",
			Description: "Refresh the auth browser context from the user's real Chrome " +
				"profile. Their logins here are a snapshot — signing into something new " +
				"in their normal browser does not appear until this runs. Chrome must be " +
				"closed first.",
			Params: llm.ObjectSchema(nil),
		},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			action := guard.Action{Kind: guard.KindWrite,
				Command: "sync chrome profile",
				Reason:  "copy cookies and saved logins into the automation profile"}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				return browser.SyncAuthProfile(ctx)
			})
		},
	})
}

// secretFieldHints name the fields a credential goes into.
var secretFieldHints = []string{
	"password", "passwd", "pwd", "passcode", "pin", "otp", "2fa", "mfa",
	"cvv", "cvc", "card", "cardnumber", "ssn", "secret", "token", "apikey",
}

// looksLikeSecretField reports whether a selector targets a credential input.
func looksLikeSecretField(selector string) bool {
	s := strings.ToLower(selector)
	if strings.Contains(s, `type="password"`) || strings.Contains(s, "type=password") {
		return true
	}
	for _, hint := range secretFieldHints {
		if strings.Contains(s, hint) {
			return true
		}
	}
	return false
}
