package llm

import (
	"encoding/json"
	"strings"
)

const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

type Message struct {
	Role         string               `json:"role"`
	Content      string               `json:"content,omitempty"`
	ContentParts []MessageContentPart `json:"-"`
	Name         string               `json:"name,omitempty"`
	ToolCallID   string               `json:"tool_call_id,omitempty"`
	ToolCalls    []ToolCall           `json:"tool_calls,omitempty"`
}

type MessageContentPart struct {
	Type     string           `json:"type"`
	Text     string           `json:"text,omitempty"`
	ImageURL *MessageImageURL `json:"image_url,omitempty"`
}

type MessageImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

func TextContentPart(text string) MessageContentPart {
	return MessageContentPart{
		Type: "text",
		Text: text,
	}
}

func ImageURLContentPart(url string) MessageContentPart {
	return MessageContentPart{
		Type: "image_url",
		ImageURL: &MessageImageURL{
			URL: url,
		},
	}
}

func (m Message) MarshalJSON() ([]byte, error) {
	type messageJSON struct {
		Role       string     `json:"role"`
		Content    any        `json:"content,omitempty"`
		Name       string     `json:"name,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
		ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	}

	output := messageJSON{
		Role:       m.Role,
		Name:       m.Name,
		ToolCallID: m.ToolCallID,
		ToolCalls:  m.ToolCalls,
	}
	if len(m.ContentParts) > 0 {
		output.Content = append([]MessageContentPart(nil), m.ContentParts...)
	} else if m.Content != "" {
		output.Content = m.Content
	}
	return json.Marshal(output)
}

func (m *Message) UnmarshalJSON(data []byte) error {
	type messageJSON struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content,omitempty"`
		Name       string          `json:"name,omitempty"`
		ToolCallID string          `json:"tool_call_id,omitempty"`
		ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	}

	var input messageJSON
	if err := json.Unmarshal(data, &input); err != nil {
		return err
	}
	m.Role = input.Role
	m.Name = input.Name
	m.ToolCallID = input.ToolCallID
	m.ToolCalls = input.ToolCalls
	m.Content = ""
	m.ContentParts = nil

	content := strings.TrimSpace(string(input.Content))
	if content == "" || content == "null" {
		return nil
	}
	if strings.HasPrefix(content, "[") {
		var parts []MessageContentPart
		if err := json.Unmarshal(input.Content, &parts); err != nil {
			return err
		}
		m.ContentParts = parts
		textParts := make([]string, 0, len(parts))
		for _, part := range parts {
			if part.Type == "text" && part.Text != "" {
				textParts = append(textParts, part.Text)
			}
		}
		m.Content = strings.Join(textParts, "\n")
		return nil
	}

	var text string
	if err := json.Unmarshal(input.Content, &text); err != nil {
		return err
	}
	m.Content = text
	return nil
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []Tool    `json:"tools,omitempty"`
	ToolChoice  any       `json:"tool_choice,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	TopK        *int      `json:"top_k,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	// Temporarily removing MinP as it's not supported with MTP on vLLM
	// MinP              *float64 `json:"min_p,omitempty"`
	MinP              *float64 `json:"-"`
	ContextLength     *int     `json:"context_length,omitempty"`
	MaxTokens         *int     `json:"max_tokens,omitempty"`
	FrequencyPenalty  *float64 `json:"frequency_penalty,omitempty"`
	PresencePenalty   *float64 `json:"presence_penalty,omitempty"`
	RepetitionPenalty *float64 `json:"repetition_penalty,omitempty"`
}

type ChatResponse struct {
	ID      string       `json:"id,omitempty"`
	Object  string       `json:"object,omitempty"`
	Created int64        `json:"created,omitempty"`
	Model   string       `json:"model,omitempty"`
	Choices []ChatChoice `json:"choices,omitempty"`
	Usage   *Usage       `json:"usage,omitempty"`
}

type ChatChoice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason,omitempty"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

type EventType string

const (
	EventToken     EventType = "token"
	EventReasoning EventType = "reasoning"
	EventToolCall  EventType = "tool_call"
	EventError     EventType = "error"
	EventComplete  EventType = "complete"
	EventCanceled  EventType = "canceled"
)

type StreamEvent struct {
	Type         EventType       `json:"type"`
	Content      string          `json:"content,omitempty"`
	ToolCall     *ToolCallDelta  `json:"toolCall,omitempty"`
	Error        string          `json:"error,omitempty"`
	FinishReason string          `json:"finishReason,omitempty"`
	Raw          json.RawMessage `json:"raw,omitempty"`
}

type ToolCallDelta struct {
	Index    int               `json:"index"`
	ID       string            `json:"id,omitempty"`
	Type     string            `json:"type,omitempty"`
	Function FunctionCallDelta `json:"function,omitempty"`
}

type FunctionCallDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type RequestOption func(*ChatRequest)

func NewChatRequest(settings Settings, messages []Message, options ...RequestOption) (ChatRequest, error) {
	settings = settings.Normalized()
	if err := settings.Validate(); err != nil {
		return ChatRequest{}, err
	}

	request := ChatRequest{
		Model:             settings.Model,
		Messages:          append([]Message(nil), messages...),
		Temperature:       float64Ptr(settings.Temperature),
		TopP:              float64Ptr(settings.TopP),
		MinP:              float64Ptr(settings.MinP),
		ContextLength:     intPtr(settings.ContextLength),
		MaxTokens:         intPtr(settings.MaxTokens),
		FrequencyPenalty:  float64Ptr(settings.FrequencyPenalty),
		PresencePenalty:   float64Ptr(settings.PresencePenalty),
		RepetitionPenalty: float64Ptr(settings.RepetitionPenalty),
	}
	if settings.TopK > 0 {
		request.TopK = intPtr(settings.TopK)
	}
	for _, option := range options {
		option(&request)
	}
	return request, nil
}

func WithStream(stream bool) RequestOption {
	return func(request *ChatRequest) {
		request.Stream = stream
	}
}

func WithTools(tools []Tool) RequestOption {
	return func(request *ChatRequest) {
		request.Tools = append([]Tool(nil), tools...)
	}
}

func WithToolChoice(toolChoice any) RequestOption {
	return func(request *ChatRequest) {
		request.ToolChoice = toolChoice
	}
}

func float64Ptr(value float64) *float64 {
	return &value
}

func intPtr(value int) *int {
	return &value
}
