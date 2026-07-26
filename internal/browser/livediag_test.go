package browser

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestDiagnoseLiveD2L is a READ-ONLY probe of whatever D2L page is currently open
// in the auth-context Chrome (port 9222). It clicks nothing and submits nothing —
// it dumps the structure of the Submit/answer controls so we can see why a
// trusted click-by-text doesn't drive them. Gated behind FREYA_LIVE_DIAG=1.
//
//	FREYA_LIVE_DIAG=1 go test ./internal/browser/ -run TestDiagnoseLiveD2L -v
func TestDiagnoseLiveD2L(t *testing.T) {
	if os.Getenv("FREYA_LIVE_DIAG") != "1" {
		t.Skip("set FREYA_LIVE_DIAG=1 to probe the live auth-context page")
	}

	// Find the D2L page target on the auth port.
	resp, err := http.Get("http://127.0.0.1:9222/json/list")
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var targets []Target
	if err := json.Unmarshal(body, &targets); err != nil {
		t.Fatalf("parse targets: %v", err)
	}
	var page *Target
	for i := range targets {
		if targets[i].Type == "page" && targets[i].WS != "" &&
			(contains(targets[i].URL, "uopeople") || contains(targets[i].URL, "quiz")) {
			page = &targets[i]
			break
		}
	}
	if page == nil {
		t.Fatal("no D2L page open in the auth context")
	}
	t.Logf("probing: %q\n  %s", page.Title, page.URL)

	client, err := Connect(ContextAuth, page)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// A read-only structural probe. It descends open shadow roots and same-origin
	// iframes, finds anything that reads "submit"/"start quiz", and for each
	// reports: tag, ids, href, whether it sits in an iframe, its box, and CRUCIALLY
	// what element actually sits at its centre point (occlusion) — both in its own
	// document and, mapped through frame offsets, in the top viewport where a real
	// mouse click lands.
	probe := `(() => {
      const out = { frames: [], hits: [] };
      const norm = s => (s||'').replace(/\s+/g,' ').trim();
      const seen = new Set();
      const rec = (root, depth) => {
        let list; try { list = root.querySelectorAll('*'); } catch(e){ return; }
        for (const el of list) {
          if (el.shadowRoot) rec(el.shadowRoot, depth);
          if (el.tagName === 'IFRAME') {
            let same = false, url = '';
            try { url = el.src; if (el.contentDocument) { same = true; rec(el.contentDocument, depth+1); } } catch(e){}
            out.frames.push({src: url, sameOrigin: same, depth: depth});
          }
          const txt = norm(el.innerText || el.textContent).toLowerCase();
          if (!txt) continue;
          if (txt.length > 40) continue;
          if (!/submit|start quiz|next|finish/.test(txt)) continue;
          const tag = el.tagName.toLowerCase();
          if (!['a','button','input','span','div','td','li'].includes(tag) && tag.indexOf('-') < 0) continue;
          const r = el.getBoundingClientRect();
          if (r.width === 0 && r.height === 0) continue;
          const doc = el.ownerDocument;
          const win = doc.defaultView;
          const inIframe = win !== win.top;
          // occlusion in the element's OWN document
          const cx = r.left + r.width/2, cy = r.top + r.height/2;
          let atPoint = '';
          try { const hp = doc.elementFromPoint(cx, cy); atPoint = hp ? (hp.tagName.toLowerCase() + (hp===el?' [SELF]':(el.contains(hp)?' [child]':(hp.contains(el)?' [ancestor]':' [OTHER]')))) : 'none'; } catch(e){ atPoint='err'; }
          // map to top viewport
          let tx = cx, ty = cy, w = win;
          while (w && w.frameElement) { const fr = w.frameElement.getBoundingClientRect(); tx += fr.left; ty += fr.top; w = w.parent; }
          const key = tag+'|'+norm(el.innerText||el.textContent)+'|'+Math.round(cx)+'x'+Math.round(cy);
          if (seen.has(key)) continue; seen.add(key);
          out.hits.push({
            tag: tag,
            text: norm(el.innerText||el.textContent).slice(0,40),
            id: el.id||'',
            href: (el.getAttribute && el.getAttribute('href'))||'',
            role: (el.getAttribute && el.getAttribute('role'))||'',
            clickableAncestor: (el.closest && el.closest('a,button,[role=button]')) ? (el.closest('a,button,[role=button]').tagName.toLowerCase()) : '',
            inIframe: inIframe,
            box: Math.round(r.width)+'x'+Math.round(r.height),
            ownDocAtCenter: atPoint,
            topXY: Math.round(tx)+','+Math.round(ty)
          });
        }
      };
      rec(document, 0);
      return JSON.stringify(out);
    })()`

	raw, err := client.EvalString(ctx, probe)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	t.Logf("STRUCTURE:\n%s", pretty(raw))
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func pretty(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
