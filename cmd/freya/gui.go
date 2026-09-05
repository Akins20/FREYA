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
	a  *agent.Agent
	s  *gui.Server
	vs *voiceState
	mu sync.Mutex
}

func (g *guiAsker) Ask(ctx context.Context, input string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Registered like every other exchange, so that a turn started in the window
	// is one the rest of her can see: Ctrl-C reaches it, the spoken "stop"
	// reaches it, stopEverything reaches it, and the daemon's yield loop — which
	// waits on currentTurn() — no longer hands the archive over from underneath
	// it. Before this it ran on context.Background() and was invisible to all
	// four.
	turnCtx, endTurn := beginTurn(ctx, "working on: "+clipLine(input, 60))
	defer endTurn()

	res, err := g.a.Ask(turnCtx, input)
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
		go g.vs.speak(context.Background(), voice.Reply, res.Reply)
	}
	return res.Reply, nil
}

// startGUI serves the window and opens it.
func startGUI(ctx context.Context, a *agent.Agent, store *memory.Store, src *guiSources, dataDir string) error {
	srv, err := gui.New(nil)
	if err != nil {
		return err
	}
	srv.SetAsker(&guiAsker{a: a, s: srv, vs: src.voice})
	srv.SetReader(archiveReader{store: store})
	srv.SetState(src.state)

	url, err := srv.Listen()
	if err != nil {
		return fmt.Errorf("the window could not take a port: %w", err)
	}
	go func() {
		if err := srv.Serve(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "%swindow: %v%s\n", cRed, err, cReset)
		}
	}()

	fmt.Printf("%s  window at %s%s\n", cDim, url, cReset)
	if err := openWindow(ctx, url, dataDir); err != nil {
		// Not fatal. The server is up and the address is printed, so a browser
		// opened by hand still reaches it — which is a worse experience and not a
		// failure of the thing that matters.
		fmt.Fprintf(os.Stderr, "%s  could not open a window (%v) — paste the address above "+
			"into a browser%s\n", cYellow, err, cReset)
	}
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
func guiBanner(url string) string {
	at := url
	if i := strings.Index(at, "/?t="); i > 0 {
		at = at[:i]
	}
	return fmt.Sprintf("window on %s — this terminal stays live for tracing", at)
}
