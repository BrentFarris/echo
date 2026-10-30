package domain

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

func ID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func Now() time.Time { return time.Now().UTC() }

type Bot struct {
	EnableMultitasking bool            `json:"enableMultitasking"`
	ID                 string          `json:"id"`
	Handle             string          `json:"handle"`
	Name               string          `json:"name"`
	Prompt             string          `json:"prompt"`
	Roots              []string        `json:"roots"`
	Plugins            map[string]bool `json:"plugins"`
	Revision           int64           `json:"revision"`
}

// PluginEnabled is the bot's saved selection, independent of global availability.
// Only Echo automatically accepts plugins added after the bot was created.
func (b Bot) PluginEnabled(id string) bool {
	enabled, exists := b.Plugins[id]
	return enabled || !exists && b.ID == "echo"
}

type Generation struct {
	EndpointID  string  `json:"endpointId"`
	Model       string  `json:"model"`
	Thinking    string  `json:"thinking"`
	Temperature float64 `json:"temperature"`
}
type Endpoint struct {
	ID                       string   `json:"id"`
	Name                     string   `json:"name"`
	URL                      string   `json:"url"`
	Model                    string   `json:"model"`
	Cloud                    bool     `json:"cloud"`
	Credential               string   `json:"credential,omitempty"`
	ContextTokens            int      `json:"contextTokens"`
	Compact                  bool     `json:"compact"`
	Provider                 string   `json:"provider,omitempty"`
	Temperature              *float64 `json:"temperature,omitempty"`
	TopK                     *int     `json:"topK,omitempty"`
	TopP                     *float64 `json:"topP,omitempty"`
	MinP                     *float64 `json:"minP,omitempty"`
	MaxTokens                *int     `json:"maxTokens,omitempty"`
	FrequencyPenalty         *float64 `json:"frequencyPenalty,omitempty"`
	PresencePenalty          *float64 `json:"presencePenalty,omitempty"`
	RepetitionPenalty        *float64 `json:"repetitionPenalty,omitempty"`
	ReasoningEffort          string   `json:"reasoningEffort,omitempty"`
	ThinkingTokenBudget      *int     `json:"thinkingTokenBudget,omitempty"`
	CompressionThreshold     int      `json:"compressionThreshold,omitempty"`
	RequestTimeoutSeconds    int      `json:"requestTimeoutSeconds,omitempty"`
	StreamIdleTimeoutSeconds int      `json:"streamIdleTimeoutSeconds,omitempty"`
}

// Attachment metadata travels with the transcript; bytes are stored once in SQLite.
type Attachment struct {
	NodeID       string `json:"nodeId,omitempty"`
	OriginChatID string `json:"originChatId,omitempty"`
	ID           string `json:"id"`
	ChatID       string `json:"chatId"`
	Name         string `json:"name"`
	MediaType    string `json:"mediaType"`
	Size         int64  `json:"size"`
}
type Segment struct {
	NodeID      string        `json:"nodeId,omitempty"`
	Type        string        `json:"type"`
	Text        string        `json:"text,omitempty"`
	BotID       string        `json:"botId,omitempty"`
	Path        string        `json:"path,omitempty"`
	AllowedBots []string      `json:"allowedBots,omitempty"`
	Kind        string        `json:"kind,omitempty"`
	Range       *SegmentRange `json:"range,omitempty"`
}

// SegmentRange uses half-open UTF-16 offsets, matching browser string indexes.
type SegmentRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// LastReadAt records when the transcript was last opened, so the sidebar can
// flag chats that finished work nobody has looked at yet.
type Chat struct {
	ForkedFrom       *ForkOrigin           `json:"forkedFrom,omitempty"`
	OwnerNodeID      string                `json:"ownerNodeId,omitempty"`
	Projection       bool                  `json:"projection,omitempty"`
	GenerationByNode map[string]Generation `json:"generationByNode,omitempty"`
	ID               string                `json:"id"`
	Title            string                `json:"title"`
	Bots             map[string]string     `json:"bots"`
	Generation       Generation            `json:"generation"`
	CreatedAt        time.Time             `json:"createdAt"`
	UpdatedAt        time.Time             `json:"updatedAt"`
	LastReadAt       *time.Time            `json:"lastReadAt,omitempty"`
}
type ForkOrigin struct {
	ChatID    string `json:"chatId"`
	MessageID string `json:"messageId"`
}

