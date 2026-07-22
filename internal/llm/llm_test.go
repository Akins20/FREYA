package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestGeminiSchemaUsesUpperCaseTypes(t *testing.T) {
	// Gemini's schema proto expects enum-style types ("OBJECT"), not JSON
	// Schema's lower-case forms. Getting this wrong is a silent 400.
	s := ObjectSchema(map[string]Property{
		"level": {Type: "number", Description: "0-100"},
		"mode":  {Type: "string", Enum: []string{"a", "b"}},
	}, "level")

	got := toGeminiSchema(s)
	if got["type"] != "OBJECT" {
		t.Fatalf("root type = %v, want OBJECT", got["type"])
	}
	props := got["properties"].(map[string]any)
	if props["level"].(map[string]any)["type"] != "NUMBER" {
		t.Errorf("level type = %v", props["level"])
	}
	if props["mode"].(map[string]any)["enum"] == nil {
		t.Error("enum values were dropped")
	}
	req := got["required"].([]string)
	if len(req) != 1 || req[0] != "level" {
		t.Errorf("required = %v", req)
	}
}

func TestGeminiMessageMapping(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Text: "set volume to 42"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{
			ID: "c1", Name: "system_volume",
			Args:      map[string]any{"level": float64(42)},
			Signature: "SIG123",
		}}},
		{Role: RoleTool, ToolName: "system_volume", ToolCallID: "c1", Text: "Volume set to 42%."},
	}

	contents := toGeminiContents(msgs)
	if len(contents) != 3 {
		t.Fatalf("got %d contents, want 3", len(contents))
	}
	if contents[0].Role != "user" {
		t.Errorf("user role = %q", contents[0].Role)
	}

	// Assistant turns map to Gemini's "model" role.
	if contents[1].Role != "model" {
		t.Errorf("assistant role = %q, want model", contents[1].Role)
	}
	fc := contents[1].Parts[0].FunctionCall
	if fc == nil || fc.Name != "system_volume" || fc.ID != "c1" {
		t.Fatalf("function call not mapped: %+v", fc)
	}
	// Thought signatures must survive the round trip or Gemini 3.x loses its
	// reasoning between tool steps.
	if contents[1].Parts[0].ThoughtSignature != "SIG123" {
		t.Error("thought signature was dropped")
	}

	fr := contents[2].Parts[0].FunctionResponse
	if fr == nil || fr.Name != "system_volume" {
		t.Fatalf("function response not mapped: %+v", fr)
	}
}

func TestGeminiSkipsEmptyAssistantTurn(t *testing.T) {
	// An assistant turn with neither text nor calls would serialise as an
	// empty parts array, which the API rejects.
	contents := toGeminiContents([]Message{
		{Role: RoleUser, Text: "hi"},
		{Role: RoleAssistant},
	})
	if len(contents) != 1 {
		t.Fatalf("empty assistant turn was not skipped: %d contents", len(contents))
	}
}

func TestAnthropicCoalescesConsecutiveToolResults(t *testing.T) {
	// Claude requires parallel tool results in a single user message; emitting
	// one message each is a 400.
	msgs := []Message{
		{Role: RoleUser, Text: "do two things"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "a", Name: "one"}, {ID: "b", Name: "two"},
		}},
		{Role: RoleTool, ToolCallID: "a", Text: "result one"},
		{Role: RoleTool, ToolCallID: "b", Text: "result two"},
	}

	out := toAnthMessages(msgs)
	if len(out) != 3 {
		t.Fatalf("got %d messages, want 3", len(out))
	}
	last := out[2]
	if last.Role != "user" || len(last.Content) != 2 {
		t.Fatalf("tool results not coalesced: %+v", last)
	}
	if last.Content[0].ToolUseID != "a" || last.Content[1].ToolUseID != "b" {
		t.Errorf("tool_use_ids wrong: %+v", last.Content)
	}
}

func TestProvidersRejectMissingCredentials(t *testing.T) {
	if _, err := NewGemini("", ""); err != ErrNoCredentials {
		t.Errorf("gemini with no key: %v", err)
	}
	if _, err := NewAnthropic("", ""); err != ErrNoCredentials {
		t.Errorf("anthropic with no key: %v", err)
	}
	if g, err := NewGemini("k", ""); err != nil || !strings.Contains(g.Name(), DefaultGeminiModel) {
		t.Errorf("default model not applied: %v %v", g, err)
	}
}

