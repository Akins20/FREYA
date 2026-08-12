package skills

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Akins20/FREYA/internal/browser"
	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/llm"
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
	RegisterBrowserState(r, g, tabs)
	// The pause for a person: a Cloudflare check, a CAPTCHA, a 2FA code. See
	// handover.go — none of those can be clicked past, which is the point of them.
	RegisterHandover(r, g, tabs)

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
				title, current, err := tabs.OpenAt(ctx, name, bctx, url)
				if err != nil {
					return "", err
				}
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

			// Whether this is the site at all, said without being asked. A
			// certificate warning and a safe-browsing block are real pages with
			// real text: read as content they become "the site now says something
			// about privacy", and the next twenty rounds are spent on Chrome's
			// error page. A page that has not finished loading reads as an empty
			// one, which is the same trap from the other side.
			state := tab.client.State(ctx)
			// A sign-in page reached in the guest context is a door she brought no
			// key to. Attached here rather than left to the playbook, because the
			// playbook already said it and she reported the limitation anyway.
			return fmt.Sprintf("%s\n%s\n\n%s%s%s%s", title, url,
				clipText(text, limit), coverage(len(text), limit), state.Describe(),
				browser.GuestSignIn(state, tab.ctx == browser.ContextGuest)), nil
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
			Description: "Click an element by CSS selector, with real mouse events " +
				"(searches shadow DOM and iframes).\n\n" +
				"Prefer browser_click_text: naming a thing by what it says is more reliable " +
				"than a selector, and it is the only target you can be sure of because you " +
				"read it off the page a moment ago. Use this one when there is no text to " +
				"aim at — a drag handle, a bare icon — and only with a selector copied " +
				"verbatim from a browser_inspect you JUST ran. A selector from memory or " +
				"inferred from a pattern will miss: app pages generate their ids fresh each " +
				"visit.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":     {Type: "string", Description: "Tab name."},
				"selector": {Type: "string", Description: "A selector copied verbatim from a browser_inspect you just ran — not composed, remembered, or guessed. Plain CSS only (no jQuery :contains())."},
			}, "selector"),
		},
		// Changes the page, which is what every framework protection keys off:
		// the certificate-warning refusal installed by Protect, and before/after
		// sampling. Unset, this tool is exempt from both without saying so.
		Mutates:     true,
		Observe:     tabs.observe,
		Affordances: tabs.affordances,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tab, ok := tabs.get(argString(args, "name"))
			if !ok {
				return "", fmt.Errorf("no tab named %q", argString(args, "name"))
			}
			// The event log was built for this call and was only ever read by the
			// gestures. See sideEffects.
			started := time.Now()
			// Sampled before the guard runs, not after: the baseline has to predate
			// the click, or the tab the click opens is already in it.
			before := pageIDs(tab.ctx)
			sel := argString(args, "selector")
			action := guard.Action{Kind: guard.KindBrowser, Command: "click " + sel,
				Reason: fmt.Sprintf("click %q in tab %q (%s context)", sel, tab.name, tab.ctx)}
			if err := selectorBudget(ctx, tab); err != nil {
				return "", err
			}
			out, rerr := g.Run(ctx, action, func(ctx context.Context) (string, error) {
				if err := tab.client.ClickReal(ctx, sel); err != nil {
					tab.missed()
					return "", clickHint(ctx, tab, err)
				}
				tab.sawReality()
				time.Sleep(900 * time.Millisecond)
				url, _ := tab.client.URL(ctx)
				title, _ := tab.client.Title(ctx)
				return fmt.Sprintf("Clicked. Now on %q\n%s", title, url), nil
			})
			if rerr != nil {
				return "", rerr
			}
			return out + sideEffects(tab, started, before), nil
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
			started := time.Now()
			// Sampled before the guard runs, not after: the baseline has to predate
			// the click, or the tab the click opens is already in it.
			before := pageIDs(tab.ctx)
			text := argString(args, "text")
			if strings.TrimSpace(text) == "" {
				return Outcome{}, fmt.Errorf("text is required")
			}
			// Not guidance — a refusal. Asked what to do on "Your connection is not
			// private" with the user waiting to sign in, she said she would click
			// Advanced and proceed. The page had already been flagged to her as a
			// browser warning: she read that, understood it, and chose to go
			// through. Some things have to be unavailable rather than discouraged.
			if why := browser.RefuseUnsafeProceed(tab.client.State(ctx), text); why != "" {
				return Outcome{}, fmt.Errorf("%s", why)
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

			o := Outcome{Text: out + tabNote + sideEffects(tab, started, before)}
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
		// Changes the page, which is what every framework protection keys off:
		// the certificate-warning refusal installed by Protect, and before/after
		// sampling. Unset, this tool is exempt from both without saying so.
		Mutates:     true,
		Observe:     tabs.observe,
		Affordances: tabs.affordances,
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
			Params: llm.ObjectSchema(map[string]llm.Property{
				"profile": {Type: "string", Description: "Which Chrome profile to copy, by " +
					"name or account. Omit for the one Chrome used last. browser_profiles " +
					"lists them, and on a machine with several this is the difference " +
					"between their account and somebody else's."},
			}),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			// KindBrowser, not KindWrite — and the reason still names exactly what
			// is copied, so the audit log is unchanged.
			//
			// As KindWrite this scored RiskMedium, which requires a confirmation,
			// and the paths she actually runs in have nobody to confirm to: the
			// daemon, voice, and one-shot -ask all lack a terminal, so the answer
			// defaults to no and the tool declines itself. Measured rather than
			// guessed — asked to sign in with three saved logins on the account,
			// she filled the username, Chrome autofilled a DIFFERENT account's
			// password, and the one tool that fixes that could not run.
			//
			// This is the same bug browser_open had, where elevating the auth
			// context meant she could not open the user's own portal at their own
			// request over voice.
			//
			// The argument for lowering it is not that credentials do not matter.
			// It is that browser_open in the auth context is ALREADY RiskLow and
			// already drives the browser as the user, with their real cookies —
			// this only populates the profile that browser_open then uses. Gating
			// the populate while auto-approving the use is backwards.
			//
			// The sync also cannot destroy anything. It copies FROM the real Chrome
			// profile and never writes to it, its destination is Freya's own
			// automation profile, and the script refuses to run at all while Chrome
			// is open rather than tearing a live database. The confirmation was
			// protecting files that are hers.
			// Which profile, resolved before the guard so the confirmation names the
			// account being copied rather than "the Chrome profile". On a machine
			// with several, that sentence is the only thing standing between the
			// user and a browser signed in as the wrong one of them.
			want := strings.TrimSpace(argString(args, "profile"))
			chosen, perr := browser.FindProfile("", want)
			if perr != nil {
				return "", perr
			}

			action := guard.Action{Kind: guard.KindBrowser,
				Command: "sync chrome profile",
				Reason: fmt.Sprintf("copy the cookies and saved logins of the %s profile "+
					"into the automation profile, so the browser she drives is signed in "+
					"as that account", chosen.Label())}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				out, err := browser.SyncAuthProfileFrom(ctx, chosen)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("%s\n\n[Copied from the %s profile. Anything signed "+
					"into a different profile is not here.]", out, chosen.Label()), nil
			})
		},
	})

	// Last, once every browser tool is registered: no interaction with a browser
	// warning page, whichever tool asks.
	//
	// This was a refusal in browser_click_text alone, whose own message told her
	// "do not look for another way through" while five other interaction tools
	// were exactly that. Attached by family rather than by call site, so a
	// browser tool added later is covered without anyone remembering to cover it.
	//
	// The exceptions are the ways OUT. Leaving a warning page is the correct
	// move, so the tools that leave must keep working on one; reads are untouched
	// already, because only mutating skills are protected.
	r.Protect("browser_", refuseOnWarning(tabs),
		"browser_open", "browser_attach", "browser_tabs", "browser_sync_logins",
		// Touches no page — it raises a window and waits for a person. Gating it
		// on what the page is would be gating the way OUT of a bad page.
		"browser_hand_over")
}

