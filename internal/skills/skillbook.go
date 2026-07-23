package skills

import (
	"context"
	"fmt"

	"github.com/akins/jarvis/internal/llm"
	"github.com/akins/jarvis/internal/playbook"
)

// The skill tool: how she reaches her own know-how.
//
// This is the one tool whose result is not an action in the world but knowledge
// about how to act — the bridge between the tools (verbs) and the skills
// (playbooks). She calls it before a kind of work to load the practice for it,
// exactly as a person reads the runbook before touching the unfamiliar system.

// RegisterSkillbook adds the skill-consulting tool.
func RegisterSkillbook(r *Registry) {
	// The available skills are listed in the description so the model sees, every
	// turn, what know-how exists and when each applies — without spending a tool
	// call to find out.
	desc := "Consult a SKILL — your own know-how for a kind of work — before you " +
		"start that work. A skill is not a tool you run; it is the practice for " +
		"using your tools well, and reading the relevant one first is what turns a " +
		"pile of capabilities into competence. Available skills:\n\n" +
		playbook.Index() + "\n\n" +
		"Consult 'web' before driving any web page, 'signin' before logging in, " +
		"'documents' before producing a file, 'research' before a real search task, " +
		"'delegation' before deciding whether to hand work to Claude."

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
				return "", fmt.Errorf("no skill named %q. Available: %v", name, playbook.Names())
			}
			return s.Body, nil
		},
	})
}
