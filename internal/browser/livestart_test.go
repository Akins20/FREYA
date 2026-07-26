package browser

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestDiagnoseStartQuiz is a READ-ONLY probe of whatever quiz Summary page is
// open in the auth-context Chrome. It clicks nothing. It exists to answer one
// question: what IS the "Start Quiz!" control, and why did a trusted click on it
// leave the page exactly where it was?
//
//	FREYA_LIVE_DIAG=1 go test ./internal/browser/ -run TestDiagnoseStartQuiz -v
func TestDiagnoseStartQuiz(t *testing.T) {
	if os.Getenv("FREYA_LIVE_DIAG") != "1" {
		t.Skip("set FREYA_LIVE_DIAG=1 to probe the live auth-context page")
	}
	resp, err := http.Get("http://127.0.0.1:9222/json/list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var targets []Target
	_ = json.Unmarshal(body, &targets)

	var page *Target
	for i := range targets {
		if targets[i].Type == "page" && targets[i].WS != "" &&
			strings.Contains(targets[i].URL, "quiz_summary") {
			page = &targets[i]
			break
		}
	}
	if page == nil {
		t.Skip("no quiz Summary page open in the auth context")
	}
	t.Logf("probing: %q\n  %s", page.Title, page.URL)

	client, err := Connect(ContextAuth, page)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Describe anything whose text looks like the start control, across shadow
	// roots and same-origin frames — tag, attributes, the form it belongs to, its
	// box, and whether anything sits on top of it.
	probe := deepPrelude + `(() => {
      const out = [];
      const norm = s => (s||'').replace(/\s+/g,' ').trim();
      const rec = (root) => {
        let list; try { list = root.querySelectorAll('*'); } catch(e){ return; }
        for (const el of list) {
          const sub = __descend(el); if (sub) rec(sub);
          const txt = norm(el.innerText || el.value || el.textContent);
          if (!txt || txt.length > 40) continue;
          if (!/start quiz|continue quiz|begin/i.test(txt)) continue;
          const tag = el.tagName.toLowerCase();
          if (!['a','button','input','span','div','td'].includes(tag) && tag.indexOf('-') < 0) continue;
          const r = el.getBoundingClientRect();
          const doc = el.ownerDocument;
          const form = el.closest ? el.closest('form') : null;
          let atPoint = '';
          try {
            const hp = doc.elementFromPoint(r.left + r.width/2, r.top + r.height/2);
            atPoint = hp ? (hp === el ? 'SELF' : (el.contains(hp) ? 'child' : hp.tagName.toLowerCase() + (hp.id ? '#'+hp.id : ''))) : 'none';
          } catch(e) { atPoint = 'err'; }
          const attrs = {};
          if (el.attributes) for (const a of el.attributes) attrs[a.name] = String(a.value).slice(0,120);
          out.push({
            tag: tag, text: txt, attrs: attrs,
            box: Math.round(r.width)+'x'+Math.round(r.height),
            visible: !!(r.width || r.height),
            inFrame: doc !== document,
            hasOnclick: !!el.onclick || el.hasAttribute('onclick'),
            form: form ? {action: form.getAttribute('action')||'', method: form.getAttribute('method')||'', target: form.getAttribute('target')||''} : null,
            atCenter: atPoint,
            parentTag: el.parentElement ? el.parentElement.tagName.toLowerCase() : ''
          });
        }
      };
      rec(document);
      return JSON.stringify({found: out, url: location.href, title: document.title}, null, 1);
    })()`

	raw, err := client.EvalString(ctx, probe)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	t.Logf("START CONTROL:\n%s", raw)

	// Is the session actually alive, or is this a logged-out shell?
	hints, _ := client.SessionHints(ctx)
	t.Logf("session hints: %s", hints)
}
