package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Akins20/FREYA/internal/agent"
	"github.com/Akins20/FREYA/internal/browser"
	"github.com/Akins20/FREYA/internal/daemon"
	"github.com/Akins20/FREYA/internal/gui"
	"github.com/Akins20/FREYA/internal/memory"
	"github.com/Akins20/FREYA/internal/voice"
)

// archiveReader hands the window the archive without handing it the store.
//
// The conversion is the point: gui.Turn is a copy of memory.Turn, so the window
// cannot reach anything else on the store and cannot grow an opinion about how
// memory works. It reads, and that is all it can do.
type archiveReader struct{ store *memory.Store }

// NewSession cuts the archive here, so what follows is a new row in the rail.
func (a archiveReader) NewSession() (string, error) { return a.store.NewSession() }

func (a archiveReader) Turns() []gui.Turn {
	src := a.store.Turns()
	out := make([]gui.Turn, 0, len(src))
	for _, t := range src {
		// Tool turns are in the archive because she needs them next turn; they are
		// not what a person scrolling their own history is looking for. The trace
		// belongs to the live view, where it is foldable and beside the reply it
		// explains.
		if t.Role == "tool" {
			continue
		}
		out = append(out, gui.Turn{
			Role: t.Role,
			Text: t.Text,
			Tool: t.ToolName,
			At:   t.Timestamp.Format("2006-01-02 15:04"),
			Sess: t.SessionID,
		})
	}
	return out
}

// Wiring the window to the agent that is already running.
//
// # One turn at a time, and one owner for the trace
//
// This used to borrow a.OnThought, a.OnInterim and a.OnTool for the length of a
// turn and put back what it found. That is a data race by construction — the
// swap writes fields another goroutine reads — and it could not compose: opening
// a window silently took the tool trace off the terminal. The hub in trace.go
// owns those fields now and everyone subscribes; this just listens.
//
// The alternative, giving the window its own Agent, is the thing
// internal/memory/journal.go spends a package comment warning against: two
// agents on one archive interleave their turns and corrupt the transcript and
// the cached prefix together.
type guiAsker struct {
	a     *agent.Agent
	s     *gui.Server
	vs    *voiceState
	store *memory.Store
	mu    sync.Mutex

	// run is what actually answers, and exists so the registration around it can
	// be tested without a model. Nil means the agent, which is what every real
	// caller wants; only the test that checks a window turn is visible to
	// currentTurn() ever sets it.
	run func(ctx context.Context, input string) (string, error)
}

// answer is the agent, or whatever was substituted for it.
func (g *guiAsker) answer(ctx context.Context, input string) (string, error) {
	if g.run != nil {
		return g.run(ctx, input)
	}
	res, err := g.a.Ask(ctx, input)
	if err != nil {
		return "", err
	}
	return res.Reply, nil
}

func (g *guiAsker) Ask(ctx context.Context, input string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// The daemon serves this window, and a terminal session takes the archive off
	// the daemon while it runs (Store.Suspend). Asked at the top rather than
	// discovered at the bottom: without this the turn does all its work and then
	// loses the reply to ErrSuspended on the final append.
	if g.store != nil && !g.store.Writing() {
		return "", fmt.Errorf("a terminal session has her memory at the moment — " +
			"close it and this window picks straight back up")
	}

	// Registered like every other exchange, so that a turn started in the window
	// is one the rest of her can see: Ctrl-C reaches it, the spoken "stop"
	// reaches it, stopEverything reaches it, and the daemon's yield loop — which
	// waits on currentTurn() — no longer hands the archive over from underneath
	// it. Before this it ran on context.Background() and was invisible to all
	// four.
	turnCtx, endTurn := beginTurn(ctx, "working on: "+clipLine(input, 60))
	defer endTurn()

	reply, err := g.answer(turnCtx, input)
	if err != nil {
		// Superseded is not failed. Something else took the turn — a spoken
		// request, a stop word — and saying "error" about that would be wrong.
		if turnCtx.Err() != nil && ctx.Err() == nil {
			return "", gui.ErrStopped
		}
		return "", err
	}

	// Typed in the window, spoken aloud when voice is on — through voiceState,
	// never the session directly, because the Speaker is the single audio gate
	// and bypassing it is how two things end up talking at once.
	if g.vs.voiceOn() {
		go g.vs.speak(context.Background(), voice.Reply, reply)
	}
	return reply, nil
}

// serveGUI starts the window server on the agent that already owns the archive
// and returns its address, token and all. It opens nothing: who does the opening
// differs between the daemon (which serves it and may be asked to) and the
// launcher (which only opens), and folding the two together is what produced a
// second agent on the same store.
func serveGUI(ctx context.Context, a *agent.Agent, store *memory.Store, src *guiSources, trace *traceHub) (*gui.Server, string, error) {
	srv, err := gui.New(nil)
	if err != nil {
		return nil, "", err
	}
	srv.SetAsker(&guiAsker{a: a, s: srv, vs: src.voice, store: store})
	srv.SetReader(archiveReader{store: store})
	srv.SetState(src.state)
	srv.SetControls(&guiControls{ctx: ctx, a: a, vs: src.voice})

	// Her trace, on the window's stream. See windowTrace.
	trace.Add(windowTrace(srv))

	url, err := srv.Listen()
	if err != nil {
		return nil, "", fmt.Errorf("the window could not take a port: %w", err)
	}
	go func() {
		if err := srv.Serve(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "%swindow: %v%s\n", cRed, err, cReset)
		}
	}()
	return srv, url, nil
}

