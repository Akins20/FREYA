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

// TestLiveReadLength measures the live D2L page's readable text: how long it is,
// and where "submit" falls in it — to see whether the 12000-char browser_read
// clip (and the 20000-char reader cap) cut the Submit button off the bottom, so
// she never sees it to click by text. Read-only. Gated by FREYA_LIVE_DIAG=1.
func TestLiveReadLength(t *testing.T) {
	if os.Getenv("FREYA_LIVE_DIAG") != "1" {
		t.Skip("set FREYA_LIVE_DIAG=1 to probe the live page")
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

	full, err := client.Text(ctx)
	if err != nil {
		t.Fatalf("text: %v", err)
	}
	t.Logf("page: %q", page.Title)
	t.Logf("readable text length (after reader's 20000 cap): %d chars", len(full))

	low := strings.ToLower(full)
	for _, needle := range []string{"submit", "back to questions", "unanswered"} {
		idx := strings.Index(low, needle)
		where := "NOT PRESENT"
		if idx >= 0 {
			beyond12k := ""
			if idx >= 12000 {
				beyond12k = "  <-- PAST the 12000 browser_read clip (she never sees it)"
			}
			where = strings.TrimSpace(strings.ReplaceAll(full[idx:min(idx+40, len(full))], "\n", " ")) +
				"  @char " + itoa(idx) + beyond12k
		}
		t.Logf("  %-18q -> %s", needle, where)
	}
	// Was the reader itself capped (hit 20000)?
	if len(full) >= 20000 {
		t.Logf("NOTE: text is at the 20000-char reader cap — content past that is not read at all.")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
