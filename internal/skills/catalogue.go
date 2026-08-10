package skills

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Akins20/FREYA/internal/llm"
)

// Knowing a tool exists is a different thing from knowing how to call it.
//
// # What the kits got wrong
//
// kits.go narrows what she is SHOWN, and had to grow a safety valve because
// narrowing hides a tool ENTIRELY: she cannot ask for what she was never told
// about, so a mis-routed exchange became a task quietly not done. The valve
// (execute-anyway, count the miss) makes that recoverable, but it only helps
// once she names the tool — and she cannot name what she has never seen.
//
// That is not a hypothetical. browser_sync_logins existed for a hundred sessions
// and was called zero times while she told the user signing in was impossible.
// Routing was not even on. She simply did not know it was there.
//
// # What the platform does instead, and why it is better
//
// Anthropic's tool-search API declares every tool but marks the rare ones
// defer_loading, so the model knows the tool EXISTS while its schema stays out
// of context until searched for; matched schemas are then APPENDED to the
// request rather than swapped into the tool block, which is what preserves the
// cache. Claude Code ships the same shape — deferred tools appear by name only,
// and a ToolSearch call loads the schema on demand.
//
// The asymmetry that makes it work is in the token costs. Measured, not
// estimated — 50 tools catalogue to 4,260 characters, and the full declarations
// for all 133 were measured at ~11,600 tokens when kits were built:
//
//	full declaration   ~87 tokens   name + description + JSON schema
//	catalogue line     ~21 tokens   name + first clause
//
// So naming all 133 costs about 2,800 tokens against 11,600 to declare them. A
// quarter of the price, paid once into a prefix that then caches forever, and
// the blind spot is gone completely. A test pins the ratio.
//
// # Why this belongs in the cached prefix
//
// The catalogue is derived from the registry at startup and never changes within
// a process — it is more static than the persona, which /persona can edit. So it
// is the best possible prefix citizen: written once, cached forever, and unlike
// the kit selection it does not vary per exchange. Deliberately it lists
// EVERYTHING, including tools she was already shown in full. The redundancy costs
// a few hundred tokens; the constancy is worth far more than that.

// Catalogue renders every registered tool as one line, grouped by kit.
//
// This is the "you have these" list, not the "here is how to call them" list —
// find_tools supplies the second when she needs it. Sorted throughout, because
// this text leads the cached prefix and map iteration order would reshuffle it
// on every process start.
func (r *Registry) Catalogue() string {
	r.mu.RLock()
	byKit := map[Kit][]Skill{}
	for name, s := range r.skills {
		k := kitOf(name)
		byKit[k] = append(byKit[k], s)
	}
	r.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString("Every tool I have. The ones offered this turn are described in full " +
		"below; for anything else here, call find_tools with what I am trying to do and " +
		"it hands back the exact arguments. Never conclude something is impossible " +
		"because its tool was not in this turn's list — it is in this catalogue, so it " +
		"exists, and find_tools will reach it.\n")

	for _, k := range AllKits() {
		group := byKit[k]
		if len(group) == 0 {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].Tool.Name < group[j].Tool.Name })
		fmt.Fprintf(&sb, "\n%s:\n", kitTitle(k))
		for _, s := range group {
			fmt.Fprintf(&sb, "  %s — %s\n", s.Tool.Name, gist(s.Tool.Description))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// kitTitle names a kit for a human reading the prompt.
func kitTitle(k Kit) string {
	switch k {
	case KitCore:
		return "Everyday"
	case KitBrowsing:
		return "Browser"
	case KitFiles:
		return "Files and documents"
	case KitDev:
		return "Code and projects"
	case KitVoice:
		return "Voice"
	case KitAdmin:
		return "This machine"
	}
	return string(k)
}

// gist is the first sentence of a description, clipped.
//
// Descriptions in this codebase lead with what the tool is for and follow with
// when to reach for it, so the first sentence is reliably the useful half. The
// rest is what find_tools is for.
func gist(desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return "(no description)"
	}
	// First sentence, first line, or first clause — whichever comes soonest.
	cut := len(desc)
	for _, sep := range []string{". ", ".\n", "\n", " — ", "; "} {
		if i := strings.Index(desc, sep); i >= 0 && i < cut {
			cut = i
		}
	}
	out := strings.TrimSpace(desc[:cut])
	const max = 88
	if len(out) > max {
		if i := strings.LastIndex(out[:max], " "); i > 40 {
			out = out[:i]
		} else {
			out = out[:max]
		}
		out += "…"
	}
	return out
}

