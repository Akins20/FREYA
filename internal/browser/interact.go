package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Interacting with a page the way a person does.
//
// # Synthetic events are not always enough
//
// The simple approach is to call element.click() from JavaScript, and for most
// pages it works. It fails on the pages where failure is most expensive: a
// synthetic click carries isTrusted=false, dispatches no pointer or mouse
// sequence, and never moves a cursor. Sites with drag handles, custom
// dropdowns, hover menus, or bot detection all notice.
//
// So the important interactions go through the browser's own input pipeline —
// Input.dispatchMouseEvent and Input.dispatchKeyEvent — which produce events
// indistinguishable from a human's, because as far as the renderer is
// concerned that is what they are. Finding the element still happens in
// JavaScript, because that is where the DOM is; only the event delivery moves.
//
// # Native dropdowns cannot be clicked at all
//
// A <select> opens an operating-system menu that lives outside the page. No
// amount of event dispatch reaches it. The only way to change one is to set its
// value and fire change, which is what SelectOption does — and why it is a
// separate method rather than a special case of Click.

// point is a viewport coordinate.
type point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// locate finds an element's clickable centre, scrolling it into view.
//
// The returned coordinates are viewport-relative because that is what the input
// domain expects — dispatching at document coordinates on a scrolled page
// clicks whatever happens to be at that spot instead.
func (c *Client) locate(ctx context.Context, selector string) (point, error) {
	expr := deepPrelude + fmt.Sprintf(`(() => {
      const sel = %q;
      let el = null, bad = false;
      const rec = (root) => {
        if (el || bad) return;
        let m; try { m = root.querySelector(sel); } catch (e) { bad = true; return; }
        if (m) { el = m; return; }
        for (const x of root.querySelectorAll('*')) { const s = __descend(x); if (s) rec(s); if (el || bad) return; }
      };
      rec(document);
      if (bad) return JSON.stringify({error:'bad-selector'});
      if (!el) return JSON.stringify({error:'not-found'});
      el.scrollIntoView({block:'center', inline:'center'});
      const r = el.getBoundingClientRect();
      const win = (el.ownerDocument && el.ownerDocument.defaultView) || window;
      const style = win.getComputedStyle(el);
      if ((r.width === 0 && r.height === 0) || style.visibility === 'hidden' || style.display === 'none')
        return JSON.stringify({error:'not-visible'});
      // getBoundingClientRect is relative to the element's OWN document. If it
      // lives in an iframe, add each ancestor frame's offset so the coordinates
      // are in the top window's viewport — which is the space CDP Input clicks in.
      let x = r.left + r.width/2, y = r.top + r.height/2;
      let w = win;
      while (w && w.frameElement) {
        const fr = w.frameElement.getBoundingClientRect();
        x += fr.left; y += fr.top;
        w = w.parent;
      }
      return JSON.stringify({x: x, y: y});
    })()`, selector)

	raw, err := c.EvalString(ctx, expr)
	if err != nil {
		return point{}, err
	}

	var result struct {
		X, Y  float64
		Error string
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return point{}, fmt.Errorf("locating %q: %w", selector, err)
	}
	switch result.Error {
	case "not-found":
		return point{}, fmt.Errorf("no element matches %q anywhere, including iframes and shadow DOM; "+
			"if you know what it says, use browser_click_text", selector)
	case "bad-selector":
		return point{}, fmt.Errorf("%q is not a valid CSS selector (no jQuery :contains()); use browser_click_text", selector)
	case "not-visible":
		return point{}, fmt.Errorf("%q exists but is not visible", selector)
	}
	return point{X: result.X, Y: result.Y}, nil
}

// ClickReal clicks using genuine mouse events.
//
// A short pause separates press from release. Some handlers measure that
// interval, and zero-duration clicks read as automation.
func (c *Client) ClickReal(ctx context.Context, selector string) error {
	p, err := c.locate(ctx, selector)
	if err != nil {
		return err
	}

	// Move first: hover handlers often build the thing being clicked.
	if _, err := c.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseMoved", "x": p.X, "y": p.Y,
	}); err != nil {
		return err
	}

	for _, phase := range []string{"mousePressed", "mouseReleased"} {
		if _, err := c.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type": phase, "x": p.X, "y": p.Y,
			"button": "left", "clickCount": 1,
		}); err != nil {
			return err
		}
		if phase == "mousePressed" {
			sleepCtx(ctx, 45*time.Millisecond)
		}
	}
	// Let whatever the click triggered load before the caller reads the page.
	c.WaitStable(ctx, 5*time.Second)
	return nil
}

