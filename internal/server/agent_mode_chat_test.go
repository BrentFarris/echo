package server

import (
	"strings"
	"testing"

	"github.com/brent/echo/internal/agentmodes"
	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/tools"
)

func TestChatSelectedAgentModeControlsPromptAndTools(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "mode-chat")
	modes, err := s.modes.Create(workspace.MainPath, agentmodes.Mode{
		Name:   "Reader",
		Prompt: "Read the code carefully and never speculate.",
		Permissions: map[string]tools.ToolPermission{
			"filesystem_read_text": {Name: "filesystem_read_text", Paths: []string{"src/**"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mode := modes[len(modes)-1]
	capturing := &capturingStreamer{}
	s.llm = capturing

	url := startWebSocketTestServer(t, s)
	conn := dialSharedClient(t, url)
	defer conn.Close()
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "mode-request",
		"message": "inspect this", "agentModeId": mode.ID,
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "done" {
		t.Fatalf("chat failed: %v", finished)
	}

	request := capturing.lastRequest()
	if len(request.Messages) == 0 || request.Messages[0].Role != llm.RoleSystem || !strings.Contains(request.Messages[0].Content, mode.Prompt) {
		t.Fatalf("selected mode prompt missing from request: %+v", request.Messages)
	}
	if len(request.Tools) != 1 || request.Tools[0].Function.Name != "filesystem_read_text" {
		t.Fatalf("selected mode did not filter tools: %+v", request.Tools)
	}

	session, err := s.sessions.get(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.transcript.Turns) != 1 || session.transcript.Turns[0].AgentModeID != mode.ID || session.transcript.Turns[0].AgentModeName != mode.Name {
		t.Fatalf("mode metadata not persisted on turn: %+v", session.transcript.Turns)
	}
	for _, message := range session.transcript.Messages {
		if message.Name == "echo-agent-mode" {
			t.Fatal("ephemeral agent mode system prompt leaked into durable history")
		}
	}
}
