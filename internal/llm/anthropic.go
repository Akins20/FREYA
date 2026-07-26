package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DefaultAnthropicModel is used when no model is configured.
const DefaultAnthropicModel = "claude-sonnet-5"

const (
	anthropicEndpoint = "https://api.anthropic.com/v1/messages"
	anthropicVersion  = "2023-06-01"
	anthropicMaxTok   = 4096
)

// Anthropic implements Provider against the Claude Messages API.
type Anthropic struct {
	APIKey string
	Model  string
	HTTP   *http.Client
}

// NewAnthropic builds a Claude provider. Model may be empty to use the default.
func NewAnthropic(apiKey, model string) (*Anthropic, error) {
	if apiKey == "" {
		return nil, ErrNoCredentials
	}
	if model == "" {
		model = DefaultAnthropicModel
	}
	return &Anthropic{
		APIKey: apiKey,
		Model:  model,
		HTTP:   &http.Client{Timeout: 90 * time.Second},
	}, nil
}

func (a *Anthropic) Name() string { return "anthropic/" + a.Model }

// --- wire types -------------------------------------------------------------

type anthBlock struct {
	Type string `json:"type"`
	// type=text
	Text string `json:"text,omitempty"`
	// type=tool_use
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
	// type=tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

type anthMessage struct {
	Role    string      `json:"role"`
	Content []anthBlock `json:"content"`
}

type anthTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema Schema `json:"input_schema"`
}

type anthRequest struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	System    string        `json:"system,omitempty"`
	Messages  []anthMessage `json:"messages"`
	Tools     []anthTool    `json:"tools,omitempty"`
}

type anthResponse struct {
	Content    []anthBlock `json:"content"`
	StopReason string      `json:"stop_reason"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Chat performs one completion round against the Claude Messages API.
func (a *Anthropic) Chat(ctx context.Context, req Request) (*Response, error) {
	body := anthRequest{
		Model:     a.Model,
		MaxTokens: anthropicMaxTok,
		System:    req.System,
		Messages:  toAnthMessages(req.Messages),
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, anthTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Params,
		})
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode request: %w", err)
	}

	payload, err := postJSON(ctx, a.HTTP, "anthropic", anthropicEndpoint, map[string]string{
		"Content-Type":      "application/json",
		"x-api-key":         a.APIKey,
		"anthropic-version": anthropicVersion,
	}, raw)
	if err != nil {
		return nil, err
	}

	var decoded anthResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}
	if decoded.Error != nil {
		return nil, fmt.Errorf("anthropic: %s", decoded.Error.Message)
	}

	out := &Response{}
	var text strings.Builder
	for _, b := range decoded.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Args: b.Input})
		}
	}
	out.Text = strings.TrimSpace(text.String())
	return out, nil
}

// toAnthMessages maps neutral messages to Claude's block format. Consecutive
// tool results must be coalesced into a single user message, which is why this
// walks with an index rather than ranging.
func toAnthMessages(msgs []Message) []anthMessage {
	var out []anthMessage
	for i := 0; i < len(msgs); {
		m := msgs[i]
		switch m.Role {
		case RoleUser:
			out = append(out, anthMessage{
				Role:    "user",
				Content: []anthBlock{{Type: "text", Text: m.Text}},
			})
			i++

		case RoleAssistant:
			blocks := make([]anthBlock, 0, len(m.ToolCalls)+1)
			if m.Text != "" {
				blocks = append(blocks, anthBlock{Type: "text", Text: m.Text})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, anthBlock{
					Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: tc.Args,
				})
			}
			if len(blocks) > 0 {
				out = append(out, anthMessage{Role: "assistant", Content: blocks})
			}
			i++

		case RoleTool:
			var blocks []anthBlock
			for i < len(msgs) && msgs[i].Role == RoleTool {
				blocks = append(blocks, anthBlock{
					Type:      "tool_result",
					ToolUseID: msgs[i].ToolCallID,
					Content:   msgs[i].Text,
				})
				i++
			}
			out = append(out, anthMessage{Role: "user", Content: blocks})

		default:
			i++
		}
	}
	return out
}
