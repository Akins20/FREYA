package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// This is the measurement behind the shadow-DOM cure, not a unit test: it drives
// a real headless Chrome against a page that reproduces the UoPeople failure —
// a "Work To Do" list rendered asynchronously inside nested open shadow roots,
// exactly what innerText and querySelector cannot see. It proves the before
// (naive body.innerText is blind to the list) and the after (the deep reader
// sees it, and click-by-text activates an item inside a shadow root).
//
// Gated behind FREYA_BROWSER_E2E=1 and a present Chrome, so `go test ./...`
// never launches a browser. Run it with:
//
//	FREYA_BROWSER_E2E=1 go test ./internal/browser/ -run TestShadowDOM -v

const shadowPortalHTML = `<!doctype html><html><head><meta charset="utf-8"><title>Work To Do</title></head>
<body>
<h1>UoPeople Online Campus</h1>
<div id="app">loading your tasks…</div>
<script>
  class D2lItem extends HTMLElement {
    connectedCallback() {
      const r = this.attachShadow({mode:'open'});
      const label = this.getAttribute('label');
      r.innerHTML = '<a href="#" class="d2l-link">' + label + '</a>';
      r.querySelector('a').addEventListener('click', (e) => {
        e.preventDefault();
        document.title = 'OPENED: ' + label;
      });
    }
  }
  customElements.define('d2l-item', D2lItem);

  class WorkToDo extends HTMLElement {
    connectedCallback() {
      const r = this.attachShadow({mode:'open'});
      r.innerHTML = '<h2>Work To Do</h2><div id="list"></div>';
      const list = r.getElementById('list');
      ['Self-Quiz Unit 1 for Basic Accounting',
       'Self-Quiz Unit 5 for Basic Accounting',
       'Discussion Unit 3 for Basic Accounting'].forEach(t => {
        const it = document.createElement('d2l-item');
        it.setAttribute('label', t);
        list.appendChild(it);
      });
    }
  }
  customElements.define('work-to-do', WorkToDo);

  // Async, like a real SPA: the real list replaces the placeholder a beat later.
  setTimeout(() => {
    const app = document.getElementById('app');
    app.innerHTML = '';
    app.appendChild(document.createElement('work-to-do'));
  }, 600);
</script>
</body></html>`

