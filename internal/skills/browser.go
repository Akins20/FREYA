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
	mu       sync.Mutex
	byName   map[string]*openTab
	lastName string // the most recently opened or used tab, for blank-name lookups
}

type openTab struct {
	name    string
	ctx     browser.Context
	target  *browser.Target
	client  *browser.Client
	opened  time.Time
	lastURL string
	// mu guards the mutable fields below: tool calls within one round run
	// concurrently, so two clicks on the same tab race the counter.
	mu sync.Mutex
	// misses counts consecutive selector clicks that found nothing. It powers a
	// circuit breaker: shown the real options after a miss, this model still tries
	// another selector, and another — so after a couple of misses selector clicking
	// is refused until she scouts (inspect/read) or clicks by visible text. Any of
	// those resets it. Taking the tool away is the only thing that stops the spiral.
	misses int
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

// resolve is get, plus a note when the tab handed back is not the one named.
//
// The fallback itself is right and stays: a blank name is a routine omission,
// there is almost always one tab, and erroring on it once sent her into a loop of
// reopening — and reloading — the page every round. What was wrong was doing it
// silently when the name was WRONG rather than absent. With two tabs open,
// asking for "portal" when the tab is called "d2l" quietly drove the other page,
// and every subsequent report was about a page she was not thinking about.
func (t *Tabs) resolve(name string) (*openTab, string, bool) {
	tab, ok := t.get(name)
	if !ok || tab == nil {
		return tab, "", ok
	}
	t.mu.Lock()
	ambiguous := len(t.byName) > 1
	t.mu.Unlock()
	if name != "" && !strings.EqualFold(name, tab.name) && ambiguous {
		return tab, fmt.Sprintf("\n(There is no tab called %q. This is tab %q — the one most recently "+
			"used. Check browser_tabs if that is not the page you meant.)", name, tab.name), true
	}
	return tab, "", true
}

func (t *Tabs) get(name string) (*openTab, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if tab, ok := t.byName[name]; ok {
		t.lastName = tab.name
		return tab, true
	}
	// The name was blank or unknown. Rather than error "no tab named \"\"" — which
	// sent her into a loop of reopening the page every round because a click kept
	// failing on a missing name — fall back to the obvious tab when there is no
	// real ambiguity: the single open tab, or the one most recently opened/used.
	// Models routinely omit the tab name; there is almost always just one anyway.
	if len(t.byName) == 1 {
		for _, tab := range t.byName {
			t.lastName = tab.name
			return tab, true
		}
	}
	if tab, ok := t.byName[t.lastName]; ok {
		return tab, true
	}
	return nil, false
}

func (t *Tabs) put(tab *openTab) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.byName[tab.name] = tab
	t.lastName = tab.name
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

// tabNoted resolves the tab an argument names, returning any disclosure that the
// tab handed back is not the one asked for.
func tabNoted(tabs *Tabs, args map[string]any) (*openTab, string, error) {
	name := argString(args, "name")
	tab, note, ok := tabs.resolve(name)
	if !ok {
		return nil, "", fmt.Errorf("no tab named %q — open one with browser_open first", name)
	}
	return tab, note, nil
}

// RegisterBrowser adds browser skills.
func RegisterBrowser(r *Registry, g *guard.Guard, tabs *Tabs) {
	// History and page interaction live in their own files; they are part of the
	// same capability and are registered together so a caller cannot get one
	// without the other.
	RegisterBrowserHistory(r)
	RegisterBrowserInteract(r, g, tabs)
	RegisterBrowserGestures(r, g, tabs)
	RegisterBrowserAttach(r, g, tabs)

	if g == nil || tabs == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_open",
			Description: "Open a browser tab and load a page. Two contexts:\n\n" +
				"'guest' is isolated and signed into nothing — use it for reading an " +
				"ordinary public page, and for anything you do not fully trust.\n\n" +
				"'auth' carries the user's real cookies and logins — it IS their Chrome. " +
				"Use it for anything involving their accounts: a portal, dashboard, email, " +
				"their school, an account page. In it they are already signed in to most " +
				"sites, so this is how you access their things. Do not fall back to guest " +
				"and then claim you cannot sign in. Everything done here acts as the user.",
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
			// Opening mints the tab every later browser call works on, so a guessed
			// address here poisons the whole session's working surface.
			guessNote, gerr := CheckURL(ctx, url)
			if gerr != nil {
				return "", gerr
			}
			bctx := browser.ContextGuest
			if strings.EqualFold(argString(args, "context"), "auth") {
				bctx = browser.ContextAuth
			}

			action := guard.Action{
				Kind:    guard.KindBrowser,
				Command: "open " + url,
				Reason:  fmt.Sprintf("open %s in the %s browser context", url, bctx),
			}
			// Opening a page — even signed in as the user — is not a destructive
			// act; it loads something they already have access to. It used to be
			// elevated to a confirm-required action, which meant that over voice,
			// where there is no terminal to confirm on, it was denied outright:
			// she could not open the user's own portal at their own request. The
			// user has explicitly standing-authorised the auth context, so this
			// stays low-risk. The reason still names it, so the audit log records
			// that a page was opened with their real session; and the genuinely
			// consequential steps — typing a secret, submitting — are gated at the
			// skills that do them, not here.
			if bctx == browser.ContextAuth {
				action.Reason = fmt.Sprintf(
					"open %s signed in as the user (auth context, real cookies)", url)
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
				// Downloads land in a folder instead of opening a chooser nothing can
				// drive, javascript dialogs get answered instead of blocking the
				// renderer forever, and both are recorded so a click that caused one
				// can say so. See internal/browser/events.go.
				client.Watch(ctx)
				if err := client.Navigate(ctx, url); err != nil {
					client.Close()
					return "", err
				}
				title, _ := client.Title(ctx)
				current, _ := client.URL(ctx)

				tabs.put(&openTab{name: name, ctx: bctx, target: target,
					client: client, opened: time.Now(), lastURL: current})
				return fmt.Sprintf("Tab %q open in %s context: %q\n%s%s",
					name, bctx, title, current, guessNote), nil
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
			}),
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
			tab.sawReality() // reading the page is scouting; clear the guess counter
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
			}, "url"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, tabNote, err := tabNoted(tabs, args)
			if err != nil {
				return "", err
			}
			url := argString(args, "url")
			// Invented ids here SUCCEED — a wrong one returns a real page with a real
			// title — so the warning rides along with the result, and a sustained
			// pattern-walk is stopped outright.
			guessNote, gerr := CheckURL(ctx, url)
			if gerr != nil {
				return "", gerr
			}
			action := guard.Action{Kind: guard.KindBrowser, Command: "navigate " + url,
				Reason: fmt.Sprintf("navigate tab %q (%s context)", tab.name, tab.ctx)}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				if err := tab.client.Navigate(ctx, url); err != nil {
					return "", err
				}
				title, _ := tab.client.Title(ctx)
				current, _ := tab.client.URL(ctx)
				tab.lastURL = current
				return fmt.Sprintf("%q\n%s%s%s", title, current, tabNote, guessNote), nil
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_click",
			Description: "Click an element by CSS selector (searches shadow DOM and iframes). " +
				"You will rarely need this — browser_click_text does a real trusted click " +
				"by visible text and works on almost everything, so reach for that first. " +
				"Only use a selector you copied verbatim from a browser_inspect you JUST " +
				"ran; a selector from memory or a pattern you inferred will miss, because " +
				"app pages generate their ids fresh each visit.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":     {Type: "string", Description: "Tab name."},
				"selector": {Type: "string", Description: "A selector copied verbatim from a browser_inspect you just ran — not composed, remembered, or guessed. Plain CSS only (no jQuery :contains())."},
			}, "selector"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, ok := tabs.get(argString(args, "name"))
			if !ok {
				return "", fmt.Errorf("no tab named %q", argString(args, "name"))
			}
			sel := argString(args, "selector")
			action := guard.Action{Kind: guard.KindBrowser, Command: "click " + sel,
				Reason: fmt.Sprintf("click %q in tab %q (%s context)", sel, tab.name, tab.ctx)}
			if err := selectorBudget(ctx, tab); err != nil {
				return "", err
			}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				if err := tab.client.Click(ctx, sel); err != nil {
					tab.missed()
					return "", clickHint(ctx, tab, err)
				}
				tab.sawReality()
				time.Sleep(900 * time.Millisecond)
				url, _ := tab.client.URL(ctx)
				title, _ := tab.client.Title(ctx)
				return fmt.Sprintf("Clicked. Now on %q\n%s", title, url), nil
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "browser_click_text",
			Description: "Click the thing on the page that reads a given text — a link, " +
				"button, tab or menu item — the way a person clicks \"Self-Quiz Unit 5\" " +
				"rather than hunting for a CSS id. Searches visible text across shadow " +
				"DOM. This is the reliable way to click on app portals; reach for it " +
				"before browser_click. Read the page or list links first to get the " +
				"exact wording.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "Tab name."},
				"text": {Type: "string", Description: "The visible text to click, e.g. 'Work To Do' or 'Self-Quiz Unit 5'. An exact match wins; otherwise the closest label containing it."},
			}, "text"),
		},
		Mutates:     true,
		Observe:     tabs.observe,
		Affordances: tabs.affordances,
		Act: func(ctx context.Context, args map[string]any) (Outcome, error) {
			tab, tabNote, terr := tabNoted(tabs, args)
			if terr != nil {
				return Outcome{}, terr
			}
			text := argString(args, "text")
			if strings.TrimSpace(text) == "" {
				return Outcome{}, fmt.Errorf("text is required")
			}
			action := guard.Action{Kind: guard.KindBrowser, Command: "click text " + text,
				Reason: fmt.Sprintf("click the element reading %q in tab %q (%s context)", text, tab.name, tab.ctx)}
			tab.sawReality() // she's using the right primitive; clear the guess counter

			var hit browser.ClickHit
			out, err := g.Run(ctx, action, func(ctx context.Context) (string, error) {
				h, err := tab.client.ClickText(ctx, text)
				if err != nil {
					return "", err
				}
				hit = h
				time.Sleep(900 * time.Millisecond)
				url, _ := tab.client.URL(ctx)
				title, _ := tab.client.Title(ctx)
				return fmt.Sprintf("Clicked %q. Now on %q\n%s", text, title, url), nil
			})
			if err != nil {
				return Outcome{}, err
			}

			o := Outcome{Text: out + tabNote}
			// Say what was actually hit when it differs from what was asked for.
			// The match is fuzzy — exact wins, else the shortest containing label —
			// so "Submit" can land on "Submit and add another", and echoing back the
			// requested text hid that entirely.
			if lbl := strings.TrimSpace(hit.Label); lbl != "" && !strings.EqualFold(lbl, strings.TrimSpace(text)) {
				o = o.WithEvidence("The element it actually hit reads %q.", lbl)
			}
			// A synthetic click is the untrusted kind portals ignore. It used to
			// return an unqualified success.
			if hit.Synthetic {
				o.Evidence = strings.TrimSpace(o.Evidence + "\nThat element had no clickable box, so this was a " +
					"scripted click rather than a real one — pages that require a genuine gesture will have ignored it.")
			}
			return o, nil
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
			}, "selector", "value"),
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

			action := guard.Action{Kind: guard.KindBrowser,
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
			}),
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
			}),
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
			Name: "browser_tabs",
			Description: "List open tabs — both the ones you opened and any the browser " +
				"opened by itself. A click on \"Open in new tab\", a download that opens a " +
				"viewer, or a sign-in that pops a window all produce a page you are not " +
				"driving yet; they appear here as unattached. Use browser_attach to take " +
				"one over.",
			Params: llm.ObjectSchema(nil),
		},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			var sb strings.Builder
			open := tabs.list()
			for _, t := range open {
				url, _ := t.client.URL(ctx)
				fmt.Fprintf(&sb, "- %s [%s] %s\n", t.name, t.ctx, clip(url, 80))
			}

			// Anything the browser opened that she is not driving. Without this a
			// click that opened a new tab looked like a click that did nothing, and
			// the page she actually wanted was sitting there unreachable.
			if extra := unattached(tabs); len(extra) > 0 {
				sb.WriteString("\nNot attached — the browser opened these, you are not driving them:\n")
				for _, t := range extra {
					title := t.Title
					if title == "" {
						title = "(untitled)"
					}
					fmt.Fprintf(&sb, "- %q %s\n", clip(title, 60), clip(t.URL, 80))
				}
				sb.WriteString("Use browser_attach with part of the title or url to drive one.")
			}
			if sb.Len() == 0 {
				return "No tabs open.", nil
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
			}),
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
				"profile, copying their live sessions across. Their logins here are a " +
				"snapshot — signing into something new in their normal browser does not " +
				"appear until this runs, and a session that has since expired here may " +
				"still be alive there.\n\n" +
				"Reach for this whenever a site says signed-out and signing in is awkward: " +
				"a session that expired in your snapshot, or a site with several saved " +
				"accounts where Chrome autofills the wrong password. It needs no password " +
				"at all, which is why it works where filling the form does not. Chrome " +
				"must be closed first, so ask the user to close it.",
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

