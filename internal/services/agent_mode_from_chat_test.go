package services

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/brent/echo/internal/llm"
)

func TestParseGeneratedAgentModeExtractsValidJSON(t *testing.T) {
	content := `{
  "name": "code-reviewer",
  "prompt": "Review code changes.",
  "toolPermissions": ["filesystem_read_text", "filesystem_list"],
  "pathPermissions": ["src/**"]
}`
	mode, err := parseGeneratedAgentMode(content)
	if err != nil {
		t.Fatalf("parse valid JSON: %v", err)
	}
	if mode.Name != "code-reviewer" {
		t.Fatalf("expected name code-reviewer, got %q", mode.Name)
	}
	if mode.Prompt != "Review code changes." {
		t.Fatalf("unexpected prompt: %q", mode.Prompt)
	}
	if len(mode.ToolPermissions) != 2 || mode.ToolPermissions[0] != "filesystem_read_text" {
		t.Fatalf("unexpected tool permissions: %#v", mode.ToolPermissions)
	}
	if len(mode.PathPermissions) != 1 || mode.PathPermissions[0] != "src/**" {
		t.Fatalf("unexpected path permissions: %#v", mode.PathPermissions)
	}
}

func TestParseGeneratedAgentModeExtractsJSONFromMarkdownBlock(t *testing.T) {
	content := "Here is the agent mode:\n\n```json\n{\"name\":\"reader\",\"prompt\":\"\",\"toolPermissions\":[\"filesystem_read_text\"]}\n```\n\nLet me know if you need changes."
	mode, err := parseGeneratedAgentMode(content)
	if err != nil {
		t.Fatalf("parse markdown JSON: %v", err)
	}
	if mode.Name != "reader" {
		t.Fatalf("expected name reader, got %q", mode.Name)
	}
}

func TestParseGeneratedAgentModeRejectsEmptyResponse(t *testing.T) {
	_, err := parseGeneratedAgentMode("")
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty response error, got %v", err)
	}

	_, err = parseGeneratedAgentMode("   \n  ")
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected whitespace-only to be empty, got %v", err)
	}
}

func TestParseGeneratedAgentModeRejectsMissingName(t *testing.T) {
	_, err := parseGeneratedAgentMode(`{"prompt":"something"}`)
	if err == nil || !strings.Contains(err.Error(), "required fields") {
		t.Fatalf("expected missing name error, got %v", err)
	}

	_, err = parseGeneratedAgentMode(`{"name":"","prompt":""}`)
	if err == nil || !strings.Contains(err.Error(), "required fields") {
		t.Fatalf("expected empty name to fail, got %v", err)
	}
}

func TestParseGeneratedAgentModeRejectsInvalidJSON(t *testing.T) {
	_, err := parseGeneratedAgentMode("This is not JSON at all.")
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("expected invalid JSON error, got %v", err)
	}

	_, err = parseGeneratedAgentMode(`{broken json`)
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("expected broken JSON error, got %v", err)
	}
}

func TestParseGeneratedAgentModeTrimsWhitespace(t *testing.T) {
	content := `  {"name": "  trim-test  ", "prompt": "  some prompt  "}  `
	mode, err := parseGeneratedAgentMode(content)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if mode.Name != "trim-test" {
		t.Fatalf("expected trimmed name, got %q", mode.Name)
	}
	if mode.Prompt != "some prompt" {
		t.Fatalf("expected trimmed prompt, got %q", mode.Prompt)
	}
}

func TestAgentModeChatTranscriptRequiresToolCalls(t *testing.T) {
	messages := []ChatMessage{
		{ID: "msg-1", Role: llm.RoleUser, Content: "Hello", Status: "complete"},
		{ID: "msg-2", Role: llm.RoleAssistant, Content: "Hi there.", Status: "complete"},
	}
	transcript, usage, err := agentModeChatTranscript(messages)
	if err == nil || !strings.Contains(err.Error(), "tool usage") {
		t.Fatalf("expected tool usage error, got transcript=%q usage=%v err=%v", transcript, usage, err)
	}
}

