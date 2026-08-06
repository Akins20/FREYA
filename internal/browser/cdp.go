package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The DevTools Protocol client.
//
// # Two browser contexts, deliberately
//
// Chrome refuses remote debugging on its default data directory. That is not an
// obstacle to route around but a sensible guard: without it, anything on the
// machine could attach a debugger to a logged-in browser and read every session
// token in it.
//
// So automation runs in its own directories, and there are two:
//
//	auth   a copy of the real profile's login-bearing files, for work that
//	       needs to be signed in — a portal, a dashboard, an account page
//	guest  clean and isolated, for everything else
//
// The split matters because they carry different risk. Anything driven in the
// auth context is acting as the user, with their sessions; the guest context
// can be pointed at an arbitrary page with nothing to lose.

const (
	// AuthPort hosts the context holding the user's logins.
	AuthPort = 9222
	// GuestPort hosts the isolated context.
	GuestPort = 9223
)

// Context identifies which browser to talk to.
type Context string

const (
	// ContextAuth is signed in as the user.
	ContextAuth Context = "auth"
	// ContextGuest is isolated and anonymous.
	ContextGuest Context = "guest"
)

// Port returns the debugging port for a context.
func (c Context) Port() int {
	if c == ContextGuest {
		return GuestPort
	}
	return AuthPort
}

// ProfileDir returns where a context stores its data.
func (c Context) ProfileDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".config", "freya-chrome", string(c))
}

// Target is a page or worker the browser exposes.
type Target struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
	URL   string `json:"url"`
	WS    string `json:"webSocketDebuggerUrl"`
}

// Client speaks CDP to one browser context.
type Client struct {
	Context Context

	mu      sync.Mutex
	conn    *wsConn
	nextID  atomic.Int64
	pending map[int64]chan json.RawMessage
	closed  bool

	// events records what the browser did outside the page — downloads, dialogs,
	// windows opening. See events.go for why the DOM alone is not enough.
	events *eventLog
}

// command is one CDP request.
type command struct {
	ID        int64          `json:"id"`
	Method    string         `json:"method"`
	Params    map[string]any `json:"params,omitempty"`
	SessionID string         `json:"sessionId,omitempty"`
}

// response is one CDP reply or event.
type response struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Method string          `json:"method"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// endpoint returns the HTTP base for a context.
func endpoint(c Context) string {
	return fmt.Sprintf("http://127.0.0.1:%d", c.Port())
}

// Running reports whether a context's browser is listening.
func Running(c Context) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(endpoint(c) + "/json/version")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Version reports the browser build for a context.
func Version(c Context) (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(endpoint(c) + "/json/version")
	if err != nil {
		return "", fmt.Errorf("browser: %s context not reachable: %w", c, err)
	}
	defer resp.Body.Close()

	var v struct {
		Browser string `json:"Browser"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", err
	}
	return v.Browser, nil
}

// Targets lists the open pages in a context.
func Targets(c Context) ([]Target, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(endpoint(c) + "/json/list")
	if err != nil {
		return nil, fmt.Errorf("browser: %s context not reachable: %w", c, err)
	}
	defer resp.Body.Close()

	var all []Target
	if err := json.NewDecoder(resp.Body).Decode(&all); err != nil {
		return nil, err
	}
	pages := make([]Target, 0, len(all))
	for _, t := range all {
		if t.Type == "page" {
			pages = append(pages, t)
		}
	}
	return pages, nil
}

