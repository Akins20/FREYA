// Package gui serves Freya's window.
//
// # What this is and is not
//
// It is a view. The agent, memory, guard, tools and voice are the ones already
// running in the process; this hands them a surface and streams back what
// happens. Nothing about her lives here, which is the property that lets the
// window be rewritten or thrown away without touching her.
//
// # Why it is served rather than drawn
//
// Every native toolkit costs the thing that makes this project what it is.
// Electron is a browser and a Node runtime, Tauri is a Rust toolchain, Fyne and
// Wails are large trees with cgo underneath — and "zero external dependencies,
// builds in seconds, one static binary" is not a slogan here, it is why the
// thing starts instantly on a 2014 laptop.
//
// net/http and embed are standard library. The markup ships inside the same
// binary and Chrome opens it with --app=, which has no tab strip and no address
// bar and takes its own place in the window list. It is a window.
//
// # This endpoint can run shell commands
//
// So it refuses anything that is not local and not carrying the token minted for
// this run. Both, not either. A page in the user's own browser can reach
// 127.0.0.1 — same-origin does not protect a loopback service from a form post —
// and the token is what makes the difference between a window and an open door.
package gui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed assets
var assets embed.FS

// Event is one thing that happened during a turn, on its way to the window.
//
// Deliberately flat and stringly-typed: it crosses to JavaScript, and a shape
// the front end has to understand deeply is a shape that has to change whenever
// she does.
type Event struct {
	Kind string `json:"kind"` // thought | tool | interim | reply | error | done | state
	Text string `json:"text,omitempty"`
	Name string `json:"name,omitempty"` // tool name, for kind=tool
	OK   *bool  `json:"ok,omitempty"`   // tool outcome, once known
}

// ErrStopped is a turn that was superseded or called off rather than one that
// failed.
//
// The window renders it as a note, not a failure: something else took the turn —
// a spoken request, the stop word — and calling that an error would be wrong
// about what happened. This package stays ignorant of WHY it stopped and still
// draws it differently.
var ErrStopped = errors.New("stopped")

// Asker is the one thing the window needs from the rest of the program.
//
// An interface rather than *agent.Agent so this package does not import the
// agent, and so the tests can drive a turn without a model. It is also the whole
// contract: everything else the window shows is read from stores it is handed.
type Asker interface {
	Ask(ctx context.Context, input string) (reply string, err error)
}

// Server is the window's back end.
type Server struct {
	token  string
	asker  Asker
	ln     net.Listener
	mu     sync.Mutex
	subs   map[chan Event]struct{}
	inTurn bool
	reader Reader

	confirmSeq int
	waiting    map[string]chan bool
	state      StateFunc
}

// New builds a server with a fresh token.
func New(a Asker) (*Server, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("mint a token: %w", err)
	}
	return &Server{token: hex.EncodeToString(raw), asker: a, subs: map[chan Event]struct{}{}}, nil
}

// SetAsker supplies what runs a turn.
//
// Separate from New because the two need each other: whatever answers a turn has
// to emit events as it goes, and it emits them through this server. Rather than
// a constructor argument nobody can satisfy, or a package-level variable, the
// knot is tied in one place by the caller that owns both.
func (s *Server) SetAsker(a Asker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asker = a
}

// Listen binds a loopback port. Explicitly 127.0.0.1 rather than a bare port,
// which would bind every interface and put a shell on the network.
func (s *Server) Listen() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	s.ln = ln
	return s.URL(), nil
}

// URL is the address to open, token included.
func (s *Server) URL() string {
	if s.ln == nil {
		return ""
	}
	return fmt.Sprintf("http://%s/?t=%s", s.ln.Addr().String(), s.token)
}