// refuseOnWarning is the family rule for browser interaction: a certificate or
// safety interstitial is Chrome talking, not the site, and nothing on it is
// hers to touch.
func refuseOnWarning(tabs *Tabs) Precheck {
	return func(ctx context.Context, args map[string]any) error {
		tab, _, err := tabNoted(tabs, args)
		if err != nil {
			// No tab is the handler's problem to report, with its own better
			// message. A guard must not turn one failure into a different one.
			return nil
		}
		if why := browser.RefuseInteraction(tab.client.State(ctx), "act on this page"); why != "" {
			return fmt.Errorf("%s", why)
		}
		return nil
	}
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
// OpenAt opens a tab on a URL and registers it, returning the title and the
// address it actually landed on.
//
// # Why this is a method rather than the body of browser_open
//
// It was the body of browser_open, and so anything else that knew where it
// wanted to go had two options: duplicate thirty lines of tab bookkeeping, or
// hand the address back to the model and ask it to call browser_open next. The
// second costs a round every time. The first is how the download-behaviour call
// and the dialog watcher end up set on some tabs and not others, which is the
// drift internal/browser/gestures.go was consolidated to stop.
//
// So the bookkeeping lives here once: launch the context, replace any tab of the
// same name, connect, start watching for downloads and dialogs, navigate, and
// register what came back. A skill that knows its destination now gets there in
// one call, and none of them can forget the watcher.
func (t *Tabs) OpenAt(ctx context.Context, name string, bctx browser.Context, url string) (title, landed string, err error) {
	if err := browser.Launch(ctx, bctx); err != nil {
		return "", "", err
	}
	if old, ok := t.remove(name); ok {
		_ = old.client.Close()
		_ = browser.CloseTab(old.ctx, old.target.ID)
	}
	target, err := browser.NewTab(bctx, "about:blank")
	if err != nil {
		return "", "", err
	}
	client, err := browser.Connect(bctx, target)
	if err != nil {
		return "", "", err
	}
	// Downloads land in a folder instead of opening a chooser nothing can drive,
	// javascript dialogs get answered instead of blocking the renderer forever,
	// and both are recorded so a click that caused one can say so. See
	// internal/browser/events.go.
	client.Watch(ctx)
	if err := client.Navigate(ctx, url); err != nil {
		client.Close()
		return "", "", err
	}
	title, _ = client.Title(ctx)
	landed, _ = client.URL(ctx)

	t.put(&openTab{name: name, ctx: bctx, target: target,
		client: client, opened: time.Now(), lastURL: landed})
	return title, landed, nil
}

func (t *Tabs) observe(ctx context.Context, args map[string]any) string {
	// The tab the CALL names, not the last one touched. Resolved through the same
	// helper the handlers use, so before and after describe the page the action
	// actually acts on even with several tabs open.
	tab, _, ok := t.resolve(argString(args, "name"))
	if !ok || tab == nil {
		return ""
	}
	// The page's own fingerprint, plus the set of pages the BROWSER has open.
	//
	// The second half is the fix for a click that opens a new tab. The page she is
	// driving is untouched by such a click — same URL, same title, same DOM — so
	// the fingerprint matched and the result said "Nothing observably changed —
	// that usually means the action did not take effect". It had taken effect; the
	// effect was one target across, in a tab she was not attached to. She clicked
	// "Apply with Indeed" three times against that sentence, re-read, re-navigated,
	// and only found the tab thirteen rounds later with browser_tabs.
	//
	// Page.windowOpen was supposed to catch this and did not fire, which is the
	// deeper lesson: one CDP event is a single oracle, and a click can produce a
	// tab through half a dozen paths that event does not cover (target=_blank in a
	// cross-origin frame, a noopener popup, a redirect chain). The target list is
	// the browser's own answer to "what pages exist", so it is true regardless of
	// how the tab came to be.
	return tab.client.Signature(ctx) + "\n" + pageIDs(tab.ctx).component()
}

// tabSet is which pages a context had open at a moment, and whether that
// question could be answered at all.
//
// The bool is the whole point of the type. "No pages were open" and "I could not
// ask" both arrive as an empty list, and treating the second as the first makes
// every open tab look newly created — so a failed reading would announce that
// the click opened six tabs it had nothing to do with. That is the Indeed bug
// inverted: instead of missing a real side effect it invents one, in the same
// tool, and points her at browser_attach for a tab she never opened.
//
// Nil-versus-empty would technically carry this today and is exactly the kind of
// distinction that survives until someone writes `return []string{}` in an error
// path. Made explicit so it cannot be lost by accident.
type tabSet struct {
	ids []string
	ok  bool
}

// pageIDs is the sorted set of page targets a browser context has open.
//
// Sorted because /json/list orders by recency of activation, so an unsorted
// reading changes whenever focus moves and would report a change that is only
// the browser reshuffling its own list.
func pageIDs(c browser.Context) tabSet {
	targets, err := browser.Targets(c)
	if err != nil {
		return tabSet{}
	}
	ids := make([]string, 0, len(targets))
	for _, t := range targets {
		ids = append(ids, t.ID)
	}
	sort.Strings(ids)
	return tabSet{ids: ids, ok: true}
}

// tabComponent is how a tab reading joins the page fingerprint.
//
// A failed reading gets a sentinel rather than an empty list. Observe samples
// this before and after an action and compares the two strings for equality, so
// there is no way to answer "unknown" through that interface — the reading is
// either part of the fingerprint or it is not, and one-sided failure shows up as
// a change either way.
//
// The sentinel makes a two-sided failure compare equal, which is the common case
// when the endpoint is briefly unreachable. A one-sided failure still reads as a
// change, and that is the direction to fail in: this whole mechanism exists
// because a false "nothing changed" cost four real job applications, while a
// false "something changed" costs one wrong sentence.
func (t tabSet) component() string {
	if !t.ok {
		return "tabs:unreadable"
	}
	return strings.Join(t.ids, ",")
}

// openedTabs names the pages that appeared while an action ran.
//
// Knowing something changed is not enough on its own: a new tab is not attached,
// so nothing she does next reaches it until she says so. The note therefore
// carries the name of the tool that closes the gap.
func openedTabs(c browser.Context, before tabSet) string {
	// No baseline, no claim. Everything open would otherwise read as new.
	if !before.ok {
		return ""
	}
	was := make(map[string]bool, len(before.ids))
	for _, id := range before.ids {
		was[id] = true
	}
	targets, err := browser.Targets(c)
	if err != nil {
		return ""
	}
	var lines []string
	for _, t := range targets {
		if was[t.ID] {
			continue
		}
		lines = append(lines, fmt.Sprintf("  · that opened a NEW TAB: %q (%s). You are not "+
			"driving it — the tab you are attached to is unchanged, which is why this page "+
			"looks the same. Use browser_attach to take it over before acting again",
			clipText(t.Title, 120), clipText(t.URL, 160)))
	}
	if len(lines) == 0 {
		return ""
	}
	return "\nAlso, outside the page:\n" + strings.Join(lines, "\n")
}

// affordances lists what is genuinely clickable on the active tab, attached to
// any browser failure. This is the generalisation of the ad-hoc click hint: a
// miss should hand back the page's real options rather than leave her to invent
// another selector.
func (t *Tabs) affordances(ctx context.Context, _ map[string]any) []string {
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

// coverage says how much of the page she has actually been given.
//
// # Why "[truncated]" was not enough
//
// browser_read fetches the whole page — innerText returns everything, scrolled
// into view or not — and then clips it to 12,000 characters. The clip said
// "[truncated at 12000 characters]", which reads as a tidy-up. It does not say
// whether the missing part is a footer or three quarters of the document.
//
// Asked to replicate a site exactly, she read it once, got the top of it, and
// built a copy of the top of it. She was not being careless: nothing she saw
// distinguished "this is the page" from "this is the first third of the page".
// The user then had to explain what thorough meant, which is the wrong shape for
// a fix — a disposition cannot be installed by instruction, but a fact can be
// made impossible to miss.
//
// So this states the fraction, and what to do about it. A number she has to read
// past is worth more than an adjective she has to live up to.
func coverage(total, shown int) string {
	if total <= shown {
		return ""
	}
	pct := 100 * shown / total
	return fmt.Sprintf("\n\n[You have seen the FIRST %d%% of this page — %d characters of "+
		"%d. The other %d%% is below what you just read and you have not seen any of "+
		"it. If you are describing, copying or summarising the whole page, read it "+
		"again with a higher limit (up to 100000) before you treat this as the "+
		"complete picture. browser_find is cheaper when you only need one phrase.]",
		pct, shown, total, 100-pct)
}

// sideEffects reports what a click caused outside the page.
//
// The event channel exists for one failure, recorded in the browser package
// doc: "a click that started a download looked exactly like a click that did
// nothing, which is how four clicks and four dialogs happen". It was then read
// only by the gestures in browser_gestures.go, right-click and double-click and
// drag, and not by browser_click or browser_click_text, which are the tools that
// do the actual clicking and the ones that story is about.
//
// The before-and-after fingerprint does not cover it. A download leaves the page
// identical, and a dialog is auto-answered before the second sample is taken, so
// Observe sees no change and the result reads as nothing having happened, which
// is the exact sentence the event log was added to stop.
// before is the target set sampled just before the action; a page that is in the
// list afterwards and was not in it before is a tab the click opened.
//
// It cannot tell HER new tab from one the user opened by hand in the same few
// seconds, and does not pretend to. A person browsing in the same Chrome while
// she works would be attributed to the click, which is a confusing sentence and
// not a wrong action — the tool it names, browser_attach, is read-only until she
// acts on what she finds there.
func sideEffects(tab *openTab, since time.Time, before tabSet) string {
	if tab == nil || tab.client == nil {
		return ""
	}
	return browser.Describe(tab.client.Since(since)) + openedTabs(tab.ctx, before)
}
