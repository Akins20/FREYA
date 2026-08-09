package skills

import (
	"sort"
	"strings"
	"sync"

	"github.com/akins/jarvis/internal/llm"
)

// Showing her the tools this piece of work needs, rather than all of them.
//
// # The measurement that prompted this
//
// 133 registered tools. 38 ever called. The declarations cost about 11,600
// tokens and they lead the request, so they are cached and the money is not the
// problem — the ATTENTION is. And attention is demonstrably what is failing: she
// misfiled an argument fourteen times running, reached for a stale tool over the
// live one, and in a hundred sessions never once called the tool that would have
// solved the sign-in she told the user was impossible.
//
// # Why this is not simply "load tools dynamically"
//
// Tool declarations lead the request, so the set IS the start of the cached
// prefix. A set that changes per task is a prefix that changes per task, and we
// would be trading a measured ~91% cache hit rate for a speculative improvement
// in attention. That is a bad trade made confidently, which is the worst kind.
//
// So kits are chosen ONCE per exchange, before the loop, and held for its whole
// length. Within an exchange the prefix is stable. Across exchanges there are a
// handful of distinct prefixes rather than one — and a handful of warm prefixes
// is exactly what a prefix cache is for.
//
// # The part that makes it safe
//
// A kit that guesses wrong is a SILENT capability loss, which is much worse than
// it sounds. She cannot ask for a tool she was never shown; she simply does not
// do that part of the task, and reports a limitation that is not real. There
// would be no error, no failed call, nothing in the telemetry — the exact shape
// of failure this codebase has spent a week learning to make visible.
//
// So the split is between what is OFFERED and what is AVAILABLE. Everything
// registered stays executable. If she names a tool that exists but was not in
// her kit — from the playbook, from memory, from a description that mentions it
// — it runs, and the miss is recorded. The worst case becomes one wasted round
// instead of a task quietly not done.

// Kit is a named group of tools for one kind of work.
type Kit string

const (
	// KitCore is always offered: memory, files, shell, the browser essentials.
	// Everything she needs to answer a question or start a task of any shape.
	KitCore Kit = "core"
	// KitBrowsing is the full browser: gestures, uploads, downloads, page state.
	KitBrowsing Kit = "browsing"
	// KitFiles is documents, folders, places, backups.
	KitFiles Kit = "files"
	// KitDev is repositories, code search, terminals, Claude delegation.
	KitDev Kit = "dev"
	// KitVoice is speech, style, and the spoken-mode controls.
	KitVoice Kit = "voice"
	// KitAdmin is the machine itself: system status, desktop, telemetry, guard.
	KitAdmin Kit = "admin"
)

// kitOf maps a tool name to the kit that carries it.
//
// By prefix, because the prefixes already encode the grouping — they were chosen
// by whoever wrote each family, which makes them a better classifier than
// anything invented here. A tool matching nothing lands in core, so a newly
// added tool is always reachable: forgetting to classify something must fail
// towards offering it.
func kitOf(name string) Kit {
	switch {
	case coreTools[name]:
		return KitCore
	case strings.HasPrefix(name, "browser_"):
		return KitBrowsing
	case strings.HasPrefix(name, "doc_"), strings.HasPrefix(name, "file_"),
		strings.HasPrefix(name, "folder_"), strings.HasPrefix(name, "place"),
		strings.HasPrefix(name, "backup"), strings.HasPrefix(name, "archive_"):
		return KitFiles
	case strings.HasPrefix(name, "dev_"), strings.HasPrefix(name, "git"),
		strings.HasPrefix(name, "terminal_"), strings.HasPrefix(name, "claude"),
		strings.HasPrefix(name, "project"):
		return KitDev
	case strings.HasPrefix(name, "voice_"), strings.HasPrefix(name, "speak"),
		strings.HasPrefix(name, "listen"):
		return KitVoice
	case strings.HasPrefix(name, "system_"), strings.HasPrefix(name, "desktop_"),
		strings.HasPrefix(name, "screen_"), strings.HasPrefix(name, "window_"),
		strings.HasPrefix(name, "usage_"), strings.HasPrefix(name, "telemetry"),
		strings.HasPrefix(name, "audit"), strings.HasPrefix(name, "volume"):
		return KitAdmin
	}
	return KitCore
}

