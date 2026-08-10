package skills

import (
	"context"
	"fmt"

	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/llm"
	"github.com/Akins20/FREYA/internal/playbook"
)

// The skill tool: how she reaches her own know-how.
//
// This is the one tool whose result is not an action in the world but knowledge
// about how to act — the bridge between the tools (verbs) and the skills
// (playbooks). She calls it before a kind of work to load the practice for it,
// exactly as a person reads the runbook before touching the unfamiliar system.

// RegisterSkillbook adds the skill-consulting tool, and — when a learned store
// is supplied — the tool that writes one.
//
// The embedded index goes in the tool description below, where it is part of the
// cached prefix and costs nothing per turn. The LEARNED index deliberately does
// not: it changes whenever she learns something, and a changing tool description
// is a changing prefix. It rides the volatile tail instead — see
// internal/playbook/learned.go for the whole argument.
func RegisterSkillbook(r *Registry, g *guard.Guard, learned *playbook.Learned) {
	// The available skills are listed in the description so the model sees, every
	// turn, what know-how exists and when each applies — without spending a tool
	// call to find out.
	desc := "Consult a SKILL — your own know-how for a kind of work — before you " +
		"start that work. A skill is not a tool you run; it is the practice for " +
		"using your tools well, and reading the relevant one first is what turns a " +
		"pile of capabilities into competence. Available skills:\n\n" +
		playbook.Index() + "\n\n" +
		"Consult 'web' before driving any web page, 'assessments' before attempting " +
		"any quiz, test or form you will submit (finishing every question, reading " +
		"the confirmation, not guessing), 'shell' before running commands (it's why " +
		"a redirection or pipe silently does nothing), 'files' before reading or " +
		"editing files, 'signin' before logging in, 'documents' before producing a " +
		"file, 'research' before a real search, 'delegation' before handing work to Claude."

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "skill",
			Description: desc,
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "Which skill to consult: " +
					fmt.Sprintf("%v", playbook.Names())},
			}, "name"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			name := argString(args, "name")
			s, ok := playbook.Get(name)
			if !ok {
				// Then what she taught herself. Checked second so an embedded
				// playbook always wins a name collision — authored practice
				// outranks one evening's conclusion.
				if learned != nil {
					if ls, lok := learned.Get(name); lok {
						return ls.Body, nil
					}
				}
				avail := playbook.Names()
				if learned != nil {
					avail = append(avail, learned.Names()...)
				}
				return "", fmt.Errorf("no skill named %q. Available: %v", name, avail)
			}
			return s.Body, nil
		},
	})

	if learned == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "skill_learn",
			Description: "Record how to do something, so next time you already know.\n\n" +
				"Write one when you worked something out that cost you real effort and " +
				"would cost it again: the door that turned out to be the right one, the " +
				"control that is not where it looks, the order of steps that finally " +
				"worked. Do NOT record a transcript of what you just did — record the " +
				"LESSON, in the order someone would need it, including what not to " +
				"bother trying. A wrong turn you can name is worth as much as the right " +
				"one.\n\n" +
				"Not for one-off facts (remember those) and not for things that were " +
				"easy. This is for practice, not for events.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "Short handle, e.g. 'uopeople-signin'."},
				"summary": {Type: "string", Description: "One line saying when to reach " +
					"for this. It is the only part you always see, so it has to be the part " +
					"that makes you open the rest."},
				"body": {Type: "string", Description: "The ordered steps that worked, and " +
					"the ones that did not."},
			}, "name", "summary", "body"),
		},
		Mutates: true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			s := playbook.Skill{
				Name:    argString(args, "name"),
				Summary: argString(args, "summary"),
				Body:    argRaw(args, "body"),
			}
			if err := learned.Add(s); err != nil {
				return "", err
			}
			// Recorded, not gated. This writes into the data directory, which is on
			// ProtectedPaths, so routing it through Run would refuse it and the tool
			// would stop working — but a skill she teaches herself changes how she
			// behaves from now on, and that belongs in the record of what she did.
			// See Guard.Note.
			g.Note(guard.Action{Kind: guard.KindWrite,
				Command: "learn skill " + s.Name,
				Reason:  s.Summary}, "ok", nil)
			// Read it back rather than confirming blandly: she can act on this
			// within the same exchange, without waiting for the index to carry it.
			return fmt.Sprintf("Learned %q — %s\n\nYou can consult it any time with "+
				"skill(name=%q). What you recorded:\n\n%s",
				s.Name, s.Summary, s.Name, s.Body), nil
		},
	})
}
