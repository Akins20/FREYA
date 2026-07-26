package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultGeminiModel is used when no model is configured.
const DefaultGeminiModel = "gemini-3.5-flash-lite"

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
	// Thought marks a part as the model's reasoning summary rather than its
	// answer. Returned only when the request set includeThoughts; a thought part
	// carries its text in Text, so parsing must check this flag before treating
	// Text as the reply.
	Thought bool `json:"thought,omitempty"`
	// ThoughtSignature must be replayed on subsequent turns for Gemini 3.x
	// thinking models to retain their reasoning across tool round-trips.
	ThoughtSignature string `json:"thoughtSignature,omitempty"`
}

// geminiThinkingConfig requests reasoning and, optionally, its summary text.
type geminiThinkingConfig struct {
	// ThinkingBudget bounds reasoning tokens: >0 a cap, -1 dynamic, 0 off.
	ThinkingBudget int `json:"thinkingBudget,omitempty"`
	// IncludeThoughts returns thought-summary parts (flagged Thought) for display.
	IncludeThoughts bool `json:"includeThoughts,omitempty"`
}

type geminiGenerationConfig struct {
	ThinkingConfig *geminiThinkingConfig `json:"thinkingConfig,omitempty"`
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
	SystemInstruction *geminiContent          `json:"system_instruction,omitempty"`
	Contents          []geminiContent         `json:"contents"`
	Tools             []geminiTool            `json:"tools,omitempty"`
	SafetySettings    []geminiSafety          `json:"safetySettings,omitempty"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiSafety struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

// SafetyThreshold controls Gemini's content filtering. "OFF" disables it.
//
// Disabled by default here, deliberately. This is a personal assistant running
// locally for a single owner, and the filters mostly produce false refusals on
// ordinary requests — declining to delete files in a scratch directory, or
// lecturing about a shell command. The protection that matters on this machine
// is internal/guard, which reasons about what a command actually does rather
// than how it reads. Content classifiers are the wrong tool for that job and
// were getting in the way of the right one.
var SafetyThreshold = "OFF"

// safetySettings builds the filter configuration for a request.
func safetySettings() []geminiSafety {
	if SafetyThreshold == "" {
		return nil
	}
	categories := []string{
		"HARM_CATEGORY_HARASSMENT",
		"HARM_CATEGORY_HATE_SPEECH",
		"HARM_CATEGORY_SEXUALLY_EXPLICIT",
		"HARM_CATEGORY_DANGEROUS_CONTENT",
		"HARM_CATEGORY_CIVIC_INTEGRITY",
	}
	out := make([]geminiSafety, 0, len(categories))
	for _, c := range categories {
		out = append(out, geminiSafety{Category: c, Threshold: SafetyThreshold})
	}
	return out
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
	Usage *geminiUsage `json:"usageMetadata"`
}

// geminiUsage mirrors the API's own accounting block.
//
// The modality breakdown matters: audio input is billed at about double the
// text rate, and without splitting it out a spoken conversation is costed as
// though it had been typed.
type geminiUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
	CachedContentTokens  int `json:"cachedContentTokenCount"`
	ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
	PromptTokensDetails  []struct {
		Modality   string `json:"modality"`
		TokenCount int    `json:"tokenCount"`
	} `json:"promptTokensDetails"`
}

// toUsage converts the API block to the shared shape.
func (g *geminiUsage) toUsage() Usage {
	if g == nil {
		return Usage{}
	}
	u := Usage{
		InputTokens:   g.PromptTokenCount,
		OutputTokens:  g.CandidatesTokenCount,
		CachedTokens:  g.CachedContentTokens,
		ThoughtTokens: g.ThoughtsTokenCount,
	}
	for _, d := range g.PromptTokensDetails {
		if strings.EqualFold(d.Modality, "AUDIO") {
			u.AudioTokens += d.TokenCount
		}
	}
	// Reasoning tokens bill as output, and with the thinking window on they are
	// the bulk of a step's output — hundreds of tokens the user paid for. The
	// explicit thoughtsTokenCount is the reliable source: candidatesTokenCount is
	// the visible answer only, and this call's totalTokenCount does NOT include
	// thoughts, so the old total-vs-sum reconciliation silently dropped them.
	// Add the explicit count; fall back to the total only when the API reports
	// thoughts solely through it. Never both, so nothing is double-charged.
	if g.ThoughtsTokenCount > 0 {
		u.OutputTokens += g.ThoughtsTokenCount
	} else if u.OutputTokens > 0 && g.TotalTokenCount > u.InputTokens+u.OutputTokens {
		u.OutputTokens = g.TotalTokenCount - u.InputTokens
	}
	return u
}

// Chat performs one completion round against the Gemini API.
func (g *Gemini) Chat(ctx context.Context, req Request) (*Response, error) {
	body := geminiRequest{
		Contents:       toGeminiContents(req.Messages),
		SafetySettings: safetySettings(),
	}
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

	// Ask the model to reason before answering when the caller wants it. The
	// budget and the summary are separate switches: a budget makes it think, the
	// summary makes that thinking legible. Requested here so it applies to every
	// round of the tool loop — she thinks afresh before each next step.
	if req.ThinkingBudget != 0 || req.ShowThoughts {
		body.GenerationConfig = &geminiGenerationConfig{
			ThinkingConfig: &geminiThinkingConfig{
				ThinkingBudget:  req.ThinkingBudget,
				IncludeThoughts: req.ShowThoughts,
			},
		}
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

	out := &Response{Usage: decoded.Usage.toUsage()}
	var text, thoughts strings.Builder
	for i, part := range decoded.Candidates[0].Content.Parts {
		if part.Text != "" {
			// A thought part carries its summary in Text too — keep the two apart,
			// or her reasoning would leak into the reply she speaks.
			if part.Thought {
				thoughts.WriteString(part.Text)
			} else {
				text.WriteString(part.Text)
			}
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
	out.Reasoning = strings.TrimSpace(thoughts.String())
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

// --- audio transcription ----------------------------------------------------

// transcribePrompt is deliberately terse. Any instruction beyond "write down
// what you hear" invites the model to answer the question in the audio rather
// than transcribe it.
const transcribePrompt = "Transcribe this audio verbatim. Output only the " +
	"transcription, with no commentary, quotes or formatting. If the audio " +
	"contains no discernible speech, output nothing at all."

// TranscribeAudio implements AudioTranscriber.
//
// Audio is sent inline as base64. Compressed input matters more than it looks:
// measured on a 6-second clip, raw WAV took 8.3s end to end while the same
// audio as Ogg Vorbis took 1.8s, with identical transcripts. Callers should
// hand this compressed audio.
func (g *Gemini) TranscribeAudio(ctx context.Context, audio []byte, mimeType string) (string, error) {
	return g.TranscribeAudioHinted(ctx, audio, mimeType, "")
}

// TranscribeAudioHinted implements HintedTranscriber.
//
// The hint is folded into the prompt as context, never as an instruction to
// produce a particular word. "The speaker may say X" biases the acoustic
// match; "output X" would make it hallucinate X from silence, which for a wake
// word is the difference between helpful and unusable.
func (g *Gemini) TranscribeAudioHinted(ctx context.Context, audio []byte, mimeType, hint string) (string, error) {
	if len(audio) == 0 {
		return "", fmt.Errorf("gemini: empty audio")
	}
	if mimeType == "" {
		mimeType = "audio/ogg"
	}

	prompt := transcribePrompt
	if hint != "" {
		prompt = hint + " " + transcribePrompt
	}

	body := map[string]any{
		"safetySettings": safetySettings(),
		"contents": []map[string]any{{
			"role": "user",
			"parts": []map[string]any{
				{"text": prompt},
				{"inline_data": map[string]any{
					"mime_type": mimeType,
					"data":      base64.StdEncoding.EncodeToString(audio),
				}},
			},
		}},
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("gemini: encode audio request: %w", err)
	}

	url := fmt.Sprintf(geminiEndpoint, g.Model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("gemini: build audio request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.APIKey)

	resp, err := g.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("gemini: audio request failed: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", fmt.Errorf("gemini: read audio response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", &APIError{Provider: "gemini", Status: resp.StatusCode, Body: string(payload)}
	}

	var decoded geminiResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", fmt.Errorf("gemini: decode audio response: %w", err)
	}
	if decoded.Error != nil {
		return "", fmt.Errorf("gemini: %s", decoded.Error.Message)
	}
	if len(decoded.Candidates) == 0 {
		return "", nil // no speech detected
	}

	var text strings.Builder
	for _, part := range decoded.Candidates[0].Content.Parts {
		text.WriteString(part.Text)
	}
	return strings.TrimSpace(text.String()), nil
}

// --- speech synthesis -------------------------------------------------------

// DefaultTTSModel is Gemini's speech model. The conversational model cannot
// emit audio, so synthesis always targets a dedicated TTS model.
const DefaultTTSModel = "gemini-3.1-flash-tts-preview"

// DefaultVoice is a warm, mid-range preset that suits a conversational
// assistant. Gemini offers around thirty; see the API's voice list.
const DefaultVoice = "Leda"

// TTSModel overrides the speech model. Empty uses DefaultTTSModel.
var TTSModel string

// SynthesizeSpeech implements SpeechSynthesizer.
//
// Delivery is steered by prefixing the text with a style instruction, which is
// how Gemini's TTS models take direction. This is what lets a persona actually
// sound like itself rather than being read out flatly.
func (g *Gemini) SynthesizeSpeech(ctx context.Context, text, voice, style string) ([]byte, string, error) {
	if strings.TrimSpace(text) == "" {
		return nil, "", nil
	}
	if voice == "" {
		voice = DefaultVoice
	}
	model := TTSModel
	if model == "" {
		model = DefaultTTSModel
	}

	prompt := text
	if style != "" {
		prompt = style + ": " + text
	}

	body := map[string]any{
		"safetySettings": safetySettings(),
		"contents": []map[string]any{{
			"parts": []map[string]any{{"text": prompt}},
		}},
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig": map[string]any{
				"voiceConfig": map[string]any{
					"prebuiltVoiceConfig": map[string]any{"voiceName": voice},
				},
			},
		},
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, "", fmt.Errorf("gemini: encode speech request: %w", err)
	}

	url := fmt.Sprintf(geminiEndpoint, model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, "", fmt.Errorf("gemini: build speech request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.APIKey)

	resp, err := g.HTTP.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("gemini: speech request failed: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, "", fmt.Errorf("gemini: read speech response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", &APIError{Provider: "gemini-tts", Status: resp.StatusCode, Body: string(payload)}
	}

	var decoded struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData *struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, "", fmt.Errorf("gemini: decode speech response: %w", err)
	}
	if decoded.Error != nil {
		return nil, "", fmt.Errorf("gemini: %s", decoded.Error.Message)
	}
	if len(decoded.Candidates) == 0 {
		return nil, "", fmt.Errorf("gemini: no audio returned")
	}

	for _, part := range decoded.Candidates[0].Content.Parts {
		if part.InlineData == nil {
			continue
		}
		audio, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
		if err != nil {
			return nil, "", fmt.Errorf("gemini: decode audio: %w", err)
		}
		return audio, part.InlineData.MimeType, nil
	}
	return nil, "", fmt.Errorf("gemini: response contained no audio")
}

// --- vision -----------------------------------------------------------------

// AnalyzeImage implements VisionAnalyzer.
//
// Several images can be sent at once, which matters for "compare these two
// screenshots" and for multi-page documents rendered to pictures — asking about
// them one at a time loses the comparison the question was about.
func (g *Gemini) AnalyzeImage(ctx context.Context, prompt string, images [][]byte, mimeTypes []string) (string, error) {
	if len(images) == 0 {
		return "", fmt.Errorf("gemini: no images supplied")
	}

	parts := []map[string]any{{"text": prompt}}
	for i, img := range images {
		mime := "image/jpeg"
		if i < len(mimeTypes) && mimeTypes[i] != "" {
			mime = mimeTypes[i]
		}
		parts = append(parts, map[string]any{
			"inline_data": map[string]any{
				"mime_type": mime,
				"data":      base64.StdEncoding.EncodeToString(img),
			},
		})
	}

	body := map[string]any{
		"safetySettings": safetySettings(),
		"contents":       []map[string]any{{"role": "user", "parts": parts}},
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("gemini: encode vision request: %w", err)
	}

	url := fmt.Sprintf(geminiEndpoint, g.Model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("gemini: build vision request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.APIKey)

	resp, err := g.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("gemini: vision request failed: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", fmt.Errorf("gemini: read vision response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", &APIError{Provider: "gemini-vision", Status: resp.StatusCode, Body: string(payload)}
	}

	var decoded geminiResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", fmt.Errorf("gemini: decode vision response: %w", err)
	}
	if decoded.Error != nil {
		return "", fmt.Errorf("gemini: %s", decoded.Error.Message)
	}
	if len(decoded.Candidates) == 0 {
		return "", fmt.Errorf("gemini: no description returned")
	}

	var text strings.Builder
	for _, p := range decoded.Candidates[0].Content.Parts {
		text.WriteString(p.Text)
	}
	return strings.TrimSpace(text.String()), nil
}
