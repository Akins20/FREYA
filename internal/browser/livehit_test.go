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

// TestLiveHitTest asks the browser, via CDP's cross-frame hit-test, what element
// actually sits at the coordinate our click-by-text math computes for the live
// "Submit Quiz" confirmation button. DOM.getNodeForLocation pierces iframes the
// way a real mouse does — so this tells us whether a trusted click would land on
// the right button, WITHOUT clicking anything. Read-only. Gated by FREYA_LIVE_DIAG=1.
func TestLiveHitTest(t *testing.T) {
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
		if targets[i].Type == "page" && targets[i].WS != "" && contains(targets[i].URL, "uopeople") {
			page = &targets[i]
			break
		}
	}
	if page == nil {
		t.Fatal("no D2L page open")
	}
	client, err := Connect(ContextAuth, page)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := client.Call(ctx, "DOM.enable", nil); err != nil {
		t.Fatalf("DOM.enable: %v", err)
	}

	// Probe the two coordinates the structural dump reported: the confirm button
	// centre (290,317) and, as a control, the "You are about to submit" text.
	for _, p := range []struct {
		label string
		x, y  float64
	}{
		{"confirm-button-center", 290, 317},
		{"submit-toolbar-center", 689, 317},
	} {
		raw, err := client.Call(ctx, "DOM.getNodeForLocation", map[string]any{
			"x": p.x, "y": p.y, "includeUserAgentShadowDOM": false,
		})
		if err != nil {
			t.Logf("%s (%v,%v): getNodeForLocation error: %v", p.label, p.x, p.y, err)
			continue
		}
		var loc struct {
			BackendNodeID int64  `json:"backendNodeId"`
			FrameID       string `json:"frameId"`
		}
		_ = json.Unmarshal(raw, &loc)

		// Resolve the node to a JS handle and describe it.
		rr, err := client.Call(ctx, "DOM.resolveNode", map[string]any{"backendNodeId": loc.BackendNodeID})
		if err != nil {
			t.Logf("%s: resolveNode error: %v", p.label, err)
			continue
		}
		var res struct {
			Object struct {
				ObjectID string `json:"objectId"`
			} `json:"object"`
		}
		_ = json.Unmarshal(rr, &res)
		if res.Object.ObjectID == "" {
			t.Logf("%s: node at point has no object (frameId=%s)", p.label, loc.FrameID)
			continue
		}
		dr, err := client.Call(ctx, "Runtime.callFunctionOn", map[string]any{
			"objectId": res.Object.ObjectID,
			"functionDeclaration": `function(){
				const path=[]; let e=this;
				for(let i=0;i<4&&e;i++){ path.push(e.tagName.toLowerCase()+(e.id?('#'+e.id):'')); e=e.parentElement; }
				return this.tagName.toLowerCase()+" | id='"+(this.id||'')+"' | text='"+((this.innerText||this.textContent)||'').replace(/\s+/g,' ').trim().slice(0,40)+"' | path="+path.join('<');
			}`,
			"returnByValue": true,
		})
		if err != nil {
			t.Logf("%s: describe error: %v", p.label, err)
			continue
		}
		var desc struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		_ = json.Unmarshal(dr, &desc)
		t.Logf("HIT @ %s (%v,%v) -> %s", p.label, p.x, p.y, desc.Result.Value)
	}
}
