// Package claude makes Claude Code available to Freya as a subordinate.
//
// # The division of labour
//
// Freya is the resident: always on, cheap, holding memory of the machine and
// the person using it, able to see the screen and speak. Claude Code is the
// specialist: expensive, stateless between invocations, and far stronger at
// sustained reasoning over a codebase.
//
// Delegating is therefore a judgement about how much quota a task deserves.
//
// # What the cost figure actually means here
//
// Claude Code reports total_cost_usd on every call, and on this machine that is
// *not* a bill. Auth is an OAuth subscription rather than an API key, so usage
// counts against rate-limit windows — five-hourly and weekly — and nothing is
// charged per token. The dollar figure is what the same work would have cost at
// API rates, which makes it a good proxy for "how much of the window did this
// consume" and a bad description of money leaving an account.
//
// It is tracked and reported on that basis. The budget cap is kept for a
// different reason: Claude Code is agentic, an ambiguous instruction can loop
// through many turns, and a ceiling stops a misunderstanding becoming an
// afternoon of quota.
//
// # Sessions are the point
//
// A new invocation starts from nothing. Resuming carries the conversation, the
// files already read and the decisions already made, which is both cheaper and
// better. Session IDs are captured from every run and persisted, so a thread
// begun this morning can be continued tonight.
package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// defaultBudgetUSD caps a single delegation, measured in API-equivalent cost.
//
// On a subscription this is a runaway guard rather than a spending limit: it
// stops an agentic loop before it eats a rate-limit window, which is the
// resource that actually runs out here.
const defaultBudgetUSD = 2.0

// Result is one completed delegation.
type Result struct {
	Text      string
	SessionID string
	CostUSD   float64
	Turns     int
	Duration  time.Duration
	Model     string
	IsError   bool
	Raw       string
}

// wire mirrors the JSON that `claude -p --output-format json` emits.
type wire struct {
	Type       string  `json:"type"`
	Subtype    string  `json:"subtype"`
	IsError    bool    `json:"is_error"`
	Result     string  `json:"result"`
	SessionID  string  `json:"session_id"`
	TotalCost  float64 `json:"total_cost_usd"`
	NumTurns   int     `json:"num_turns"`
	DurationMS int     `json:"duration_ms"`
	ModelUsage map[string]struct {
		CostUSD float64 `json:"costUSD"`
	} `json:"modelUsage"`
}

// Options configure one delegation.
type Options struct {
	// Prompt is the task. A leading slash invokes a skill, e.g. "/review".
	Prompt string
	// Resume continues a specific session by id. Empty starts fresh unless
	// Continue is set.
	Resume string
	// Continue resumes the most recent session in Dir.
	Continue bool
	// Dir is the working directory, which also scopes Continue.
	Dir string
	// Model is an alias — opus, sonnet, haiku, fable — or a full name.
	Model string
	// Effort is low, medium, high, xhigh or max.
	Effort string
	// PermissionMode governs tool use: plan, acceptEdits, dontAsk,
	// bypassPermissions.
	PermissionMode string
	// AllowedTools and DisallowedTools narrow what Claude may do.
	AllowedTools    []string
	DisallowedTools []string
	// ExtraDirs grants access beyond Dir.
	ExtraDirs []string
	// SystemPrompt is appended to Claude's own.
	SystemPrompt string
	// JSONSchema requests structured output matching a schema.
	JSONSchema string
	// BudgetUSD caps the delegation in API-equivalent cost. On a subscription
	// this bounds runaway loops rather than money. Zero uses the default.
	BudgetUSD float64
	// Timeout bounds the call.
	Timeout time.Duration
}

// Session records a delegation thread so it can be picked up later.
type Session struct {
	ID       string    `json:"id"`
	Label    string    `json:"label"`
	Dir      string    `json:"dir"`
	Started  time.Time `json:"started"`
	LastUsed time.Time `json:"last_used"`
	Turns    int       `json:"turns"`
	CostUSD  float64   `json:"cost_usd"`
	LastTask string    `json:"last_task"`
}

