package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Knowing what the browser itself has done to the page.
//
// # The failures this exists to name
//
// Chrome intervenes constantly, and every intervention it makes looks, through
// the DOM, exactly like an ordinary page:
//
//   - A certificate error or a "Deceptive site ahead" warning is a real page
//     with real text. Read it and you conclude the site now says something about
//     privacy, and you start looking for the login form on it.
//   - A blocked popup is nothing at all. The click succeeded, the page did not
//     change, and the window that should have opened never existed.
//   - A page that is still loading looks like a page that has finished and is
//     empty.
//
// None of these is visible as a failure. Each one produces a confident, wrong
// conclusion, and the round budget then goes on acting against it.

// PageState is what the browser has to say about the page, as opposed to what
// the page has to say about itself.
type PageState struct {
	URL   string
	Title string
	// Interstitial names a browser warning standing in for the real page, empty
	// when the page is genuinely the site's own.
	Interstitial string
	// Loading reports that navigation has not settled.
	Loading bool
	// Errors are console errors, newest last. A page that threw is a page whose
	// controls may simply not be wired up.
	Errors []string
}

// Describe renders the parts worth telling her about, empty when the page is
// unremarkable.
func (s PageState) Describe() string {
	var lines []string
	if s.Interstitial != "" {
		lines = append(lines, "  · "+s.Interstitial)
	}
	if s.Loading {
		lines = append(lines, "  · the page is still loading — read it again in a moment "+
			"rather than concluding it is empty")
	}
	for _, e := range s.Errors {
		lines = append(lines, "  · the page reported an error: "+clipText(e, 160))
	}
	if len(lines) == 0 {
		return ""
	}
	return "\nAbout the page itself:\n" + strings.Join(lines, "\n")
}

// interstitialPatterns are the browser's own error pages.
//
// Matched on the URL scheme and on the distinctive text, because these pages
// have no markup in common with each other and no site puts these words on a
// working page.
var interstitialPatterns = []struct {
	needle string
	says   string
}{
	{"your connection is not private",
		"this is CHROME'S OWN certificate warning, not the site. The site is not " +
			"reachable securely — do not try to sign in here, and tell the user"},
	{"deceptive site ahead",
		"this is CHROME'S OWN safe-browsing warning, not the site. Do not click through it"},
	{"dangerous site",
		"this is CHROME'S OWN safe-browsing warning, not the site. Do not click through it"},
	{"this site can’t be reached",
		"the site did not respond at all — check the address, or the network"},
	{"this site can't be reached",
		"the site did not respond at all — check the address, or the network"},
	{"err_connection", "the connection failed; this is a browser error page, not the site"},
	{"err_name_not_resolved", "that host does not exist; the address is probably wrong"},
	{"err_cert", "the site's certificate was rejected; this is a browser error page"},
	{"no internet", "the browser reports there is no connection"},
}

// State reports what the browser has done to the page.
//
// Cheap enough to attach to every read: one script, no round trips beyond it.
func (c *Client) State(ctx context.Context) PageState {
	var s PageState
	s.URL, _ = c.URL(ctx)
	s.Title, _ = c.Title(ctx)

	raw, err := c.EvalString(ctx, `JSON.stringify({
      ready: document.readyState,
      text: (document.body ? (document.body.innerText || '') : '').slice(0, 800)
    })`)
	if err != nil {
		return s
	}

	var probe struct {
		Ready string `json:"ready"`
		Text  string `json:"text"`
	}
	if json.Unmarshal([]byte(raw), &probe) != nil {
		return s
	}
	s.Loading = probe.Ready != "complete"

	hay := strings.ToLower(probe.Text + " " + s.Title + " " + s.URL)
	for _, p := range interstitialPatterns {
		if strings.Contains(hay, p.needle) {
			s.Interstitial = fmt.Sprintf("what you are looking at is not the site — %s", p.says)
			break
		}
	}
	// chrome:// pages are the browser's own UI, and reading one as though it were
	// a site is how she ends up describing the settings page as the portal.
	if strings.HasPrefix(s.URL, "chrome-error://") {
		s.Interstitial = "this is a browser error page, not the site"
	}
	return s
}