// attachWindow is what `freya -gui` does, and it is deliberately almost nothing:
// find the process that owns the archive, ask it where its window is, open that,
// and exit.
//
// # Why the launcher is not the program
//
// The obvious shape — -gui builds an agent and serves a window — is the bug
// internal/memory/journal.go warns about. Starting a session while the daemon is
// up makes the daemon yield the store (main.go's d.Yield), so the window you
// just opened is served by a process that took her memory away from the one that
// was already running her. Two windows would be two agents on one append-only
// archive, interleaving turns and collapsing each other's prompt cache.
//
// So the window belongs to the daemon, and this is a remote control for it. It
// builds no agent, loads no provider, and touches no store — which is also why
// it opens in well under a second rather than the several a full startup takes.
func attachWindow(ctx context.Context, dataDir string) error {
	if !daemon.Running(dataDir) {
		fmt.Printf("%s  no daemon running — starting one%s\n", cDim, cReset)
		if err := daemon.Spawn(ctx, dataDir, 20*time.Second); err != nil {
			return err
		}
	}

	reply, err := daemon.Ask(dataDir, "window")
	if err != nil {
		return fmt.Errorf("ask the daemon for its window: %w", err)
	}
	if !reply.OK || reply.Window == nil {
		msg := reply.Message
		if msg == "" {
			msg = "the daemon gave no address"
		}
		return fmt.Errorf("%s", msg)
	}

	if err := openWindow(ctx, reply.Window.URL, dataDir); err != nil {
		// The server is up regardless, so an address is still worth something:
		// a browser opened by hand reaches the same window.
		fmt.Fprintf(os.Stderr, "%s  could not open a window (%v) — paste this into a "+
			"browser:%s\n  %s\n", cYellow, err, cReset, reply.Window.URL)
		return nil
	}
	fmt.Printf("%s  %s (daemon pid %d)%s\n", cDim, guiBanner(reply.Window.URL), reply.Window.PID, cReset)
	return nil
}

// openWindow launches Chrome as an application window.
//
// # Why a separate profile
//
// --app on her usual profile would open the window among the tabs she drives,
// where a click meant for a page could land on the window and vice versa, and
// where closing the last tab closes the window. A profile of its own makes it an
// independent thing that shows up in the window list on its own.
//
// It is also the profile that must never carry the user's cookies. This page can
// run shell commands; it has no business sharing a session with anything.
func openWindow(ctx context.Context, url, dataDir string) error {
	bin := browser.ChromeBinary()
	if bin == "" {
		return fmt.Errorf("no chrome or chromium on PATH")
	}
	profile := filepath.Join(dataDir, "window-profile")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return err
	}
	cmd := exec.Command(bin,
		"--app="+url,
		"--user-data-dir="+profile,
		"--window-size=1180,800",
		"--no-first-run",
		"--no-default-browser-check",
		// Nothing here needs to survive a restart, and a window that remembers a
		// crashed session greets her with a bar asking about it.
		"--disable-session-crashed-bubble",
	)
	// Detached, and its streams closed: run() waits on nothing, and an inherited
	// pipe is what once made system_open hang until Chrome exited.
	cmd.Stdout, cmd.Stderr, cmd.Stdin = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		// Reaped so the process does not linger as a zombie for the life of the
		// daemon. Nothing waits on the result: the window closing is not an event
		// the rest of the program cares about.
		_ = cmd.Wait()
	}()
	// A beat, so "window at …" and any failure from Chrome do not interleave.
	select {
	case <-ctx.Done():
	case <-time.After(200 * time.Millisecond):
	}
	return nil
}

// guiBanner is what the terminal says while the window is the real interface.
// guiBanner is what the terminal says about the window, address only.
//
// The credential is cut off deliberately. This line goes to stdout, which in the
// daemon is daemon.log — a file that outlives the run, gets read over a
// shoulder, and gets pasted into bug reports. `freya -gui` is how you open the
// window; the address is here so you can see which port it took.
func guiBanner(url string) string {
	at := url
	if i := strings.IndexAny(at, "?"); i > 0 {
		at = strings.TrimSuffix(at[:i], "/")
	}
	return fmt.Sprintf("window on %s", at)
}

// windowTrace translates the hub's vocabulary into the window's.
//
// It is a named function rather than a closure inside serveGUI so that it can be
// tested without building an agent — and it is worth testing, because the way it
// fails is by dropping a kind silently. Taking the per-turn hook swap out in
// favour of the hub left the window with no subscription at all for a while:
// thought bubbles, tool steps and the whole spoken path went nowhere, nothing
// errored, and the window simply sat there looking finished.
func windowTrace(srv *gui.Server) TraceFunc {
	return func(kind, name, call, text string) {
		yes, no := true, false
		switch kind {
		case "thought", "interim":
			srv.Emit(gui.Event{Kind: kind, Text: text})
		case "tool-start":
			srv.Emit(gui.Event{Kind: "tool", Name: name, Call: call, Text: text})
		case "tool-ok":
			srv.Emit(gui.Event{Kind: "tool", Name: name, Call: call, Text: text, OK: &yes})
		case "tool-error":
			srv.Emit(gui.Event{Kind: "tool", Name: name, Call: call, Text: text, OK: &no})
		case "retry":
			// Not a tool. She looked at what she had and decided to go round again,
			// and drawing that as a successful call put a tick next to "unfinished".
			srv.Emit(gui.Event{Kind: "retry", Name: name, Text: text})

		// A spoken exchange, rendered as the turn it is. Without these the window
		// sits blank from the moment the microphone opens until she answers out
		// loud — and the answer never appears in the thread at all, because a
		// voice turn never goes through /ask.
		case "listening", "heard", "speaking":
			srv.Emit(gui.Event{Kind: kind, Text: text})
		case "spoken":
			srv.Emit(gui.Event{Kind: "reply", Text: text})
		case "turn-done":
			srv.Emit(gui.Event{Kind: "done"})
		}
	}
}
