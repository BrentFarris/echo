// Package sessions defines Echo's durable, per-workspace chat transcript.
package sessions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/brent/echo/internal/llm"
)

const (
	Version  = 1
	FileName = "chat-session.json"
)

type Transcript struct {
	Version     int           `json:"version"`
	WorkspaceID string        `json:"workspaceId"`
	Revision    uint64        `json:"revision"`
	Turns       []Turn        `json:"turns"`
	Messages    []llm.Message `json:"messages"`
}

type Turn struct {
	ID                string              `json:"id"`
	RequestID         string              `json:"requestId"`
	UserContent       string              `json:"userContent"`
	UserMessageIndex  int                 `json:"userMessageIndex,omitempty"`
	Images            []MediaAttachment   `json:"images,omitempty"`
	Videos            []MediaAttachment   `json:"videos,omitempty"`
	Model             string              `json:"model,omitempty"`
	AgentModeID       string              `json:"agentModeId,omitempty"`
	AgentModeName     string              `json:"agentModeName,omitempty"`
	Status            string              `json:"status"`
	Error             string              `json:"error,omitempty"`
	StartedAt         time.Time           `json:"startedAt"`
	CompletedAt       *time.Time          `json:"completedAt,omitempty"`
	AssistantTurns    []AssistantTurn     `json:"assistantTurns"`
	ResearchAgents    []ResearchAgent     `json:"researchAgents,omitempty"`
	ResearchReasoning []ResearchReasoning `json:"researchReasoning,omitempty"`
	ResearchTools     []ToolActivity      `json:"researchTools,omitempty"`
	FileChanges       []FileChange        `json:"fileChanges,omitempty"`
	UserDeleted       bool                `json:"userDeleted,omitempty"`
	AssistantDeleted  bool                `json:"assistantDeleted,omitempty"`
}

// FileChange is the compact, durable portion of a tool-reported workspace
// mutation. Full before/after snapshots remain private to tool execution.
type FileChange struct {
	Path      string         `json:"path"`
	Operation string         `json:"operation"`
	Ref       *FileReference `json:"ref,omitempty"`
}

// FileReference identifies an entry inside one of the workspace's confined
// roots so clients can open a changed file without using a host path.
type FileReference struct {
	RootID string `json:"rootId"`
	Path   string `json:"path"`
}

// MediaAttachment is a normalized image or video attached to a user turn.
// DataURL is stored on the turn rather than the parallel LLM message so the
// persisted transcript contains a single copy of each potentially large
// payload. The server rehydrates LLM content parts from these values.
type MediaAttachment struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Bytes     int64  `json:"bytes"`
	DataURL   string `json:"dataUrl"`
}

type AssistantTurn struct {
	Number       int            `json:"number"`
	Content      string         `json:"content,omitempty"`
	Reasoning    string         `json:"reasoning,omitempty"`
	HasToolCalls bool           `json:"hasToolCalls,omitempty"`
	Tools        []ToolActivity `json:"tools,omitempty"`
}

type ToolActivity struct {
	CallID        string           `json:"callId"`
	CallOrder     int              `json:"callOrder"`
	Name          string           `json:"name"`
	Arguments     string           `json:"arguments,omitempty"`
	Status        string           `json:"status"`
	Success       bool             `json:"success"`
	Result        string           `json:"result,omitempty"`
	PlanQuestions *PlanQuestionSet `json:"planQuestions,omitempty"`
	Answers       []PlanAnswer     `json:"answers,omitempty"`
	Skipped       bool             `json:"skipped,omitempty"`
	AgentID       string           `json:"agentId,omitempty"`
	AgentName     string           `json:"agentName,omitempty"`
}

// ResearchAgent is transient progress state for a running child agent. The
// server clears these indicators before persisting a completed turn.
type ResearchAgent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Phase     string `json:"phase,omitempty"`
	TaskLabel string `json:"taskLabel,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ResearchReasoning is bounded, attributed reasoning retained with the turn
// for the expandable work history. Replace marks a live event as a full value.
type ResearchReasoning struct {
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
	Reasoning string `json:"reasoning"`
	Truncated bool   `json:"truncated,omitempty"`
	Replace   bool   `json:"replace,omitempty"`
}

// PlanQuestionSet is one interactive ask_user_questions call. QuestionSetID
// is the tool call ID and remains stable across snapshots and reconnects.
type PlanQuestionSet struct {
	QuestionSetID string         `json:"questionSetId"`
	Questions     []PlanQuestion `json:"questions"`
}

type PlanQuestion struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// PlanAnswer selects a zero-based option, or supplies free text when
// OptionIndex is negative.
type PlanAnswer struct {
	QuestionID  string `json:"questionId"`
	OptionIndex int    `json:"optionIndex"`
	Text        string `json:"text,omitempty"`
}

type Store struct {
	path string
}

func NewStore(workspacePath string) *Store {
	return &Store{path: filepath.Join(workspacePath, ".echo", FileName)}
}

func (s *Store) Path() string { return s.path }

func (s *Store) Load(workspaceID string) (Transcript, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return Transcript{Version: Version, WorkspaceID: workspaceID, Turns: []Turn{}, Messages: []llm.Message{}}, nil
		}
		return Transcript{}, fmt.Errorf("read chat session: %w", err)
	}
	var transcript Transcript
	if err := json.Unmarshal(data, &transcript); err != nil {
		return Transcript{}, fmt.Errorf("parse chat session %q: %w", s.path, err)
	}
	if transcript.Version != Version {
		return Transcript{}, fmt.Errorf("unsupported chat session version %d", transcript.Version)
	}
	if transcript.WorkspaceID != "" && transcript.WorkspaceID != workspaceID {
		return Transcript{}, fmt.Errorf("chat session belongs to workspace %q", transcript.WorkspaceID)
	}
	transcript.WorkspaceID = workspaceID
	if transcript.Turns == nil {
		transcript.Turns = []Turn{}
	}
	if transcript.Messages == nil {
		transcript.Messages = []llm.Message{}
	}
	return transcript, nil
}

// Save atomically replaces the transcript. Callers invoke it once per
// terminal response; streaming deltas never touch disk.
func (s *Store) Save(transcript Transcript) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create chat session directory: %w", err)
	}
	data, err := json.MarshalIndent(transcript, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal chat session: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write chat session: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace chat session: %w", err)
	}
	return nil
}
