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
	return c.waitReady(ctx)
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

// readableText extracts what a person would read, skipping the furniture.
const readableText = `(() => {
  const drop = ['script','style','noscript','svg','nav','footer','header','aside','form'];
  const doc = document.cloneNode(true);
  drop.forEach(t => doc.querySelectorAll(t).forEach(e => e.remove()));
  const main = doc.querySelector('main,article,[role=main]') || doc.body;
  if (!main) return '';
  return main.innerText.replace(/\n{3,}/g, '\n\n').trim();
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

// Click finds an element by CSS selector and clicks it.
func (c *Client) Click(ctx context.Context, selector string) error {
	expr := fmt.Sprintf(`(() => {
      const el = document.querySelector(%q);
      if (!el) return 'not-found';
      el.scrollIntoView({block:'center'});
      el.click();
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

// Fill sets a form field's value and fires the events frameworks listen for.
//
// Assigning .value alone is not enough: React and Vue track their own state and
// ignore a value that changed without the corresponding input event.
func (c *Client) Fill(ctx context.Context, selector, value string) error {
	expr := fmt.Sprintf(`(() => {
      const el = document.querySelector(%q);
      if (!el) return 'not-found';
      const setter = Object.getOwnPropertyDescriptor(
        el.tagName === 'TEXTAREA' ? window.HTMLTextAreaElement.prototype
                                  : window.HTMLInputElement.prototype, 'value').set;
      setter.call(el, %q);
      el.dispatchEvent(new Event('input', {bubbles:true}));
      el.dispatchEvent(new Event('change', {bubbles:true}));
      return 'ok';
    })()`, selector, value)
	res, err := c.EvalString(ctx, expr)
	if err != nil {
		return err
	}
	if res == "not-found" {
		return fmt.Errorf("no field matches %q", selector)
	}
	return nil
}

// Links lists the page's links.
func (c *Client) Links(ctx context.Context, limit int) (string, error) {
	expr := fmt.Sprintf(`(() => {
      const out = [];
      document.querySelectorAll('a[href]').forEach(a => {
        const t = (a.innerText||'').trim().replace(/\s+/g,' ');
        if (t && out.length < %d) out.push(t + ' -> ' + a.href);
      });
      return out.join('\n');
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