// Serve runs until the context ends.
func (s *Server) Serve(ctx context.Context) error {
	if s.ln == nil {
		if _, err := s.Listen(); err != nil {
			return err
		}
	}
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/", s.guard(http.FileServer(http.FS(sub))))
	mux.Handle("/events", s.guard(http.HandlerFunc(s.events)))
	mux.Handle("/ask", s.guard(http.HandlerFunc(s.ask)))
	mux.Handle("/history", s.guard(http.HandlerFunc(s.history)))
	mux.Handle("/conversation", s.guard(http.HandlerFunc(s.conversation)))
	mux.Handle("/answer", s.guard(http.HandlerFunc(s.answer)))
	mux.Handle("/state", s.guard(http.HandlerFunc(s.stateHandler)))

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	if err := srv.Serve(s.ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// guard refuses anything that is not local and carrying this run's token.
//
// # Why both, and why the token is not in a header
//
// Loopback alone is not a boundary. Any page in any tab can post a form to
// 127.0.0.1 without reading the reply, and that is enough when the endpoint runs
// commands. The token is what a page from elsewhere cannot know.
//
// It arrives as a query parameter because the window is opened by handing Chrome
// a URL, and there is no earlier moment to set a header in. The page moves it to
// a cookie on first load so it stops appearing in later requests; the query is
// only ever the way in.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || (host != "127.0.0.1" && host != "::1") {
			http.Error(w, "local only", http.StatusForbidden)
			return
		}
		got := r.URL.Query().Get("t")
		if got == "" {
			if c, err := r.Cookie("freya_token"); err == nil {
				got = c.Value
			}
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			http.Error(w, "not this window", http.StatusForbidden)
			return
		}
		if r.URL.Query().Get("t") != "" {
			http.SetCookie(w, &http.Cookie{
				Name: "freya_token", Value: s.token, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteStrictMode,
			})
		}
		next.ServeHTTP(w, r)
	})
}

// Emit sends an event to every open window.
//
// Never blocks. A window that has stopped reading — minimised, asleep, gone —
// must not be able to stall the turn producing the events, so a full channel
// drops the event rather than waiting. Losing a thought bubble is nothing;
// stalling her mid-task is not.
func (s *Server) Emit(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// events is the stream the window listens on.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ch := make(chan Event, 64)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// A beat, so a proxy or a sleeping tab does not silently drop the stream and
	// leave the window looking connected when it is not.
	beat := time.NewTicker(20 * time.Second)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			fmt.Fprint(w, ": beat\n\n")
			flusher.Flush()
		case e := <-ch:
			b, err := json.Marshal(e)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

// ask starts a turn.
//
// One at a time, and it says so rather than queueing. Two turns at once would
// interleave into one archive and one cached prefix, which internal/memory's
// journal explains at length is how both get corrupted — and the window is not
// the place to discover that.
func (s *Server) ask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "post", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		http.Error(w, "say something", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if s.inTurn {
		s.mu.Unlock()
		http.Error(w, "she is already working on something", http.StatusConflict)
		return
	}
	asker := s.asker
	s.inTurn = true
	s.mu.Unlock()

	if asker == nil {
		s.mu.Lock()
		s.inTurn = false
		s.mu.Unlock()
		http.Error(w, "nothing is wired up to answer", http.StatusServiceUnavailable)
		return
	}

	w.WriteHeader(http.StatusAccepted)

	// Detached from the request: the window may be closed mid-turn, and the work
	// she has already started should finish and land in the archive regardless.
	go func() {
		defer func() {
			s.mu.Lock()
			s.inTurn = false
			s.mu.Unlock()
			s.Emit(Event{Kind: "done"})
		}()
		reply, err := asker.Ask(context.Background(), body.Text)
		if err != nil {
			s.Emit(Event{Kind: "error", Text: err.Error()})
			return
		}
		s.Emit(Event{Kind: "reply", Text: reply})
	}()
}

// Busy reports whether a turn is running, for anything that needs to know
// without asking over HTTP.
func (s *Server) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inTurn
}

// ---- history --------------------------------------------------------------

