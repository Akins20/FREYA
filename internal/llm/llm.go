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
	// ThinkingBudget asks the model to reason before it answers, and to return a
	// summary of that reasoning. 0 leaves the provider default; a positive value
	// is a token budget for thinking; -1 lets the model decide how much. Higher
	// budgets buy deeper planning at the cost of latency and output-priced tokens,
	// so callers raise it for agentic work and keep it low for conversation.
	ThinkingBudget int
	// ShowThoughts requests the thought summary text (Response.Reasoning). Without
	// it the model may still think, but nothing legible comes back.
	ShowThoughts bool
}

// Response is what the model returned: prose, tool calls, or both.
type Response struct {
	Text      string
	ToolCalls []ToolCall
	// Reasoning is the model's own account of how it reached this step — a summary
	// of its thinking — when the request asked for it. It is shown between tool
	// calls so her decisions are legible and inspectable, and it is empty for
	// providers or calls that return no thoughts.
	Reasoning string
	// Usage is what the call cost, as reported by the provider. Zero when the
	// provider does not report it.
	Usage Usage
	// Truncated reports that the model stopped because it ran out of output room
	// rather than because it had finished.
	//
	// Every provider reports this and none of them was read, so a reply cut off
	// mid-sentence was indistinguishable from a complete one. It matters most
	// when the output IS the artefact — a page, a document, a file — because what
	// lands then is a thing that ends part-way through with nothing saying so.
	Truncated bool
}

// Usage is a provider's own accounting of a call.
//
// These are the provider's numbers, not an estimate: guessing token counts from
// character counts is off by enough to make a cost report misleading, and every
// provider returns the real figures for free alongside the response.
type Usage struct {
	// InputTokens is everything sent: system prompt, history, tools, the lot.
	InputTokens int
	// OutputTokens is what came back, including any reasoning tokens the model
	// billed for but did not show.
	OutputTokens int
	// CachedTokens are input tokens the provider served from its cache, billed
	// at a lower rate. This is the number that makes the memory architecture's
	// stable-prefix ordering worth having, so it is worth watching.
	CachedTokens int
	// AudioTokens are input tokens that came from sound rather than text. Billed
	// at roughly double, so voice interaction costs more than it appears to.
	AudioTokens int
	// ThoughtTokens are reasoning tokens, counted within OutputTokens.
	ThoughtTokens int
}

// Total is every token the call touched.
func (u Usage) Total() int { return u.InputTokens + u.OutputTokens }

// CacheRate is the share of input served from cache, as a fraction.
func (u Usage) CacheRate() float64 {
	if u.InputTokens == 0 {
		return 0
	}
	return float64(u.CachedTokens) / float64(u.InputTokens)
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

// HintedTranscriber is an optional refinement of AudioTranscriber: it accepts a
// short vocabulary hint that biases recognition toward expected words.
//
// It exists for one specific failure. An invented name spoken alone in a
// half-second clip has no ordinary-language prior, so the model renders it as
// whatever real words it resembles — "Freya" comes back as "a friend" or "ya".
// Naming the expected word up front fixes it, because the model then has a
// candidate to match the sound against. The hint is advisory: it must never
// cause the model to invent the word when it was not said, only to spell it
// correctly when it was.
type HintedTranscriber interface {
	TranscribeAudioHinted(ctx context.Context, audio []byte, mimeType, hint string) (string, error)
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

// SpeechSynthesizer is an optional capability: a provider that can render text
// as spoken audio. Kept separate from Provider for the same reason as
// AudioTranscriber — text-only backends need not know it exists.
type SpeechSynthesizer interface {
	// SynthesizeSpeech renders text as audio.
	//
	// voice names a provider-specific preset. style is a natural-language
	// delivery instruction ("dry and amused, casual") which providers that
	// support it use to shape prosody; others ignore it.
	//
	// Returns the audio bytes and their MIME type.
	SynthesizeSpeech(ctx context.Context, text, voice, style string) (audio []byte, mimeType string, err error)
}

// VisionAnalyzer is an optional capability: a Provider whose model can look at
// images. Separate from Provider for the same reason as the audio interfaces —
// a text-only backend need not know images exist.
type VisionAnalyzer interface {
	// AnalyzeImage answers a question about one or more images.
	// mimeTypes[i] describes images[i].
	AnalyzeImage(ctx context.Context, prompt string, images [][]byte, mimeTypes []string) (string, error)
}
