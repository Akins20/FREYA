package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Page-level operations built on raw CDP calls.

// Navigate loads a URL and waits for the page to settle.
func (c *Client) Navigate(ctx context.Context, url string) error {
	if !strings.Contains(url, "://") {
		url = "https://" + url
	}
	if _, err := c.Call(ctx, "Page.enable", nil); err != nil {
		return err
	}
	if _, err := c.Call(ctx, "Page.navigate", map[string]any{"url": url}); err != nil {
		return err
	}
	if err := c.waitReady(ctx); err != nil {
		return err
	}
	// Then wait for the app's own content, not just its shell — otherwise a
	// read a moment later sees placeholders.
	c.WaitStable(ctx, 8*time.Second)
	return nil
}

// waitReady polls document.readyState.
//
// Polling rather than waiting on Page.loadEventFired because plenty of pages
// never fire it — a long-lived connection, a stalled analytics beacon — while
// being perfectly usable. Readiness is what matters, not the event.
func (c *Client) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		state, err := c.EvalString(ctx, "document.readyState")
		if err == nil && (state == "complete" || state == "interactive") {
			// A brief settle lets frameworks paint their first render.
			time.Sleep(400 * time.Millisecond)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil // usable enough; a slow page is not a failure
}

// WaitStable waits until the page's content stops changing.
//
// # Why readyState is not enough
//
// A single-page app reports readyState "complete" the instant its HTML shell
// arrives — and then fetches the actual content and swaps it in a beat later.
// Reading at that moment sees skeleton placeholders, not the list that is about
// to appear, which is exactly the "it keeps coming up empty" failure: she looked
// too early. This waits for the real thing.
//
// The signal is a content signature — how much text is on the page, and how many
// elements — polled until it holds still. Placeholders are sparse; when the
// fetch lands, text and element counts jump, then settle. When the signature
// stops moving for a few polls, the content has arrived. A page that never
// settles (a live ticker, an animation) falls through to the timeout, which is
// correct: at that point what is there is what there is going to be.
func (c *Client) WaitStable(ctx context.Context, timeout time.Duration) {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	deadline := time.Now().Add(timeout)
	// The signature must count what is inside shadow roots too. A shadow-DOM app
	// renders its skeleton in the light DOM and swaps the real content into shadow
	// trees; a light-DOM-only element count barely moves when that happens, so the
	// old signature reported "settled" while the page was still empty and she read
	// too early. Walking shadow roots makes the count jump when content actually
	// lands. Text length is a second, cheaper mover for skeleton→content.
	const sigExpr = deepPrelude + `(() => {
      let elems = 0;
      const rec = (root) => {
        let list;
        try { list = root.querySelectorAll('*'); } catch (e) { return; }
        elems += list.length;
        for (const el of list) { const s = __descend(el); if (s) rec(s); }
      };
      rec(document);
      const b = document.body;
      const textLen = b ? b.innerText.length : 0;
      return elems + ":" + textLen;
    })()`

	var last string
	stable := 0
	for time.Now().Before(deadline) {
		sig, err := c.EvalString(ctx, sigExpr)
		if err == nil {
			if sig == last && sig != "0:0" {
				stable++
				// Unchanged for three polls (~600ms) means it has settled.
				if stable >= 3 {
					return
				}
			} else {
				stable = 0
				last = sig
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Eval runs JavaScript and returns the raw result value.
func (c *Client) Eval(ctx context.Context, expr string) (json.RawMessage, error) {
	res, err := c.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expr,
		"returnByValue": true,
		"awaitPromise":  true,
	})
	if err != nil {
		return nil, err
	}

	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, err
	}
	if out.ExceptionDetails != nil {
		msg := out.ExceptionDetails.Text
		if out.ExceptionDetails.Exception != nil {
			msg = out.ExceptionDetails.Exception.Description
		}
		return nil, fmt.Errorf("page script error: %s", msg)
	}
	return out.Result.Value, nil
}

// EvalString runs JavaScript expecting a string back.
func (c *Client) EvalString(ctx context.Context, expr string) (string, error) {
	v, err := c.Eval(ctx, expr)
	if err != nil {
		return "", err
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s, nil
	}
	return strings.Trim(string(v), `"`), nil
}

// deepPrelude defines the helpers every DOM read and interaction shares.
//
// # Why this exists: innerText and querySelector are blind to Shadow DOM
//
// Modern component frameworks — D2L Brightspace, much of Google, Salesforce —
// render their real content inside open shadow roots. innerText does not descend
// into a shadow root, and document.querySelector does not match inside one. So a
// reader built on innerText saw a page's chrome and none of its actual content:
// on the UoPeople portal she read "Work To Do" as empty placeholders, waited on
// elements that "never appeared" because they lived in shadow trees, fell back to
// viewport screenshots, and finally invented CSS ids that never existed. She was
// driving blind. Every read below walks light DOM, open shadow roots and
// same-origin iframes so she sees the page a person sees.
//
// __vis keeps the old honesty: content behind display:none / visibility:hidden /
// opacity:0 is skipped, so she still has to actually reveal a modal, tab or
// collapsed panel rather than reading through it. __host climbs out of a shadow
// root to its host element.
// The helpers are assigned onto globalThis, not declared with const. Every read
// runs as its own Runtime.evaluate in the SAME page context, and a top-level
// `const __vis` persists into the global lexical scope — so the second read
// threw "Identifier '__vis' has already been declared". Idempotent assignment
// re-defines them harmlessly on each call. The IIFE bodies below reference them
// as bare names, which resolve through globalThis.
const deepPrelude = `
  globalThis.__vis = function (el) {
    try {
      if (!el || el.nodeType !== 1) return true;
      const win = (el.ownerDocument && el.ownerDocument.defaultView) || window;
      const s = win.getComputedStyle(el);
      if (!s) return true;
      if (s.display === 'none' || s.visibility === 'hidden' || s.visibility === 'collapse') return false;
      if (parseFloat(s.opacity || '1') === 0) return false;
      // The checks above only see this element's own style. An element inside a
      // display:none ancestor keeps its own display:block yet is not rendered —
      // which made a closed modal's buttons look live to the flat scans (Inspect,
      // ClickText). A rendered element has at least one client rect; a hidden
      // subtree has none. This is what keeps her from "clicking" a modal that
      // isn't actually open.
      if (typeof el.getClientRects === 'function' && el.getClientRects().length === 0) return false;
      return true;
    } catch (e) { return true; }
  };
  globalThis.__host = function (n) { const r = n && n.getRootNode && n.getRootNode(); return r && r.host ? r.host : null; };
  // __descend returns the sub-tree hidden under an element — its open shadow root
  // or, for a same-origin iframe, that frame's document. Reading already crossed
  // these boundaries; every interaction (query, click, fill, locate) now crosses
  // the same ones, because a portal like D2L renders its quiz INSIDE an iframe and
  // its widgets inside shadow roots, so an element that isn't reachable through
  // both is an element she can read but never touch. Cross-origin frames throw on
  // access and come back null.
  globalThis.__descend = function (el) {
    try { if (el.shadowRoot) return el.shadowRoot; } catch (e) {}
    try { if ((el.tagName === 'IFRAME' || el.tagName === 'FRAME') && el.contentDocument) return el.contentDocument; } catch (e) {}
    return null;
  };
  // __find is THE selector lookup. Every tool that takes a selector must use it,
  // because the alternative is what actually happened: browser_inspect walked
  // shadow roots and iframes and handed back selectors from inside them, while
  // check, select, submit, wait, focus and read_element used a plain
  // document.querySelector and could not reach a single one. She scouted exactly
  // as instructed, copied a selector verbatim, and was told "no element matches" —
  // then blamed for guessing. The tools disagreed about what the page was.
  //
  // Returns {el, bad}: bad marks a selector the engine itself rejected (jQuery's
  // :contains() is the usual culprit), which deserves a different message from an
  // honest miss.
  globalThis.__find = function (sel) {
    let found = null, bad = false;
    const rec = (root) => {
      if (found || bad) return;
      let m;
      try { m = root.querySelector(sel); } catch (e) { bad = true; return; }
      if (m) { found = m; return; }
      let list;
      try { list = root.querySelectorAll('*'); } catch (e) { return; }
      for (const x of list) {
        const sub = __descend(x);
        if (sub) rec(sub);
        if (found || bad) return;
      }
    };
    rec(document);
    return {el: found, bad: bad};
  };
  // __deepText is the text of the whole page including shadow roots and
  // same-origin frames. Waiting on text has to look where reading looks, or she
  // waits out the timeout for content that is already on screen.
  //
  // A document exposes innerText via its body; a shadow root has no body, so its
  // text comes from textContent — which is a superset, since it also includes
  // hidden nodes. That asymmetry is deliberate for a wait: erring towards seeing
  // the phrase means a wait ends early rather than never, and the read that
  // follows is the one that decides what is genuinely visible.
  globalThis.__deepText = function () {
    let out = '';
    const rec = (root) => {
      try {
        if (root.body) out += ' ' + (root.body.innerText || '');
        else if (root.textContent) out += ' ' + root.textContent;
      } catch (e) {}
      let list;
      try { list = root.querySelectorAll('*'); } catch (e) { return; }
      for (const el of list) { const sub = __descend(el); if (sub) rec(sub); }
    };
    rec(document);
    return out;
  };
`

// readableText extracts what a person would actually read on the page, now
// piercing shadow roots (see deepPrelude). It reads the LIVE tree, not a clone:
// a detached clone's innerText ignores layout and would leak content hidden
// behind display:none. Same-origin iframes and open shadow roots are walked in;
// cross-origin frames throw on access and are skipped. Output is capped so a
// large app cannot flood a single turn's context.
const readableText = deepPrelude + `(() => {
  const SKIP = {SCRIPT:1, STYLE:1, NOSCRIPT:1, TEMPLATE:1, HEAD:1, SVG:1};
  const parts = [];
  let budget = 20000;
  const rec = (root) => {
    if (budget <= 0) return;
    for (const n of (root.childNodes || [])) {
      if (budget <= 0) return;
      if (n.nodeType === 3) {
        const t = n.textContent.replace(/\s+/g, ' ');
        if (t.trim()) { parts.push(t); budget -= t.length; }
        continue;
      }
      if (n.nodeType !== 1 || SKIP[n.tagName] || !__vis(n)) continue;
      if (n.shadowRoot) rec(n.shadowRoot);
      if (n.tagName === 'IFRAME') {
        try { const d = n.contentDocument; if (d && d.body) rec(d.body); } catch (e) {}
      }
      rec(n);
      const tag = n.tagName;
      const block = tag.indexOf('-') > -1 ||
        /^(DIV|P|LI|TR|UL|OL|SECTION|ARTICLE|HEADER|FOOTER|NAV|MAIN|H1|H2|H3|H4|H5|H6|TABLE|BR|BUTTON|A|LABEL)$/.test(tag);
      if (block) parts.push('\n');
    }
  };
  rec(document.body || document.documentElement);
  return parts.join(' ')
    .replace(/[ \t]*\n[ \t]*/g, '\n')
    .replace(/\n{3,}/g, '\n\n')
    .replace(/ {2,}/g, ' ')
    .trim();
})()`

// Text returns the page's readable content.
func (c *Client) Text(ctx context.Context) (string, error) {
	return c.EvalString(ctx, readableText)
}

// Title returns the document title.
func (c *Client) Title(ctx context.Context) (string, error) {
	return c.EvalString(ctx, "document.title")
}

// URL returns the current location.
func (c *Client) URL(ctx context.Context) (string, error) {
	return c.EvalString(ctx, "location.href")
}

// LoggedInHints looks for evidence the page considers the user signed in.
//
// Heuristic and honest about it: there is no general way to ask a page "am I
// logged in". Sign-out affordances and account menus are the usual tell.
const loggedInHints = `(() => {
  const t = document.body ? document.body.innerText.toLowerCase() : '';
  const signals = ['sign out','log out','logout','my account','your account','profile'];
  const found = signals.filter(s => t.includes(s));
  const signin = ['sign in','log in','login','create account'].filter(s => t.includes(s));
  return JSON.stringify({loggedIn: found, anonymous: signin});
})()`

// SessionHints reports whether a page looks signed in.
func (c *Client) SessionHints(ctx context.Context) (string, error) {
	return c.EvalString(ctx, loggedInHints)
}

// Click finds an element by CSS selector and clicks it, searching across shadow
// roots. Two failures used to burn a whole tool round each: a selector that
// matched only inside a shadow tree returned "not-found", and an invalid
// selector (she reached for jQuery's :contains(), which is not CSS) threw a raw
// SyntaxError. Both now return a directed message that points her at
// ClickText — clicking by visible text is almost always what she actually wants
// on an app with opaque, generated ids.
func (c *Client) Click(ctx context.Context, selector string) error {
	expr := deepPrelude + fmt.Sprintf(`(() => {
      const f = __find(%q);
      if (f.bad) return 'bad-selector';
      if (!f.el) return 'not-found';
      f.el.scrollIntoView({block:'center'});
      f.el.click();
      return 'ok';
    })()`, selector)
	res, err := c.EvalString(ctx, expr)
	if err != nil {
		return err
	}
	switch res {
	case "bad-selector":
		return fmt.Errorf("%q is not a valid CSS selector (jQuery tricks like :contains() do not work); "+
			"to click by what the element says, use browser_click_text", selector)
	case "not-found":
		return fmt.Errorf("no element matches %q anywhere on the page, including shadow DOM; "+
			"try browser_click_text to click by visible text", selector)
	}
	// A click on an app usually swaps content in; wait for it to settle so the
	// next read sees the result, not the page mid-transition.
	c.WaitStable(ctx, 5*time.Second)
	return nil
}

// ClickText clicks the element whose visible text matches, searching light DOM
// and open shadow roots. This is the primitive an LLM should reach for first:
// a person clicks "Self-Quiz Unit 5", not "#d2l_1_5_130". It prefers an exact
// match, falls back to the shortest containing match (the most specific — the
// leaf label, not its whole container), climbs to the nearest clickable ancestor
// (crossing shadow boundaries) when the text sits in a non-clickable node, and
// fires a full pointer/mouse sequence so framework handlers actually run.
func (c *Client) ClickText(ctx context.Context, text string) error {
	expr := deepPrelude + fmt.Sprintf(`(() => {
      const needle = %q.trim().toLowerCase();
      if (!needle) return 'empty';
      const isClickable = (el) => {
        if (!el || el.nodeType !== 1) return false;
        const tag = el.tagName.toLowerCase();
        if (tag === 'a' || tag === 'button' || tag === 'summary') return true;
        if (tag === 'input' && ['submit','button','checkbox','radio','image'].includes(el.type)) return true;
        const role = ((el.getAttribute && el.getAttribute('role')) || '').toLowerCase();
        if (['button','link','menuitem','menuitemcheckbox','tab','option'].includes(role)) return true;
        if (el.onclick || el.hasAttribute('onclick') || el.hasAttribute('href')) return true;
        if (tag.indexOf('-') > -1 && /(link|button|item|tab|action)/.test(tag)) return true;
        return false;
      };
      // When a modal is open the background is inert — a person can only click
      // inside the dialog. So a match inside a visible dialog outranks an
      // identically-worded one behind it. This is the fix for the quiz's submit
      // confirmation: the page and the dialog both had a "Submit Quiz" button, and
      // without this she kept clicking the background one. Walks up across shadow
      // and iframe boundaries, since the dialog may sit in either.
      const inDialog = (el) => {
        let n = el;
        while (n) {
          if (n.nodeType === 1) {
            const tag = (n.tagName || '').toLowerCase();
            const role = ((n.getAttribute && n.getAttribute('role')) || '').toLowerCase();
            let cls = '';
            try { cls = (typeof n.className === 'string') ? n.className : (n.className && n.className.baseVal) || ''; } catch (e) {}
            if ((role === 'dialog' || role === 'alertdialog' ||
                 (n.getAttribute && n.getAttribute('aria-modal') === 'true') ||
                 tag === 'dialog' || tag.indexOf('dialog') > -1 ||
                 /(^|[ _-])(modal|dialog)([ _-]|$)/.test(cls)) && __vis(n)) {
              return true;
            }
          }
          n = n.parentElement || __host(n) ||
              (n.ownerDocument && n.ownerDocument.defaultView && n.ownerDocument.defaultView.frameElement);
        }
        return false;
      };
      let best = null;
      const rec = (root) => {
        let list;
        try { list = root.querySelectorAll('*'); } catch (e) { return; }
        for (const el of list) {
          const sub = __descend(el);
          if (sub) rec(sub);
          if (!__vis(el)) continue;
          const txt = ((el.innerText || el.textContent) || '').trim().replace(/\s+/g, ' ').toLowerCase();
          if (!txt) continue;
          const exact = txt === needle;
          if (!exact && !txt.includes(needle)) continue;
          // Resolve to the real target: the element itself if it is clickable,
          // else the nearest clickable ancestor, crossing shadow and iframe
          // boundaries.
          let target = el;
          if (!isClickable(el)) {
            let p = el;
            for (let i = 0; i < 8 && p; i++) {
              if (isClickable(p)) { target = p; break; }
              p = p.parentElement || __host(p) ||
                  (p.ownerDocument && p.ownerDocument.defaultView && p.ownerDocument.defaultView.frameElement);
            }
          }
          // Lower is better. Prefer: inside an open dialog; a genuinely clickable
          // target over a bare container that only happens to contain the text;
          // exact over partial; then the shortest (most specific) label. The
          // clickable term is what stops a whole <body> whose one visible word is a
          // button's label from tying with that button and taking a no-op click.
          const score = (inDialog(target) ? 0 : 4000000)
                      + (isClickable(target) ? 0 : 2000000)
                      + (exact ? 0 : 100000)
                      + txt.length;
          if (best && score >= best.score) continue;
          best = {el: target, score: score};
        }
      };
      rec(document);
      if (!best) return JSON.stringify({error:'not-found'});
      const el = best.el;
      el.scrollIntoView({block:'center', inline:'center'});
      const label = ((el.innerText || el.textContent) || '').trim().replace(/\s+/g, ' ').slice(0, 60);
      const r = el.getBoundingClientRect();
      if (r.width === 0 && r.height === 0) {
        // No box to aim a real click at (a zero-size custom element, say). Fall
        // back to a synthetic click — better than nothing for handlers that don't
        // gate on isTrusted.
        const o = {bubbles:true, cancelable:true, view:window};
        ['pointerdown','mousedown','pointerup','mouseup','click'].forEach(type => {
          try { el.dispatchEvent(new MouseEvent(type, o)); } catch (e) {}
        });
        try { el.click(); } catch (e) {}
        return JSON.stringify({synthetic:true, label:label});
      }
      // Return top-window viewport coordinates so Go can dispatch a REAL, trusted
      // mouse click. A synthetic el.click() is isTrusted:false, and many portal
      // links (D2L's quiz list) navigate only on a genuine gesture — which is why
      // clicking a quiz by name "succeeded" yet the page never moved. Add each
      // ancestor frame's offset so an in-iframe target maps to the top viewport.
      let x = r.left + r.width/2, y = r.top + r.height/2;
      let w = (el.ownerDocument && el.ownerDocument.defaultView) || window;
      while (w && w.frameElement) {
        const fr = w.frameElement.getBoundingClientRect();
        x += fr.left; y += fr.top; w = w.parent;
      }
      return JSON.stringify({x:x, y:y, label:label});
    })()`, text)
	raw, err := c.EvalString(ctx, expr)
	if err != nil {
		return err
	}
	var hit struct {
		X, Y      float64
		Label     string
		Error     string
		Synthetic bool
	}
	if err := json.Unmarshal([]byte(raw), &hit); err != nil {
		return fmt.Errorf("click %q: %w", text, err)
	}
	switch hit.Error {
	case "empty":
		return fmt.Errorf("no text given to click")
	case "not-found":
		return fmt.Errorf("nothing on the page reads %q (searched shadow DOM and iframes too)", text)
	}
	if hit.Synthetic {
		// Already clicked in the page; just let it settle.
		c.WaitStable(ctx, 5*time.Second)
		return nil
	}
	// A genuine mouse click at the element's centre — move, press, pause, release
	// — so isTrusted-gated navigation actually fires.
	if _, err := c.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseMoved", "x": hit.X, "y": hit.Y,
	}); err != nil {
		return err
	}
	for _, phase := range []string{"mousePressed", "mouseReleased"} {
		if _, err := c.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type": phase, "x": hit.X, "y": hit.Y, "button": "left", "clickCount": 1,
		}); err != nil {
			return err
		}
		if phase == "mousePressed" {
			sleepCtx(ctx, 45*time.Millisecond)
		}
	}
	c.WaitStable(ctx, 5*time.Second)
	return nil
}

