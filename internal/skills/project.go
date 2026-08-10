package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/llm"
	"github.com/Akins20/FREYA/internal/playbook"
	"github.com/Akins20/FREYA/internal/term"
)

// Where work goes, and how it gets cleaned up after.
//
// # The heap
//
// Her workspace after two days: about.html, contact.html, copilot.html,
// deck.pdf, demo.pdf, development_audit_report.html,
// development_audit_report_v2.html, for-teams.html, index.html, pricing.html.
// A six-page website, two versions of one report and two unrelated PDFs, in one
// flat directory with nothing saying which belongs to which.
//
// Nothing was wrong with any individual write. Every path she was given was
// relative, every relative path resolves against the workspace root, and so
// everything landed in the same place — including the second attempt at a
// report, which sits beside the first with a _v2 suffix because there was
// nowhere else for it to go.
//
// The fix is not a tidying tool run afterwards. It is having somewhere to put
// things at the moment of writing: a piece of work gets a folder, she moves into
// it, and every relative path after that lands inside it for free. The scope
// machinery already carries a per-thread working directory — this points it
// somewhere deliberate rather than at the root.
//
// # And what she leaves running
//
// A dev server started in a terminal session outlives the exchange, which is the
// point of sessions and also how a machine ends up with four node processes on
// four ports nobody remembers starting. Serving records the port and the session
// together so stopping is one call, and so "what have I got running" has an
// answer.

// serving records what each port is for. The terminal session knows its name and
// its start time and nothing about the folder, and "port 41799" is not an answer
// to "what have I got running".
//
// Process-lifetime on purpose, matching the sessions themselves: a server is tied
// to her, not to the exchange that started it, and not to anything that outlives
// her. Restarting her takes the servers with it, which is correct and is also why
// this list has to report whether a port still ANSWERS rather than whether she
// once started something on it — a URL she handed over before a restart is dead,
// and she has no other way to find that out.
var serving struct {
	sync.Mutex
	dir   map[int]string
	stale []servedRecord // what a previous life was serving, reported once
	file  string         // where the record survives a restart, empty to not keep one
}

func init() { serving.dir = map[int]string{} }

// servedRecord is what she was serving when she was last stopped.
//
// # Why this outlives the servers themselves
//
// The servers are tied to her process, which is correct. The CONSEQUENCE is not:
// she tells someone "it is at localhost:41799", gets restarted an hour later for
// an unrelated reason, and that link is dead with nothing anywhere saying so.
// The session record died with the process too, so she cannot even find out that
// she used to be serving something.
//
// The user only ever speaks to her. There is no terminal where a dead link is
// obvious — they click it, get nothing, and the last thing she said on the
// subject was that it was ready.
//
// So the addresses she handed out survive her, marked as stale, and she is told
// on the first serve_list after a restart. Not restarted automatically: a folder
// that was worth serving an hour ago may not be, and quietly reopening ports on
// startup is the kind of helpfulness nobody asked for.
type servedRecord struct {
	Port int    `json:"port"`
	Dir  string `json:"dir"`
}

// LoadServed restores what she was serving before, so a restart is something she
// can report rather than something that silently invalidates what she said.
func LoadServed(dataDir string) {
	if dataDir == "" {
		return
	}
	path := filepath.Join(dataDir, "serving.json")
	serving.Lock()
	serving.file = path
	serving.Unlock()

	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var prev []servedRecord
	if json.Unmarshal(raw, &prev) != nil {
		return
	}
	serving.Lock()
	defer serving.Unlock()
	for _, r := range prev {
		// Only as history. It is not running — this process has just started —
		// and saying otherwise is the failure being fixed.
		serving.stale = append(serving.stale, r)
	}
}