// Turn is one entry of a past conversation, as the window needs it.
//
// A copy of memory.Turn rather than the thing itself, so this package does not
// import the archive. The window is a view; giving it the store's type would let
// it grow opinions about the store.
type Turn struct {
	Role string `json:"role"` // user | assistant | tool
	Text string `json:"text"`
	Tool string `json:"tool,omitempty"`
	At   string `json:"at"`
	Sess string `json:"-"`
}

// Conversation is one session, summarised for the rail.
type Conversation struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	At    string `json:"at"`
	Turns int    `json:"turns"`
}

// Reader supplies past turns, newest last, exactly as the archive holds them.
type Reader interface {
	Turns() []Turn
}

// SetReader supplies the history the rail lists.
func (s *Server) SetReader(r Reader) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reader = r
}

// conversations groups turns into sessions, newest first.
//
// The archive is a flat append-only log with a session id on every turn, which
// is the right shape for the thing it is and the wrong shape for a list of
// conversations. Grouping happens here rather than in the store because it is a
// question only the window asks.
func conversations(turns []Turn) []Conversation {
	order := []string{}
	byID := map[string]*Conversation{}
	for _, t := range turns {
		if t.Sess == "" {
			continue
		}
		c, ok := byID[t.Sess]
		if !ok {
			c = &Conversation{ID: t.Sess, At: t.At}
			byID[t.Sess] = c
			order = append(order, t.Sess)
		}
		c.Turns++
		c.At = t.At
		// The first thing the user said is the only title that means anything.
		// A generated one would be another model call to name something they can
		// already recognise from their own words.
		if c.Title == "" && t.Role == "user" {
			c.Title = firstLine(t.Text, 60)
		}
	}
	out := make([]Conversation, 0, len(order))
	for i := len(order) - 1; i >= 0; i-- {
		c := byID[order[i]]
		if c.Title == "" {
			// A session with no user turn is a background job or a watcher note.
			// Named for what it is rather than left blank.
			c.Title = "(no message)"
		}
		out = append(out, *c)
	}
	return out
}

// firstLine is a title: the opening line, cut on a word.
func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = s[:i]
	}
	if len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)[:max]
	if i := strings.LastIndex(string(r), " "); i > max/2 {
		return string(r[:i]) + "…"
	}
	return string(r) + "…"
}

// history lists past conversations.
func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	reader := s.reader
	s.mu.Unlock()
	if reader == nil {
		writeJSON(w, []Conversation{})
		return
	}
	all := conversations(reader.Turns())
	if len(all) > 60 {
		all = all[:60]
	}
	writeJSON(w, all)
}

// conversation returns one session's turns.
func (s *Server) conversation(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "which conversation", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	reader := s.reader
	s.mu.Unlock()
	if reader == nil {
		writeJSON(w, []Turn{})
		return
	}
	var out []Turn
	for _, t := range reader.Turns() {
		if t.Sess == id {
			out = append(out, t)
		}
	}
	writeJSON(w, out)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ---- asking permission in the window -------------------------------------

// Pending is a confirmation waiting on the person in front of the window.
type Pending struct {
	ID      string `json:"id"`
	Command string `json:"command"`
	Reason  string `json:"reason"`
	Risk    string `json:"risk"`
	Preview string `json:"preview"`
}

// Confirm asks the window and blocks until it answers.
//
// # Why the window has to be able to answer at all
//
// The guard refuses anything above its auto-approve line when nobody can be
// asked, which is right — but "nobody" was being decided by whether stdin is a
// terminal. Open the window and the terminal is exactly where the person is
// not, so every deletion, every system path, every escalation would come back
// refused with the user sitting there looking at her.
//
// # And why it times out rather than waiting
//
// A window that is closed, asleep or on another desktop cannot answer, and a
// turn blocked forever on a question nobody will see is worse than a refusal:
// the refusal at least says so. Silence after a minute is read as no, which is
// the safe direction and the one the guard already takes when it cannot ask.
func (s *Server) Confirm(ctx context.Context, command, reason, risk, preview string) bool {
	s.mu.Lock()
	if len(s.subs) == 0 {
		// No window open. Not this channel's question to answer.
		s.mu.Unlock()
		return false
	}
	s.confirmSeq++
	id := fmt.Sprintf("c%d", s.confirmSeq)
	answer := make(chan bool, 1)
	if s.waiting == nil {
		s.waiting = map[string]chan bool{}
	}
	s.waiting[id] = answer
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.waiting, id)
		s.mu.Unlock()
	}()

	body, _ := json.Marshal(Pending{ID: id, Command: command, Reason: reason, Risk: risk, Preview: preview})
	s.Emit(Event{Kind: "confirm", Text: string(body)})

	select {
	case ok := <-answer:
		return ok
	case <-ctx.Done():
		return false
	case <-time.After(confirmWait):
		s.Emit(Event{Kind: "confirm-timeout", Text: id})
		return false
	}
}

