package skills

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/llm"
	"github.com/Akins20/FREYA/internal/schedule"
)

// RegisterSchedule gives Freya a way to set a task for her FUTURE self — one the
// daemon actually runs when it comes due. This is the difference between a note
// to the user ("remind you to call the bank") and a promise she can keep ("in
// ten minutes I'll check the download and unzip it"): the first ends in a
// notification, the second ends in work. Without it, "I'll check back shortly"
// is a promise she structurally cannot keep, because when a turn ends nothing
// runs. This is the tool that makes the intention executable.
func RegisterSchedule(r *Registry, store *schedule.Store) {
	if store == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "schedule_self",
			Description: "Set a task for your FUTURE self to run automatically at a later " +
				"time — it will actually run on its own when due, with no one prompting it. " +
				"Reach for this whenever you say you'll follow up, check back, wait for " +
				"something, or continue later: a download that's still going, a page that " +
				"will update, a deadline tonight. This is NOT a reminder to the user " +
				"(that's note_add) — it is work you will do yourself. Write the prompt as a " +
				"complete instruction to yourself, because when it fires you will NOT have " +
				"this conversation's context: say what to do and how to tell it worked.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"prompt": {Type: "string", Description: "The full, self-contained instruction to run when due — you won't remember this chat, so include what to check and what to do."},
				"when":   {Type: "string", Description: "When to run: an offset like '30s', '10m', '2h', '1d', or an absolute time (RFC3339, or 'YYYY-MM-DD HH:MM')."},
				"note":   {Type: "string", Description: "Optional short label of why, shown to the user."},
			}, "prompt", "when"),
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			prompt := strings.TrimSpace(argString(args, "prompt"))
			if prompt == "" {
				return "", fmt.Errorf("prompt is required — what should your future self actually do?")
			}
			due, err := parseWhen(argString(args, "when"))
			if err != nil {
				return "", err
			}
			t, err := store.Add(prompt, due, argString(args, "note"))
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Scheduled to run at %s (in %s): %s",
				t.Due.Format("Mon 15:04"), time.Until(t.Due).Round(time.Second), prompt), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "scheduled_list",
			Description: "List the tasks you've scheduled for your future self that haven't run yet, with their ids.",
			Params:      llm.ObjectSchema(map[string]llm.Property{}),
		},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			pending, err := store.Pending()
			if err != nil {
				return "", err
			}
			// "Nothing scheduled" and "the schedule was thrown away" look identical
			// from here, and only one of them means she has quietly dropped work
			// she promised to come back to.
			lost := ""
			if p := store.Lost(); p != "" {
				lost = fmt.Sprintf("\n\n[The task list could not be read and was set aside "+
					"at %s. Anything scheduled before now is gone — say so rather than "+
					"letting it look like there was never anything there.]", p)
			}
			if len(pending) == 0 {
				return strings.TrimSpace("Nothing scheduled." + lost), nil
			}
			var sb strings.Builder
			for _, t := range pending {
				fmt.Fprintf(&sb, "- [%s] %s (in %s): %s\n",
					t.ID, t.Due.Format("Mon 15:04"),
					time.Until(t.Due).Round(time.Second), t.Prompt)
			}
			return strings.TrimSpace(sb.String()) + lost, nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "scheduled_cancel",
			Description: "Cancel a scheduled self-task by its id (from scheduled_list) before it runs.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"id": {Type: "string", Description: "The task id to cancel."},
			}, "id"),
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			ok, err := store.Cancel(strings.TrimSpace(argString(args, "id")))
			if err != nil {
				return "", err
			}
			if !ok {
				return "No pending task with that id.", nil
			}
			return "Cancelled.", nil
		},
	})
}

// parseWhen reads an offset ('10m', '2h', '1d') or an absolute time, with an
// optional leading "in ". Absolute times parse in the local zone so "15:00"
// means three in the afternoon here, not in UTC.
func parseWhen(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if len(s) >= 3 && strings.EqualFold(s[:3], "in ") {
		s = strings.TrimSpace(s[3:])
	}
	if s == "" {
		return time.Time{}, fmt.Errorf("when is required, e.g. '10m' or a time like '2026-07-24 17:00'")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t, nil
	}
	// Durations: try as written, then lower-cased so "10M" reads as ten minutes.
	if strings.HasSuffix(strings.ToLower(s), "d") {
		if d, err := time.ParseDuration(strings.TrimSuffix(strings.ToLower(s), "d") + "h"); err == nil {
			return time.Now().Add(d * 24), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(d), nil
	}
	if d, err := time.ParseDuration(strings.ToLower(s)); err == nil {
		return time.Now().Add(d), nil
	}
	return time.Time{}, fmt.Errorf("could not read %q as a time; use an offset like '10m' or 'YYYY-MM-DD HH:MM'", s)
}
