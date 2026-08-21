package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/tools"
)

type skillTestCompleter struct {
	mu       sync.Mutex
	content  string
	err      error
	requests []llm.ChatRequest
}

func (f *skillTestCompleter) Complete(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.mu.Unlock()
	if f.err != nil {
		return llm.ChatResponse{}, f.err
	}
	return llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.Message{Role: llm.RoleAssistant, Content: f.content}}}}, nil
}

func (f *skillTestCompleter) lastRequest() llm.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

func seedCompletedSkillTurn(t *testing.T, s *Server, workspaceID, chatID string) {
	t.Helper()
	parent, err := s.sessions.get(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	tab, _, err := parent.resolveTab(chatID)
	if err != nil {
		t.Fatal(err)
	}
	tab.mu.Lock()
	tab.transcript.Turns = []sessions.Turn{{
		ID: "turn-1", UserContent: "Investigate chat streaming.", Status: "done",
		AssistantTurns: []sessions.AssistantTurn{{
			Number: 1, Content: "The chat loop streams tokens before completing the turn.", Reasoning: "private reasoning marker",
			Tools: []sessions.ToolActivity{{CallID: "call-1", Name: "filesystem_read_text", Arguments: `{"path":"internal/server/chat_sessions.go"}`, Status: "complete", Success: true, Result: `{"content":"research marker"}`}},
		}},
	}}
	tab.mu.Unlock()
}

func TestCreateSkillFromSpecificChatAndAvoidNameCollisions(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "skill-create")
	parent, err := s.sessions.get(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	chatID := parent.activeChatID
	seedCompletedSkillTurn(t, s, workspace.ID, chatID)
	// Keep a different blank tab active to prove the route targets chatId rather
	// than silently falling back to the shared active selection.
	blank := blankTabTranscript()
	parent.mu.Lock()
	parent.tabs[blank.ChatID] = &chatSession{manager: s.sessions, parent: parent, workspace: workspace, transcript: blank}
	parent.tabOrder = append(parent.tabOrder, blank.ChatID)
	parent.activeChatID = blank.ChatID
	parent.mu.Unlock()
	label := normalizeWorkspaceFolderLabel(filepath.Base(workspace.MainPath))
	completer := &skillTestCompleter{content: `{
		"folder": "` + label + `",
		"name": "chat-streaming",
		"description": "How Echo streams chat responses and records tool research.",
		"triggers": ["chat streaming", "tool research"],
		"body": "# Chat streaming\n\nUse the streaming loop and preserve completed tool research."
	}`}
	s.llmCompleter = completer

	path := "/api/workspaces/" + workspace.ID + "/chats/" + chatID + "/skills"
	first := doRequest(t, s, http.MethodPost, path)
	if first.Code != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	var firstBody struct {
		Data skillCreationResult `json:"data"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatal(err)
	}
	if firstBody.Data.ID != label+"/chat-streaming" || firstBody.Data.Path != label+"/.echo/skills/chat-streaming/SKILL.md" {
		t.Fatalf("result=%#v", firstBody.Data)
	}
	requestData, _ := json.Marshal(completer.lastRequest().Messages)
	if !strings.Contains(string(requestData), "Investigate chat streaming.") || !strings.Contains(string(requestData), "research marker") || !strings.Contains(string(requestData), "Available workspace folders") {
		t.Fatalf("synthesis request omitted transcript metadata: %s", requestData)
	}
	if strings.Contains(string(requestData), "private reasoning marker") {
		t.Fatalf("synthesis request included reasoning: %s", requestData)
	}
	if data, err := os.ReadFile(filepath.Join(workspace.MainPath, ".echo", "skills", "chat-streaming", "SKILL.md")); err != nil || !strings.Contains(string(data), "name: chat-streaming") {
		t.Fatalf("skill file data=%s err=%v", data, err)
	}

	second := doRequest(t, s, http.MethodPost, path)
	if second.Code != http.StatusCreated || !strings.Contains(second.Body.String(), "chat-streaming-2") {
		t.Fatalf("collision status=%d body=%s", second.Code, second.Body.String())
	}
}

func TestCreateSkillRejectsEmptyBusyAndInvalidOutput(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "skill-errors")
	parent, err := s.sessions.get(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	tab, chatID, err := parent.resolveTab("")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/workspaces/" + workspace.ID + "/chats/" + chatID + "/skills"
	if response := doRequest(t, s, http.MethodPost, path); response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "empty_chat") {
		t.Fatalf("empty status=%d body=%s", response.Code, response.Body.String())
	}
	tab.mu.Lock()
	tab.active = &sessions.Turn{ID: "active", Status: "streaming"}
	tab.mu.Unlock()
	if response := doRequest(t, s, http.MethodPost, path); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "session_busy") {
		t.Fatalf("busy status=%d body=%s", response.Code, response.Body.String())
	}
	tab.mu.Lock()
	tab.active = nil
	tab.mu.Unlock()
	seedCompletedSkillTurn(t, s, workspace.ID, chatID)
	s.llmCompleter = &skillTestCompleter{content: `{"folder":"workspace","name":"Bad Name"}`}
	if response := doRequest(t, s, http.MethodPost, path); response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "invalid_skill_response") {
		t.Fatalf("invalid status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(workspace.MainPath, ".echo", "skills")); !os.IsNotExist(err) {
		t.Fatalf("invalid output created skills directory: %v", err)
	}
}

func TestSkillCandidatesEnrichPromptWithoutBodyOrCheckpoint(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "skill-prompt")
	label := normalizeWorkspaceFolderLabel(filepath.Base(workspace.MainPath))
	_, err := s.workspaceSkills(workspace).Upsert(context.Background(), tools.WorkspaceSkillRecordRequest{
		Action: "upsert", Folder: label, Name: "file-database", Description: "Cached workspace file search behavior.",
		Triggers: []string{"workspace file search"}, Body: "# Private body marker\n\nDo not preload this.",
	})
	if err != nil {
		t.Fatal(err)
	}
	mode, err := s.modes.Resolve(workspace.MainPath, "general")
	if err != nil {
		t.Fatal(err)
	}
	prompt := s.agentModeSystemMessage(workspace, mode, "Improve workspace file search", false).Content
	for _, expected := range []string{"file-database", "Cached workspace file search behavior.", "workspace_skill_read", "Recording a new skill is optional"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt missing %q: %s", expected, prompt)
		}
	}
	if strings.Contains(prompt, "Private body marker") || strings.Contains(prompt, "must complete") {
		t.Fatalf("prompt preloaded body or enabled checkpoint: %s", prompt)
	}
}

func TestPlanModeCanReadSkillsButCannotRecordThem(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "skill-plan")
	mode, err := s.modes.Resolve(workspace.MainPath, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if !modeAllowsTool(mode, "workspace_skill_search") || !modeAllowsTool(mode, "workspace_skill_read") || modeAllowsTool(mode, "workspace_skill_record") {
		t.Fatalf("unexpected plan skill permissions: %#v", mode.Permissions)
	}
	prompt := s.agentModeSystemMessage(workspace, mode, "Plan workspace changes", false).Content
	if strings.Contains(prompt, "workspace_skill_record") || strings.Contains(prompt, "must complete") {
		t.Fatalf("plan prompt enabled skill recording: %s", prompt)
	}
}
