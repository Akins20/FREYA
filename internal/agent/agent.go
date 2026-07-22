// Package agent runs Freya's think-act loop: assemble context, ask the model,
// execute any tools it requests, feed results back, repeat until it answers.
package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/akins/jarvis/internal/llm"
	"github.com/akins/jarvis/internal/memory"
	"github.com/akins/jarvis/internal/skills"
)

// maxToolRounds bounds one exchange. Genuine exploration — list projects, check
// git, grep for the error — legitimately runs several rounds deep, so the cap
// sits well above typical use; it exists to stop a malfunctioning model burning
// tokens indefinitely, not to curb normal work.
const maxToolRounds = 12

// Agent is one configured assistant.
type Agent struct {
	Provider llm.Provider
	Skills   *skills.Registry
	Store    *memory.Store
	Builder  *memory.ContextBuilder
	Persona  Persona

	// OnTool is called before and after each tool execution, for tracing.
	OnTool func(event, name, detail string)
}

// Result is the outcome of one user exchange.
type Result struct {
	Reply     string
	Snapshot  memory.Snapshot
	ToolCalls []string
	Rounds    int
}

// New builds an agent.
func New(p llm.Provider, reg *skills.Registry, store *memory.Store,
	builder *memory.ContextBuilder, persona Persona) *Agent {
	return &Agent{
		Provider: p,
		Skills:   reg,
		Store:    store,
		Builder:  builder,
		Persona:  persona,
	}
}

// Ask runs one exchange to completion, including any tool round-trips.
//
// Every turn — user, assistant, and each tool result — is archived, so the
// working set replays a faithful transcript on the next request rather than a
// reconstruction.
func (a *Agent) Ask(ctx context.Context, input string) (*Result, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return &Result{Reply: "Say something and I'll get to work."}, nil
	}

	// The persona may have changed since construction; rebuild the brief.
	a.Builder.Persona = a.Persona.Prompt(a.Skills.Names())

	// Build context before archiving this turn: the working set covers history,
	// and the new input is appended explicitly as the final message.
	system, history, snap := a.Builder.Build(input)

	userTurn, err := a.Store.AppendTurn(memory.Turn{Role: "user", Text: input})
	if err != nil {
		return nil, fmt.Errorf("archive user turn: %w", err)
	}
	if a.Builder.Index != nil {
		a.Builder.Index.Add(userTurn.ID, "turn", input)
	}

	msgs := append(history, llm.Message{Role: llm.RoleUser, Text: input})
	tools := a.Skills.Tools()
	result := &Result{Snapshot: snap}

	for round := 1; round <= maxToolRounds; round++ {
		result.Rounds = round

		resp, err := a.Provider.Chat(ctx, llm.Request{
			System:   system,
			Messages: msgs,
			Tools:    tools,
		})
		if err != nil {
			return nil, err
		}

		// No tools requested: this is the final answer.
		if len(resp.ToolCalls) == 0 {
			reply := strings.TrimSpace(resp.Text)
			if reply == "" {
				reply = "I've got nothing useful to add there."
			}
			assistantTurn, err := a.Store.AppendTurn(memory.Turn{Role: "assistant", Text: reply})
			if err != nil {
				return nil, fmt.Errorf("archive assistant turn: %w", err)
			}
			if a.Builder.Index != nil {
				a.Builder.Index.Add(assistantTurn.ID, "turn", reply)
			}
			result.Reply = reply
			return result, nil
		}

		// Record the assistant's tool-call turn so the model sees its own
		// request on the next round. Signatures ride along untouched.
		msgs = append(msgs, llm.Message{
			Role:      llm.RoleAssistant,
			Text:      resp.Text,
			ToolCalls: resp.ToolCalls,
		})

		for _, call := range resp.ToolCalls {
			result.ToolCalls = append(result.ToolCalls, call.Name)
			a.trace("start", call.Name, formatArgs(call.Args))

			output, err := a.Skills.Execute(ctx, call.Name, call.Args)
			if err != nil {
				// Errors go back to the model as text: a failed tool is
				// information it can act on, not a reason to abort the turn.
				output = "ERROR: " + err.Error()
				a.trace("error", call.Name, err.Error())
			} else {
				a.trace("ok", call.Name, truncate(output, 200))
			}

			// Tool output is archived but deliberately not indexed: it is bulky,
			// re-derivable by re-running the tool, and would swamp BM25 scores
			// against the conversation that actually matters.
			if _, err := a.Store.AppendTurn(memory.Turn{
				Role: "tool", Text: output, ToolName: call.Name,
			}); err != nil {
				return nil, fmt.Errorf("archive tool turn: %w", err)
			}
			msgs = append(msgs, llm.Message{
				Role:       llm.RoleTool,
				Text:       output,
				ToolName:   call.Name,
				ToolCallID: call.ID,
			})
		}
	}

	// Round limit reached. Rather than discarding everything gathered so far,
	// ask once more with no tools available — the model cannot call anything,
	// so it must answer from what it already has.
	final, err := a.Provider.Chat(ctx, llm.Request{
		System: system + "\n\nYou have reached the tool-call limit for this exchange. " +
			"Answer now using only what you have already gathered. Do not request more tools. " +
			"If it is genuinely not enough, say briefly what you still need.",
		Messages: msgs,
	})

	reply := ""
	if err == nil && final != nil {
		reply = strings.TrimSpace(final.Text)
	}
	if reply == "" {
		reply = fmt.Sprintf("I used all %d tool rounds without landing an answer. "+
			"Narrow the question and I'll try again.", maxToolRounds)
	}

	if _, err := a.Store.AppendTurn(memory.Turn{Role: "assistant", Text: reply}); err != nil {
		return nil, err
	}
	result.Reply = reply
	return result, nil
}

func (a *Agent) trace(event, name, detail string) {
	if a.OnTool != nil {
		a.OnTool(event, name, detail)
	}
}

func formatArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, 0, len(args))
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("%s=%v", k, truncate(fmt.Sprint(v), 80)))
	}
	return strings.Join(parts, " ")
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
