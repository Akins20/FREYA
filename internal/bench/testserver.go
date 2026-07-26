package bench

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// A local single-page app that reproduces, deterministically, the exact things
// Freya failed on against a real portal.
//
// # Why a fake portal instead of the real one
//
// The real portal is non-deterministic (its content changes), needs a login she
// cannot script in a test, and would make the benchmark a flaky integration with
// someone else's website. A local server under our control is reproducible: the
// target is always the same item, on the same page, behind the same lazy load,
// so a pass means she worked the page and a fail means she did not — not that the
// site happened to be slow today.
//
// # What it deliberately gets "wrong"
//
// Every awkward behaviour here is intentional, because each is a thing she
// stumbled on:
//   - the work list arrives a beat after the page, behind skeleton rows, so a
//     read that happens too early sees placeholders;
//   - the list is paginated and the target sits on the last page, so she must
//     work the pagination rather than give up on page one;
//   - the "next" button ignores scripted clicks and honours only a real one, so
//     browser_click alone gets nowhere and browser_click_real is required;
//   - opening the target records it server-side, so the check verifies she
//     actually reached it, not that she said she did.

// PortalState records what a run actually accomplished on the server, so checks
// can verify the world rather than the reply.
type PortalState struct {
	OpenedQuiz  atomic.Int32 // the unit number of the quiz page she reached, 0 if none
	SubmittedTo atomic.Value // string: the form target she submitted, if any
	loginDone   atomic.Bool
}

// Reset clears what a previous run accomplished, so the next one has to earn its
// own pass. Called from the Setup of every benchmark that reads this state —
// without it the record is cumulative across runs and the second run of a
// benchmark passes on the first run's work.
func (p *PortalState) Reset() {
	p.OpenedQuiz.Store(0)
	p.SubmittedTo.Store("")
	p.loginDone.Store(false)
}

// TestServer is a running fake portal.
type TestServer struct {
	URL   string
	State *PortalState
	http  *http.Server
	ln    net.Listener
	once  sync.Once
}

// StartPortal launches the fake portal on a free localhost port.
func StartPortal() (*TestServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ts := &TestServer{
		URL:   fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port),
		State: &PortalState{},
		ln:    ln,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", ts.handlePortal)
	mux.HandleFunc("/portal", ts.handlePortal)
	mux.HandleFunc("/api/worklist", ts.handleWorklist)
	mux.HandleFunc("/quiz/", ts.handleQuiz)
	mux.HandleFunc("/login", ts.handleLogin)
	ts.registerChallenges(mux)

	ts.http = &http.Server{Handler: mux}
	go func() { _ = ts.http.Serve(ln) }()
	return ts, nil
}

// Close stops the server.
func (ts *TestServer) Close() {
	ts.once.Do(func() {
		if ts.http != nil {
			_ = ts.http.Close()
		}
	})
}

// handlePortal serves the SPA shell. The list is NOT in the HTML — it is fetched
// by script after a delay, which is the whole point.
func (ts *TestServer) handlePortal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, portalHTML)
}

// handleWorklist returns the paginated list, after a delay, so a too-early read
// sees skeletons. The target quiz is on the last page.
func (ts *TestServer) handleWorklist(w http.ResponseWriter, r *http.Request) {
	page := r.URL.Query().Get("page")
	if page == "" {
		page = "1"
	}
	w.Header().Set("Content-Type", "application/json")
	// The delay lives in the client so the network trace stays simple; here we
	// just serve the page's rows. Pages 1 and 2 are filler; page 3 has the target.
	switch page {
	case "1":
		fmt.Fprint(w, `{"page":1,"pages":3,"items":[
      {"unit":1,"title":"Self-Quiz Unit 1 for BUS 1102 Basic Accounting"},
      {"unit":2,"title":"Discussion Forum Unit 1"}]}`)
	case "2":
		fmt.Fprint(w, `{"page":2,"pages":3,"items":[
      {"unit":3,"title":"Self-Quiz Unit 3 for BUS 1102 Basic Accounting"},
      {"unit":4,"title":"Written Assignment Unit 4"}]}`)
	default:
		fmt.Fprint(w, `{"page":3,"pages":3,"items":[
      {"unit":5,"title":"Self-Quiz Unit 5 for BUS 1102 Basic Accounting"},
      {"unit":6,"title":"Learning Journal Unit 6"}]}`)
	}
}

// handleQuiz records that she reached a quiz, and serves its content.
func (ts *TestServer) handleQuiz(w http.ResponseWriter, r *http.Request) {
	var unit int
	fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/quiz/"), "%d", &unit)
	if unit > 0 {
		ts.State.OpenedQuiz.Store(int32(unit))
	}
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<html><body><h1>Self-Quiz Unit %d for BUS 1102 Basic Accounting</h1>
    <p id="q1">Question 1: A debit increases which type of account?</p>
    <p>The password to prove you reached this page is APOLLO-%d.</p>
    </body></html>`, unit, unit)
}

// handleLogin records a submission and "logs in".
func (ts *TestServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		ts.State.loginDone.Store(true)
		ts.State.SubmittedTo.Store("/login")
		http.Redirect(w, r, "/portal", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, loginHTML)
}

// portalHTML is a small but genuine SPA: skeletons first, real rows after a
// delay, pagination, and a "next" button that only a real click advances.
const portalHTML = `<!doctype html><html><head><title>Student Portal</title></head>
<body>
<h1>Work To Do</h1>
<div id="list"><div class="skeleton">Loading your work…</div>
  <div class="skeleton">░░░░░░░░</div><div class="skeleton">░░░░░░░░</div></div>
<div id="pager">
  <span id="pageinfo"></span>
  <button id="next">Next</button>
</div>
<script>
let page = 1;
function render(data){
  const l = document.getElementById('list');
  l.innerHTML = '';
  data.items.forEach(it => {
    const a = document.createElement('a');
    a.href = '/quiz/' + it.unit;
    a.textContent = it.title;
    a.className = 'workitem';
    a.style.display = 'block';
    l.appendChild(a);
  });
  document.getElementById('pageinfo').textContent = 'Page ' + data.page + ' of ' + data.pages;
  window.__pages = data.pages;
}
function load(p){
  // The deliberate delay: the list arrives a beat after the page, behind the
  // skeletons already in the DOM.
  setTimeout(() => {
    fetch('/api/worklist?page=' + p).then(r => r.json()).then(render);
  }, 700);
}
// The next button honours only a trusted (real) click — a scripted .click()
// with isTrusted false is ignored, exactly like many real app controls.
document.getElementById('next').addEventListener('click', function(e){
  if (!e.isTrusted) return;
  if (page < (window.__pages||3)) { page++; load(page); }
});
load(1);
</script>
</body></html>`

// loginHTML is a plain form — username and a submit. No password autofill is
// exercised here (that needs real Chrome credentials); this tests fill + submit.
const loginHTML = `<!doctype html><html><head><title>Sign in</title></head>
<body><h1>Sign in</h1>
<form method="POST" action="/login">
  <input id="username" name="username" placeholder="Username">
  <button id="signin" type="submit">Sign in</button>
</form></body></html>`