// Hover moves the pointer over an element without clicking.
func (c *Client) Hover(ctx context.Context, selector string) error {
	p, err := c.locate(ctx, selector)
	if err != nil {
		return err
	}
	_, err = c.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseMoved", "x": p.X, "y": p.Y,
	})
	// Menus that open on hover need a moment to render before anything can be
	// done with them.
	sleepCtx(ctx, 250*time.Millisecond)
	return err
}

// keyCodes maps names to the virtual key codes the input domain expects.
//
// The codes are the Windows values, which Chrome uses on every platform.
var keyCodes = map[string]struct {
	key  string
	code string
	vk   int
	text string
}{
	"enter":     {"Enter", "Enter", 13, "\r"},
	"return":    {"Enter", "Enter", 13, "\r"},
	"tab":       {"Tab", "Tab", 9, "\t"},
	"escape":    {"Escape", "Escape", 27, ""},
	"esc":       {"Escape", "Escape", 27, ""},
	"backspace": {"Backspace", "Backspace", 8, ""},
	"delete":    {"Delete", "Delete", 46, ""},
	"space":     {" ", "Space", 32, " "},
	"up":        {"ArrowUp", "ArrowUp", 38, ""},
	"down":      {"ArrowDown", "ArrowDown", 40, ""},
	"left":      {"ArrowLeft", "ArrowLeft", 37, ""},
	"right":     {"ArrowRight", "ArrowRight", 39, ""},
	"home":      {"Home", "Home", 36, ""},
	"end":       {"End", "End", 35, ""},
	"pageup":    {"PageUp", "PageUp", 33, ""},
	"pagedown":  {"PageDown", "PageDown", 34, ""},
}

// modifierBits are the flags the input domain uses for held keys.
var modifierBits = map[string]int{
	"alt": 1, "ctrl": 2, "control": 2, "meta": 4, "cmd": 4, "shift": 8,
}

// PressKey sends a named key, optionally with modifiers.
//
// The name may carry modifiers joined by '+', as in "ctrl+a" or "shift+tab".
func (c *Client) PressKey(ctx context.Context, name string) error {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(name)), "+")
	mods := 0
	keyName := parts[len(parts)-1]
	for _, p := range parts[:len(parts)-1] {
		bit, ok := modifierBits[strings.TrimSpace(p)]
		if !ok {
			return fmt.Errorf("unknown modifier %q", p)
		}
		mods |= bit
	}

	spec, ok := keyCodes[keyName]
	if !ok {
		// A single character is a legitimate key too.
		if len([]rune(keyName)) == 1 {
			r := []rune(keyName)[0]
			spec = struct {
				key  string
				code string
				vk   int
				text string
			}{string(r), "Key" + strings.ToUpper(string(r)), int(r), string(r)}
		} else {
			return fmt.Errorf("unknown key %q — try enter, tab, escape, up, down, or a single character", keyName)
		}
	}

	for _, kind := range []string{"keyDown", "keyUp"} {
		params := map[string]any{
			"type": kind, "key": spec.key, "code": spec.code,
			"windowsVirtualKeyCode": spec.vk, "nativeVirtualKeyCode": spec.vk,
			"modifiers": mods,
		}
		// Text belongs only on the press, and only when the key produces a
		// character — attaching it to a modifier combination types a stray
		// letter alongside the shortcut.
		if kind == "keyDown" && spec.text != "" && mods&^8 == 0 {
			params["text"] = spec.text
			params["unmodifiedText"] = spec.text
		}
		if _, err := c.Call(ctx, "Input.dispatchKeyEvent", params); err != nil {
			return err
		}
	}
	return nil
}

