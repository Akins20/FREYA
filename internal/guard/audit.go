package guard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Record is one line of the audit trail.
type Record struct {
	Time      time.Time     `json:"time"`
	Action    Action        `json:"action"`
	Risk      string        `json:"risk"`
	Rule      string        `json:"rule,omitempty"`
	Preview   string        `json:"preview,omitempty"`
	Confirmed bool          `json:"confirmed,omitempty"`
	Outcome   string        `json:"outcome"` // ok | error | denied | forbidden | dry-run
	Error     string        `json:"error,omitempty"`
	Duration  time.Duration `json:"duration_ns,omitempty"`
}

// Log is an append-only record of everything guard decided.
//
// Append-only and opened O_APPEND deliberately: the log's value is that it
// cannot be quietly rewritten after the fact. `auditTamper` in rules.go refuses
// commands that would delete or truncate it.
type Log struct {
	mu   sync.Mutex
	file *os.File
	path string
}

const auditFile = "audit.jsonl"

// OpenLog opens (or creates) the audit log in dir.
func OpenLog(dir string) (*Log, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("guard: create audit dir: %w", err)
	}
	path := filepath.Join(dir, auditFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("guard: open audit log: %w", err)
	}
	return &Log{file: f, path: path}, nil
}

// Append writes one record. Failures here must not break the caller — an
// unwritable log is a problem to report, not a reason to abandon the action
// the user already approved.
func (l *Log) Append(r Record) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}

	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = l.file.Write(append(line, '\n'))
	return err
}

// Close releases the log file.
func (l *Log) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Path reports where the log lives.
func (l *Log) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Recent reads back the last n records, newest first.
func (l *Log) Recent(n int) ([]Record, error) {
	if l == nil {
		return nil, nil
	}
	b, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var out []Record
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(lines[i]), &r); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// Summary renders a record as a single readable line.
func (r Record) Summary() string {
	cmd := commandLine(r.Action)
	if r.Action.Elevated {
		cmd = "sudo " + cmd
	}
	marker := map[string]string{
		"ok": "✓", "error": "✗", "denied": "–", "forbidden": "⨯", "dry-run": "·",
	}[r.Outcome]
	if marker == "" {
		marker = "?"
	}
	line := fmt.Sprintf("%s %s [%s] %s",
		r.Time.Format("15:04:05"), marker, r.Risk, truncateLine(cmd, 70))
	if r.Error != "" {
		line += " — " + truncateLine(r.Error, 60)
	}
	return line
}

func truncateLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
