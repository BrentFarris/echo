package server

import (
	"testing"

	"github.com/brent/echo/internal/llm"
)

// TestChatSendTemperatureOverride verifies that a per-request temperature
// selected in the agent mode popup reaches the LLM request.
func TestChatSendTemperatureOverride(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "temperature-chat")
	capturing := &capturingStreamer{}
	s.llm = capturing

	url := startWebSocketTestServer(t, s)
	conn := dialSharedClient(t, url)
	defer conn.Close()
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "temperature-request",
		"message": "inspect this", "agentModeId": "general", "temperature": 0.35,
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "done" {
		t.Fatalf("chat failed: %v", finished)
	}

	request := capturing.lastRequest()
	if request.Temperature == nil || *request.Temperature != 0.35 {
		t.Fatalf("expected temperature 0.35, got %v", request.Temperature)
	}
}

// TestChatSendTemperatureInvalid verifies an out-of-range temperature is
// rejected before any LLM request is made.
func TestChatSendTemperatureInvalid(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "temperature-invalid")
	capturing := &capturingStreamer{}
	s.llm = capturing

	url := startWebSocketTestServer(t, s)
	conn := dialSharedClient(t, url)
	defer conn.Close()
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "temperature-invalid",
		"message": "inspect this", "agentModeId": "general", "temperature": 1.5,
	}); err != nil {
		t.Fatal(err)
	}
	errMessage := readUntilMessageType(t, conn, "command_error")
	if errMessage["code"] != "invalid_temperature" {
		t.Fatalf("expected invalid_temperature command error, got %v", errMessage)
	}

	request := capturing.lastRequest()
	if request.Temperature != nil {
		t.Fatalf("expected no request to reach the LLM, got request with temperature %v", *request.Temperature)
	}
}

// TestChatSendNoOverridePreservesEndpointTemperature verifies that omitting
// the override leaves the endpoint-configured temperature intact.
func TestChatSendNoOverridePreservesEndpointTemperature(t *testing.T) {
	s, _ := newTestServer(t)
	cfg := llm.DefaultSettings()
	cfg.Endpoints[0].Temperature = 1.4
	cfg.EndpointSelection.Chat = cfg.Endpoints[0].ID
	if err := s.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	s.initLLM()
	workspace := createChatWorkspace(t, s, "temperature-default")
	capturing := &capturingStreamer{}
	s.llm = capturing

	url := startWebSocketTestServer(t, s)
	conn := dialSharedClient(t, url)
	defer conn.Close()
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "temperature-default",
		"message": "inspect this", "agentModeId": "general",
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "done" {
		t.Fatalf("chat failed: %v", finished)
	}

	request := capturing.lastRequest()
	if request.Temperature == nil || *request.Temperature != 1.4 {
		t.Fatalf("expected endpoint temperature 1.4 to be preserved, got %v", request.Temperature)
	}
}