// TypeText types into the focused element with real keystrokes.
//
// Slower than setting .value, and worth it where a page validates as you type,
// autocompletes, or enables its submit button on the first keypress.
func (c *Client) TypeText(ctx context.Context, selector, text string) error {
	if selector != "" {
		if err := c.Focus(ctx, selector); err != nil {
			return err
		}
	}
	// insertText delivers the whole string through the same path as an IME,
	// which every input handler understands and which is far faster than a key
	// event per character.
	_, err := c.Call(ctx, "Input.insertText", map[string]any{"text": text})
	return err
}

// Focus puts the caret in an element.
func (c *Client) Focus(ctx context.Context, selector string) error {
	expr := fmt.Sprintf(`(() => {
      const el = document.querySelector(%q);
      if (!el) return 'not-found';
      el.scrollIntoView({block:'center'});
      el.focus();
      return 'ok';
    })()`, selector)
	res, err := c.EvalString(ctx, expr)
	if err != nil {
		return err
	}
	if res == "not-found" {
		return fmt.Errorf("no element matches %q", selector)
	}
	return nil
}

// SelectOption chooses from a <select> dropdown.
//
// Matching is by visible label first, then by value, then by index, because a
// person asking for "Nigeria" knows the label and not the country code the page
// happens to use for it.
func (c *Client) SelectOption(ctx context.Context, selector, choice string) (string, error) {
	expr := fmt.Sprintf(`(() => {
      const el = document.querySelector(%q);
      if (!el) return JSON.stringify({error:'not-found'});
      if (el.tagName !== 'SELECT')
        return JSON.stringify({error:'not-a-select', tag: el.tagName});

      const want = %q.trim().toLowerCase();
      const opts = Array.from(el.options);
      let idx = opts.findIndex(o => (o.text||'').trim().toLowerCase() === want);
      if (idx < 0) idx = opts.findIndex(o => (o.value||'').trim().toLowerCase() === want);
      if (idx < 0) idx = opts.findIndex(o => (o.text||'').trim().toLowerCase().includes(want));
      if (idx < 0) {
        const n = parseInt(want, 10);
        if (!isNaN(n) && n >= 0 && n < opts.length) idx = n;
      }
      if (idx < 0)
        return JSON.stringify({error:'no-option',
          available: opts.slice(0,40).map(o => (o.text||'').trim()).filter(Boolean)});

      el.selectedIndex = idx;
      el.dispatchEvent(new Event('input',  {bubbles:true}));
      el.dispatchEvent(new Event('change', {bubbles:true}));
      return JSON.stringify({chosen: (opts[idx].text||'').trim(), value: opts[idx].value});
    })()`, selector, choice)

	raw, err := c.EvalString(ctx, expr)
	if err != nil {
		return "", err
	}
	var result struct {
		Error     string   `json:"error"`
		Tag       string   `json:"tag"`
		Chosen    string   `json:"chosen"`
		Value     string   `json:"value"`
		Available []string `json:"available"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return "", err
	}

	switch result.Error {
	case "not-found":
		return "", fmt.Errorf("no element matches %q", selector)
	case "not-a-select":
		return "", fmt.Errorf("%q is a <%s>, not a dropdown — a custom dropdown is clicked, not selected",
			selector, strings.ToLower(result.Tag))
	case "no-option":
		// Listing what was actually there turns a dead end into a next step.
		if len(result.Available) > 0 {
			return "", fmt.Errorf("no option matching %q. Available: %s",
				choice, strings.Join(result.Available, ", "))
		}
		return "", fmt.Errorf("no option matching %q, and the dropdown is empty", choice)
	}
	return fmt.Sprintf("selected %q (value %q)", result.Chosen, result.Value), nil
}

// SetChecked ticks or unticks a checkbox or radio button.
//
// Clicking is preferred over setting .checked because a radio group's other
// members must be updated too, and only a click does that.
func (c *Client) SetChecked(ctx context.Context, selector string, want bool) (string, error) {
	expr := fmt.Sprintf(`(() => {
      const el = document.querySelector(%q);
      if (!el) return 'not-found';
      if (el.type !== 'checkbox' && el.type !== 'radio') return 'wrong-type:' + el.type;
      el.scrollIntoView({block:'center'});
      if (el.checked === %t) return 'already';
      el.click();
      return el.checked ? 'checked' : 'unchecked';
    })()`, selector, want)

	res, err := c.EvalString(ctx, expr)
	if err != nil {
		return "", err
	}
	switch {
	case res == "not-found":
		return "", fmt.Errorf("no element matches %q", selector)
	case strings.HasPrefix(res, "wrong-type:"):
		return "", fmt.Errorf("%q is a %s input, not a checkbox or radio",
			selector, strings.TrimPrefix(res, "wrong-type:"))
	case res == "already":
		return fmt.Sprintf("%q was already %s", selector, checkedWord(want)), nil
	}
	return fmt.Sprintf("%q is now %s", selector, res), nil
}

func checkedWord(b bool) string {
	if b {
		return "checked"
	}
	return "unchecked"
}

// Scroll moves the page or an element by a number of pixels.
//
// Positive is down and right. A selector scrolls that element's own overflow,
// which is how most modern feeds and modals work.
func (c *Client) Scroll(ctx context.Context, selector string, dx, dy float64) error {
	if selector == "" {
		_, err := c.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type": "mouseWheel", "x": 10, "y": 10,
			"deltaX": dx, "deltaY": dy,
		})
		if err == nil {
			// Smooth-scrolling pages animate; reading immediately catches the
			// old position.
			sleepCtx(ctx, 350*time.Millisecond)
		}
		return err
	}

	expr := fmt.Sprintf(`(() => {
      const el = document.querySelector(%q);
      if (!el) return 'not-found';
      el.scrollBy(%f, %f);
      return 'ok';
    })()`, selector, dx, dy)
	res, err := c.EvalString(ctx, expr)
	if err != nil {
		return err
	}
	if res == "not-found" {
		return fmt.Errorf("no element matches %q", selector)
	}
	sleepCtx(ctx, 250*time.Millisecond)
	return nil
}

// ScrollTo jumps to a named position: top, bottom, or a selector.
func (c *Client) ScrollTo(ctx context.Context, where string) error {
	switch strings.ToLower(strings.TrimSpace(where)) {
	case "top":
		_, err := c.EvalString(ctx, `(() => { window.scrollTo(0,0); return 'ok'; })()`)
		sleepCtx(ctx, 300*time.Millisecond)
		return err
	case "bottom":
		// Not a single jump. Lazy lists load on the scroll event, and infinite
		// scroll loads more each time you reach the bottom — so step down
		// repeatedly, firing the scroll event explicitly (some listeners miss a
		// programmatic scrollTo), and keep going until the page stops growing.
		// That reveals content that a one-shot jump to the bottom leaves unloaded.
		_, err := c.EvalString(ctx, `(async () => {
          let last = -1;
          for (let i = 0; i < 25; i++) {
            window.scrollTo(0, document.body.scrollHeight);
            window.dispatchEvent(new Event('scroll'));
            await new Promise(r => setTimeout(r, 250));
            const h = document.body.scrollHeight;
            if (h === last) break;   // height stopped growing: fully loaded
            last = h;
          }
          return 'ok';
        })()`)
		c.WaitStable(ctx, 3*time.Second)
		return err
	default:
		expr := fmt.Sprintf(`(() => {
          const el = document.querySelector(%q);
          if (!el) return 'not-found';
          el.scrollIntoView({block:'center', behavior:'instant'});
          return 'ok';
        })()`, where)
		res, err := c.EvalString(ctx, expr)
		if err != nil {
			return err
		}
		if res == "not-found" {
			return fmt.Errorf("no element matches %q", where)
		}
		sleepCtx(ctx, 250*time.Millisecond)
		return nil
	}
}

// WaitFor blocks until an element appears, or the timeout elapses.
//
// Polling rather than using DOM events is the right trade here: the CDP event
// stream for a busy page is enormous, and a poll every 150ms costs one cheap
// evaluation while remaining well inside human reaction time.
func (c *Client) WaitFor(ctx context.Context, selector string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	deadline := time.Now().Add(timeout)
	expr := fmt.Sprintf(`(() => {
      const el = document.querySelector(%q);
      if (!el) return 'no';
      const r = el.getBoundingClientRect();
      return (r.width > 0 || r.height > 0) ? 'yes' : 'no';
    })()`, selector)

	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		res, err := c.EvalString(ctx, expr)
		if err == nil && res == "yes" {
			return nil
		}
		sleepCtx(ctx, 150*time.Millisecond)
	}
	return fmt.Errorf("%q did not appear within %s", selector, timeout)
}

// WaitForText blocks until the page contains a phrase.
func (c *Client) WaitForText(ctx context.Context, phrase string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	deadline := time.Now().Add(timeout)
	expr := fmt.Sprintf(`(document.body && document.body.innerText || '').toLowerCase().includes(%q)`,
		strings.ToLower(phrase))

	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if res, err := c.EvalString(ctx, expr); err == nil && res == "true" {
			return nil
		}
		sleepCtx(ctx, 200*time.Millisecond)
	}
	return fmt.Errorf("the phrase %q did not appear within %s", phrase, timeout)
}

// Back goes to the previous page.
func (c *Client) Back(ctx context.Context) error {
	_, err := c.EvalString(ctx, `(() => { history.back(); return 'ok'; })()`)
	sleepCtx(ctx, 600*time.Millisecond)
	return err
}

// Forward goes to the next page.
func (c *Client) Forward(ctx context.Context) error {
	_, err := c.EvalString(ctx, `(() => { history.forward(); return 'ok'; })()`)
	sleepCtx(ctx, 600*time.Millisecond)
	return err
}

// Reload reloads the page.
func (c *Client) Reload(ctx context.Context) error {
	_, err := c.Call(ctx, "Page.reload", map[string]any{})
	if err != nil {
		return err
	}
	return c.waitReady(ctx)
}

// Submit submits the form containing an element.
func (c *Client) Submit(ctx context.Context, selector string) error {
	if selector == "" {
		selector = "form"
	}
	expr := fmt.Sprintf(`(() => {
      let el = document.querySelector(%q);
      if (!el) return 'not-found';
      const form = el.tagName === 'FORM' ? el : el.closest('form');
      if (!form) return 'no-form';
      // requestSubmit runs validation and fires submit handlers; form.submit()
      // skips both, which silently breaks pages that validate on submit.
      if (form.requestSubmit) form.requestSubmit(); else form.submit();
      return 'ok';
    })()`, selector)

	res, err := c.EvalString(ctx, expr)
	if err != nil {
		return err
	}
	switch res {
	case "not-found":
		return fmt.Errorf("no element matches %q", selector)
	case "no-form":
		return fmt.Errorf("%q is not inside a form", selector)
	}
	sleepCtx(ctx, 800*time.Millisecond)
	return nil
}

// Inspect describes the interactive elements on a page.
//
// # Why this is the most important method here
//
// Every other method needs a CSS selector, and a selector cannot be guessed
// from a page's text. Without a way to ask "what is on this page and what do I
// call it", the model has to invent selectors and check whether they worked,
// which is slow and fails in ways that look like the page being broken.
//
// The output is deliberately compact. A real page has hundreds of elements and
// listing them all would swamp the context that has to hold the rest of the
// conversation.
func (c *Client) Inspect(ctx context.Context, limit int) (string, error) {
	if limit <= 0 {
		limit = 60
	}
	expr := deepPrelude + fmt.Sprintf(`(() => {
      const out = [];
      const seen = new Set();

      // Gather every document to search: the top page, each same-origin iframe,
      // and every open shadow root, so radios and buttons that live inside the
      // quiz frame are listed like any other. Without this the quiz looked empty.
      const roots = [];
      const collect = (root) => {
        roots.push(root);
        let list; try { list = root.querySelectorAll('*'); } catch (e) { return; }
        for (const x of list) { const s = __descend(x); if (s) collect(s); }
      };
      collect(document);
      const inFrame = (el) => { try { return el.ownerDocument !== document; } catch (e) { return false; } };
      const qsa = (q) => { let r = []; for (const root of roots) { try { r = r.concat(Array.from(root.querySelectorAll(q))); } catch (e) {} } return r; };

      // A stable, short way to refer back to an element. Prefer whatever the
      // page already gives us over a generated path.
      const sel = el => {
        const doc = el.ownerDocument || document;
        if (el.id && !/^[0-9]/.test(el.id) && doc.querySelectorAll('#'+CSS.escape(el.id)).length === 1)
          return '#' + CSS.escape(el.id);
        for (const a of ['name','data-testid','aria-label','placeholder']) {
          const v = el.getAttribute && el.getAttribute(a);
          if (v) {
            const s = el.tagName.toLowerCase() + '[' + a + '=' + JSON.stringify(v) + ']';
            try { if (doc.querySelectorAll(s).length === 1) return s; } catch (e) {}
          }
        }
        const p = el.parentElement;
        if (!p) return el.tagName.toLowerCase();
        const same = Array.from(p.children).filter(x => x.tagName === el.tagName);
        const i = same.indexOf(el) + 1;
        const parentSel = p.id ? '#' + CSS.escape(p.id) : p.tagName.toLowerCase();
        return parentSel + ' > ' + el.tagName.toLowerCase() + ':nth-of-type(' + i + ')';
      };

      const label = el => {
        if (el.getAttribute && el.getAttribute('aria-label')) return el.getAttribute('aria-label');
        if (el.labels && el.labels.length) return (el.labels[0].innerText||'').trim();
        if (el.placeholder) return el.placeholder;
        if (el.name) return el.name;
        if (el.title) return el.title;
        return (el.innerText||el.value||'').trim().replace(/\s+/g,' ').slice(0,60);
      };

      const add = (el, kind, extra) => {
        if (out.length >= %d || !__vis(el)) return;
        const s = sel(el);
        const key = (inFrame(el) ? 'f:' : '') + s + '|' + label(el);
        if (seen.has(key)) return;
        seen.add(key);
        out.push(kind + ' | ' + s + ' | ' + (label(el)||'(no label)') + (extra||'') + (inFrame(el) ? ' | in iframe — click by text' : ''));
      };

      qsa('input,textarea').forEach(el => {
        const t = (el.type||'text').toLowerCase();
        if (t === 'hidden') return;
        let extra = '';
        if (t === 'checkbox' || t === 'radio') extra = el.checked ? ' | CHECKED' : ' | unchecked';
        else if (el.value) extra = ' | currently: ' + String(el.value).slice(0,30);
        if (el.required) extra += ' | required';
        add(el, t === 'password' ? 'password' : 'field(' + t + ')', extra);
      });

      qsa('select').forEach(el => {
        const opts = Array.from(el.options).slice(0,12).map(o => (o.text||'').trim()).filter(Boolean);
        add(el, 'dropdown', ' | options: ' + opts.join(', ') +
            (el.options.length > 12 ? ' …+' + (el.options.length-12) : ''));
      });

      qsa('button,[role=button],[role=radio],[role=checkbox],input[type=submit],input[type=button],a[href]')
        .forEach(el => add(el, el.tagName.toLowerCase() === 'a' ? 'link' : 'button', el.disabled ? ' | DISABLED' : ''));

      return out.join('\n');
    })()`, limit)

	res, err := c.EvalString(ctx, expr)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(res) == "" {
		return "No interactive elements found — the page may still be loading.", nil
	}
	return "kind | selector | label\n" + res, nil
}

// ReadElement returns the text of one element.
func (c *Client) ReadElement(ctx context.Context, selector string) (string, error) {
	expr := fmt.Sprintf(`(() => {
      const el = document.querySelector(%q);
      if (!el) return '\x00not-found';
      return (el.innerText || el.value || el.textContent || '').trim();
    })()`, selector)
	res, err := c.EvalString(ctx, expr)
	if err != nil {
		return "", err
	}
	if res == "\x00not-found" {
		return "", fmt.Errorf("no element matches %q", selector)
	}
	return res, nil
}

// sleepCtx pauses, but gives up if the context ends.
func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}
