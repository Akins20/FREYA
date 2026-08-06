package skills

import (
	"context"
	"fmt"
	"strings"

	"github.com/akins/jarvis/internal/defect"
	"github.com/akins/jarvis/internal/llm"
)

// Saying that the software got in the way.
//
// An exchange where every call failed files itself; this is for the cases only
// she can see. A tool that technically succeeded and did the wrong thing, an
// error that was true but useless, a capability she plainly needs and does not
// have — none of those show up as a failed call, and until now there was nowhere
// for them to go except into the conversation, where they scrolled away.
//
// It is deliberately not an apology channel. The description says what a useful
// report contains, because a report that says "the browser was difficult" cannot
// be acted on, and one that quotes the error and names the page can.
func RegisterDefects(r *Registry, j *defect.Journal) {
	if j == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "report_problem",
			Description: "Write down that your own software got in the way, so an engineer " +
				"can look at it later. Use when a tool misled you, an error told you a rule " +
				"but not what was actually wrong, something succeeded but did the wrong " +
				"thing, or you needed a capability you do not have. Do NOT use it for a task " +
				"that was merely hard, or for something you got wrong yourself — those are " +
				"not software problems. Filing it changes nothing now; carry on and tell the " +
				"user what you can still do.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"problem": {Type: "string", Description: "What went wrong with the software, " +
					"concretely: what you called, what you expected, what you got. Quote the " +
					"actual error."},
				"goal": {Type: "string", Description: "What the user had asked you to do."},
			}, "problem"),
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			problem := strings.TrimSpace(argRaw(args, "problem"))
			if problem == "" {
				return "", fmt.Errorf("say what went wrong, concretely — what you called, " +
					"what you expected, and what came back")
			}
			rep, err := j.File(defect.Report{
				Kind: defect.Reported,
				Goal: argString(args, "goal"),
				Note: problem,
			})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Noted as %s. It will be looked at; nothing changes right now, "+
				"so carry on with what you can still do and tell the user where you got to.",
				rep.ID), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "problems_list",
			Description: "List the software problems noted so far and what came of them. " +
				"Use when the user asks what has been going wrong, or whether something they " +
				"reported was ever looked at.",
			Params: llm.ObjectSchema(nil),
		},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			all := j.All()
			if len(all) == 0 {
				return "Nothing noted — no software problems on record.", nil
			}
			var sb strings.Builder
			for i, r := range all {
				if i >= 12 {
					fmt.Fprintf(&sb, "…and %d older.\n", len(all)-i)
					break
				}
				sb.WriteString(r.Summary())
				sb.WriteByte('\n')
				// The verdict is the useful part when there is one: it says whether
				// this was her software or her approach, which changes what she
				// should do next time.
				if r.Verdict != "" {
					fmt.Fprintf(&sb, "      %s\n", clip(firstLineOf(r.Verdict), 160))
				}
			}
			return strings.TrimRight(sb.String(), "\n"), nil
		},
	})
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