// Fill sets a form field's value and fires the events frameworks listen for.
//
// Assigning .value alone is not enough: React and Vue track their own state and
// ignore a value that changed without the corresponding input event.
func (c *Client) Fill(ctx context.Context, selector, value string) error {
	expr := deepPrelude + fmt.Sprintf(`(() => {
      const f = __find(%q);
      if (f.bad) return 'bad-selector';
      if (!f.el) return 'not-found';
      const el = f.el;
      const tag = el.tagName;
      if (tag !== 'INPUT' && tag !== 'TEXTAREA' && !el.isContentEditable)
        return 'not-a-field:' + tag.toLowerCase();
      if (el.disabled) return 'disabled';
      if (el.readOnly) return 'readonly';
      const setter = Object.getOwnPropertyDescriptor(
        tag === 'TEXTAREA' ? window.HTMLTextAreaElement.prototype
                           : window.HTMLInputElement.prototype, 'value').set;
      setter.call(el, %q);
      el.dispatchEvent(new Event('input', {bubbles:true}));
      el.dispatchEvent(new Event('change', {bubbles:true}));
      // Read it back. A field that silently rejects or reformats what was typed —
      // a masked input, a component that resets on blur — used to report a clean
      // success, and the first anyone knew of it was a form that would not submit.
      return 'ok:' + String(el.value == null ? '' : el.value);
    })()`, selector, value)
	res, err := c.EvalString(ctx, expr)
	if err != nil {
		return err
	}
	switch {
	case res == "bad-selector":
		return fmt.Errorf("%q is not a valid CSS selector; take one from browser_inspect", selector)
	case res == "not-found":
		return fmt.Errorf("no field matches %q anywhere, including iframes and shadow DOM", selector)
	case res == "disabled":
		return fmt.Errorf("%q is disabled — something has to enable it first", selector)
	case res == "readonly":
		return fmt.Errorf("%q is read-only", selector)
	case strings.HasPrefix(res, "not-a-field:"):
		return fmt.Errorf("%q is a <%s>, not a text field", selector, strings.TrimPrefix(res, "not-a-field:"))
	}
	if got := strings.TrimPrefix(res, "ok:"); got != value {
		return fmt.Errorf("typed into %q but it now reads %q, not %q — the field rewrote or rejected the value",
			selector, got, value)
	}
	return nil
}