// RegisterFinder adds find_tools, the way back from a narrowed tool set.
//
// One round to recover a capability, against a task not done at all. That trade
// is not close, which is why this tool is in the core kit and named in the
// catalogue header: it must be reachable from every exchange, whatever routing
// decided.
func RegisterFinder(r *Registry) {
	r.Register(Skill{
		Tool: llm.Tool{
			Name: "find_tools",
			Description: "Get the exact arguments for any tool in my catalogue that was " +
				"not described in full this turn.\n\n" +
				"Call this the moment a task needs something not in the current list — " +
				"before telling the user it cannot be done, and before improvising a " +
				"worse route with a tool that is listed. Describe what you are TRYING " +
				"TO DO, not the tool name you are guessing at: 'download a file with no " +
				"download button', 'upload a file to a form', 'see what is downloading'. " +
				"The result is the full argument list, ready to call on the next step.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"task": {Type: "string", Description: "What you are trying to do, in plain words."},
			}, "task"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			task := argString(args, "task")
			if task == "" {
				return "", fmt.Errorf("say what you are trying to do, and I will find the tool for it")
			}
			found := r.find(task, 5)
			if len(found) == 0 {
				return fmt.Sprintf("Nothing in the catalogue matches %q. Try describing the "+
					"ACTION rather than the thing — 'save a page that has no download link' "+
					"rather than 'receipt'.", task), nil
			}

			// Surface them for the rest of the exchange, so the next round can
			// actually call what this round found. See Scope.Surfaced.
			names := make([]string, 0, len(found))
			for _, t := range found {
				names = append(names, t.Name)
			}
			ScopeFrom(ctx).surface(names...)

			var sb strings.Builder
			fmt.Fprintf(&sb, "%d tool(s) for %q. These are now callable:\n", len(found), task)
			for _, t := range found {
				sb.WriteString("\n" + declare(t))
			}
			return strings.TrimRight(sb.String(), "\n"), nil
		},
	})
}

// declare renders one tool the way she needs to read it: name, what it is for,
// and every argument with its type and whether it is required. The argument keys
// are the point — a value filed under the wrong key was fourteen consecutive
// failures once, and a list of the real keys is the cheapest cure for it.
func declare(t llm.Tool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n  %s\n", t.Name, strings.TrimSpace(t.Description))
	req := map[string]bool{}
	for _, n := range t.Params.Required {
		req[n] = true
	}
	keys := make([]string, 0, len(t.Params.Properties))
	for k := range t.Params.Properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		sb.WriteString("  (no arguments)\n")
		return sb.String()
	}
	sb.WriteString("  arguments:\n")
	for _, k := range keys {
		p := t.Params.Properties[k]
		mark := ""
		if req[k] {
			mark = " (required)"
		}
		fmt.Fprintf(&sb, "    %s: %s%s — %s\n", k, p.Type, mark, strings.TrimSpace(p.Description))
	}
	return sb.String()
}

// find scores every registered tool against a description of the work.
//
// Lexical and deterministic, for the same reason Route is: a model call to pick
// tools would spend a round answering a question a word list answers, and could
// not be tested. The rarity weight is what makes it usable — "browser" appears in
// forty descriptions and says nothing, "chooser" appears in one and says
// everything, so a plain word count would rank the whole browser family above
// the single tool that solves the problem.
func (r *Registry) find(task string, limit int) []llm.Tool {
	words := terms(task)
	if len(words) == 0 {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	// How many tools mention each word, for the rarity weight.
	docs := map[string]int{}
	text := map[string][]string{}
	for name, s := range r.skills {
		bag := terms(name + " " + s.Tool.Description)
		text[name] = bag
		seen := map[string]bool{}
		for _, w := range bag {
			if !seen[w] {
				seen[w] = true
				docs[w]++
			}
		}
	}
	total := len(r.skills)

	type scored struct {
		tool llm.Tool
		s    float64
	}
	var out []scored
	for name, s := range r.skills {
		if name == "find_tools" {
			continue
		}
		var score float64
		counts := map[string]int{}
		for _, w := range text[name] {
			counts[w]++
		}
		for _, w := range words {
			n := counts[w]
			if n == 0 {
				continue
			}
			// Rarer words are worth more; a word in every tool is worth nothing.
			rarity := 1.0
			if d := docs[w]; d > 0 {
				rarity = float64(total) / float64(d)
			}
			// Diminishing returns on repeats, so a description that says
			// "download" six times does not out-rank the download tool.
			hit := 1.0
			if n > 1 {
				hit = 1.0 + 0.3*float64(n-1)
			}
			score += rarity * hit
			// The name is the strongest signal there is.
			if strings.Contains(name, w) {
				score += 4 * rarity
			}
		}
		if score > 0 {
			out = append(out, scored{s.Tool, score})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].s != out[j].s {
			return out[i].s > out[j].s
		}
		return out[i].tool.Name < out[j].tool.Name // stable, for tests
	})
	if len(out) > limit {
		out = out[:limit]
	}
	tools := make([]llm.Tool, 0, len(out))
	for _, s := range out {
		tools = append(tools, s.tool)
	}
	return tools
}

// terms lowercases and splits on anything that is not a letter or digit, drops
// words too common to discriminate, and stems the handful of endings that
// actually matter here (download/downloads/downloading must all match).
func terms(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(f) < 3 || stopWords[f] {
			continue
		}
		out = append(out, stem(f))
	}
	return out
}

// stem trims the endings that would otherwise split one concept into three.
func stem(w string) string {
	for _, suf := range []string{"ing", "ed", "es", "s"} {
		if len(w) > len(suf)+3 && strings.HasSuffix(w, suf) {
			return strings.TrimSuffix(w, suf)
		}
	}
	return w
}

// stopWords are words that appear everywhere and rank nothing.
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "you": true, "use": true, "this": true,
	"that": true, "with": true, "from": true, "what": true, "when": true, "not": true,
	"are": true, "was": true, "can": true, "how": true, "its": true, "but": true,
	"has": true, "have": true, "will": true, "would": true, "into": true, "than": true,
	"then": true, "there": true, "their": true, "them": true, "your": true, "get": true,
	"one": true, "out": true, "all": true, "any": true, "which": true, "instead": true,
	"user": true, "name": true, "tool": true, "using": true, "want": true, "need": true,
}