func TestShadowDOMPortal(t *testing.T) {
	if os.Getenv("FREYA_BROWSER_E2E") != "1" {
		t.Skip("set FREYA_BROWSER_E2E=1 to run the headless-Chrome shadow-DOM measurement")
	}
	bin := chromeBinary()
	if bin == "" {
		t.Skip("no Chrome/Chromium on PATH")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, shadowPortalHTML)
	}))
	defer srv.Close()

	// A private headless Chrome on an unusual port with a throwaway profile, so
	// it touches neither the user's browser nor Freya's own contexts.
	const port = 9412
	profile, err := os.MkdirTemp("", "freya-shadow-e2e-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(profile)

	chrome := exec.Command(bin,
		"--headless=new",
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--user-data-dir="+profile,
		"--no-first-run", "--no-default-browser-check",
		"--window-size=1280,900",
		"about:blank",
	)
	if err := chrome.Start(); err != nil {
		t.Fatalf("launch headless chrome: %v", err)
	}
	defer func() { _ = chrome.Process.Kill(); _, _ = chrome.Process.Wait() }()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitForDevtools(t, base, 20*time.Second) {
		t.Fatal("headless chrome devtools did not come up")
	}

	// Open a tab straight through the devtools HTTP endpoint (PUT /json/new),
	// then attach the real Client to it — the same Client the daemon uses.
	target := newTabDirect(t, base, srv.URL)
	client, err := Connect(ContextGuest, target)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := client.Navigate(ctx, srv.URL); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	// --- BEFORE: naive body.innerText is blind to the shadow-DOM list ----------
	naive, _ := client.EvalString(ctx, "document.body.innerText")
	if strings.Contains(naive, "Self-Quiz Unit 5") {
		t.Fatalf("precondition broken: naive innerText already sees shadow content:\n%s", naive)
	}
	t.Logf("BEFORE (naive innerText, %d chars): %q", len(naive), oneLine(naive))

	// --- AFTER: the deep reader sees the whole shadow-DOM list -----------------
	deep, err := client.Text(ctx)
	if err != nil {
		t.Fatalf("deep Text: %v", err)
	}
	for _, want := range []string{
		"Work To Do",
		"Self-Quiz Unit 1 for Basic Accounting",
		"Self-Quiz Unit 5 for Basic Accounting",
		"Discussion Unit 3 for Basic Accounting",
	} {
		if !strings.Contains(deep, want) {
			t.Fatalf("deep reader missed %q\n--- deep text ---\n%s", want, deep)
		}
	}
	t.Logf("AFTER (deep Text, %d chars): contains the full Work To Do list ✓", len(deep))

	// --- Links lists the clickable items even though they carry no real href ---
	links, err := client.Links(ctx, 60)
	if err != nil {
		t.Fatalf("links: %v", err)
	}
	if !strings.Contains(links, "Self-Quiz Unit 5 for Basic Accounting") {
		t.Fatalf("Links did not surface the shadow-DOM item:\n%s", links)
	}

	// --- ClickText activates an item nested two shadow roots deep --------------
	if _, err := client.ClickText(ctx, "Self-Quiz Unit 5 for Basic Accounting"); err != nil {
		t.Fatalf("ClickText: %v", err)
	}
	title, _ := client.Title(ctx)
	if title != "OPENED: Self-Quiz Unit 5 for Basic Accounting" {
		t.Fatalf("click-by-text did not fire the item's handler; title=%q", title)
	}
	t.Logf("CLICK (by visible text, across shadow DOM): fired the right item ✓ (title=%q)", title)
}

// quizOuterHTML embeds the quiz in a same-origin iframe, the way D2L serves a
// quiz attempt. Nothing interactive lives in the top document.
const quizOuterHTML = `<!doctype html><html><head><meta charset="utf-8"><title>Quiz Attempt</title></head>
<body><h1>BUS 1102 — Basic Accounting</h1>
<iframe id="qf" src="/quiz" style="width:900px;height:520px;border:0"></iframe>
</body></html>`

const quizFrameHTML = `<!doctype html><html><head><meta charset="utf-8"><title>quiz</title></head>
<body>
<h2>Question 1</h2>
<p>Understating an expense leaves net income…</p>
<form>
  <label><input type="radio" name="q1" value="over"
     onclick="window.top.document.title='PICKED: Overstated'"> Overstated</label><br>
  <label><input type="radio" name="q1" value="under"
     onclick="window.top.document.title='PICKED: Understated'"> Understated</label><br>
  <button type="button" id="submitbtn"
     onclick="window.top.document.title='SUBMITTED'">Submit Quiz</button>
</form>
</body></html>`