// Links lists what she can click, across shadow roots. It is deliberately more
// than <a href>: on a component app the thing to click is often a <button>, a
// role=menuitem, or a d2l-link web component with no href at all. Listing those
// by their visible text is what makes browser_click_text usable — she reads the
// label here, then clicks that exact label.
func (c *Client) Links(ctx context.Context, limit int) (string, error) {
	expr := deepPrelude + fmt.Sprintf(`(() => {
      const LIMIT = %d;
      const out = [], seen = new Set();
      const rec = (root) => {
        let list;
        try { list = root.querySelectorAll('*'); } catch (e) { return; }
        for (const el of list) {
          const sub = __descend(el);
          if (sub) rec(sub);
          if (out.length >= LIMIT) return;
          const tag = el.tagName.toLowerCase();
          const role = ((el.getAttribute && el.getAttribute('role')) || '').toLowerCase();
          const href = el.getAttribute && el.getAttribute('href');
          const clickable = tag === 'a' || tag === 'button' ||
            ['button','link','menuitem','tab','option'].includes(role) ||
            (tag.indexOf('-') > -1 && /(link|button|menu-item|item|tab)/.test(tag));
          if (!clickable || !__vis(el)) continue;
          const t = ((el.innerText || el.textContent) || '').trim().replace(/\s+/g, ' ');
          if (!t) continue;
          const key = t + '|' + (href || '');
          if (seen.has(key)) continue;
          seen.add(key);
          out.push(href ? (t + ' -> ' + el.href) : ('[' + tag + '] ' + t));
        }
      };
      rec(document);
      return out.slice(0, LIMIT).join('\n');
    })()`, limit)
	return c.EvalString(ctx, expr)
}

// Screenshot captures the viewport as base64 PNG.
func (c *Client) Screenshot(ctx context.Context) (string, error) {
	res, err := c.Call(ctx, "Page.captureScreenshot", map[string]any{"format": "png"})
	if err != nil {
		return "", err
	}
	var out struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", err
	}
	return out.Data, nil
}