// confirmWait is how long a question waits for a person.
//
// Long enough to read a preview and think; short enough that a turn does not
// hang until someone notices. The guard's answer when nobody replies is the same
// as its answer when nobody can be asked, which is no.
const confirmWait = 90 * time.Second

// answer takes the window's yes or no.
func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "post", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID string `json:"id"`
		OK bool   `json:"ok"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	ch, ok := s.waiting[body.ID]
	s.mu.Unlock()
	if !ok {
		// Already answered, or timed out and gone. Saying so is better than a
		// silent 200 that leaves the window thinking it decided something.
		http.Error(w, "that question is no longer open", http.StatusGone)
		return
	}
	select {
	case ch <- body.OK:
	default:
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- what she has on, right now ------------------------------------------

// Step is one item of the plan she wrote for the work in front of her.
type Step struct {
	Text  string `json:"text"`
	State string `json:"state"` // todo | doing | done | dropped
	Note  string `json:"note,omitempty"`
}

// Job is a piece of background work.
type Job struct {
	ID    string `json:"id"`
	Goal  string `json:"goal"`
	State string `json:"state"`
	For   string `json:"for,omitempty"`
}

// Reminder is a note with a time on it.
type Reminder struct {
	Text string `json:"text"`
	Due  string `json:"due,omitempty"`
	Late bool   `json:"late,omitempty"`
}

// Watch is something a watcher noticed and has not said yet.
type Watch struct {
	Summary string `json:"summary"`
	Urgency string `json:"urgency"`
	Source  string `json:"source"`
}

// Server is one of hers, and whether the address still answers.
type Serving struct {
	URL   string `json:"url"`
	Dir   string `json:"dir"`
	Alive bool   `json:"alive"`
}

// State is everything the window shows outside the conversation.
//
// One struct and one endpoint rather than six, because these are all answers to
// the same question — what is she doing and what is waiting — and six polls to
// draw one panel is six chances for the panels to disagree with each other.
type State struct {
	Model      string     `json:"model"`
	Provider   string     `json:"provider"`
	Busy       bool       `json:"busy"`
	Voice      string     `json:"voice"`
	CostToday  float64    `json:"costToday"`
	CallsToday int        `json:"callsToday"`
	Plan       []Step     `json:"plan,omitempty"`
	Jobs       []Job      `json:"jobs,omitempty"`
	Reminders  []Reminder `json:"reminders,omitempty"`
	Watching   []Watch    `json:"watching,omitempty"`
	Servers    []Serving  `json:"servers,omitempty"`
	Tabs       []string   `json:"tabs,omitempty"`
	Watchers   int        `json:"watchers"`
}

// StateFunc gathers it. A function rather than an interface because every field
// comes from somewhere different and the only caller that can reach all of them
// is the one that built them.
type StateFunc func() State

// SetState supplies the gatherer.
func (s *Server) SetState(f StateFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = f
}

func (s *Server) stateHandler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	f := s.state
	busy := s.inTurn
	s.mu.Unlock()
	if f == nil {
		writeJSON(w, State{Busy: busy})
		return
	}
	st := f()
	st.Busy = busy
	writeJSON(w, st)
}