func TestAgentModeChatTranscriptExtractsToolUsage(t *testing.T) {
	messages := []ChatMessage{
		{ID: "msg-1", Role: llm.RoleUser, Content: "Read the file.", Status: "complete"},
		{
			ID:      "msg-2",
			Role:    llm.RoleAssistant,
			Content: "Reading the file now.",
			Status:  "complete",
			ToolCalls: []ChatToolActivity{
				{
					ID:        "call-1",
					Name:      "filesystem_read_text",
					Arguments: `{"path":"src/main.go"}`,
					Status:    "complete",
					Result:    `{"content":"package main"}`,
				},
				{
					ID:        "call-2",
					Name:      "filesystem_list",
					Arguments: `{"path":"src/"}`,
					Status:    "complete",
					Result:    `["main.go"]`,
				},
			},
		},
	}
	transcript, usage, err := agentModeChatTranscript(messages)
	if err != nil {
		t.Fatalf("transcript: %v", err)
	}
	if transcript == "" {
		t.Fatal("expected non-empty transcript")
	}

	if len(usage) != 2 {
		t.Fatalf("expected 2 tool usages, got %d", len(usage))
	}

	var readUsage *toolUsageSummary
	var listUsage *toolUsageSummary
	for i := range usage {
		if usage[i].Name == "filesystem_read_text" {
			readUsage = &usage[i]
		}
		if usage[i].Name == "filesystem_list" {
			listUsage = &usage[i]
		}
	}
	if readUsage == nil || listUsage == nil {
		t.Fatalf("expected both tools in usage, got %#v", usage)
	}
	if readUsage.CallCount != 1 {
		t.Fatalf("expected read call count 1, got %d", readUsage.CallCount)
	}
	if len(readUsage.PathArgs) != 1 || readUsage.PathArgs[0] != "src/main.go" {
		t.Fatalf("unexpected path args: %#v", readUsage.PathArgs)
	}
	if listUsage.CallCount != 1 {
		t.Fatalf("expected list call count 1, got %d", listUsage.CallCount)
	}
}

func TestAgentModeChatTranscriptSkipsErrorAndEmptyResults(t *testing.T) {
	messages := []ChatMessage{
		{ID: "msg-1", Role: llm.RoleUser, Content: "Do something.", Status: "complete"},
		{
			ID:      "msg-2",
			Role:    llm.RoleAssistant,
			Content: "Trying...",
			Status:  "complete",
			ToolCalls: []ChatToolActivity{
				{ID: "call-1", Name: "bad_tool", Status: "error", Result: "failed"},
				{ID: "call-2", Name: "empty_result", Status: "complete", Result: ""},
				{ID: "call-3", Name: "filesystem_read_text", Arguments: `{"path":"a.go"}`, Status: "complete", Result: "content"},
			},
		},
	}
	transcript, usage, err := agentModeChatTranscript(messages)
	if err != nil {
		t.Fatalf("transcript: %v", err)
	}

	if len(usage) != 1 || usage[0].Name != "filesystem_read_text" {
		t.Fatalf("expected only successful tool in usage, got %#v", usage)
	}
	if !strings.Contains(transcript, "filesystem_read_text") {
		t.Fatal("expected transcript to contain successful tool")
	}
}

func TestAgentModeChatTranscriptSkipsNonCompleteAssistantMessages(t *testing.T) {
	messages := []ChatMessage{
		{ID: "msg-1", Role: llm.RoleUser, Content: "Hello.", Status: "complete"},
		{
			ID:   "msg-2",
			Role: llm.RoleAssistant,
			Content: "Streaming...",
			Status:  "streaming",
			ToolCalls: []ChatToolActivity{
				{ID: "call-1", Name: "filesystem_read_text", Arguments: `{"path":"a.go"}`, Status: "complete", Result: "content"},
			},
		},
	}
	transcript, usage, err := agentModeChatTranscript(messages)
	if err != nil || transcript == "" {
		t.Fatalf("expected streaming message to be included, got err=%v transcript=%q usage=%v", err, transcript, usage)
	}

	messages[1].Status = "canceled"
	transcript2, _, err2 := agentModeChatTranscript(messages)
	if err2 == nil || !strings.Contains(err2.Error(), "tool usage") {
		t.Fatalf("expected canceled message to be skipped, got err=%v transcript=%q", err2, transcript2)
	}
}

func TestCreateAgentModeFromChatCallsLLMAndCreatesMode(t *testing.T) {
	root := t.TempDir()
	var captured llm.ChatRequest
	var requestCount atomic.Int32
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCompleteRequest(t, r)
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requestCount.Add(1)
		writeChatResponse(t, w, `{"name":"auto-reader","prompt":"Auto-generated mode.","toolPermissions":["filesystem_read_text"],"pathPermissions":["src/**"]}`)
	}))

	// Seed chat with tool usage for transcript.
	seedChatPlan(service, workspaceID, []ChatMessage{
		{ID: "user-1", Role: llm.RoleUser, Content: "Read the codebase.", Status: "complete"},
		{
			ID:      "assistant-1",
			Role:    llm.RoleAssistant,
			Content: "Reading files.",
			Status:  "complete",
			ToolCalls: []ChatToolActivity{
				{ID: "call-1", Name: "filesystem_read_text", Arguments: `{"path":"src/main.go"}`, Status: "complete", Result: "package main"},
			},
		},
	}, nil)

	result, err := service.CreateAgentModeFromChat(workspaceID)
	if err != nil {
		t.Fatalf("create mode from chat: %v", err)
	}
	if result.Name != "auto-reader" {
		t.Fatalf("expected auto-reader, got %q", result.Name)
	}
	if result.Prompt != "Auto-generated mode." {
		t.Fatalf("unexpected prompt: %q", result.Prompt)
	}
	if len(result.ID) == 0 {
		t.Fatal("expected non-empty ID in creation result")
	}

	// Verify the LLM was called with the correct system prompt.
	if requestCount.Load() != 1 {
		t.Fatalf("expected one LLM request, got %d", requestCount.Load())
	}
	if len(captured.Messages) == 0 || captured.Messages[0].Role != llm.RoleSystem {
		t.Fatal("expected system message in synthesis request")
	}
	if !strings.Contains(captured.Messages[0].Content, "agent modes from tool usage patterns") {
		t.Fatalf("expected agent mode system prompt, got %q", captured.Messages[0].Content)
	}

	// Verify the mode exists in the service.
	modes := service.ListAgentModes("")
	var found *AgentMode
	for i, m := range modes {
		if m.Name == "auto-reader" {
			found = &modes[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected created mode in list")
	}
	if found.ID != result.ID {
		t.Fatalf("mode ID mismatch: service=%s result=%s", found.ID, result.ID)
	}
}

func TestCreateAgentModeFromChatRequiresToolUsage(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("LLM should not be called without tool usage")
	}))

	// Seed chat without tool calls.
	seedChatPlan(service, workspaceID, []ChatMessage{
		{ID: "user-1", Role: llm.RoleUser, Content: "Hello.", Status: "complete"},
		{ID: "assistant-1", Role: llm.RoleAssistant, Content: "Hi.", Status: "complete"},
	}, nil)

	_, err := service.CreateAgentModeFromChat(workspaceID)
	if err == nil || !strings.Contains(err.Error(), "tool usage") {
		t.Fatalf("expected tool usage error, got %v", err)
	}
}