// Client runs Claude Code and remembers what it has been asked.
type Client struct {
	// Binary is the executable. Empty means "claude" on PATH.
	Binary string
	// StateDir persists the session index.
	StateDir string

	mu       sync.Mutex
	sessions map[string]*Session
	usage    float64
}

const sessionsFile = "claude-sessions.json"

// New creates a client and loads any remembered sessions.
func New(stateDir string) *Client {
	c := &Client{StateDir: stateDir, sessions: map[string]*Session{}}
	c.load()
	return c
}

// Available reports whether Claude Code can be run at all.
func (c *Client) Available() bool {
	_, err := exec.LookPath(c.binary())
	return err == nil
}

func (c *Client) binary() string {
	if c.Binary != "" {
		return c.Binary
	}
	return "claude"
}

// Version returns the installed Claude Code version.
func (c *Client) Version(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, c.binary(), "--version").Output()
	if err != nil {
		return "", fmt.Errorf("claude: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Run performs one delegation.
func (c *Client) Run(ctx context.Context, opts Options) (*Result, error) {
	if strings.TrimSpace(opts.Prompt) == "" {
		return nil, fmt.Errorf("claude: no task given")
	}
	if !c.Available() {
		return nil, fmt.Errorf("claude: not installed or not on PATH")
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	budget := opts.BudgetUSD
	if budget <= 0 {
		budget = defaultBudgetUSD
	}

	args := []string{"-p", "--output-format", "json", "--max-budget-usd",
		fmt.Sprintf("%.2f", budget)}

	// Resuming is preferred wherever possible: a fresh invocation re-reads
	// everything and re-derives every decision already made.
	switch {
	case opts.Resume != "":
		args = append(args, "--resume", opts.Resume)
	case opts.Continue:
		args = append(args, "--continue")
	}

	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Effort != "" {
		args = append(args, "--effort", opts.Effort)
	}
	if opts.PermissionMode != "" {
		args = append(args, "--permission-mode", opts.PermissionMode)
	}
	if len(opts.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(opts.AllowedTools, ","))
	}
	if len(opts.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(opts.DisallowedTools, ","))
	}
	for _, d := range opts.ExtraDirs {
		args = append(args, "--add-dir", d)
	}
	if opts.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.SystemPrompt)
	}
	if opts.JSONSchema != "" {
		args = append(args, "--json-schema", opts.JSONSchema)
	}
	args = append(args, opts.Prompt)

	cmd := exec.CommandContext(ctx, c.binary(), args...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}

	output, runErr := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("claude: timed out after %s", timeout)
	}

	res, parseErr := parseResult(string(output))
	if parseErr != nil {
		if runErr != nil {
			return nil, fmt.Errorf("claude: %w", runErr)
		}
		return nil, parseErr
	}

	c.record(res, opts)
	if res.IsError {
		return res, fmt.Errorf("claude reported an error: %s", clip(res.Text, 300))
	}
	return res, nil
}

// parseResult reads the JSON envelope.
func parseResult(output string) (*Result, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return nil, fmt.Errorf("claude: no output")
	}
	// The envelope is the last JSON object on the stream.
	if i := strings.LastIndex(trimmed, "\n{"); i >= 0 {
		trimmed = trimmed[i+1:]
	}

	var w wire
	if err := json.Unmarshal([]byte(trimmed), &w); err != nil {
		// Not JSON: treat the whole output as the answer rather than losing it.
		return &Result{Text: strings.TrimSpace(output), Raw: output}, nil
	}

	model := ""
	var best float64
	for name, usage := range w.ModelUsage {
		if usage.CostUSD >= best {
			best, model = usage.CostUSD, name
		}
	}

	return &Result{
		Text:      w.Result,
		SessionID: w.SessionID,
		CostUSD:   w.TotalCost,
		Turns:     w.NumTurns,
		Duration:  time.Duration(w.DurationMS) * time.Millisecond,
		Model:     model,
		IsError:   w.IsError,
		Raw:       output,
	}, nil
}

