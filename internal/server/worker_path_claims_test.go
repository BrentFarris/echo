package server

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/tools"
)

func TestExtractWorkerClaimPaths(t *testing.T) {
	paths := extractWorkerClaimPaths("filesystem_move", `{"path":" echo/src/a.ts ","destination":"./echo/src/b.ts"}`)
	if len(paths) != 2 || paths[0] != "echo/src/a.ts" || paths[1] != "echo/src/b.ts" {
		t.Fatalf("move paths = %#v", paths)
	}
	if paths := extractWorkerClaimPaths("filesystem_edit_text", `{"path":"echo/only.ts"}`); len(paths) != 1 || paths[0] != "echo/only.ts" {
		t.Fatalf("edit paths = %#v", paths)
	}
	// Non-mutating tools never claim.
	if paths := extractWorkerClaimPaths("filesystem_read_text", `{"path":"echo/x.ts"}`); paths != nil {
		t.Fatalf("read tool claimed: %#v", paths)
	}
	if paths := extractWorkerClaimPaths("shell_command", `{"command":"ls"}`); paths != nil {
		t.Fatalf("shell claimed: %#v", paths)
	}
	// Unparsable or path-less arguments claim nothing.
	if paths := extractWorkerClaimPaths("filesystem_edit_text", `not json`); paths != nil {
		t.Fatalf("invalid json claimed: %#v", paths)
	}
	if paths := extractWorkerClaimPaths("filesystem_edit_text", `{}`); paths != nil {
		t.Fatalf("path-less arguments claimed: %#v", paths)
	}
}

func TestWorkerPathsConflict(t *testing.T) {
	cases := []struct {
		existing, newPath string
		conflict          bool
	}{
		{"echo/a.ts", "echo/a.ts", true},
		{"echo", "echo/a.ts", true},
		{"echo/a.ts", "echo", true},
		{"echo/a.ts", "echo/a.ts.bak", false},
		{"echo/a.ts", "echo/b.ts", false},
		{"echo/a.ts", "other/a.ts", false},
	}
	for _, testCase := range cases {
		if got := workerPathsConflict(testCase.existing, testCase.newPath); got != testCase.conflict {
			t.Fatalf("workerPathsConflict(%q, %q) = %v, want %v", testCase.existing, testCase.newPath, got, testCase.conflict)
		}
	}
}

func TestWorkerPathClaimsAcquireConflictRelease(t *testing.T) {
	run := &chatResearchRun{}

	if owner, path, claimed := run.claimWorkerPaths("agent-1", "filesystem_edit_text", `{"path":"echo/src/a.ts"}`); claimed {
		t.Fatalf("first claim conflicted: owner=%q path=%q", owner, path)
	}
	// Re-claiming is idempotent for the owner (follow-up jobs).
	if _, _, claimed := run.claimWorkerPaths("agent-1", "filesystem_edit_text", `{"path":"echo/src/a.ts"}`); claimed {
		t.Fatal("owner re-claim conflicted")
	}

	// A different agent is denied the exact path.
	owner, path, claimed := run.claimWorkerPaths("agent-2", "filesystem_create_text", `{"path":"echo/src/a.ts"}`)
	if !claimed || owner != "agent-1" || path != "echo/src/a.ts" {
		t.Fatalf("expected conflict with agent-1, got owner=%q path=%q claimed=%v", owner, path, claimed)
	}

	// A path containing a claimed file conflicts; nothing is claimed on conflict.
	if _, _, claimed := run.claimWorkerPaths("agent-2", "filesystem_delete_file", `{"path":"echo/src"}`); !claimed {
		t.Fatal("expected descendant conflict for echo/src")
	}

	// filesystem_move claims both the source and the destination.
	if _, _, claimed := run.claimWorkerPaths("agent-2", "filesystem_move", `{"path":"echo/other.ts","destination":"echo/src/a.ts"}`); !claimed {
		t.Fatal("expected destination conflict for filesystem_move")
	}
	if _, _, claimed := run.claimWorkerPaths("agent-2", "filesystem_move", `{"path":"echo/x.ts","destination":"echo/y.ts"}`); claimed {
		t.Fatal("unrelated move conflicted")
	}

	// Conflict messages resolve the owner's display name.
	run.mu.Lock()
	run.agents = map[string]*chatResearchAgentRun{"agent-1": {id: "agent-1", name: "Alpha"}}
	run.mu.Unlock()
	owner, _, _ = run.claimWorkerPaths("agent-3", "filesystem_edit_text", `{"path":"echo/src/a.ts"}`)
	if owner != "Alpha (agent-1)" {
		t.Fatalf("owner label = %q", owner)
	}

	// Released claims are claimable again.
	run.releaseWorkerPathClaims("agent-1")
	if _, _, claimed := run.claimWorkerPaths("agent-3", "filesystem_edit_text", `{"path":"echo/src/a.ts"}`); claimed {
		t.Fatal("claim survived release")
	}
}

