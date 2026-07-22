package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultGeminiModel is used when no model is configured.
const DefaultGeminiModel = "gemini-2.5-flash"

const geminiEndpoint = "https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent"

// Gemini implements Provider against Google's Generative Language API.
type Gemini struct {
	APIKey string
	Model  string
	HTTP   *http.Client
}

// NewGemini builds a Gemini provider. Model may be empty to use the default.
func NewGemini(apiKey, model string) (*Gemini, error) {
	if apiKey == "" {
		return nil, ErrNoCredentials
	}
	if model == "" {
		model = DefaultGeminiModel
	}
	return &Gemini{
		APIKey: apiKey,
		Model:  model,
		HTTP:   &http.Client{Timeout: 90 * time.Second},
	}, nil
}

func (g *Gemini) Name() string { return "gemini/" + g.Model }

// --- wire types -------------------------------------------------------------

type geminiPart struct {
	Text             string              `json:"text,omitempty"`
	FunctionCall     *geminiFuncCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFuncResponse `json:"functionResponse,omitempty"`
	// ThoughtSignature must be replayed on subsequent turns for Gemini 3.x
	// thinking models to retain their reasoning across tool round-trips.
	ThoughtSignature string `json:"thoughtSignature,omitempty"`
}

type geminiFuncCall struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type geminiFuncResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiRequest struct {
	SystemInstruction *geminiContent  `json:"system_instruction,omitempty"`
	Contents          []geminiContent `json:"contents"`
	Tools             []geminiTool    `json:"tools,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFuncDecl `json:"functionDeclarations"`
}

type geminiFuncDecl struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
	} `json:"candidates"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Chat performs one completion round against the Gemini API.
func (g *Gemini) Chat(ctx context.Context, req Request) (*Response, error) {
	body := geminiRequest{Contents: toGeminiContents(req.Messages)}
	if req.System != "" {
		body.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: req.System}}}
	}
	if len(req.Tools) > 0 {
		decls := make([]geminiFuncDecl, 0, len(req.Tools))
		for _, t := range req.Tools {
			decls = append(decls, geminiFuncDecl{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  toGeminiSchema(t.Params),
			})
		}
		body.Tools = []geminiTool{{FunctionDeclarations: decls}}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("gemini: encode request: %w", err)
	}

	url := fmt.Sprintf(geminiEndpoint, g.Model)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("gemini: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Header rather than ?key= so the secret stays out of URLs and proxy logs.
	httpReq.Header.Set("x-goog-api-key", g.APIKey)

	resp, err := g.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini: request failed: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("gemini: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{Provider: "gemini", Status: resp.StatusCode, Body: string(payload)}
	}

	var decoded geminiResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("gemini: decode response: %w", err)
	}
	if decoded.Error != nil {
		return nil, fmt.Errorf("gemini: %s", decoded.Error.Message)
	}
	if len(decoded.Candidates) == 0 {
		return nil, fmt.Errorf("gemini: model returned no candidates")
	}

	out := &Response{}
	var text strings.Builder
	for i, part := range decoded.Candidates[0].Content.Parts {
		if part.Text != "" {
			text.WriteString(part.Text)
		}
		if part.FunctionCall != nil {
			id := part.FunctionCall.ID
			if id == "" {
				// Older Gemini models omit call IDs; synthesize a stable one.
				id = part.FunctionCall.Name + "-" + strconv.Itoa(i)
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:        id,
				Name:      part.FunctionCall.Name,
				Args:      part.FunctionCall.Args,
				Signature: part.ThoughtSignature,
			})
		}
	}
	out.Text = strings.TrimSpace(text.String())
	return out, nil
}

// toGeminiContents maps neutral messages onto Gemini's user/model role pair.
func toGeminiContents(msgs []Message) []geminiContent {
	contents := make([]geminiContent, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			contents = append(contents, geminiContent{
				Role:  "user",
				Parts: []geminiPart{{Text: m.Text}},
			})

		case RoleAssistant:
			parts := make([]geminiPart, 0, len(m.ToolCalls)+1)
			if m.Text != "" {
				parts = append(parts, geminiPart{Text: m.Text})
			}
			for _, tc := range m.ToolCalls {
				parts = append(parts, geminiPart{
					FunctionCall: &geminiFuncCall{
						ID: tc.ID, Name: tc.Name, Args: tc.Args,
					},
					ThoughtSignature: tc.Signature,
				})
			}
			if len(parts) == 0 {
				continue
			}
			contents = append(contents, geminiContent{Role: "model", Parts: parts})

		case RoleTool:
			// Gemini expects tool output wrapped in a functionResponse part.
			contents = append(contents, geminiContent{
				Role: "user",
				Parts: []geminiPart{{
					FunctionResponse: &geminiFuncResponse{
						Name:     m.ToolName,
						Response: map[string]any{"result": m.Text},
					},
				}},
			})
		}
	}
	return contents
}

// toGeminiSchema converts a Schema to Gemini's OpenAPI-derived form, whose
// type enum is upper-case ("OBJECT", "STRING") rather than JSON Schema's lower.
func toGeminiSchema(s Schema) map[string]any {
	out := map[string]any{"type": strings.ToUpper(s.Type)}
	if len(s.Properties) > 0 {
		props := make(map[string]any, len(s.Properties))
		for name, p := range s.Properties {
			prop := map[string]any{"type": strings.ToUpper(p.Type)}
			if p.Description != "" {
				prop["description"] = p.Description
			}
			if len(p.Enum) > 0 {
				prop["enum"] = p.Enum
			}
			props[name] = prop
		}
		out["properties"] = props
	}
	if len(s.Required) > 0 {
		out["required"] = s.Required
	}
	return out
}