// clickHint turns a failed selector click into a scouting result. Instead of a
// bare "no element matches" — which she answers by inventing the NEXT selector,
// and the next, twenty deep — it appends the page's actual clickable elements by
// their visible text, so one miss becomes the real answer: click one of these by
// text.
func clickHint(ctx context.Context, tab *openTab, err error) error {
	opts, lerr := tab.client.Links(ctx, 25)
	if lerr != nil || strings.TrimSpace(opts) == "" {
		return err
	}
	return fmt.Errorf("%w\n\nDo not guess another selector. Here is what is actually clickable on "+
		"the page right now — click one of these by its text with browser_click_text:\n%s", err, opts)
}

// maxSelectorMisses is how many consecutive missed selector clicks a tab tolerates
// before selector clicking is refused outright. Two — because the hint after the
// first and second miss is already ignored, and a third guess is never the one
// that lands.
const maxSelectorMisses = 2

// selectorBudget refuses a selector click once a tab has burned through its miss
// budget, breaking the guessing spiral that hints alone do not. It hands back the
// real clickable options so the forced pivot to browser_click_text is trivial.
// Scouting or clicking by text (which reset misses) lifts the block immediately.
func selectorBudget(ctx context.Context, tab *openTab) error {
	if tab.missCount() < maxSelectorMisses {
		return nil
	}
	opts, _ := tab.client.Links(ctx, 25)
	return fmt.Errorf("selector clicking is OFF for this page: %d guesses in a row missed, and guesses "+
		"do not converge — you are pattern-matching CSS, not driving the page. Click one of these by its "+
		"text with browser_click_text, or run browser_inspect to see the real controls:\n%s", tab.missCount(), opts)
}

