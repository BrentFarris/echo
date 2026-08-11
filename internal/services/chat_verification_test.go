package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/brent/echo/internal/llm"
)

// chatVerificationRunnerStub lets tests inject deterministic verification
// reports into the chat loop without executing real test/build commands.
type chatVerificationRunnerStub struct {
	reports []kanbanVerificationReport
	calls   int
	paths   [][]string
}

func (stub *chatVerificationRunnerStub) run(_ context.Context, _ Workspace, paths []string) (kanbanVerificationReport, error) {
	stub.calls++
	stub.paths = append(stub.paths, append([]string(nil), paths...))
	if len(stub.reports) == 0 {
		return kanbanVerificationReport{Status: kanbanVerificationStatusPassed, Message: "Verification passed."}, nil
	}
	report := stub.reports[0]
	stub.reports = stub.reports[1:]
	return report, nil
}

func chatVerificationFailedReport(attempt int) kanbanVerificationReport {
	return kanbanVerificationReport{
		Status:  kanbanVerificationStatusFailed,
		Message: fmt.Sprintf("Verification failed (attempt %d).", attempt),
	}
}

func chatVerificationEditSSE(path string) (string, string) {
	args := fmt.Sprintf(`{"path":%q,"oldText":"before\n","newText":"after\n"}`, path)
	return fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"filesystem_edit_text","arguments":%q}}]}}]}`, args),
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`
}

func chatVerificationContentSSE(content string) (string, string) {
	return fmt.Sprintf(`{"choices":[{"index":0,"delta":{"content":%q}}]}`, content),
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
}

func chatVerificationLastUserContent(captured llm.ChatRequest) string {
	for i := len(captured.Messages) - 1; i >= 0; i-- {
		if captured.Messages[i].Role == llm.RoleUser {
			return captured.Messages[i].Content
		}
	}
	return ""
}

func chatVerificationFinalMessage(t *testing.T, session ChatSession) ChatMessage {
	t.Helper()
	if len(session.Messages) == 0 {
		t.Fatal("expected at least one message")
	}
	return session.Messages[len(session.Messages)-1]
}

func TestChatTurnVerificationRepairsThenPasses(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requestCount atomic.Int32
	var capturedThird llm.ChatRequest
	var notesPath string
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			edit, toolDone := chatVerificationEditSSE(notesPath)
			writeSSE(t, w, edit, toolDone)
		case 2:
			content, done := chatVerificationContentSSE("Done.")
			writeSSE(t, w, content, done)
		case 3:
			if err := json.NewDecoder(r.Body).Decode(&capturedThird); err != nil {
				t.Fatalf("decode request 3: %v", err)
			}
			writeSSE(t, w,
				kanbanToolCallPayload(t, "call_skill", "workspace_skill_record", map[string]any{"action": "skip", "reason": "Not needed."}),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 4:
			content, done := chatVerificationContentSSE("Fixed.")
			writeSSE(t, w, content, done)
		default:
			t.Fatalf("unexpected request %d", requestCount.Load())
		}
	}))
	notesPath = labeledTestPath(t, service, workspaceID, "notes.txt")

	stub := &chatVerificationRunnerStub{reports: []kanbanVerificationReport{
		chatVerificationFailedReport(1),
		{Status: kanbanVerificationStatusPassed, Message: "Verification passed."},
	}}
	service.chatVerificationRunner = stub.run

	if _, err := service.SendChatMessage(workspaceID, "Update notes"); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)

	if stub.calls != 2 {
		t.Fatalf("expected 2 verification runs, got %d", stub.calls)
	}
	if !strings.Contains(chatVerificationLastUserContent(capturedThird), "Automatic verification failed") {
		t.Fatalf("expected repair prompt in request 3, got %q", chatVerificationLastUserContent(capturedThird))
	}
	final := chatVerificationFinalMessage(t, session)
	if !strings.Contains(final.Content, "Verification passed") {
		t.Fatalf("expected passed line in final message, got %q", final.Content)
	}
	if strings.Contains(final.Content, "Verification failed after") {
		t.Fatalf("did not expect failed notice in final message, got %q", final.Content)
	}
}

func TestChatTurnVerificationSkipsWithoutFileChanges(t *testing.T) {
	root := t.TempDir()
	var requestCount atomic.Int32
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			content, done := chatVerificationContentSSE("Nothing to do.")
			writeSSE(t, w, content, done)
		default:
			t.Fatalf("unexpected request %d", requestCount.Load())
		}
	}))

	stub := &chatVerificationRunnerStub{}
	service.chatVerificationRunner = stub.run

	if _, err := service.SendChatMessage(workspaceID, "Hello"); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)

	if stub.calls != 0 {
		t.Fatalf("expected no verification runs without file changes, got %d", stub.calls)
	}
	final := chatVerificationFinalMessage(t, session)
	if strings.Contains(final.Content, "Verification") {
		t.Fatalf("did not expect a verification line without changes, got %q", final.Content)
	}
}