// NewTab opens a page and returns its target.
func NewTab(c Context, url string) (*Target, error) {
	if url == "" {
		url = "about:blank"
	}
	// PUT, not POST or GET. Chrome tightened this endpoint after GET requests
	// to it were found to be triggerable from a page, and now rejects the older
	// verbs with a plain-text complaint rather than JSON.
	req, err := http.NewRequest(http.MethodPut, endpoint(c)+"/json/new?"+url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("browser: open tab: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var t Target
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, fmt.Errorf("browser: unexpected reply opening a tab: %s",
			strings.TrimSpace(string(body)))
	}
	return &t, nil
}

// CloseTab closes a page by target id.
func CloseTab(c Context, id string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(endpoint(c) + "/json/close/" + id)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// Connect attaches to a specific page.
func Connect(c Context, target *Target) (*Client, error) {
	if target.WS == "" {
		return nil, fmt.Errorf("browser: target has no debugger url")
	}
	conn, err := wsDial(target.WS, 15*time.Second)
	if err != nil {
		return nil, err
	}

	client := &Client{
		Context: c,
		conn:    conn,
		pending: map[int64]chan json.RawMessage{},
		events:  &eventLog{},
	}
	go client.readLoop()
	return client, nil
}

// readLoop dispatches replies to whoever is waiting for them.
//
// CDP multiplexes replies and events over one socket, so a reader must run
// continuously: waiting synchronously for a specific id would discard every
// event and reply that arrived first.
func (c *Client) readLoop() {
	for {
		data, err := c.conn.readMessage()
		if err != nil {
			c.failAll()
			return
		}

		var r response
		if err := json.Unmarshal(data, &r); err != nil {
			continue
		}
		if r.ID == 0 {
			// An event. These carry everything the DOM cannot show — a download
			// starting, a dialog blocking the renderer, a click that opened a new
			// window — and they used to be dropped on the floor. See events.go.
			var ev struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(data, &ev) == nil && ev.Method != "" {
				c.handleEvent(ev.Method, ev.Params)
			}
			continue
		}

		c.mu.Lock()
		ch, ok := c.pending[r.ID]
		delete(c.pending, r.ID)
		c.mu.Unlock()

		if !ok {
			continue
		}
		if r.Error != nil {
			ch <- json.RawMessage(fmt.Sprintf(`{"__error":%q}`, r.Error.Message))
		} else {
			ch <- r.Result
		}
		close(ch)
	}
}

func (c *Client) failAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
}

// Call sends a CDP command and waits for its reply.
func (c *Client) Call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	// A client with no socket is a bug somewhere else, but crashing the process
	// over it is not the way to report one — and the dialog handler calls this
	// from a goroutine, where a panic takes down everything rather than failing
	// one action.
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("browser: not connected")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("browser: connection closed")
	}
	if c.pending == nil {
		c.pending = map[int64]chan json.RawMessage{}
	}
	id := c.nextID.Add(1)
	ch := make(chan json.RawMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	payload, err := json.Marshal(command{ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	if err := c.conn.writeMessage(payload); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case result, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("browser: connection lost waiting for %s", method)
		}
		var probe struct {
			Err string `json:"__error"`
		}
		if json.Unmarshal(result, &probe) == nil && probe.Err != "" {
			return nil, fmt.Errorf("browser: %s: %s", method, probe.Err)
		}
		return result, nil
	}
}

// Close ends the connection.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	return c.conn.Close()
}

// Launch starts a browser for a context if one is not already running.
func Launch(ctx context.Context, c Context) error {
	if Running(c) {
		return nil
	}

	dir := c.ProfileDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("browser: create profile dir: %w", err)
	}

	bin := chromeBinary()
	if bin == "" {
		return fmt.Errorf("browser: Chrome not found on PATH")
	}

	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", c.Port()),
		"--user-data-dir=" + dir,
		"--no-first-run",
		"--no-default-browser-check",
		// A realistic window, so tall pages actually scroll and lazy-loaded
		// content below the fold has a fold to be below. Without a fixed size the
		// window inherits whatever the desktop gives it — often large enough that
		// a long page fits with no scroll, which breaks scroll-to-load pages and
		// makes screenshots and element coordinates behave unlike a real browser.
		"--window-size=1280,900",
		"about:blank",
	}
	if c == ContextGuest {
		// The isolated context keeps nothing between runs.
		args = append([]string{"--incognito"}, args...)
	}

	cmd := exec.Command(bin, args...)
	// Detached: the browser must outlive the call that started it, and stay up
	// across the whole conversation rather than one exchange.
	cmd.SysProcAttr = detachedAttr()
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("browser: launch: %w", err)
	}
	_ = cmd.Process.Release()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if Running(c) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return fmt.Errorf("browser: %s context did not come up within 30s", c)
}

func chromeBinary() string {
	for _, name := range []string{
		"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
	} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// SyncScript returns the path of the profile sync helper.
func SyncScript() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "freya", "sync-chrome-profile.sh")
}

// SyncAuthProfile refreshes the auth context from the real Chrome profile.
//
// Logins are a snapshot: signing into something new in the real browser does
// not appear here until this runs again. Chrome must be closed, since these are
// SQLite databases and a live copy is a torn one.
func SyncAuthProfile(ctx context.Context) (string, error) {
	script := SyncScript()
	if _, err := os.Stat(script); err != nil {
		return "", fmt.Errorf("browser: sync script not found at %s", script)
	}
	out, err := exec.CommandContext(ctx, "bash", script).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("browser: sync failed: %s", text)
	}
	return text, nil
}