func TestMockRoutesToRegisteredToolsOnly(t *testing.T) {
	m := NewMock()
	ctx := context.Background()

	// The trigger matches, but the tool is not registered, so it must not be called.
	resp, err := m.Chat(ctx, Request{
		Messages: []Message{{Role: RoleUser, Text: "note that the drive is full"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("called an unregistered tool: %+v", resp.ToolCalls)
	}

	// With the tool available it should route.
	resp, err = m.Chat(ctx, Request{
		Messages: []Message{{Role: RoleUser, Text: "note that the drive is full"}},
		Tools:    []Tool{{Name: "note_add"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "note_add" {
		t.Fatalf("did not route to note_add: %+v", resp.ToolCalls)
	}
	if got := resp.ToolCalls[0].Args["text"]; got != "the drive is full" {
		t.Errorf("argument extraction = %q", got)
	}
}

func TestMockSummarisesToolResultsInsteadOfLooping(t *testing.T) {
	m := NewMock()
	resp, err := m.Chat(context.Background(), Request{
		Messages: []Message{
			{Role: RoleUser, Text: "system status"},
			{Role: RoleTool, ToolName: "system_status", Text: "Disk /: 72% used"},
		},
		Tools: []Tool{{Name: "system_status"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatal("mock re-called a tool after receiving its result — infinite loop risk")
	}
	if !strings.Contains(resp.Text, "72%") {
		t.Errorf("tool output not surfaced: %q", resp.Text)
	}
}

func TestMockVolumeExtraction(t *testing.T) {
	m := NewMock()
	resp, err := m.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Text: "set the volume to 42"}},
		Tools:    []Tool{{Name: "system_volume"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("no tool call: %+v", resp)
	}
	if got := resp.ToolCalls[0].Args["level"]; got != float64(42) {
		t.Errorf("level = %v (%T), want 42", got, got)
	}
}

func TestAPIErrorMessage(t *testing.T) {
	e := &APIError{Provider: "gemini", Status: 429, Body: "quota"}
	if !strings.Contains(e.Error(), "429") || !strings.Contains(e.Error(), "quota") {
		t.Errorf("unhelpful error text: %s", e.Error())
	}
}

func TestGeminiRequestSerialises(t *testing.T) {
	// Guard the JSON field names the API actually reads.
	body := geminiRequest{
		SystemInstruction: &geminiContent{Parts: []geminiPart{{Text: "sys"}}},
		Contents:          toGeminiContents([]Message{{Role: RoleUser, Text: "hi"}}),
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"system_instruction", "contents", "parts"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("serialised body missing %q: %s", want, raw)
		}
	}
}

func TestMockIgnoresChanceSubstringsInLargeInput(t *testing.T) {
	// Regression: a 500KB paste that happened to contain "mute" routed to
	// system_volume and set the machine to zero. Matching must be whole-word
	// and bounded, so a haystack of noise cannot trigger a side effect.
	noise := strings.Repeat("abcmutedef", 50000) // "mute" present, never as a word
	m := NewMock()

	resp, err := m.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Text: noise}},
		Tools:    []Tool{{Name: "system_volume"}, {Name: "note_add"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("noise triggered %s — substring collision not fixed", resp.ToolCalls[0].Name)
	}
}

func TestMockWholeWordMatching(t *testing.T) {
	m := NewMock()
	tools := []Tool{{Name: "system_volume"}}

	// "commuted" contains "mute" but is not the word "mute".
	resp, _ := m.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Text: "the sentence was commuted yesterday"}},
		Tools:    tools,
	})
	if len(resp.ToolCalls) != 0 {
		t.Errorf("substring inside a word triggered %s", resp.ToolCalls[0].Name)
	}

	// The actual word must still work.
	resp, _ = m.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Text: "mute the speakers"}},
		Tools:    tools,
	})
	if len(resp.ToolCalls) != 1 {
		t.Fatal("genuine 'mute' command no longer matches")
	}
	if got := resp.ToolCalls[0].Args["level"]; got != float64(0) {
		t.Errorf("mute level = %v, want 0", got)
	}
}

func TestMockReminderRoutesToNoteAddWithDue(t *testing.T) {
	// There is no reminder_add skill; reminders are note_add plus a due time.
	m := NewMock()
	resp, err := m.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Text: "remind me to call the bank in 2h"}},
		Tools:    []Tool{{Name: "note_add"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "note_add" {
		t.Fatalf("routed to %+v", resp.ToolCalls)
	}
	if got := resp.ToolCalls[0].Args["due"]; got != "2h" {
		t.Errorf("due = %v, want 2h", got)
	}
}