func TestChatTurnVerificationCompletesWhenUnverified(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requestCount atomic.Int32
	var notesPath string
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			edit, toolDone := chatVerificationEditSSE(notesPath)
			writeSSE(t, w, edit, toolDone)
		case 2:
			content, done := chatVerificationContentSSE("Done.")
			writeSSE(t, w, content, done)
		case 3:
			writeSSE(t, w,
				kanbanToolCallPayload(t, "call_skill", "workspace_skill_record", map[string]any{"action": "skip", "reason": "Not needed."}),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 4:
			content, done := chatVerificationContentSSE("Final.")
			writeSSE(t, w, content, done)
		default:
			t.Fatalf("unexpected request %d", requestCount.Load())
		}
	}))
	notesPath = labeledTestPath(t, service, workspaceID, "notes.txt")

	stub := &chatVerificationRunnerStub{reports: []kanbanVerificationReport{{
		Status:  kanbanVerificationStatusUnverified,
		Message: "Unverified: no matching verification command was detected.",
	}}}
	service.chatVerificationRunner = stub.run

	if _, err := service.SendChatMessage(workspaceID, "Update notes"); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)

	if stub.calls != 1 {
		t.Fatalf("expected 1 verification run, got %d", stub.calls)
	}
	final := chatVerificationFinalMessage(t, session)
	if !strings.Contains(final.Content, "could not auto-verify") {
		t.Fatalf("expected unverified line in final message, got %q", final.Content)
	}
}

func TestChatTurnVerificationCompletesAfterRetryBudget(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requestCount atomic.Int32
	var notesPath string
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			edit, toolDone := chatVerificationEditSSE(notesPath)
			writeSSE(t, w, edit, toolDone)
		case 2:
			content, done := chatVerificationContentSSE("Done.")
			writeSSE(t, w, content, done)
		case 3:
			writeSSE(t, w,
				kanbanToolCallPayload(t, "call_skill", "workspace_skill_record", map[string]any{"action": "skip", "reason": "Not needed."}),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 4:
			content, done := chatVerificationContentSSE("Still failing.")
			writeSSE(t, w, content, done)
		case 5:
			content, done := chatVerificationContentSSE("No good.")
			writeSSE(t, w, content, done)
		default:
			t.Fatalf("unexpected request %d", requestCount.Load())
		}
	}))
	notesPath = labeledTestPath(t, service, workspaceID, "notes.txt")

	stub := &chatVerificationRunnerStub{reports: []kanbanVerificationReport{
		chatVerificationFailedReport(1),
		chatVerificationFailedReport(2),
		chatVerificationFailedReport(3),
	}}
	service.chatVerificationRunner = stub.run

	if _, err := service.SendChatMessage(workspaceID, "Update notes"); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)

	if stub.calls != 3 {
		t.Fatalf("expected 3 verification runs before the retry budget is exhausted, got %d", stub.calls)
	}
	final := chatVerificationFinalMessage(t, session)
	if !strings.Contains(final.Content, "Verification failed after 3 attempt(s)") {
		t.Fatalf("expected failed notice in final message, got %q", final.Content)
	}
}

func TestChatTurnVerificationNeverRunsInPlanMode(t *testing.T) {
	root := t.TempDir()
	var requestCount atomic.Int32
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"filesystem_create_text","arguments":"{\"path\":\"blocked.txt\",\"content\":\"nope\"}"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 2:
			content, done := chatVerificationContentSSE("I cannot edit in plan mode.")
			writeSSE(t, w, content, done)
		default:
			t.Fatalf("unexpected request %d", requestCount.Load())
		}
	}))

	stub := &chatVerificationRunnerStub{}
	service.chatVerificationRunner = stub.run

	if _, err := service.SendChatMessageWithPlanMode(workspaceID, "Create a file", true); err != nil {
		t.Fatalf("send plan-mode chat: %v", err)
	}
	waitForChatIdle(t, service, workspaceID)

	if stub.calls != 0 {
		t.Fatalf("expected no verification runs in plan mode, got %d", stub.calls)
	}
}

func TestDetectKanbanVerificationCommandsGoAndNode(t *testing.T) {
	root := t.TempDir()

	goRoot := filepath.Join(root, "go-mod")
	if err := os.MkdirAll(filepath.Join(goRoot, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goRoot, "go.mod"), []byte("module example.com/gomod\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goRoot, "pkg", "math.go"), []byte("package pkg\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	goWorkspace := workspaceFromPath(goRoot)
	goCommands := detectKanbanVerificationCommands(goWorkspace, []string{goWorkspace.Folders[0].Label + "/pkg/math.go"})
	if len(goCommands) != 1 || goCommands[0].Command != "go test ./..." {
		t.Fatalf("expected go test ./... detection, got %#v", goCommands)
	}

	nodeRoot := filepath.Join(root, "node-mod")
	if err := os.MkdirAll(filepath.Join(nodeRoot, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nodeRoot, "package.json"), []byte(`{"scripts":{"test":"echo hi"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nodeRoot, "src", "app.ts"), []byte("export const x = 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	nodeWorkspace := workspaceFromPath(nodeRoot)
	nodeCommands := detectKanbanVerificationCommands(nodeWorkspace, []string{nodeWorkspace.Folders[0].Label + "/src/app.ts"})
	if len(nodeCommands) != 1 || nodeCommands[0].Command != "npm test" {
		t.Fatalf("expected npm test detection, got %#v", nodeCommands)
	}
}
