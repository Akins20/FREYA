package main

import (
	"path/filepath"
	"sync"
	"time"

	"github.com/Akins20/FREYA/internal/agent"
	"github.com/Akins20/FREYA/internal/config"
	"github.com/Akins20/FREYA/internal/gui"
	"github.com/Akins20/FREYA/internal/schedule"
	"github.com/Akins20/FREYA/internal/sentinel"
	"github.com/Akins20/FREYA/internal/skills"
	"github.com/Akins20/FREYA/internal/telemetry"
	"github.com/Akins20/FREYA/internal/term"
)

// What the window shows outside the conversation, gathered from the things that
// already own it.
//
// Every field here has exactly one source and the window has none of them: it
// renders what this returns and holds no opinion about any of it. That is the
// property that lets the whole front end be rewritten without touching her.
type guiSources struct {
	cfg       *config.Config
	agent     *agent.Agent
	notes     *skills.NoteBook
	tasks     *schedule.Store
	sentinel  *sentinel.Sentinel
	tabs      *skills.Tabs
	terminals *term.Manager
	voice     *voiceState

	// Cost is read from a file that grows all day and the window polls, so the
	// answer is remembered briefly rather than recomputed per request.
	costMu   sync.Mutex
	costAt   time.Time
	costUSD  float64
	costCall int
}

// state is the StateFunc the window polls.
func (s *guiSources) state() gui.State {
	st := gui.State{Busy: currentTurn() != nil}

	if s.agent != nil && s.agent.Provider != nil {
		st.Provider = s.agent.Provider.Name()
	}
	if s.cfg != nil {
		st.Model = s.cfg.Model
	}
	st.Voice = s.voiceState()

	// The plan she is working to, if she wrote one for this exchange.
	if s.agent != nil {
		for _, step := range s.agent.Scope.Plan().Snapshot() {
			st.Plan = append(st.Plan, gui.Step{
				Text: step.Text, State: string(step.State), Note: step.Note,
			})
		}
	}

	// Background work. Unfinished first, because that is what someone glancing at
	// a panel wants to know.
	if jobs != nil {
		for _, j := range jobs.List() {
			st.Jobs = append(st.Jobs, gui.Job{
				ID: j.ID, Goal: j.Goal, State: string(j.State()), For: j.Origin,
			})
		}
	}

	st.Reminders = s.reminders()

	// Peek, never Pending: Pending DRAINS the queue, so a window polling every few
	// seconds would quietly consume every observation before the daemon could say
	// it aloud, and the user would never hear a single one.
	if s.sentinel != nil {
		st.Watchers = len(s.sentinel.Watchers())
		for _, o := range s.sentinel.Peek() {
			st.Watching = append(st.Watching, gui.Watch{
				Summary: o.Summary, Urgency: o.Urgency.String(), Source: o.Source,
			})
		}
	}

	for _, sv := range skills.Serving(s.terminals) {
		st.Servers = append(st.Servers, gui.Serving{
			URL:   "http://localhost:" + itoa(sv.Port),
			Dir:   sv.Dir,
			Alive: sv.Alive,
		})
	}

	for _, t := range s.tabs.Open() {
		st.Tabs = append(st.Tabs, t.Name+" — "+t.URL)
	}

	st.CostToday, st.CallsToday = s.costToday()
	return st
}

// voiceState is the one word for what the microphone and speaker are doing.
//
// Ordered by what matters to see: speaking beats listening, because if she is
// talking that is the thing to know.
func (s *guiSources) voiceState() string {
	v := s.voice
	if v == nil || !v.voiceOn() {
		return "off"
	}
	if v.speaker != nil && v.speaker.Speaking() {
		return "speaking"
	}
	if who := mic.Holder(); who != "" {
		return "hearing"
	}
	if v.listener != nil && v.listener.Listening() {
		return "listening"
	}
	return "on"
}

// reminders folds her notes and her self-set tasks into one list, because from
// the outside they are the same thing: something she said she would do later.
func (s *guiSources) reminders() []gui.Reminder {
	var out []gui.Reminder
	now := time.Now()

	if s.notes != nil {
		for _, n := range s.notes.Outstanding() {
			r := gui.Reminder{Text: n.Text}
			if n.Due != nil {
				r.Due = n.Due.Format("Mon 15:04")
				r.Late = n.Due.Before(now)
			}
			out = append(out, r)
		}
	}
	if s.tasks != nil {
		if pending, err := s.tasks.Pending(); err == nil {
			for _, t := range pending {
				out = append(out, gui.Reminder{
					Text: t.Prompt,
					Due:  t.Due.Format("Mon 15:04"),
					Late: t.Due.Before(now),
				})
			}
		}
	}
	return out
}

// costRefresh is how stale the day's spend may be.
//
// The telemetry file is append-only and grows all day; re-reading it on every
// poll would make the window the most expensive thing watching her. Thirty
// seconds is under the interval at which a number reads as stuck.
const costRefresh = 30 * time.Second

func (s *guiSources) costToday() (float64, int) {
	s.costMu.Lock()
	defer s.costMu.Unlock()
	if time.Since(s.costAt) < costRefresh {
		return s.costUSD, s.costCall
	}
	s.costAt = time.Now()
	s.costUSD, s.costCall = 0, 0
	if s.cfg == nil {
		return 0, 0
	}
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	events, err := telemetry.Load(filepath.Join(s.cfg.DataDir, "telemetry.jsonl"), midnight)
	if err != nil {
		return 0, 0
	}
	sum := telemetry.Summarise(events)
	s.costUSD = sum.TotalCostUSD
	for _, m := range sum.Models {
		s.costCall += m.Calls
	}
	return s.costUSD, s.costCall
}

// itoa keeps the port formatting in one place and off the hot path's imports.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
