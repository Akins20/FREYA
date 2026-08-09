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
	// when the page is genuinely the site's own. Matched on prose, so it is a
	// LOOSE signal — good enough to warn her, not good enough to refuse on.
	Interstitial string
	// BrowserPage is the same claim made with certainty: this document was served
	// by Chrome itself, not by any site.
	//
	// The distinction is load-bearing because the two carry different costs.
	// Interstitial matches page text for phrases like "no internet" and
	// "err_connection", and a developer's browser is full of pages that legitimately
	// contain those words — a Stack Overflow answer about ERR_CONNECTION_REFUSED
	// is exactly the page she reads while debugging. Warning her about one of
	// those wastes a sentence. REFUSING every click on one would break the task,
	// so the blanket refusal keys on this instead: Chrome's own error and
	// security interstitials are built from element IDs no site uses, and served
	// from a scheme no site can claim.
	BrowserPage bool
	// Loading reports that navigation has not settled.
	Loading bool
	// Rendering reports that the document is COMPLETE but the page is still
	// putting itself together — skeleton bars, spinners, aria-busy regions.
	//
	// Loading and this are different moments, and only the second one is the
	// failure the web playbook warns about. readyState flips to "complete" as
	// soon as the document and its subresources are in, which on any modern
	// application is long before the data arrives: the shell is there, the rows
	// are grey placeholders. Reading then gives her a page that is genuinely
	// complete and genuinely empty, and "it's showing empty" gets reported as the
	// answer instead of as looking too early. That instruction was in prose and
	// nothing checked it.
	Rendering bool
	// NeedsHuman reports a wall that exists specifically to confirm a person is
	// present — a Cloudflare interstitial, a Turnstile or reCAPTCHA widget, a
	// "verify you are human" gate.
	//
	// Named separately from Loading and Interstitial because the remedy is
	// different in kind. Those two are waits and warnings; this one cannot be
	// solved by anything she does, at any speed, in any number of rounds. It is
	// the class of problem where the correct move is to stop and fetch the user,
	// and until it was detected she burned rounds on browser_wait timeouts and
	// then reported a failure that was not hers.
	NeedsHuman bool
	// SignIn reports a password field on the page, which is the one unambiguous
	// marker of a sign-in. Used to catch a contradiction rather than to act: a
	// sign-in page in the GUEST context means she is looking at a door she has no
	// key for, because guest carries none of the user's cookies.
	SignIn bool
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
	if s.NeedsHuman {
		lines = append(lines, "  · this page is a HUMAN VERIFICATION check — a Cloudflare "+
			"challenge, a CAPTCHA or similar. It exists to confirm a person is present, so "+
			"there is nothing you can click, type or wait for that will satisfy it, and "+
			"retrying will not help. Use browser_hand_over to bring the window to the "+
			"user's attention and let them clear it")
	}
	if s.Rendering {
		lines = append(lines, "  · the page has finished loading but is still filling in — "+
			"there are skeleton placeholders or spinners on it. What you just read may be "+
			"the shell rather than the data. Wait for something you expect and read again; "+
			"do NOT report this as empty or missing")
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

	// The chrome flag looks for Chrome's OWN interstitial markup. These ids and
	// classes belong to the browser's built-in error and security pages; no site
	// ships them, which is what makes this safe to refuse on where matching the
	// page's prose is not.
	raw, err := c.EvalString(ctx, `JSON.stringify({
      ready: document.readyState,
      text: (document.body ? (document.body.innerText || '') : '').slice(0, 800),
      chrome: !!document.querySelector(
        '#main-frame-error, .interstitial-wrapper, #proceed-link, #security-interstitial'),
      busy: !!document.querySelector(
        '[aria-busy="true"], [role="progressbar"], [class*="skeleton"], ' +
        '[class*="shimmer"], [class*="placeholder"], [class*="animate-pulse"]'),
      signin: !!document.querySelector('input[type="password"]'),
      human: !!document.querySelector(
        'iframe[src*="challenges.cloudflare.com"], .cf-turnstile, #cf-challenge-running, ' +
        '#challenge-form, iframe[src*="recaptcha"], .g-recaptcha, iframe[src*="hcaptcha"], ' +
        '.h-captcha, [data-sitekey]')
    })`)
	if err != nil {
		return s
	}

	var probe struct {
		Ready  string `json:"ready"`
		Text   string `json:"text"`
		Chrome bool   `json:"chrome"`
		Busy   bool   `json:"busy"`
		SignIn bool   `json:"signin"`
		Human  bool   `json:"human"`
	}
	if json.Unmarshal([]byte(raw), &probe) != nil {
		return s
	}
	s.Loading = probe.Ready != "complete"
	// Only interesting once the document itself is done: before that, Loading
	// already says everything and saying both would be noise.
	s.Rendering = !s.Loading && probe.Busy
	s.SignIn = probe.SignIn
	// The widget, or the text Cloudflare shows while its own check runs. The text
	// alone is too weak to act on — a page may discuss verification — so it only
	// counts alongside a title that is the challenge rather than the site.
	low := strings.ToLower(probe.Text + " " + s.Title)
	s.NeedsHuman = probe.Human ||
		strings.Contains(low, "verify you are human") ||
		strings.Contains(low, "checking your browser") ||
		(strings.Contains(low, "just a moment") && strings.Contains(strings.ToLower(s.Title), "just a moment"))

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

	// Certainty, as opposed to suspicion. Chrome's own markup, or a scheme only
	// Chrome can serve — either way this document did not come from a site.
	s.BrowserPage = probe.Chrome ||
		strings.HasPrefix(s.URL, "chrome-error://") ||
		strings.HasPrefix(s.URL, "chrome://")
	if s.BrowserPage && s.Interstitial == "" {
		s.Interstitial = "this is one of the browser's own pages, not the site"
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

// RefuseInteraction refuses ANY interaction with a browser warning page.
//
// RefuseUnsafeProceed reads the target's text, which only works for a tool that
// has target text to read. A selector click, a keypress, a form submit and a
// right-click carry no words to match — so the page that one tool refused to
// push past could be pushed past by any of the other five, while the refusal
// message told her not to look for another way through. Naming the other ways
// through and leaving them open is worse than not refusing at all.
//
// So the rule here is blanket rather than word-aware, and it is the stronger
// rule: there is nothing on a certificate warning she has legitimate business
// clicking, typing into, or submitting. The one legitimate move — leaving — is a
// navigation, not an interaction, and is untouched. So is reading it, which is
// exactly what she should do.
func RefuseInteraction(s PageState, what string) string {
	// BrowserPage, not Interstitial. Blocking every click on a page merely
	// SUSPECTED of being a warning would break ordinary work — see the field
	// comment for why a developer's browser sets that suspicion off constantly.
	if !s.BrowserPage {
		return ""
	}
	return fmt.Sprintf("refusing to %s: this page is a browser SAFETY WARNING, not the "+
		"site — %s. Every control on it exists to push past the warning, so there is "+
		"nothing here to interact with.\n\nDo not look for another way through; there "+
		"isn't one, and the other interaction tools refuse this page too. Navigate "+
		"away, or tell the user what the warning says and let them decide. If they "+
		"gave you the address, check it is the one they meant.", what, s.Interstitial)
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

// GuestSignIn is the contradiction: a sign-in page, in the context that carries
// none of the user's logins.
//
// "Never sit in the guest context and then say you can't sign in" has been in
// the sign-in playbook for weeks, and nothing checked it. The failure is not that
// she cannot sign in — it is that she is standing at a door she brought no key
// to, concludes the door is locked, and reports a limitation that is not real.
// The user's session exists; it is one context away.
//
// Detected from a password field, which is the one unambiguous marker of a
// sign-in page and cannot be confused with ordinary content.
func GuestSignIn(s PageState, guest bool) string {
	if !guest || !s.SignIn {
		return ""
	}
	return "\n\nThis is a sign-in page and you are in the GUEST context, which carries " +
		"none of the user's cookies or saved logins — so of course there is no session " +
		"here. Do not conclude that you cannot sign in, and do not try to type their " +
		"password. Reopen this page with browser_open in the 'auth' context, where they " +
		"are already signed in to most things."
}