// FindInPage counts and locates a phrase without reading the whole page back.
//
// Reading a long page to answer "is X on it" costs thousands of tokens and often
// truncates before reaching the answer, so the answer comes back "no" when it is
// really "not in the part I read". This asks the page directly, and reports
// where the matches are so she can act on one.
func (c *Client) FindInPage(ctx context.Context, phrase string) (int, []string, error) {
	expr := deepPrelude + fmt.Sprintf(`(() => {
      const needle = %q.toLowerCase();
      if (!needle) return JSON.stringify({n:0, where:[]});
      const where = [];
      let n = 0;
      const consider = (el) => {
        if (!el || el.nodeType !== 1 || !__vis(el)) return;
        // Only the element that directly holds the text, so a match is not
        // reported once for every ancestor as well.
        let own = '';
        for (const node of el.childNodes) {
          if (node.nodeType === 3) own += node.textContent;
        }
        if (!own.toLowerCase().includes(needle)) return;
        n++;
        if (where.length < 12) {
          const label = (el.innerText || own).trim().replace(/\s+/g, ' ');
          where.push(el.tagName.toLowerCase() + ': ' + label.slice(0, 120));
        }
      };
      const rec = (root) => {
        let list;
        try { list = root.querySelectorAll('*'); } catch (e) { return; }
        for (const x of list) {
          consider(x);
          const sub = __descend(x);
          if (sub) rec(sub);
        }
      };
      rec(document);
      return JSON.stringify({n:n, where:where});
    })()`, phrase)

	raw, err := c.EvalString(ctx, expr)
	if err != nil {
		return 0, nil, err
	}
	var res struct {
		N     int      `json:"n"`
		Where []string `json:"where"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return 0, nil, err
	}
	return res.N, res.Where, nil
}

// PrintToPDF renders the page as a PDF, which is how "save this" is answered for
// something that is not a file to begin with — a receipt, a confirmation, an
// article behind a session.
func (c *Client) PrintToPDF(ctx context.Context) ([]byte, error) {
	raw, err := c.Call(ctx, "Page.printToPDF", map[string]any{
		"printBackground": true, "preferCSSPageSize": true,
	})
	if err != nil {
		return nil, err
	}
	var res struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(res.Data)
}

// proceedWords are the controls that push past a browser safety warning.
//
// Chrome deliberately makes these awkward — hidden behind "Advanced", worded to
// give pause — because getting past them is almost never what someone means to
// do. An assistant clicking them on the user's behalf is worse than a person
// doing it: the person at least saw the warning.
var proceedWords = []string{
	"proceed to", "continue to", "unsafe", "advanced",
	"accept the risk", "i understand the risks", "visit this unsafe site",
	"details", "back to safety",
}

// RefuseUnsafeProceed reports why a click must not happen, or empty when it is
// fine.
//
// The measured case: asked what to do on "Your connection is not private" with
// the user waiting to sign in, she said she would click Advanced and proceed.
// The page had already been flagged to her as a browser warning; she read that,
// understood it, and decided to go through anyway. Guidance was not enough, so
// this is a refusal rather than a note.
func RefuseUnsafeProceed(s PageState, target string) string {
	if s.Interstitial == "" {
		return ""
	}
	low := strings.ToLower(strings.TrimSpace(target))
	if low == "" {
		return ""
	}
	for _, w := range proceedWords {
		if !strings.Contains(low, w) {
			continue
		}
		if strings.Contains(low, "back to safety") {
			return "" // leaving is always allowed
		}
		return fmt.Sprintf("refusing to click %q: this is a browser SAFETY WARNING, not the "+
			"site, and that control exists to push past it. The connection is not "+
			"trustworthy, so anything typed here — a password above all — would go "+
			"somewhere it should not.\n\nDo not look for another way through. Tell the "+
			"user what the warning says and let them decide; if they gave you the "+
			"address, check it is the one they meant.", target)
	}
	return ""
}