// coreTools are offered on every exchange whatever it is about.
//
// Deliberately small, and chosen by what she reaches for rather than by what
// seems important: the top of her usage log, plus the handful without which a
// request cannot even be started. browser_open and browser_read are here because
// "look at this page for me" is the single most common thing she is asked, and
// making that wait for a routing decision would be absurd.
var coreTools = map[string]bool{
	"memory_remember": true, "memory_recall": true, "memory_forget": true,
	"recall_episode": true,
	// The real names. These were read_file/write_file/list_dir for weeks — none of
	// which is a tool. Every one fell through to the files kit instead, so on any
	// exchange that did not route to files (a portal, a quiz, a download) she had
	// no way to read or write a file at all. A test now refuses a core entry that
	// names nothing.
	"code_check": true, "site_check": true, "project_new": true, "serve": true, "serve_stop": true, "pdf_design": true, "slides_design": true, "file_read": true, "file_write": true, "folder_list": true, "change_dir": true,
	"run_shell": true, "run_command": true, "web_search": true, "web_fetch": true,
	"browser_open": true, "browser_read": true, "browser_click_text": true,
	"browser_tabs": true, "browser_status": true,
	"note_add": true, "note_list": true, "note_done": true,
	"schedule_self": true, "scheduled_list": true,
	"skill": true, "skill_learn": true, "report_problem": true,
	// The way back from a narrowed set. It has to be reachable from every
	// exchange, whatever routing decided, or the catalogue names tools she has
	// no way to reach — see catalogue.go.
	"find_tools": true,
	// Her only way to put something on the user's own screen. Asked to show them
	// a site she had just built, she pasted the markup into the reply instead —
	// this must never wait on a routing decision.
	"system_open": true,
	// The pause for a person. It has to be reachable whatever the request routed
	// to, because a verification wall can appear on any page at any time.
	"browser_hand_over": true,
	"work_start":        true, "work_list": true, "work_cancel": true,
}

// kitSignals are the words that call for a kit.
//
// Lexical and deterministic on purpose. A model call to classify would cost a
// round trip on every exchange to answer a question a word list answers, and
// would make the tool set — the start of the cached prefix — depend on a
// non-deterministic output. It also could not be tested.
var kitSignals = map[Kit][]string{
	KitBrowsing: {
		"browser", "web", "site", "page", "url", "http", "click", "download",
		"upload", "portal", "log in", "login", "sign in", "signin", "tab",
		"drive", "gmail", "email", "quiz", "form", "submit", "search for",
		"look up", "find online", "shop", "book", "order", "account",
	},
	KitFiles: {
		"file", "folder", "directory", "document", "pdf", "spreadsheet",
		"write up", "draft", "save", "backup", "photo", "picture", "image",
		"download", "rename", "move", "copy", "delete",
	},
	KitDev: {
		"code", "repo", "repository", "git", "commit", "branch", "build",
		"test", "compile", "bug", "function", "refactor", "terminal", "shell",
		"command", "script", "claude", "project",
	},
	KitVoice: {
		"voice", "speak", "say", "louder", "quieter", "accent", "tone",
		"pronounce", "listen", "wake word",
	},
	KitAdmin: {
		"disk", "memory usage", "cpu", "battery", "system", "screen",
		"window", "volume", "mute", "cost", "spend", "usage", "telemetry",
	},
}