// persistServed writes the current set. Best effort: failing to record this must
// never fail a serve.
func persistServed() {
	serving.Lock()
	path := serving.file
	out := make([]servedRecord, 0, len(serving.dir))
	for p, d := range serving.dir {
		out = append(out, servedRecord{Port: p, Dir: d})
	}
	serving.Unlock()
	if path == "" {
		return
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	if b, err := json.Marshal(out); err == nil {
		tmp := path + ".tmp"
		if os.WriteFile(tmp, b, 0o644) == nil {
			_ = os.Rename(tmp, path)
		}
	}
}

// RegisterProjects adds project folders, servers and their cleanup.
func RegisterProjects(r *Registry, g *guard.Guard, terminals *term.Manager) {
	if g == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "project_new",
			Description: "Start a piece of work in its own folder, and move into it.\n\n" +
				"Do this FIRST for anything that will be more than one file — a site, a " +
				"report with assets, a script and its data. Afterwards every relative " +
				"path you write lands inside that folder, so the work stays together " +
				"without you thinking about it again.\n\n" +
				"Without it everything you make goes to the same directory and mixes " +
				"with everything you made before: a six-page site, two reports and a " +
				"deck in one heap, with a _v2 suffix where a second attempt had nowhere " +
				"else to go. A name and one call avoids all of that.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "What the work is, in a few words — " +
					"'gisada replica', 'q3 report'. Becomes the folder name."},
			}, "name"),
		},
		Mutates: true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			name := safeFilename(argString(args, "name"))
			if name == "" || name == "page" {
				return "", fmt.Errorf("give the work a name so its folder means something later")
			}
			scope := ScopeFrom(ctx)
			// Always under the workspace root, never relative to wherever she has
			// wandered — otherwise "new project" nests inside the last one.
			root := workspaceRoot(scope)
			dir := filepath.Join(root, name)

			action := guard.Action{Kind: guard.KindWrite, Paths: []string{dir},
				Reason: "create a project folder for " + name}
			return g.Run(ctx, action, func(context.Context) (string, error) {
				existed := false
				if info, err := os.Stat(dir); err == nil && info.IsDir() {
					existed = true
				}
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return "", err
				}
				scope.SetDir(dir)
				if existed {
					return fmt.Sprintf("%s already existed — working in it now. Anything you "+
						"write with a relative path lands here.\n\n%s", dir, listBrief(dir)) +
						designBrief(), nil
				}
				return fmt.Sprintf("Created %s and moved into it. Anything you write with a "+
					"relative path now lands here rather than in the workspace root.", dir) +
					designBrief(), nil
			})
		},
	})

	if terminals == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "serve",
			Description: "Start a local web server for a folder and get the URL back.\n\n" +
				"For looking at something you built the way a browser will see it — a " +
				"site opened as file:// behaves differently from one served over http, " +
				"and anything doing fetch, modules or routing needs a real server.\n\n" +
				"Picks a free port itself and keeps running after this call returns, so " +
				"you can open the URL, change a file and reload. Use serve_stop when " +
				"you are done; leaving servers running is how a machine ends up with " +
				"four of them on four ports nobody remembers starting.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"dir":  {Type: "string", Description: "Folder to serve. Defaults to where you are."},
				"port": {Type: "number", Description: "Optional. Left out, a free one is chosen."},
			}),
		},
		Mutates: true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			dir := argString(args, "dir")
			if dir == "" {
				dir = ScopeFrom(ctx).Dir()
			} else {
				dir = expandIn(ctx, dir)
			}
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				return "", fmt.Errorf("%s is not a folder to serve", dir)
			}
			port := argInt(args, "port", 0)
			if port == 0 {
				var err error
				if port, err = freePort(); err != nil {
					return "", fmt.Errorf("find a free port: %w", err)
				}
			} else if busy(port) {
				return "", fmt.Errorf("port %d is already in use — leave the port out and "+
					"a free one is chosen, or stop whatever is on it first", port)
			}

			if !have("python3") {
				return "", fmt.Errorf("serving needs python3, which is not on PATH")
			}
			session := fmt.Sprintf("serve-%d", port)
			action := guard.Action{Kind: guard.KindExec, Command: "python3",
				Args:   []string{"-m", "http.server", fmt.Sprint(port)},
				Paths:  []string{dir},
				Reason: fmt.Sprintf("serve %s on port %d", dir, port)}

			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				s, err := terminals.Start(session, "", dir)
				if err != nil {
					return "", fmt.Errorf("open a session for the server: %w", err)
				}
				if err := s.Send(fmt.Sprintf("python3 -m http.server %d", port)); err != nil {
					return "", err
				}
				// Confirm it is actually listening rather than reporting a URL that
				// refuses connections — a server that failed to bind looks exactly
				// like one that started, from here.
				if !waitListening(port, 5*time.Second) {
					out := s.Read()
					_ = terminals.Close(session)
					return "", fmt.Errorf("the server did not come up on port %d:\n%s", port, clip(out, 400))
				}
				return fmt.Sprintf("Serving %s at http://localhost:%d — open it with "+
					"browser_open to look at it yourself, or system_open to put it on "+
					"their screen. Session %q; stop it with serve_stop when you are "+
					"done.%s", dir, port, session, oneShotWarning()), nil
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "serve_list",
			Description: "What you have running, and whether it still answers.\n\n" +
				"Servers live as long as you do, not as long as the reply — so one you " +
				"started earlier in the conversation is still up, and one you started " +
				"before you were last restarted is not. A URL you handed someone can be " +
				"dead without anything telling you. Check here before pointing anyone at " +
				"a link you gave out a while ago, and before starting a second server for " +
				"a folder you are already serving.",
			Params: llm.ObjectSchema(map[string]llm.Property{}),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			var live, dead []string
			for _, sess := range terminals.List() {
				if !strings.HasPrefix(sess.Name, "serve-") {
					continue
				}
				port, err := strconv.Atoi(strings.TrimPrefix(sess.Name, "serve-"))
				if err != nil {
					continue
				}
				serving.Lock()
				dir := serving.dir[port]
				serving.Unlock()
				if dir == "" {
					dir = "(folder not recorded)"
				}
				age := time.Since(sess.Started).Round(time.Second)
				line := fmt.Sprintf("http://localhost:%d  →  %s  (up %s)", port, dir, age)
				if busy(port) {
					live = append(live, line)
				} else {
					dead = append(dead, line+" — NOT ANSWERING; serve it again if anyone has that link")
				}
			}
			sort.Strings(live)
			sort.Strings(dead)

			// Anything a previous life was serving, said once and then forgotten.
			// This is the only moment she can learn that a link she gave out has
			// stopped working.
			serving.Lock()
			was := serving.stale
			serving.stale = nil
			serving.Unlock()
			var before string
			if len(was) > 0 {
				var b strings.Builder
				b.WriteString("\n\n[Before you were last restarted you were serving:\n")
				for _, r := range was {
					fmt.Fprintf(&b, "  http://localhost:%d  →  %s\n", r.Port, r.Dir)
				}
				b.WriteString("Those are dead. If you gave anyone one of those addresses, " +
					"say so and serve it again — they have no way of knowing, and the last " +
					"thing you told them was that it was ready.]")
				before = b.String()
			}

			if len(live)+len(dead) == 0 {
				return "Nothing of yours is serving." + before, nil
			}
			var sb strings.Builder
			for _, l := range live {
				sb.WriteString("  " + l + "\n")
			}
			for _, l := range dead {
				sb.WriteString("  " + l + "\n")
			}
			fmt.Fprintf(&sb, "\n%d running, %d not answering.%s%s", len(live), len(dead),
				before, oneShotWarning())
			return strings.TrimLeft(sb.String(), "\n"), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "serve_stop",
			Description: "Stop a server you started, or all of them. Do this when the " +
				"work is finished — a forgotten server holds a port and a process for " +
				"the rest of the day.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"port": {Type: "number", Description: "Which one. Left out, stops every server you started."},
			}),
		},
		Mutates: true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			want := argInt(args, "port", 0)
			var stopped []string
			for _, sess := range terminals.List() {
				name := sess.Name
				if !strings.HasPrefix(name, "serve-") {
					continue
				}
				if want != 0 && name != fmt.Sprintf("serve-%d", want) {
					continue
				}
				if err := terminals.Close(name); err == nil {
					stopped = append(stopped, strings.TrimPrefix(name, "serve-"))
				}
			}
			sort.Strings(stopped)
			// Recorded rather than gated: this stops a subprocess she started, so
			// asking permission would be absurd, but leaving it out of the audit log
			// means /audit under-reports what she did. See Guard.Note.
			g.Note(guard.Action{Kind: guard.KindExec, Command: "stop server(s) " +
				strings.Join(stopped, ", "), Reason: "serve_stop"}, "ok", nil)
			if len(stopped) == 0 {
				if want != 0 {
					return fmt.Sprintf("Nothing of yours is serving on port %d.", want), nil
				}
				return "No servers of yours are running.", nil
			}
			return fmt.Sprintf("Stopped the server(s) on port %s.", strings.Join(stopped, ", ")), nil
		},
	})
}

