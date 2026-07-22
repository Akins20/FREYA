// Package llm defines a provider-agnostic interface to a reasoning model.
//
// The agent loop talks only to Provider. Swapping Gemini for Claude, or for a
// local model served over HTTP, means adding one file here — nothing in
// internal/agent or internal/skills changes.
package llm

import (
	"context"
	"errors"
	"fmt"
)

// Role identifies who produced a Message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one turn in a conversation. A message carries either free text,
// a set of tool calls the model wants executed, or the result of one such call.
type Message struct {
	Role       Role
	Text       string
	ToolCalls  []ToolCall // set when the model wants to invoke skills
	ToolName   string     // set on RoleTool: which skill produced Text
	ToolCallID string     // set on RoleTool: the call this result answers
}

// ToolCall is the model's request to run a registered skill.
type ToolCall struct {
	ID   string
	Name string
	Args map[string]any

	// Signature is provider-opaque state that must be echoed back verbatim on
	// later turns. Gemini 3.x thinking models return a thoughtSignature here;
	// dropping it degrades multi-step tool reasoning. Ignored by providers
	// that do not use it.
	Signature string
}

// Tool describes a skill to the model, using a JSON Schema subset that every
// major provider accepts.
type Tool struct {
	Name        string
	Description string
	Params      Schema
}

// Schema is a JSON Schema object description.
type Schema struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties,omitempty"`
	Required   []string            `json:"required,omitempty"`
}

// Property is a single field within a Schema.
type Property struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// ObjectSchema builds a Schema for an object with the given properties.
func ObjectSchema(props map[string]Property, required ...string) Schema {
	return Schema{Type: "object", Properties: props, Required: required}
}

// Request is a single completion request.
type Request struct {
	System   string
	Messages []Message
	Tools    []Tool
}

// Response is what the model returned: prose, tool calls, or both.
type Response struct {
	Text      string
	ToolCalls []ToolCall
}

// Provider is a reasoning backend. Implementations must be safe for use from
// multiple goroutines.
type Provider interface {
	// Name identifies the backend for logging and the /status command.
	Name() string
	// Chat performs one completion round.
	Chat(ctx context.Context, req Request) (*Response, error)
}

// AudioTranscriber is an optional capability: a Provider whose model accepts
// audio natively and can return a transcript.
//
// It is kept separate from Provider so that text-only backends need not know
// audio exists. Callers type-assert for it and fall back to a local engine
// when the assertion fails.
type AudioTranscriber interface {
	// TranscribeAudio returns a verbatim transcript of the supplied audio.
	// mimeType is a value such as "audio/ogg", "audio/mp3" or "audio/wav".
	TranscribeAudio(ctx context.Context, audio []byte, mimeType string) (string, error)
}

// ErrNoCredentials signals that a provider was selected but has no API key.
var ErrNoCredentials = errors.New("llm: no API credentials configured")

// APIError carries a non-2xx response from a provider's HTTP endpoint.
type APIError struct {
	Provider string
	Status   int
	Body     string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: http %d: %s", e.Provider, e.Status, e.Body)
}
