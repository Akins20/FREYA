package bench

import (
	"fmt"
	"net/http"
	"time"
)

// The awkward ways content hides on a real page — each a separate challenge, each
// gated by a token that is not in the page's readable text until she performs
// the right interaction to reveal it.
//
// The trick that makes these honest: browser reads use innerText, which excludes
// display:none content and does not reach into iframes. So a token behind a
// modal, a collapsed panel, a second tab, a filter, or an iframe genuinely is
// not there until she opens it — reporting it proves she did, not that she
// guessed. Content merely below the fold or injected on scroll is in innerText
// only once it has rendered, so those test scroll and settle timing.

// registerChallenges mounts the challenge routes on the portal's mux.
func (ts *TestServer) registerChallenges(mux *http.ServeMux) {
	page := func(path, html string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, html)
		})
	}
	page("/c/scroll", challengeScroll)
	page("/c/reveal", challengeReveal)
	page("/c/modal", challengeModal)
	page("/c/iframe", challengeIframe)
	page("/c/iframe-inner", challengeIframeInner)
	page("/c/tabs", challengeTabs)
	page("/c/search", challengeSearch)
	page("/c/accordion", challengeAccordion)
	page("/c/afteraction", challengeAfterAction)
	page("/c/wrapper", challengeWrapper)

	// The URL-fabrication trap.
	//
	// Every id returns a real, plausible page with a real title — exactly like the
	// portal that cost her a session, where walking quiz ids by pattern produced
	// forty rounds of HTTP 200 and nothing reached. Only the id actually linked
	// from the list carries the token, and that id is unguessable, so the only way
	// through is to read the list and follow what is there. A run that fabricates
	// ids will look busy and succeed at nothing, which is the point.
	mux.HandleFunc("/c/catalogue", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><html><head><title>Catalogue</title></head><body>
<h1>Reading list</h1>
<p>Open the entry for <b>Unit 6</b> and read the access code printed on it.</p>
<ul>
 <li><a href="/c/entry?id=%d">Unit 4 — Inventory</a></li>
 <li><a href="/c/entry?id=%d">Unit 5 — Receivables</a></li>
 <li><a href="/c/entry?id=%d">Unit 6 — Depreciation</a></li>
</ul></body></html>`, ts.decoyA, ts.decoyB, ts.realEntry)
	})
	mux.HandleFunc("/c/entry", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		id := r.URL.Query().Get("id")
		// Any id at all renders a believable entry. That is the trap: a page
		// loading proves nothing.
		body := "<p>This entry has no access code. Go back to the reading list and open the right one.</p>"
		if id == fmt.Sprint(ts.realEntry) {
			body = "<p>Access code: " + catalogueToken + "</p>"
		}
		fmt.Fprintf(w, `<!doctype html><html><head><title>Entry %s</title></head><body>
<h1>Course entry %s</h1>%s</body></html>`, id, id, body)
	})
}

// ChallengeBenchmarks builds one benchmark per hiding-place. Each asks for the
// token on the page; the check is simply that her reply contains it, because the
// token cannot be read without performing the reveal.
func (ts *TestServer) ChallengeBenchmarks() []Benchmark {
	type ch struct {
		name, route, token, brief string
		difficulty                int
	}
	cases := []ch{
		{"content-below-the-fold", "/c/scroll", "SCROLL-7731", "content that only renders after you scroll near the bottom", 4},
		{"content-revealed-on-click", "/c/reveal", "REVEAL-4412", "a value hidden until a Show button is clicked", 3},
		{"content-in-a-modal", "/c/modal", "MODAL-9925", "a value inside a dialog that opens on a button click", 4},
		{"content-in-an-iframe", "/c/iframe", "IFRAME-6058", "a value inside an embedded frame", 5},
		{"content-behind-a-tab", "/c/tabs", "TABS-3140", "a value under a second tab that isn't shown by default", 4},
		{"content-behind-search", "/c/search", "SEARCH-8827", "a value that only appears after typing a query to filter a list", 4},
		{"content-in-an-accordion", "/c/accordion", "ACCORD-2276", "a value inside a collapsed panel you must expand", 3},
		{"content-after-an-action", "/c/afteraction", "ACTION-5590", "a value that loads only after you press a Load button and wait", 4},
		{"control-inside-a-wrapper", "/c/wrapper", "WRAPPER-3390", "a value revealed by a Start Quiz! button that sits inside a much wider wrapper element", 5},
	}
	// The URL-fabrication benchmark. Deliberately NOT in the table above: its
	// prompt must name only the front page, so the only route to the token is
	// reading the list and following the link on it. Guessing ids returns real
	// pages that say nothing, which is exactly the trap it exists to punish.
	out := []Benchmark{{
		Name:       "reach-by-link-not-by-guessing",
		Category:   "browser-edge",
		Difficulty: 5,
		Timeout:    5 * time.Minute,
		Prompt: fmt.Sprintf("Open %s/c/catalogue in the browser. Find the entry for Unit 6, open it, "+
			"and tell me the access code printed on it. Entry addresses are not guessable — "+
			"follow what is on the page.", ts.URL),
		Check: All(ReplyHas(catalogueToken), NoThrash(3)),
	}}
	for _, c := range cases {
		c := c
		out = append(out, Benchmark{
			Name:       c.name,
			Category:   "browser-edge",
			Difficulty: c.difficulty,
			Timeout:    5 * time.Minute,
			Prompt: fmt.Sprintf("Open %s%s in the browser. There is a token on that page — %s. "+
				"Find it and tell me the exact token.", ts.URL, c.route, c.brief),
			Check: ReplyHas(c.token),
		})
	}
	return out
}

// --- the challenge pages -----------------------------------------------------
//
// In every one, the token is absent from the initial readable text and appears
// only after the intended interaction.

// Below the fold and injected on scroll: nothing until you scroll down.
const challengeScroll = `<!doctype html><html><body>
<h1>Long page</h1>
<div style="height:3000px">Scroll down to load the rest…</div>
<div id="slot"></div>
<script>
let loaded=false;
window.addEventListener('scroll', () => {
  if (loaded) return;
  if (window.scrollY > 1200) {
    loaded=true;
    document.getElementById('slot').textContent = 'Your token is SCROLL-7731';
  }
});
</script></body></html>`

// Hidden until a button reveals it (display:none excludes it from innerText).
const challengeReveal = `<!doctype html><html><body>
<h1>Details</h1>
<button id="show" onclick="document.getElementById('secret').style.display='block'">Show details</button>
<div id="secret" style="display:none">Your token is REVEAL-4412</div>
</body></html>`

// In a modal that opens on click.
const challengeModal = `<!doctype html><html><body>
<h1>Records</h1>
<button id="open" onclick="document.getElementById('modal').style.display='block'">Open record</button>
<div id="modal" style="display:none;position:fixed;top:20%;left:20%;background:#fff;border:2px solid #333;padding:20px;z-index:1000">
  <p>Your token is MODAL-9925</p>
  <button onclick="document.getElementById('modal').style.display='none'">Close</button>
</div>
</body></html>`

// Inside a same-origin iframe — not in the top document's innerText at all.
const challengeIframe = `<!doctype html><html><body>
<h1>Embedded report</h1>
<iframe id="frame" src="/c/iframe-inner" style="width:400px;height:200px"></iframe>
</body></html>`
const challengeIframeInner = `<!doctype html><html><body>
<p>Your token is IFRAME-6058</p></body></html>`

// Under a second tab, hidden until selected.
const challengeTabs = `<!doctype html><html><body>
<h1>Report</h1>
<button onclick="sel(0)">Overview</button>
<button id="tab2" onclick="sel(1)">Details</button>
<div id="t0">Overview content, nothing here.</div>
<div id="t1" style="display:none">Your token is TABS-3140</div>
<script>function sel(i){document.getElementById('t0').style.display=i==0?'block':'none';
document.getElementById('t1').style.display=i==1?'block':'none';}</script>
</body></html>`

// Only appears after typing a query that matches.
const challengeSearch = `<!doctype html><html><body>
<h1>Directory</h1>
<input id="q" placeholder="Search…" oninput="filter()">
<div id="results"></div>
<script>
const rows=[{k:'apollo',v:'Your token is SEARCH-8827'},{k:'gemini',v:'Nothing here'},{k:'mercury',v:'Nothing here'}];
function filter(){
  const q=document.getElementById('q').value.toLowerCase();
  const r=document.getElementById('results');
  r.innerHTML='';
  if(!q) return;
  rows.filter(x=>x.k.includes(q)).forEach(x=>{const d=document.createElement('div');d.textContent=x.v;r.appendChild(d);});
}
</script></body></html>`

// Inside a collapsed accordion panel.
const challengeAccordion = `<!doctype html><html><body>
<h1>FAQ</h1>
<button id="exp" onclick="document.getElementById('panel').style.display='block'">Show unit details ▾</button>
<div id="panel" style="display:none">Your token is ACCORD-2276</div>
</body></html>`

// Loads only after an action, and after a delay — tests click, then WAIT.
const challengeAfterAction = `<!doctype html><html><body>
<h1>On demand</h1>
<button id="load" onclick="go()">Load result</button>
<div id="out">Not loaded yet.</div>
<script>function go(){document.getElementById('out').textContent='Working…';
setTimeout(()=>{document.getElementById('out').textContent='Your token is ACTION-5590';},900);}</script>
</body></html>`

// catalogueToken proves she followed the real link rather than guessing an id.
const catalogueToken = "CATALOG-5512"

// challengeWrapper reproduces the control that defeated a trusted click: a wide
// wrapper element whose tag name contains "button" and whose text is the button's
// own text, with the real, small button floated to one edge. Aiming at the
// wrapper's centre hits empty space, the page does not move, and the click
// reports success — which is how "Start Quiz!" appeared to do nothing.
const challengeWrapper = `<!doctype html><html><head><meta charset="utf-8"><title>Wrapper</title>
<style>
  #bar { display:block; width: 900px; height: 70px; border: 1px solid #ccc; }
  #go  { float: left; width: 150px; height: 40px; margin-top: 15px; }
  #out { margin-top: 20px; }
</style></head>
<body>
<h1>Attempt summary</h1>
<d2l-floating-buttons id="bar">
  <button type="button" id="go"
     onclick="document.getElementById('out').textContent='Access code: WRAPPER-3390'">Start Quiz!</button>
</d2l-floating-buttons>
<div id="out"></div>
</body></html>`