// claimFlowStreamer drives a turn where two workers are asked to write the
// same file. Exactly one worker's create call must be blocked with
// path_claimed_by_agent; the blocked worker sees the typed error in its
// private transcript and reports it.
type claimFlowStreamer struct {
	mu             sync.Mutex
	workspaceLabel string
	workerCalls    map[string]int
	conflicts      map[string]bool
	parentCalls    int
}

func (f *claimFlowStreamer) isWorker(request llm.ChatRequest) bool {
	return len(request.Messages) > 0 && request.Messages[0].Name == "echo-research-agent" &&
		strings.Contains(request.Messages[0].Content, "implementation worker subagent")
}

func (f *claimFlowStreamer) taskKey(request llm.ChatRequest) string {
	if strings.Contains(request.Messages[0].Content, "alpha") {
		return "alpha"
	}
	return "beta"
}

func (f *claimFlowStreamer) StreamChat(_ context.Context, request llm.ChatRequest) *llm.Stream {
	f.mu.Lock()
	var call int
	var key string
	if f.isWorker(request) {
		key = f.taskKey(request)
		f.workerCalls[key]++
		call = f.workerCalls[key]
		if call > 1 {
			for _, message := range request.Messages {
				if message.Role == llm.RoleTool && strings.Contains(message.Content, "path_claimed_by_agent") {
					f.conflicts[key] = true
				}
			}
		}
	} else {
		f.parentCalls++
		call = f.parentCalls
	}
	shared := f.workspaceLabel + "/shared-report.md"
	f.mu.Unlock()

	events := make(chan llm.StreamEvent, 4)
	if key != "" {
		switch call {
		case 1:
			events <- researchToolCall("create-"+key, "filesystem_create_text", `{"path":"`+shared+`","content":"`+key+` section"}`)
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
		default:
			events <- llm.StreamEvent{Type: llm.EventToken, Content: key + " worker finished."}
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
		}
	} else {
		switch call {
		case 1:
			events <- researchToolCall("spawn-workers", tools.WorkerAgentsSpawnToolName,
				`{"workers":[`+
					`{"name":"Alpha","task":"Create the alpha section of `+shared+` and touch nothing else."},`+
					`{"name":"Beta","task":"Create the beta section of `+shared+` and touch nothing else."}]}`)
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
		case 2:
			events <- researchToolCall("wait-workers", tools.WorkerAgentsWaitToolName, `{"waitFor":"all","timeoutSeconds":15}`)
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
		default:
			events <- llm.StreamEvent{Type: llm.EventToken, Content: "Both workers reported."}
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
		}
	}
	close(events)
	return &llm.Stream{ID: "claim-flow", Events: events}
}

func TestWorkerPathClaimsBlockOverlappingWrites(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "claim-flow")
	streamer := &claimFlowStreamer{
		workspaceLabel: normalizeWorkspaceFolderLabel(filepath.Base(workspace.MainPath)),
		workerCalls:    map[string]int{},
		conflicts:      map[string]bool{},
	}
	server.llm = streamer

	url := startWebSocketTestServer(t, server)
	conn := dialSharedClient(t, url)
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "claim-request",
		"message": "Have two workers write the shared report.",
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "done" {
		t.Fatalf("claim chat failed: %#v", finished)
	}

	// Exactly one worker saw the typed conflict in its private transcript.
	conflicted := 0
	for key, saw := range streamer.conflicts {
		if saw {
			conflicted++
		} else {
			t.Fatalf("worker %q finished without any tool outcome recorded", key)
		}
	}
	if conflicted != 1 {
		t.Fatalf("expected exactly one conflicting worker, got %d (%#v)", conflicted, streamer.conflicts)
	}

	// The conflict is visible in the trajectory as a failed tool result.
	parent, err := server.sessions.get(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	tab, _, err := parent.resolveSurfaceTab(loadActiveTabTranscript(t, workspace).ChatID, chatSurfaceMain)
	if err != nil {
		t.Fatal(err)
	}
	page, err := tab.trajectory.Page(0, 500)
	if err != nil {
		t.Fatal(err)
	}
	conflictResults := 0
	for _, event := range page.Events {
		if event.Type == "research/tool_result" && strings.Contains(string(event.Data), "path_claimed_by_agent") {
			conflictResults++
		}
	}
	if conflictResults != 1 {
		t.Fatalf("expected one path_claimed_by_agent tool result in the trajectory, got %d", conflictResults)
	}
}
