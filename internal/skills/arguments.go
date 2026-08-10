package skills

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Akins20/FREYA/internal/llm"
)

// When the value is right and the label is wrong.
//
// # The measured failure
//
// Fourteen consecutive failures in one exchange, every one of them
// `text is required`, with fourteen DIFFERENT argument fingerprints — so she was
// sending something each time, varying it each time, and the thing the tool reads
// was empty every time. She had the label ("Submit Quiz"); she was filing it under
// a key the tool does not read. The two click tools are a step apart —
// browser_click_text takes `text`, browser_click takes `selector` — and she
// alternated between them.
//
// The error she got back was the whole problem: "text is required" states a rule
// and says nothing about what she actually sent. So she read it, concluded she had
// sent text, changed something else, and failed identically. That is a perfect
// loop generator, and the circuit breakers could only bound the damage — they
// cannot fix a mistake she is unable to see.
//
// Nothing else could show her either: telemetry records an argument HASH and never
// the arguments, deliberately, so the tool result is the only channel that can
// carry this. It has to carry it.
//
// # What this does
//
// Two things, in order of confidence. It SALVAGES the unambiguous case — one
// required field missing, one supplied field the tool does not declare, so the
// value plainly belongs in the empty slot — and it names what was actually sent in
// every case it cannot. Both are driven by the tool's own declared schema, so all
// ~106 tools get them without one of them being edited.

// checkArgs reconciles what she sent against what the tool declares.
//
// It returns arguments to run with and a note to attach to the result, or an error
// that names what was sent. A note is not a warning to be suppressed: it is how she
// learns the right key for next time, which is the difference between one recovered
// call and forty.
func (r *Registry) checkArgs(s Skill, args map[string]any) (map[string]any, string, error) {
	missing := missingRequired(s.Tool, args)
	if len(missing) == 0 {
		return args, "", nil
	}
	extra := undeclared(s.Tool, args)

	// One empty slot, one value with nowhere to go. There is only one thing it
	// can mean, and refusing to act on that is pedantry that costs a turn.
	if len(missing) == 1 && len(extra) == 1 {
		fixed := make(map[string]any, len(args))
		for k, v := range args {
			if k != extra[0] {
				fixed[k] = v
			}
		}
		fixed[missing[0]] = args[extra[0]]
		return fixed, fmt.Sprintf(
			"\n(You passed that as %q; this tool calls it %q. I've used it — "+
				"use %q here next time.)", extra[0], missing[0], missing[0]), nil
	}

	return nil, "", r.argError(s.Tool, args, missing, extra)
}

// argError says what is missing, what was sent, and where the value belongs.
//
// Every clause exists because its absence was observed to cost a turn: the rule
// alone leaves her certain she complied; what she sent lets her see the mismatch;
// and naming the tool that DOES take that field is what stops her retrying the
// same misfiling on the sibling tool a second later.
func (r *Registry) argError(t llm.Tool, args map[string]any, missing, extra []string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: missing %s", t.Name, quoteList(missing))

	if len(args) == 0 {
		b.WriteString(", and you sent no arguments at all")
	} else {
		fmt.Fprintf(&b, ". You sent: %s", describeArgs(args))
	}

	// Where the stray value actually belongs. This is the clause that ends the
	// ping-pong between two tools that differ only in what they call the payload.
	for _, e := range extra {
		if owners := r.toolsTaking(e, t.Name); len(owners) > 0 {
			fmt.Fprintf(&b, ". %q is not a parameter of %s — %s takes it",
				e, t.Name, strings.Join(owners, " and "))
		}
	}

	fmt.Fprintf(&b, ". %s takes: %s", t.Name, describeParams(t))
	return fmt.Errorf("%s", b.String())
}

// missingRequired lists declared-required parameters that arrived absent or blank.
//
// Blank counts as missing because that is how it actually failed: the tool read
// its key, got "", and reported it as required. A caller that sends an empty
// string has the same problem as one that sends nothing.
func missingRequired(t llm.Tool, args map[string]any) []string {
	var out []string
	for _, req := range t.Params.Required {
		v, ok := args[req]
		if !ok || v == nil {
			out = append(out, req)
			continue
		}
		if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
			out = append(out, req)
		}
	}
	sort.Strings(out)
	return out
}

// undeclared lists supplied arguments the tool does not declare, keeping only
// non-empty strings — the ones that could be a misfiled value rather than a stray
// flag.
func undeclared(t llm.Tool, args map[string]any) []string {
	var out []string
	for k, v := range args {
		if _, declared := t.Params.Properties[k]; declared {
			continue
		}
		s, isStr := v.(string)
		if !isStr || strings.TrimSpace(s) == "" {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// toolsTaking names other tools that declare this parameter, so a misfiled value
// gets pointed at its home rather than merely rejected.
func (r *Registry) toolsTaking(param, exclude string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []string
	for name, s := range r.skills {
		if name == exclude {
			continue
		}
		if _, ok := s.Tool.Params.Properties[param]; ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	if len(out) > 2 {
		out = out[:2] // two is a pointer; twenty is a haystack
	}
	return out
}

// describeArgs renders what was sent, values included and clipped.
//
// The values matter as much as the keys. "you sent selector" leaves her guessing;
// `selector="Submit Quiz"` shows her the label sitting in the wrong box.
func describeArgs(args map[string]any) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		switch v := args[k].(type) {
		case string:
			parts = append(parts, fmt.Sprintf("%s=%q", k, clip(v, 60)))
		case nil:
			parts = append(parts, k+"=null")
		default:
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return strings.Join(parts, ", ")
}

// describeParams lists what the tool actually accepts, marking the required ones.
func describeParams(t llm.Tool) string {
	req := map[string]bool{}
	for _, r := range t.Params.Required {
		req[r] = true
	}
	names := make([]string, 0, len(t.Params.Properties))
	for k := range t.Params.Properties {
		names = append(names, k)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, n := range names {
		if req[n] {
			parts = append(parts, n+" (required)")
			continue
		}
		parts = append(parts, n)
	}
	if len(parts) == 0 {
		return "no arguments"
	}
	return strings.Join(parts, ", ")
}

func quoteList(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(out, " and ")
}