// sawReality resets the miss counter: she scouted (read/inspect) or clicked by
// text, so she is working from what is actually on the page again.
//
// Guarded, because tool calls in one round run concurrently and this counter is
// read and written from several of them.
func (t *openTab) sawReality() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.misses = 0
	t.mu.Unlock()
}

// missed records a selector that found nothing, and reports the running count.
func (t *openTab) missed() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.misses++
	return t.misses
}

// missCount reads the counter without changing it.
func (t *openTab) missCount() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.misses
}

// observe fingerprints the page the active tab is showing, for the framework's
// before/after comparison. Empty when there is no tab, which switches the
// verification off rather than inventing a change.
func (t *Tabs) observe(ctx context.Context) string {
	tab, ok := t.get("")
	if !ok {
		return ""
	}
	return tab.client.Signature(ctx)
}

// affordances lists what is genuinely clickable on the active tab, attached to
// any browser failure. This is the generalisation of the ad-hoc click hint: a
// miss should hand back the page's real options rather than leave her to invent
// another selector.
func (t *Tabs) affordances(ctx context.Context) []string {
	tab, ok := t.get("")
	if !ok {
		return nil
	}
	links, err := tab.client.Links(ctx, 20)
	if err != nil || strings.TrimSpace(links) == "" {
		return nil
	}
	return strings.Split(strings.TrimSpace(links), "\n")
}

// looksLikeCommit reports whether a clicked label is a commit-type action — the
// kind that raises a confirmation rather than navigating. Used to explain a
// "nothing moved" result on a Submit so it isn't mistaken for a dead click.
func looksLikeCommit(text string) bool {
	s := strings.ToLower(strings.TrimSpace(text))
	for _, w := range []string{"submit", "finish", "confirm", "complete", "save and", "turn in"} {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}