func TestCreateAgentModeFromChatRejectsInvalidLLMResponse(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCompleteRequest(t, r)
		writeChatResponse(t, w, `not valid json`)
	}))

	seedChatPlan(service, workspaceID, []ChatMessage{
		{ID: "user-1", Role: llm.RoleUser, Content: "Read code.", Status: "complete"},
		{
			ID:      "assistant-1",
			Role:    llm.RoleAssistant,
			Content: "Reading.",
			Status:  "complete",
			ToolCalls: []ChatToolActivity{
				{ID: "call-1", Name: "filesystem_read_text", Arguments: `{"path":"a.go"}`, Status: "complete", Result: "ok"},
			},
		},
	}, nil)

	_, err := service.CreateAgentModeFromChat(workspaceID)
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("expected invalid JSON error, got %v", err)
	}
}

func TestCreateAgentModeFromChatRejectsEmptyLLMResponse(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCompleteRequest(t, r)
		// Return a response with no choices.
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{}})
	}))

	seedChatPlan(service, workspaceID, []ChatMessage{
		{ID: "user-1", Role: llm.RoleUser, Content: "Read code.", Status: "complete"},
		{
			ID:      "assistant-1",
			Role:    llm.RoleAssistant,
			Content: "Reading.",
			Status:  "complete",
			ToolCalls: []ChatToolActivity{
				{ID: "call-1", Name: "filesystem_read_text", Arguments: `{"path":"a.go"}`, Status: "complete", Result: "ok"},
			},
		},
	}, nil)

	_, err := service.CreateAgentModeFromChat(workspaceID)
	if err == nil || !strings.Contains(err.Error(), "no choices") {
		t.Fatalf("expected no choices error, got %v", err)
	}
}

func TestCreateAgentModeFromChatRejectsDuplicateName(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCompleteRequest(t, r)
		writeChatResponse(t, w, `{"name":"general","prompt":""}`)
	}))

	seedChatPlan(service, workspaceID, []ChatMessage{
		{ID: "user-1", Role: llm.RoleUser, Content: "Read code.", Status: "complete"},
		{
			ID:      "assistant-1",
			Role:    llm.RoleAssistant,
			Content: "Reading.",
			Status:  "complete",
			ToolCalls: []ChatToolActivity{
				{ID: "call-1", Name: "filesystem_read_text", Arguments: `{"path":"a.go"}`, Status: "complete", Result: "ok"},
			},
		},
	}, nil)

	_, err := service.CreateAgentModeFromChat(workspaceID)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected duplicate name error, got %v", err)
	}
}

func TestCreateAgentModeFromChatRequiresWorkspace(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	_, err := service.CreateAgentModeFromChat("nonexistent")
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("expected workspace error, got %v", err)
	}
}

func TestAgentModeFromChatUserPromptIncludesWorkspaceFolders(t *testing.T) {
	workspace := Workspace{
		Folders: []WorkspaceFolder{
			{Label: "echo", Missing: false},
			{Label: "other", Missing: false},
		},
	}
	usage := []toolUsageSummary{{Name: "filesystem_read_text", CallCount: 1}}
	prompt := agentModeFromChatUserPrompt(workspace, "transcript content", usage)

	if !strings.Contains(prompt, "echo") || !strings.Contains(prompt, "other") {
		t.Fatalf("expected workspace folders in prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "filesystem_read_text") {
		t.Fatal("expected tool usage in prompt")
	}
	if !strings.Contains(prompt, "transcript content") {
		t.Fatal("expected transcript in prompt")
	}
}
