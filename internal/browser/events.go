package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Everything that happens outside the page.
//
// # The blind spot this closes
//
// She knows the browser only through the DOM: read it, click it, read it again.
// CDP delivers a second channel — events — and the read loop threw every one of
// them away. So a whole category of things was simply invisible:
//
//   - a download starting, finishing, or failing;
//   - a javascript alert or confirm opening, which BLOCKS the renderer, so every
//     later call against that page hangs until something answers it;
//   - a click that opened a new tab or window;
//   - a navigation she did not ask for.
//
// The consequence was worse than not knowing. She clicks Download, the page does
// not change, she concludes nothing happened — and clicks again. Four times. That
// is the silent no-op failure with a different hat on, and no amount of reading
// the DOM can fix it, because the answer was never in the DOM.
//
// # The shape
//
// A bounded log of NOTABLE events, plus one synchronous handler for dialogs.
// Bounded because a page can emit thousands of events a second and none of the
// interesting ones are frequent; synchronous for dialogs because a blocked
// renderer is a hang, and the only cure is answering it promptly.
//
// What this deliberately does NOT do is interpret. It records that a download
// began and what it was called. Whether that download was what the user wanted is
// a judgement, and judgements belong upstream where the goal is known.

// Event is something the browser did that the page cannot show.
type Event struct {
	At   time.Time
	Kind string // "download", "dialog", "newtab", "navigation"
	// Detail is human-readable, and is what reaches the model.
	Detail string
}

// maxEvents is how many notable events one page keeps.
//
// Small: these exist to answer "what just happened because of what I did", a
// question about the last few seconds. A longer history would be a log nobody
// reads, and the events that matter are rare.
const maxEvents = 32

// eventLog is the per-client record of what happened outside the DOM.
type eventLog struct {
	mu     sync.Mutex
	events []Event
}

func (l *eventLog) add(kind, detail string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, Event{At: time.Now(), Kind: kind, Detail: detail})
	if len(l.events) > maxEvents {
		l.events = l.events[len(l.events)-maxEvents:]
	}
}

// since returns events recorded after a moment, oldest first.
func (l *eventLog) since(t time.Time) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Event
	for _, e := range l.events {
		if e.At.After(t) {
			out = append(out, e)
		}
	}
	return out
}

// Since returns what the browser did outside the page since a moment.
//
// This is how an action reports its side effects: note the time, do the thing,
// ask what happened. A click that started a download says so, instead of looking
// identical to a click that did nothing at all.
func (c *Client) Since(t time.Time) []Event {
	if c == nil {
		return nil
	}
	return c.events.since(t)
}

// Watch turns on the event streams worth listening to.
//
// Called once per page, after Page.enable. Failures are not fatal: an older
// Chrome missing a domain should cost the extra awareness, not the session.
func (c *Client) Watch(ctx context.Context) {
	if c == nil {
		return
	}
	// Downloads are reported by the browser target rather than the page, so this
	// arrives whichever page triggered it.
	_, _ = c.Call(ctx, "Browser.setDownloadBehavior", map[string]any{
		"behavior":      "allowAndName",
		"downloadPath":  DownloadDir(),
		"eventsEnabled": true,
	})
	_, _ = c.Call(ctx, "Page.setInterceptFileChooserDialog", map[string]any{"enabled": false})
}

// handleEvent records a notable event and answers the ones that block.
//
// Returning quickly matters. A javascript dialog holds the renderer until it is
// answered, so anything slow here is a hang for every other call against the
// page.
func (c *Client) handleEvent(method string, params json.RawMessage) {
	switch method {
	case "Page.javascriptDialogOpening":
		c.handleDialog(params)

	case "Browser.downloadWillBegin":
		var p struct {
			SuggestedFilename string `json:"suggestedFilename"`
			URL               string `json:"url"`
		}
		_ = json.Unmarshal(params, &p)
		name := p.SuggestedFilename
		if name == "" {
			name = p.URL
		}
		c.events.add("download", fmt.Sprintf("a download started: %s", clipText(name, 120)))

	case "Browser.downloadProgress":
		var p struct {
			State         string  `json:"state"`
			TotalBytes    float64 `json:"totalBytes"`
			ReceivedBytes float64 `json:"receivedBytes"`
			GUID          string  `json:"guid"`
		}
		_ = json.Unmarshal(params, &p)
		switch p.State {
		case "completed":
			c.events.add("download", fmt.Sprintf("a download finished (%s) — saved under %s",
				humanBytes(p.ReceivedBytes), DownloadDir()))
		case "canceled":
			c.events.add("download", "a download was cancelled")
		}

	case "Page.windowOpen":
		var p struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(params, &p)
		c.events.add("newtab", fmt.Sprintf("that opened a new window or tab: %s", clipText(p.URL, 120)))
	}
}

// handleDialog answers a javascript alert/confirm/prompt and records what it
// said.
//
// It must be answered. Chrome blocks the renderer while a dialog is open, so
// leaving one unanswered hangs every subsequent call against that page —
// including the ones that would have discovered the problem. Accepting is the
// right default for the same reason a person clicks OK on "your download will
// start": refusing silently changes what the page does.
//
// A beforeunload dialog is the exception. Accepting it navigates away and throws
// away whatever was on the page, which is exactly the destructive answer.
func (c *Client) handleDialog(params json.RawMessage) {
	var p struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	}
	_ = json.Unmarshal(params, &p)

	accept := p.Type != "beforeunload"
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = c.Call(ctx, "Page.handleJavaScriptDialog", map[string]any{"accept": accept})
	}()

	verb := "dismissed"
	if accept {
		verb = "accepted"
	}
	c.events.add("dialog", fmt.Sprintf("the page opened a %s dialog saying %q — %s it "+
		"(it was blocking the page)", p.Type, clipText(p.Message, 200), verb))
}

// Describe renders events for a tool result, or empty when nothing happened.
func Describe(events []Event) string {
	if len(events) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var lines []string
	for _, e := range events {
		if seen[e.Detail] {
			continue
		}
		seen[e.Detail] = true
		lines = append(lines, "  · "+e.Detail)
	}
	return "\nAlso, outside the page:\n" + strings.Join(lines, "\n")
}

func humanBytes(n float64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", n/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", n/(1<<10))
	}
	return fmt.Sprintf("%.0f B", n)
}

func clipText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// DownloadDir is where downloads land.
//
// Chosen and told to Chrome explicitly, which is what removes the native "Save
// as" dialog from the picture entirely. That dialog is a GTK window, not page
// content: no amount of CDP reaches it, so a download that prompts is a download
// she cannot finish and cannot even see blocking her. Setting a path means the
// file simply appears, and downloadProgress says when.
func DownloadDir() string {
	if d := os.Getenv("FREYA_DOWNLOAD_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp"
	}
	return filepath.Join(home, "Downloads")
}
