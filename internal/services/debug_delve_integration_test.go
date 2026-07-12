package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDelveDAPBreakpointEvaluateAndStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Delve integration test in short mode")
	}
	if _, err := exec.LookPath("dlv"); err != nil {
		t.Skip("Delve is not installed")
	}
	workspaceRoot := t.TempDir()
	root := filepath.Join(workspaceRoot, "src")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(root, "main.go")
	program := `package main

import (
	"fmt"
	"time"
)

func main() {
	x := 41
	x++
	time.Sleep(30 * time.Second)
	fmt.Println(x)
}
`
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module echo-debug-test\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainPath, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var outputMu sync.Mutex
	var adapterOutput strings.Builder
	handle, err := (delveDebugAdapter{}).Start(ctx, map[string]any{"cwd": workspaceRoot, "dlvCwd": root}, func(_ string, value string) {
		outputMu.Lock()
		adapterOutput.WriteString(value)
		outputMu.Unlock()
	})
	if err != nil {
		t.Fatalf("start Delve: %v", err)
	}
	defer handle.stop()

	events := make(chan dapEnvelope, 32)
	connection := newDAPConnection(handle.transport, func(event dapEnvelope) { events <- event })
	defer connection.Close()
	request := func(command string, arguments any) dapEnvelope {
		t.Helper()
		response, err := connection.request(ctx, command, arguments)
		if err != nil {
			outputMu.Lock()
			logged := adapterOutput.String()
			outputMu.Unlock()
			t.Fatalf("DAP %s: %v\nDelve output:\n%s", command, err, logged)
		}
		return response
	}

	request("initialize", map[string]any{
		"clientID": "echo-test", "adapterID": "go", "pathFormat": "path",
		"linesStartAt1": true, "columnsStartAt1": true,
		"supportsVariableType": true, "supportsVariablePaging": true,
	})
	launchDone := make(chan error, 1)
	go func() {
		_, err := connection.request(ctx, "launch", map[string]any{
			"request": "launch", "mode": "debug", "program": root, "cwd": workspaceRoot,
			"buildFlags": "-buildvcs=false",
		})
		launchDone <- err
	}()
	waitDebugEvent(t, ctx, events, "initialized")
	breakpoints := request("setBreakpoints", map[string]any{
		"source":      map[string]any{"name": "main.go", "path": mainPath},
		"breakpoints": []map[string]any{{"line": 10}},
	})
	var breakpointBody struct {
		Breakpoints []DebugBreakpoint `json:"breakpoints"`
	}
	if err := json.Unmarshal(breakpoints.Body, &breakpointBody); err != nil {
		t.Fatal(err)
	}
	if len(breakpointBody.Breakpoints) != 1 || !breakpointBody.Breakpoints[0].Verified {
		t.Fatalf("breakpoint was not verified: %#v", breakpointBody.Breakpoints)
	}
	request("configurationDone", map[string]any{})
	select {
	case err := <-launchDone:
		if err != nil {
			t.Fatalf("launch: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	stopped := waitDebugEvent(t, ctx, events, "stopped")
	var stoppedBody struct {
		ThreadID int `json:"threadId"`
	}
	if err := json.Unmarshal(stopped.Body, &stoppedBody); err != nil {
		t.Fatal(err)
	}
	if stoppedBody.ThreadID == 0 {
		t.Fatal("stopped event did not identify a thread")
	}
	stack := request("stackTrace", map[string]any{"threadId": stoppedBody.ThreadID, "startFrame": 0, "levels": 1})
	var stackBody struct {
		StackFrames []struct {
			ID     int `json:"id"`
			Line   int `json:"line"`
			Source struct {
				Path string `json:"path"`
			} `json:"source"`
		} `json:"stackFrames"`
	}
	if err := json.Unmarshal(stack.Body, &stackBody); err != nil {
		t.Fatal(err)
	}
	if len(stackBody.StackFrames) == 0 {
		t.Fatal("Delve returned no stack frame")
	}
	frame := stackBody.StackFrames[0]
	if !strings.EqualFold(filepath.Clean(frame.Source.Path), filepath.Clean(mainPath)) || frame.Line != 10 {
		t.Fatalf("stopped at %s:%d, want %s:10", frame.Source.Path, frame.Line, mainPath)
	}
	evaluation := request("evaluate", map[string]any{"expression": "x", "frameId": frame.ID, "context": "hover"})
	var evaluateBody struct {
		Result string `json:"result"`
		Type   string `json:"type"`
	}
	if err := json.Unmarshal(evaluation.Body, &evaluateBody); err != nil {
		t.Fatal(err)
	}
	if evaluateBody.Result != "41" {
		t.Fatalf("x = %q (%s), want 41", evaluateBody.Result, evaluateBody.Type)
	}
	request("continue", map[string]any{"threadId": stoppedBody.ThreadID})
	request("disconnect", map[string]any{"terminateDebuggee": true})
	cancel()
	_ = connection.Close()
	handle.stop()
	if handle.cmd.ProcessState == nil || !handle.cmd.ProcessState.Exited() {
		t.Fatalf("Delve process did not exit; state=%v", handle.cmd.ProcessState)
	}
}

func waitDebugEvent(t *testing.T, ctx context.Context, events <-chan dapEnvelope, name string) dapEnvelope {
	t.Helper()
	for {
		select {
		case event := <-events:
			if event.Event == name {
				return event
			}
		case <-ctx.Done():
			t.Fatal(fmt.Errorf("waiting for DAP %s event: %w", name, ctx.Err()))
		}
	}
}