// TestIframeQuiz reproduces the second failure the user hit: she found and read
// the quiz, but every click died with "no element matches" because the quiz —
// its radios and Submit button — lives inside an iframe, and the interaction
// tools only queried the top document. It proves the fix: click-by-text selects
// an answer inside the frame, and a coordinate click (browser_click_real) lands
// on the frame's Submit button through the frame offset.
func TestIframeQuiz(t *testing.T) {
	if os.Getenv("FREYA_BROWSER_E2E") != "1" {
		t.Skip("set FREYA_BROWSER_E2E=1 to run the headless-Chrome iframe measurement")
	}
	bin := chromeBinary()
	if bin == "" {
		t.Skip("no Chrome/Chromium on PATH")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/quiz" {
			_, _ = io.WriteString(w, quizFrameHTML)
			return
		}
		_, _ = io.WriteString(w, quizOuterHTML)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	const port = 9413
	profile, err := os.MkdirTemp("", "freya-iframe-e2e-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(profile)

	chrome := exec.Command(bin, "--headless=new",
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check",
		"--window-size=1280,900", "about:blank")
	if err := chrome.Start(); err != nil {
		t.Fatalf("launch headless chrome: %v", err)
	}
	defer func() { _ = chrome.Process.Kill(); _, _ = chrome.Process.Wait() }()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitForDevtools(t, base, 20*time.Second) {
		t.Fatal("devtools did not come up")
	}
	target := newTabDirect(t, base, srv.URL)
	client, err := Connect(ContextGuest, target)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Navigate(ctx, srv.URL); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	// BEFORE: the top document has no radios — they are in the frame.
	naive, _ := client.EvalString(ctx, "String(document.querySelectorAll('input[type=radio]').length)")
	if naive != "0" {
		t.Fatalf("precondition broken: top document already sees %s radios", naive)
	}

	// Inspect must now surface the radios and the button from inside the frame.
	inspect, err := client.Inspect(ctx, 60)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	for _, want := range []string{"Overstated", "Submit Quiz"} {
		if !strings.Contains(inspect, want) {
			t.Fatalf("inspect missed %q inside the iframe:\n%s", want, inspect)
		}
	}
	t.Logf("INSPECT sees the in-frame controls ✓")

	// Select an answer by its visible text — the reliable primitive.
	if _, err := client.ClickText(ctx, "Overstated"); err != nil {
		t.Fatalf("ClickText answer: %v", err)
	}
	if title, _ := client.Title(ctx); title != "PICKED: Overstated" {
		t.Fatalf("answer click did not register in the frame; title=%q", title)
	}
	t.Logf("CLICK-BY-TEXT selected the answer inside the iframe ✓")

	// Submit with a coordinate click (browser_click_real) on a selector that only
	// resolves inside the frame — exercises the frame-offset math in locate.
	if err := client.ClickReal(ctx, "#submitbtn"); err != nil {
		t.Fatalf("ClickReal submit: %v", err)
	}
	if title, _ := client.Title(ctx); title != "SUBMITTED" {
		t.Fatalf("real click did not land on the frame's Submit button; title=%q", title)
	}
	t.Logf("CLICK-REAL (coordinates, through frame offset) hit Submit ✓")
}

// modalFrameHTML mirrors what actually happened: the page's "Submit Quiz" opens
// an INFORMATIONAL modal — the submission notice, warning about unanswered
// questions — whose only buttons are "Back to Questions" and "Submit Anyway".
// There is no second "Submit Quiz". The point she missed was reading it.
const modalOuterHTML = `<!doctype html><html><head><meta charset="utf-8"><title>Attempt</title></head>
<body><h1>Quiz</h1><iframe id="qf" src="/mframe" style="width:900px;height:520px;border:0"></iframe></body></html>`

const modalFrameHTML = `<!doctype html><html><head><meta charset="utf-8"><title>mframe</title></head>
<body>
<button type="button" id="pageSubmit"
  onclick="document.getElementById('dlg').style.display='block'">Submit Quiz</button>
<div id="dlg" role="dialog" aria-modal="true" style="display:none">
  <h2>Submission Notice</h2>
  <p>You have 2 unanswered questions. Once you submit you cannot change your answers.</p>
  <button type="button" id="back"
     onclick="window.top.document.title='WENT_BACK'">Back to Questions</button>
  <button type="button" id="anyway"
     onclick="window.top.document.title='SUBMITTED_ANYWAY'">Submit Anyway</button>
</div>
</body></html>`

// TestModalInIframe proves the two things she actually needed for the modal: its
// text (the unanswered-questions warning) is readable while it is open and NOT
// while it is closed, and its real button ("Back to Questions") is clickable —
// all inside the iframe. The judgment to act on the warning is persona/playbook,
// not code; this just proves the page is legible and operable.
func TestModalInIframe(t *testing.T) {
	if os.Getenv("FREYA_BROWSER_E2E") != "1" {
		t.Skip("set FREYA_BROWSER_E2E=1 to run the headless-Chrome modal measurement")
	}
	bin := chromeBinary()
	if bin == "" {
		t.Skip("no Chrome/Chromium on PATH")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/mframe" {
			_, _ = io.WriteString(w, modalFrameHTML)
			return
		}
		_, _ = io.WriteString(w, modalOuterHTML)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	const port = 9414
	profile, _ := os.MkdirTemp("", "freya-modal-e2e-*")
	defer os.RemoveAll(profile)
	chrome := exec.Command(bin, "--headless=new",
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check",
		"--window-size=1280,900", "about:blank")
	if err := chrome.Start(); err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer func() { _ = chrome.Process.Kill(); _, _ = chrome.Process.Wait() }()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitForDevtools(t, base, 20*time.Second) {
		t.Fatal("devtools down")
	}
	client, err := Connect(ContextGuest, newTabDirect(t, base, srv.URL))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Navigate(ctx, srv.URL); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	// While the modal is CLOSED its warning must not leak into the read — else
	// she'd "see" a warning that isn't on screen.
	closed, _ := client.Text(ctx)
	if strings.Contains(closed, "unanswered questions") {
		t.Fatalf("closed modal text leaked into the read:\n%s", closed)
	}

	// Open the submission notice.
	if _, err := client.ClickText(ctx, "Submit Quiz"); err != nil {
		t.Fatalf("open modal: %v", err)
	}

	// Now the warning is on screen and must be readable — this is the context
	// clue she is supposed to reason about.
	open, _ := client.Text(ctx)
	if !strings.Contains(open, "2 unanswered questions") {
		t.Fatalf("modal warning not readable once open:\n%s", open)
	}
	t.Logf("READ the modal warning once open ✓ (\"2 unanswered questions…\")")

	// Its real button — "Back to Questions" — must be clickable inside the frame.
	if _, err := client.ClickText(ctx, "Back to Questions"); err != nil {
		t.Fatalf("click modal button: %v", err)
	}
	if title, _ := client.Title(ctx); title != "WENT_BACK" {
		t.Fatalf("modal button click did not register; title=%q", title)
	}
	t.Logf("CLICKED the modal's real button (\"Back to Questions\") inside the iframe ✓")
}

// trustedOuterHTML / trustedFrameHTML reproduce a D2L quiz-list link: it lives
// in an iframe and navigates ONLY on a genuine (isTrusted) gesture, so a
// synthetic el.click() does nothing. This is what made her click a quiz by name,
// be told "Clicked", and go nowhere — then guess selectors and bounce to the
// homepage, over and over.
const trustedOuterHTML = `<!doctype html><html><head><meta charset="utf-8"><title>List</title></head>
<body><h1>Quiz List</h1><iframe id="qf" src="/tframe" style="width:900px;height:400px;border:0"></iframe></body></html>`

const trustedFrameHTML = `<!doctype html><html><head><meta charset="utf-8"><title>tframe</title></head>
<body><ul><li>
<a href="#" id="u2" onclick="if(event.isTrusted){window.top.document.title='NAVIGATED';} return false;">Self-Quiz Unit 2</a>
</li></ul></body></html>`

// TestTrustedClickByText proves click-by-text now performs a real, trusted click:
// a synthetic click leaves the isTrusted-gated link dead, but ClickText fires it —
// through the iframe-offset coordinate math.
func TestTrustedClickByText(t *testing.T) {
	if os.Getenv("FREYA_BROWSER_E2E") != "1" {
		t.Skip("set FREYA_BROWSER_E2E=1 to run the headless-Chrome trusted-click measurement")
	}
	bin := chromeBinary()
	if bin == "" {
		t.Skip("no Chrome/Chromium on PATH")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/tframe" {
			_, _ = io.WriteString(w, trustedFrameHTML)
			return
		}
		_, _ = io.WriteString(w, trustedOuterHTML)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	const port = 9415
	profile, _ := os.MkdirTemp("", "freya-trusted-e2e-*")
	defer os.RemoveAll(profile)
	chrome := exec.Command(bin, "--headless=new",
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check",
		"--window-size=1280,900", "about:blank")
	if err := chrome.Start(); err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer func() { _ = chrome.Process.Kill(); _, _ = chrome.Process.Wait() }()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitForDevtools(t, base, 20*time.Second) {
		t.Fatal("devtools down")
	}
	client, err := Connect(ContextGuest, newTabDirect(t, base, srv.URL))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Navigate(ctx, srv.URL); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	// Control: a synthetic click on the link leaves it dead (isTrusted:false).
	_, _ = client.EvalString(ctx, `(() => { const f=document.querySelector('iframe'); f.contentDocument.getElementById('u2').click(); return 'x'; })()`)
	if title, _ := client.Title(ctx); title == "NAVIGATED" {
		t.Fatal("precondition broken: a synthetic click already navigated")
	}
	t.Logf("CONTROL: synthetic click did NOT fire the isTrusted-gated link ✓")

	// The fix: click-by-text does a real, trusted click through the frame offset.
	if _, err := client.ClickText(ctx, "Self-Quiz Unit 2"); err != nil {
		t.Fatalf("ClickText: %v", err)
	}
	if title, _ := client.Title(ctx); title != "NAVIGATED" {
		t.Fatalf("click-by-text did not fire the trusted-only link; title=%q", title)
	}
	t.Logf("FIX: click-by-text fired the trusted-only link inside the iframe ✓")
}

// deepFormOuterHTML puts a form inside a shadow root AND another inside an
// iframe — the two places browser_inspect could see into and every other
// selector-taking tool could not.
const deepFormOuterHTML = `<!doctype html><html><head><meta charset="utf-8"><title>Deep form</title></head>
<body>
<h1>Enrolment</h1>
<shadow-form></shadow-form>
<iframe id="ff" src="/frameform" style="width:600px;height:300px;border:0"></iframe>
<script>
  class ShadowForm extends HTMLElement {
    connectedCallback() {
      const r = this.attachShadow({mode:'open'});
      r.innerHTML =
        '<form id="sform">' +
        '  <input type="checkbox" id="agree">' +
        '  <select id="course">' +
        '    <option value="a">Basic Accounting</option>' +
        '    <option value="b">Macroeconomics</option>' +
        '  </select>' +
        '  <input type="text" id="who">' +
        '  <p id="motto">Shadow motto: PERSIST</p>' +
        '</form>';
      r.getElementById('sform').addEventListener('submit', (e) => {
        e.preventDefault();
        window.top.document.title = 'SHADOW_SUBMITTED';
      });
    }
  }
  customElements.define('shadow-form', ShadowForm);
  // Appears late, so a wait has something real to wait for.
  setTimeout(() => {
    const d = document.createElement('div');
    d.id = 'late';
    d.textContent = 'LATE_CONTENT_READY';
    document.querySelector('shadow-form').shadowRoot.appendChild(d);
  }, 700);
</script>
</body></html>`

const deepFrameFormHTML = `<!doctype html><html><head><meta charset="utf-8"><title>frameform</title></head>
<body><form id="iform">
  <input type="checkbox" id="fagree">
  <select id="fcourse">
    <option value="x">Frame Option X</option>
    <option value="y">Frame Option Y</option>
  </select>
  <p id="fmotto">Frame motto: NESTED</p>
</form>
<script>
  document.getElementById('iform').addEventListener('submit', (e) => {
    e.preventDefault();
    window.top.document.title = 'FRAME_SUBMITTED';
  });
</script>
</body></html>`

// TestDeepSelectorToolsReachShadowAndFrames is the regression test for the
// substrate's own inconsistency: browser_inspect walked shadow roots and iframes
// and handed back selectors from inside them, while check, select, submit, wait,
// focus and read_element used a plain document.querySelector and could not reach
// one of them. She scouted as instructed, copied a selector verbatim, and was
// told "no element matches" — then blamed for guessing. Every tool exercised here
// failed against this page before the shared deep lookup.
func TestDeepSelectorToolsReachShadowAndFrames(t *testing.T) {
	if os.Getenv("FREYA_BROWSER_E2E") != "1" {
		t.Skip("set FREYA_BROWSER_E2E=1 to run the headless-Chrome deep-selector measurement")
	}
	bin := chromeBinary()
	if bin == "" {
		t.Skip("no Chrome/Chromium on PATH")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/frameform" {
			_, _ = io.WriteString(w, deepFrameFormHTML)
			return
		}
		_, _ = io.WriteString(w, deepFormOuterHTML)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	const port = 9416
	profile, _ := os.MkdirTemp("", "freya-deep-e2e-*")
	defer os.RemoveAll(profile)
	chrome := exec.Command(bin, "--headless=new",
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check",
		"--window-size=1280,900", "about:blank")
	if err := chrome.Start(); err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer func() { _ = chrome.Process.Kill(); _, _ = chrome.Process.Wait() }()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitForDevtools(t, base, 20*time.Second) {
		t.Fatal("devtools down")
	}
	client, err := Connect(ContextGuest, newTabDirect(t, base, srv.URL))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := client.Navigate(ctx, srv.URL); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	// Precondition: the top document genuinely cannot see any of this, so the
	// test is exercising the descent and not a page that was flat all along.
	flat, _ := client.EvalString(ctx, "String(document.querySelectorAll('#agree,#fagree').length)")
	if flat != "0" {
		t.Fatalf("precondition broken: top document already sees the controls (%s)", flat)
	}

	// --- inside a shadow root ---------------------------------------------------
	if got, err := client.SetChecked(ctx, "#agree", true); err != nil {
		t.Fatalf("SetChecked in shadow root: %v", err)
	} else if !strings.Contains(got, "checked") {
		t.Fatalf("SetChecked reported %q, want it checked", got)
	}
	if got, err := client.SelectOption(ctx, "#course", "Macroeconomics"); err != nil {
		t.Fatalf("SelectOption in shadow root: %v", err)
	} else if !strings.Contains(got, "Macroeconomics") {
		t.Fatalf("SelectOption chose %q", got)
	}
	if err := client.Focus(ctx, "#who"); err != nil {
		t.Fatalf("Focus in shadow root: %v", err)
	}
	if err := client.Fill(ctx, "#who", "Elijah"); err != nil {
		t.Fatalf("Fill in shadow root: %v", err)
	}
	if got, err := client.ReadElement(ctx, "#motto"); err != nil {
		t.Fatalf("ReadElement in shadow root: %v", err)
	} else if !strings.Contains(got, "PERSIST") {
		t.Fatalf("ReadElement returned %q", got)
	}
	t.Logf("shadow root: check, select, focus, fill, read all reached it ✓")

	// A wait must see what a read sees — the late element arrives inside the
	// shadow root, which the old top-document-only wait could never observe.
	if err := client.WaitFor(ctx, "#late", 5*time.Second); err != nil {
		t.Fatalf("WaitFor on a shadow-root element: %v", err)
	}
	if err := client.WaitForText(ctx, "LATE_CONTENT_READY", 5*time.Second); err != nil {
		t.Fatalf("WaitForText on shadow-root text: %v", err)
	}
	t.Logf("waits observe shadow-root content ✓")

	// --- inside an iframe -------------------------------------------------------
	if got, err := client.SetChecked(ctx, "#fagree", true); err != nil {
		t.Fatalf("SetChecked in iframe: %v", err)
	} else if !strings.Contains(got, "checked") {
		t.Fatalf("SetChecked in iframe reported %q", got)
	}
	if got, err := client.SelectOption(ctx, "#fcourse", "Frame Option Y"); err != nil {
		t.Fatalf("SelectOption in iframe: %v", err)
	} else if !strings.Contains(got, "Frame Option Y") {
		t.Fatalf("SelectOption in iframe chose %q", got)
	}
	if got, err := client.ReadElement(ctx, "#fmotto"); err != nil {
		t.Fatalf("ReadElement in iframe: %v", err)
	} else if !strings.Contains(got, "NESTED") {
		t.Fatalf("ReadElement in iframe returned %q", got)
	}
	t.Logf("iframe: check, select, read all reached it ✓")

	// Submitting a form that lives inside the frame.
	if err := client.Submit(ctx, "#iform"); err != nil {
		t.Fatalf("Submit inside iframe: %v", err)
	}
	if title, _ := client.Title(ctx); title != "FRAME_SUBMITTED" {
		t.Fatalf("in-frame submit did not fire; title=%q", title)
	}
	t.Logf("submit reached the form inside the iframe ✓")

	// And one that lives inside the shadow root.
	if err := client.Submit(ctx, "#sform"); err != nil {
		t.Fatalf("Submit inside shadow root: %v", err)
	}
	if title, _ := client.Title(ctx); title != "SHADOW_SUBMITTED" {
		t.Fatalf("shadow submit did not fire; title=%q", title)
	}
	t.Logf("submit reached the form inside the shadow root ✓")
}

// TestFillReadsBackWhatItTyped pins the postcondition: a field that rejects or
// rewrites the value must not report a clean success.
func TestFillReadsBackWhatItTyped(t *testing.T) {
	if os.Getenv("FREYA_BROWSER_E2E") != "1" {
		t.Skip("set FREYA_BROWSER_E2E=1 to run the headless-Chrome fill measurement")
	}
	bin := chromeBinary()
	if bin == "" {
		t.Skip("no Chrome/Chromium on PATH")
	}

	const page = `<!doctype html><html><head><meta charset="utf-8"><title>fields</title></head><body>
	<input type="text" id="good">
	<input type="text" id="shouty" oninput="this.value=this.value.toUpperCase()">
	<input type="text" id="locked" disabled>
	<div id="notafield">just text</div>
	</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, page)
	}))
	defer srv.Close()

	const port = 9417
	profile, _ := os.MkdirTemp("", "freya-fill-e2e-*")
	defer os.RemoveAll(profile)
	chrome := exec.Command(bin, "--headless=new",
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check",
		"--window-size=1280,900", "about:blank")
	if err := chrome.Start(); err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer func() { _ = chrome.Process.Kill(); _, _ = chrome.Process.Wait() }()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitForDevtools(t, base, 20*time.Second) {
		t.Fatal("devtools down")
	}
	client, err := Connect(ContextGuest, newTabDirect(t, base, srv.URL))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Navigate(ctx, srv.URL); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	if err := client.Fill(ctx, "#good", "hello"); err != nil {
		t.Fatalf("an ordinary field should fill cleanly: %v", err)
	}
	// A field that rewrites what was typed is a silent failure today; it must be
	// reported, because the form will not carry the value she thinks it has.
	if err := client.Fill(ctx, "#shouty", "hello"); err == nil {
		t.Error("a field that rewrote the value reported success")
	} else {
		t.Logf("rewritten value reported: %v", err)
	}
	if err := client.Fill(ctx, "#locked", "hello"); err == nil {
		t.Error("a disabled field reported success")
	}
	if err := client.Fill(ctx, "#notafield", "hello"); err == nil {
		t.Error("a <div> reported success as a text field")
	}
}

// waitForDevtools polls the devtools HTTP endpoint until it answers.
func waitForDevtools(t *testing.T, base string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/json/version")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// newTabDirect opens a page via the devtools endpoint and returns its target,
// independent of the fixed context ports.
func newTabDirect(t *testing.T, base, url string) *Target {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, base+"/json/new?"+url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("open tab: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tg Target
	if err := json.Unmarshal(body, &tg); err != nil {
		t.Fatalf("unexpected /json/new reply: %s", string(body))
	}
	if tg.WS == "" {
		t.Fatalf("tab has no webSocketDebuggerUrl: %s", string(body))
	}
	return &tg
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