// Route picks the kits for one request.
//
// Fail-open by design, twice over: a request that matches nothing gets
// everything, and a request that matches several gets all of them. A tool she
// was not offered is a task quietly not done; a tool she does not need is a few
// hundred tokens she ignores. Those costs are not close, so ambiguity always
// resolves towards more.
func Route(request string) []Kit {
	low := strings.ToLower(request)
	hits := map[Kit]bool{KitCore: true}

	for kit, words := range kitSignals {
		for _, w := range words {
			if strings.Contains(low, w) {
				hits[kit] = true
				break
			}
		}
	}

	// Nothing matched: this is conversation, or something phrased in a way the
	// word list does not cover. Offer everything rather than guess.
	if len(hits) == 1 {
		return AllKits()
	}

	out := make([]Kit, 0, len(hits))
	for k := range hits {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// AllKits is every kit, for the fail-open path and for callers that want the lot.
func AllKits() []Kit {
	return []Kit{KitAdmin, KitBrowsing, KitCore, KitDev, KitFiles, KitVoice}
}

// ToolsFor returns the declarations for a set of kits, sorted.
//
// Sorted for the same reason Tools() is: the order is part of the cached prefix,
// and making it depend on map iteration would reshuffle it every process.
func (r *Registry) ToolsFor(kits []Kit) []llm.Tool {
	want := map[Kit]bool{}
	for _, k := range kits {
		want[k] = true
	}
	// No kits asked for means no filtering. Callers that have not adopted this
	// keep every tool, so adding kits cannot silently narrow anything.
	if len(want) == 0 {
		return r.Tools()
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]llm.Tool, 0, len(r.skills))
	for name, s := range r.skills {
		if want[kitOf(name)] {
			out = append(out, s.Tool)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ToolsWith returns the kit declarations plus any named extras, sorted.
//
// The extras are what find_tools surfaced: tools outside the routed kits that
// this piece of work turned out to need. Sorted with everything else rather than
// appended, because a name is a name — the model has no way to tell a promoted
// declaration from an original one, and should not.
func (r *Registry) ToolsWith(kits []Kit, extra []string) []llm.Tool {
	if len(extra) == 0 {
		return r.ToolsFor(kits)
	}
	want := map[Kit]bool{}
	for _, k := range kits {
		want[k] = true
	}
	also := map[string]bool{}
	for _, n := range extra {
		also[n] = true
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]llm.Tool, 0, len(r.skills))
	for name, s := range r.skills {
		// No kits means nothing was narrowed, so everything is already offered
		// and the extras are already in it.
		if len(want) == 0 || want[kitOf(name)] || also[name] {
			out = append(out, s.Tool)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Offered reports whether a tool would be shown for these kits.
func (r *Registry) Offered(name string, kits []Kit) bool {
	if len(kits) == 0 {
		return true
	}
	for _, k := range kits {
		if kitOf(name) == k {
			return true
		}
	}
	return false
}

// Misses is the registry's record of tools named but not offered.
func (r *Registry) Misses() []string { return r.misses.Report() }

// NoteMiss records that she reached for a tool routing did not offer.
func (r *Registry) NoteMiss(name string) { r.misses.record(name) }

// misses records tools she named that existed but were not offered.
//
// This is the number that says whether the whole idea earns its keep. A tool
// missed repeatedly belongs in the core; a kit missed repeatedly is routed
// wrongly. Without it, kits would be a change we could not evaluate — which,
// given it trades attention against capability, is not a change worth making.
type kitMisses struct {
	mu     sync.Mutex
	byTool map[string]int
}

func (m *kitMisses) record(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.byTool == nil {
		m.byTool = map[string]int{}
	}
	m.byTool[name]++
}

// Report lists what routing failed to offer, most missed first.
func (m *kitMisses) Report() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	type row struct {
		name string
		n    int
	}
	var rows []row
	for n, c := range m.byTool {
		rows = append(rows, row{n, c})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].n > rows[j].n })
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.name+" ×"+itoa(r.n)+" (kit "+string(kitOf(r.name))+")")
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