// record updates the session index and running spend.
func (c *Client) record(r *Result, opts Options) {
	if r.SessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	c.usage += r.CostUSD
	s, ok := c.sessions[r.SessionID]
	if !ok {
		s = &Session{ID: r.SessionID, Started: time.Now(), Dir: opts.Dir}
		c.sessions[r.SessionID] = s
	}
	s.LastUsed = time.Now()
	s.Turns += max(r.Turns, 1)
	s.CostUSD += r.CostUSD
	s.LastTask = clip(opts.Prompt, 160)
	if s.Label == "" {
		s.Label = clip(opts.Prompt, 60)
	}
	c.saveLocked()
}

// Sessions returns known threads, most recently used first.
func (c *Client) Sessions() []Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Session, 0, len(c.sessions))
	for _, s := range c.sessions {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastUsed.After(out[j].LastUsed) })
	return out
}

// Usage reports the API-equivalent cost of everything delegated this run.
//
// Named for what it measures. On a subscription nothing is charged; this is a
// proxy for how much rate-limit window has been consumed.
func (c *Client) Usage() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.usage
}

// Label names a session so it can be referred to conversationally.
func (c *Client) Label(id, label string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.sessions[id]
	if !ok {
		return fmt.Errorf("no session %s", id)
	}
	s.Label = label
	return c.saveLocked()
}

// Find resolves a session by id prefix or by label.
func (c *Client) Find(ref string) (*Session, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if s, ok := c.sessions[ref]; ok {
		return s, true
	}
	lower := strings.ToLower(ref)
	var matches []*Session
	for id, s := range c.sessions {
		if strings.HasPrefix(id, ref) || strings.Contains(strings.ToLower(s.Label), lower) {
			matches = append(matches, s)
		}
	}
	if len(matches) == 0 {
		return nil, false
	}
	// Most recent wins, since "the session about the parser" usually means the
	// one most recently worked on.
	sort.Slice(matches, func(i, j int) bool { return matches[i].LastUsed.After(matches[j].LastUsed) })
	return matches[0], true
}

func (c *Client) path() string { return filepath.Join(c.StateDir, sessionsFile) }

func (c *Client) load() {
	b, err := os.ReadFile(c.path())
	if err != nil || len(b) == 0 {
		return
	}
	_ = json.Unmarshal(b, &c.sessions)
	if c.sessions == nil {
		c.sessions = map[string]*Session{}
	}
}

// saveLocked persists the index. Callers must hold the lock.
func (c *Client) saveLocked() error {
	if c.StateDir == "" {
		return nil
	}
	if err := os.MkdirAll(c.StateDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c.sessions, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path())
}

// Describe summarises a result for display.
func (r *Result) Describe() string {
	var sb strings.Builder
	if r.SessionID != "" {
		fmt.Fprintf(&sb, "session %s", shortID(r.SessionID))
	}
	if r.CostUSD > 0 {
		// Labelled as equivalent, because on a subscription it is not a charge.
		fmt.Fprintf(&sb, " · ~$%.4f equiv", r.CostUSD)
	}
	if r.Turns > 0 {
		fmt.Fprintf(&sb, " · %d turn(s)", r.Turns)
	}
	if r.Duration > 0 {
		fmt.Fprintf(&sb, " · %s", r.Duration.Round(time.Millisecond))
	}
	if r.Model != "" {
		fmt.Fprintf(&sb, " · %s", r.Model)
	}
	return strings.TrimSpace(strings.TrimPrefix(sb.String(), " · "))
}

// Describe summarises a session.
func (s Session) Describe() string {
	return fmt.Sprintf("%s — %q, %d turn(s), ~$%.4f equiv, last used %s",
		shortID(s.ID), s.Label, s.Turns, s.CostUSD, s.LastUsed.Format("2 Jan 15:04"))
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