// TokenUsage is provider-reported usage for one request, not a running total.
type TokenUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	TotalTokens  int `json:"totalTokens"`
}

type Message struct {
	Usage *TokenUsage `json:"usage,omitempty"`
	// ModelContent replaces only model input; original output and attachments stay inspectable.
	ModelContent      *string      `json:"modelContent,omitempty"`
	RerunUnavailable  bool         `json:"rerunUnavailable,omitempty"` // Derived when serving a transcript.
	NodeID            string       `json:"nodeId,omitempty"`
	ID                string       `json:"id"`
	ChatID            string       `json:"chatId"`
	RunID             string       `json:"runId,omitempty"`
	BotID             string       `json:"botId,omitempty"`
	Author            string       `json:"author"`
	Role              string       `json:"role"`
	Content           string       `json:"content"`
	Attachments       []Attachment `json:"attachments,omitempty"`
	Reasoning         string       `json:"reasoning,omitempty"`
	Segments          []Segment    `json:"segments,omitempty"`
	Recipients        []string     `json:"recipients,omitempty"`
	Calls             []ToolCall   `json:"toolCalls,omitempty"`
	CallID            string       `json:"toolCallId,omitempty"`
	Status            string       `json:"status"`
	CreatedAt         time.Time    `json:"createdAt"`
	FileChanges       []FileChange `json:"fileChanges,omitempty"`
	FileChangesNotice string       `json:"fileChangesNotice,omitempty"`
}

// File changes are host-observed metadata, never inferred from model prose.
type FileChange struct {
	Path        string `json:"path"`
	DisplayPath string `json:"displayPath"`
	Operation   string `json:"operation"`
	Before      string `json:"before,omitempty"`
	After       string `json:"after,omitempty"`
}
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type Run struct {
	UserMessageID string     `json:"userMessageId,omitempty"`
	ID            string     `json:"id"`
	ChatID        string     `json:"chatId"`
	DispatchID    string     `json:"dispatchId"`
	BotID         string     `json:"botId"`
	Status        string     `json:"status"`
	Error         string     `json:"error,omitempty"`
	Generation    Generation `json:"generation"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}
type ExecutionScope struct {
	Bot           Bot    `json:"bot"`
	ChatID        string `json:"chatId"`
	RunID         string `json:"runId"`
	DispatchID    string `json:"dispatchId"`
	PluginID      string `json:"pluginId"`
	ToolMessageID string `json:"toolMessageId,omitempty"`
}
type Event struct {
	Sequence int64           `json:"sequence"`
	Type     string          `json:"type"`
	ChatID   string          `json:"chatId,omitempty"`
	RunID    string          `json:"runId,omitempty"`
	BotID    string          `json:"botId,omitempty"`
	Payload  json.RawMessage `json:"payload"`
}
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Capability  string         `json:"capability,omitempty"`
}
type View struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	HTML  string `json:"html"`
	Kind  string `json:"kind"`
}

// AppRegistration exposes a plugin's primary floating or embedded view in Apps.
type AppRegistration struct {
	ViewID string `json:"viewId"`
	Icon   string `json:"icon,omitempty"`
}
type PluginManifest struct {
	API                     int              `json:"api"`
	ID                      string           `json:"id"`
	Name                    string           `json:"name"`
	Version                 string           `json:"version"`
	Scope                   string           `json:"scope"`
	Builtin                 bool             `json:"builtin"`
	Enabled                 bool             `json:"enabled"`
	Capabilities            []string         `json:"capabilities"`
	Network                 *PluginNetwork   `json:"network,omitempty"`
	RequestedNetworkOrigins []string         `json:"requestedNetworkOrigins,omitempty"`
	Tools                   []Tool           `json:"tools"`
	Views                   []View           `json:"views,omitempty"`
	App                     *AppRegistration `json:"app,omitempty"`
	Renderers               []string         `json:"renderers,omitempty"`
	Providers               []string         `json:"providers,omitempty"`
	Settings                map[string]any   `json:"settings"`
	Config                  map[string]any   `json:"config"`
	Digest                  string           `json:"digest,omitempty"`
	Backend                 string           `json:"backend,omitempty"`
	Pending                 bool             `json:"pending"`
}

// PluginNetwork grants exact origins, including their scheme and port.
type PluginNetwork struct {
	Origins []string `json:"origins"`
}