// workspaceRoot is the top of her working area, whatever folder she is in now.
//
// Taken from the environment rather than the current directory, so starting a
// second project while inside the first puts it beside that one instead of
// nested inside it.
func workspaceRoot(s Scope) string {
	if root := strings.TrimSpace(os.Getenv("FREYA_WORK_DIR")); root != "" {
		return root
	}
	return s.Dir()
}

// listBrief summarises what is already in a folder, so reopening a project says
// what is in it rather than only that it exists.
func listBrief(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return "It is empty."
	}
	names := make([]string, 0, len(entries))
	for i, e := range entries {
		if i >= 12 {
			names = append(names, fmt.Sprintf("… and %d more", len(entries)-12))
			break
		}
		n := e.Name()
		if e.IsDir() {
			n += "/"
		}
		names = append(names, n)
	}
	return "Already in it: " + strings.Join(names, ", ")
}

// freePort asks the kernel for one, which is the only way to get an answer that
// is still true a moment later.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// busy reports whether something already holds a port.
func busy(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return true
	}
	_ = l.Close()
	return false
}

// waitListening waits for the server to accept a connection, so a URL is only
// reported once it answers.
func waitListening(port int, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

// oneShotWarning says when a server will not outlive the turn.
//
// Terminal sessions are closed when the process exits — deliberately, so a
// one-shot run does not leave orphans behind. In the daemon that never happens
// and a server runs until it is stopped. In a `-ask` run it means the URL she
// just reported stops answering the moment she finishes speaking, which she has
// no way to know and the user finds out by clicking a dead link.
func oneShotWarning() string {
	if isDaemon() {
		return ""
	}
	return "\n\n[This is a one-shot run, so the server stops when this turn ends and " +
		"the URL will not answer afterwards. Say so if you hand it over — it is live " +
		"for you right now, but not for them later.]"
}

// isDaemon reports whether this process is the long-running one. Set by the
// daemon at startup rather than sniffed, because guessing from the environment
// is how this sort of check quietly gets it backwards.
var daemonMode bool

// SetDaemonMode marks this process as the persistent one.
func SetDaemonMode(on bool) { daemonMode = on }

func isDaemon() bool { return daemonMode }

// designBrief hands over the design playbook at the moment a piece of work
// starts, rather than waiting to be asked for it.
//
// # Why it is pushed and not offered
//
// The playbooks are progressive disclosure: an index in the skill tool's
// description, bodies fetched on demand. That is the right shape for most of
// them and the wrong shape for this one, because the moment design matters is
// the moment before anything exists, and at that moment she is not looking for
// advice — she is starting.
//
// Measured. After the design playbook landed, three sites came back with zero
// cards, zero emoji, zero em dashes and no 1200px container. Two builds later,
// with the identical rules still sitting in the index: nine cards and two emoji,
// and the skill tool never called once across the whole exchange. The rules did
// not stop working. They stopped being read.
//
// So it rides on project_new, which is the first call of every build and costs
// nothing on the exchanges that never make one. Same shape as every other cure
// today — attach the thing she needs to a call she already makes, rather than
// asking her to remember to make another one.
func designBrief() string {
	s, ok := playbook.Get("design")
	if !ok {
		return ""
	}
	return "\n\n---\n\n[Design rules for anything with a look — read them now, they are " +
		"measured from your own past work and every one of them names something you have " +
		"actually done:]\n\n" + s.Body
}
