package skills

import (
	"context"
	"fmt"
	"strings"
)

// A tool result with room for the truth.
//
// # Why a plain string was not enough
//
// Every skill used to return (string, error), so the only thing distinguishing a
// success from a failure — for the model, and for anything measuring her — was
// the prefix "ERROR: " bolted on by the agent loop. That left nowhere to put the
// two facts that matter most about an action: what it actually did, and whether
// the world changed at all.
//
// The consequences were not theoretical. Click-by-text computed the label of the
// element it really resolved to, parsed it into a Go field, and never used it —
// so a click that landed on the wrong control still reported the text that was
// asked for. A click that fell back to an untrusted synthetic event, the kind
// that silently does nothing on a real portal, returned success. A form fill
// never read anything back. In each case the evidence existed and the type had
// nowhere to carry it.
//
// Outcome is that place. It renders to the same plain text the model has always
// read, so nothing downstream changes; what is new is that the framework can now
// inspect an action's result — annotate it, verify it, and count it.
type Outcome struct {
	// Text is what the model reads: the sentence a handler would have returned.
	Text string
	// Changed reports whether the world observably changed. nil means unknown,
	// which is honest for a read; false is the interesting one, because "I did it
	// and nothing happened" is the failure that used to be invisible.
	Changed *bool
	// Evidence is what actually happened, as distinct from what was asked for —
	// the label really clicked, the value the field really holds, an exit status.
	Evidence string
	// Options is what IS available, attached when an action fails. The best error
	// in the codebase was always the one that said "no option matching X.
	// Available: ..."; this makes that shape the norm rather than the exception.
	Options []string
}

// Changedf builds an Outcome that knows whether it changed anything.
func Changedf(changed bool, format string, args ...any) Outcome {
	return Outcome{Text: fmt.Sprintf(format, args...), Changed: &changed}
}

// Say builds an Outcome carrying only text, for actions that cannot tell.
func Say(format string, args ...any) Outcome {
	return Outcome{Text: fmt.Sprintf(format, args...)}
}

// WithEvidence attaches what actually happened.
func (o Outcome) WithEvidence(format string, args ...any) Outcome {
	o.Evidence = fmt.Sprintf(format, args...)
	return o
}

// Render flattens an outcome into the text the model reads.
//
// The no-change note is the point of the whole type: an action that ran without
// error and moved nothing is the failure mode she cannot see, because it arrives
// looking exactly like success. Saying so plainly is what lets her stop and look
// instead of pressing on believing the click landed.
func (o Outcome) Render() string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(o.Text))
	if o.Evidence != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(o.Evidence)
	}
	if o.Changed != nil && !*o.Changed {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("(Nothing observably changed — the page looks exactly as it did before. " +
			"That usually means the action did not take effect, not that it succeeded quietly. " +
			"Read before acting again.)")
	}
	if len(o.Options) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("What is actually available here:\n- ")
		b.WriteString(strings.Join(o.Options, "\n- "))
	}
	return b.String()
}

// Acting is a handler that reports an Outcome instead of a bare string. Skills
// migrate to it where the extra truth is worth carrying; the rest keep using
// Handler and behave exactly as before.
type Acting func(ctx context.Context, args map[string]any) (Outcome, error)

// withOptions attaches available choices to an error, so a failure carries the
// state needed to succeed rather than only the news that it did not.
func withOptions(err error, options []string) error {
	if err == nil || len(options) == 0 {
		return err
	}
	return fmt.Errorf("%w\n\nWhat is actually available here:\n- %s", err, strings.Join(options, "\n- "))
}
