package services

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/brent/echo/internal/llm"
)

func TestCreateSkillFromChatSynthesizesResearchAndAvoidsNameCollisions(t *testing.T) {
	root := t.TempDir()
	var requestCount atomic.Int32
	var captured llm.ChatRequest
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCompleteRequest(t, r)
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requestCount.Add(1)
		writeChatResponse(t, w, `{
			"folder": "001",
			"name": "chat-streaming",
			"description": "How Echo streams chat responses and records tool research.",
			"triggers": ["chat streaming", "tool research"],
			"body": "# Chat streaming\n\nThe chat loop streams assistant content and records completed tool results."
		}`)
	}))
	seedChatPlan(service, workspaceID, []ChatMessage{
		{ID: "user-1", Role: llm.RoleUser, Content: "Investigate chat streaming.", Status: "complete"},
		{
			ID:      "assistant-1",
			Role:    llm.RoleAssistant,
			Content: "The chat loop streams tokens before completing the message.",
			Status:  "complete",
			ToolCalls: []ChatToolActivity{{
				ID:        "call-1",
				Name:      "filesystem_read_text",
				Arguments: `{"path":"001/internal/services/chat.go"}`,
				Status:    "complete",
				Result:    `{"content":"func runChatTurn() { /* research marker */ }"}`,
			}},
		},
	}, nil)

	first, err := service.CreateSkillFromChat(workspaceID)
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}
	if first.ID != "001/chat-streaming" || first.Path != "001/.echo/skills/chat-streaming/SKILL.md" {
		t.Fatalf("unexpected first result: %#v", first)
	}
	if !requestContainsContent(captured, "Investigate chat streaming.") ||
		!requestContainsContent(captured, "research marker") ||
		!requestContainsContent(captured, "Available workspace folders: [\"001\"]") {
		t.Fatalf("skill synthesis request omitted chat research: %#v", captured.Messages)
	}
	data, err := os.ReadFile(filepath.Join(root, ".echo", "skills", "chat-streaming", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "name: chat-streaming") || !strings.Contains(string(data), "# Chat streaming") {
		t.Fatalf("unexpected skill file: %s", data)
	}

	second, err := service.CreateSkillFromChat(workspaceID)
	if err != nil {
		t.Fatalf("create colliding skill: %v", err)
	}
	if second.ID != "001/chat-streaming-2" {
		t.Fatalf("expected collision suffix, got %#v", second)
	}
	if requestCount.Load() != 2 {
		t.Fatalf("expected two synthesis requests, got %d", requestCount.Load())
	}
}

func TestCreateSkillFromChatRequiresCompletedResearch(t *testing.T) {
	service, workspaceID, _ := newWorkspaceFilesTestService(t)
	if _, err := service.CreateSkillFromChat(workspaceID); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty chat error, got %v", err)
	}

	seedChatPlan(service, workspaceID, []ChatMessage{
		{ID: "user-1", Role: llm.RoleUser, Content: "Investigate this.", Status: "complete"},
	}, nil)
	if _, err := service.CreateSkillFromChat(workspaceID); err == nil || !strings.Contains(err.Error(), "completed research") {
		t.Fatalf("expected completed research error, got %v", err)
	}
}

func TestCreateSkillFromChatRejectsInvalidModelOutput(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCompleteRequest(t, r)
		writeChatResponse(t, w, `{"folder":"001","name":"Bad Name"}`)
	}))
	seedChatPlan(service, workspaceID, []ChatMessage{
		{ID: "user-1", Role: llm.RoleUser, Content: "Investigate chat.", Status: "complete"},
		{ID: "assistant-1", Role: llm.RoleAssistant, Content: "Durable research.", Status: "complete"},
	}, nil)

	if _, err := service.CreateSkillFromChat(workspaceID); err == nil {
		t.Fatal("expected invalid model output error")
	}
	if _, err := os.Stat(filepath.Join(root, ".echo", "skills")); !os.IsNotExist(err) {
		t.Fatalf("invalid output should not create the skills cache, stat error: %v", err)
	}
}
