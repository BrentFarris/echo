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
	ID             string          `json:"id"`
	RequestID      string          `json:"requestId"`
	UserContent    string          `json:"userContent"`
	Model          string          `json:"model,omitempty"`
	Status         string          `json:"status"`
	Error          string          `json:"error,omitempty"`
	StartedAt      time.Time       `json:"startedAt"`
	CompletedAt    *time.Time      `json:"completedAt,omitempty"`
	AssistantTurns []AssistantTurn `json:"assistantTurns"`
}

type AssistantTurn struct {
	Number       int            `json:"number"`
	Content      string         `json:"content,omitempty"`
	Reasoning    string         `json:"reasoning,omitempty"`
	HasToolCalls bool           `json:"hasToolCalls,omitempty"`
	Tools        []ToolActivity `json:"tools,omitempty"`
}

type ToolActivity struct {
	CallID    string `json:"callId"`
	CallOrder int    `json:"callOrder"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
	Status    string `json:"status"`
	Success   bool   `json:"success"`
	Result    string `json:"result,omitempty"`
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
