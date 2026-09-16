package debugger

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/workspaces"
)

// The adapter runs independently of the DAP reader, as real adapters do. Tests
// can send stops before the command acknowledgement and deliberately withhold it.
func sourceStepSession(t *testing.T, handle func(*Service, *session, dapEnvelope) any) (*Service, *session, <-chan Event) {
	t.Helper()
	client, adapter := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	current := &session{
		id: "session", workspaceID: "workspace", status: StatusStopped, threadID: 7,
		revision: 4, stopGeneration: 2, ctx: ctx, cancel: cancel,
		profile:      debugconfig.AdapterProfile{AdapterID: "go"},
		capabilities: map[string]any{"supportsSteppingGranularity": true},
	}
	service := &Service{workspaces: staticWorkspaceResolver{workspaces.Workspace{ID: "workspace"}}, runtimes: map[string]*workspaceRuntime{
		"workspace": {sessions: map[string]*session{current.id: current}},
	}}
	events := make(chan Event, 64)
	service.SetNotifier(func(event Event) { events <- event })
	current.conn = newDAPConnection(client, func(event dapEnvelope) { service.handleDAPEvent("workspace", current.id, event) }, nil, nil)
	connection := current.conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		reader := bufio.NewReader(adapter)
		for {
			payload, err := readDAPMessage(reader)
			if err != nil {
				return
			}
			var request dapEnvelope
			if json.Unmarshal(payload, &request) != nil {
				return
			}
			body := handle(service, current, request)
			if body == nil {
				body = map[string]any{}
			}
			response := mustJSON(map[string]any{"seq": request.Seq, "type": "response", "request_seq": request.Seq, "success": true, "command": request.Command, "body": body})
			if _, err := io.WriteString(adapter, "Content-Length: "+strconv.Itoa(len(response))+"\r\n\r\n"); err != nil {
				return
			}
			if _, err := adapter.Write(response); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { cancel(); _ = connection.Close(); _ = adapter.Close(); <-done })
	return service, current, events
}

func testStop(service *Service, current *session, reason string, thread int) {
	service.handleDAPEvent(current.workspaceID, current.id, dapEnvelope{Event: "stopped", Body: mustJSON(map[string]any{"reason": reason, "threadId": thread, "allThreadsStopped": true})})
}

func awaitStepStop(t *testing.T, events <-chan Event) Event {
	t.Helper()
	continued := 0
	for {
		select {
		case event := <-events:
			if event.Event == "continued" {
				continued++
			}
			if event.Event == "stopped" {
				if continued != 1 {
					t.Errorf("visible continued events = %d, want 1", continued)
				}
				return event
			}
		case <-time.After(10 * time.Second):
			t.Fatal("logical step never stopped")
		}
	}
}

func testStep(t *testing.T, service *Service, current *session, thread int) {
	t.Helper()
	if _, err := service.Request(context.Background(), "workspace", current.id, "stepIn", ControlRequest{ExpectedRevision: 4, StopGeneration: 2, Arguments: map[string]any{"threadId": thread}}); err != nil {
		t.Fatal(err)
	}
}

func TestCGOStepHidesBridgeAndPreservesSelectedThread(t *testing.T) {
	phase := 0
	service, current, events := sourceStepSession(t, func(s *Service, current *session, request dapEnvelope) any {
		var args map[string]any
		_ = json.Unmarshal(request.Arguments, &args)
		switch request.Command {
		case "stackTrace":
			names := []string{"main.main", "main._Cfunc_add", "runtime.asmcgocall", "C.add"}
			return map[string]any{"stackFrames": []stepFrame{{ID: 1, Name: names[phase], Line: 10}}}
		case "stepIn":
			if intValue(args["threadId"]) != 42 {
				t.Errorf("stepped thread %v, want selected thread 42", args["threadId"])
			}
			if phase > 0 && args["granularity"] != "instruction" {
				t.Errorf("bridge command = %s", request.Arguments)
			}
			phase++
			s.handleDAPEvent("workspace", current.id, dapEnvelope{Event: "continued"})
			testStop(s, current, "step", 42) // Stop deliberately precedes ACK.
		}
		return nil
	})
	testStep(t, service, current, 42)
	stop := awaitStepStop(t, events)
	if stop.Session.ThreadID != 42 || stop.Session.StopGeneration != 4 {
		t.Fatalf("final stop = %+v", stop.Session)
	}
}

func TestCGOStepExposesUserStops(t *testing.T) {
	for _, stop := range []struct {
		reason string
		thread int
	}{{"breakpoint", 7}, {"exception", 7}, {"pause", 7}, {"step", 19}} {
		t.Run(stop.reason+strconv.Itoa(stop.thread), func(t *testing.T) {
			steps := 0
			service, current, events := sourceStepSession(t, func(s *Service, current *session, request dapEnvelope) any {
				if request.Command == "stackTrace" {
					return map[string]any{"stackFrames": []stepFrame{{ID: 1, Name: "main._Cfunc_add", Line: 1}}}
				}
				if request.Command == "stepIn" {
					steps++
					if steps == 1 {
						testStop(s, current, "step", 7)
					} else {
						testStop(s, current, stop.reason, stop.thread)
					}
				}
				return nil
			})
			testStep(t, service, current, 7)
			event := awaitStepStop(t, events)
			if event.Session.StoppedReason != stop.reason || event.Session.ThreadID != stop.thread {
				t.Fatalf("stop = %+v", event.Session)
			}
		})
	}
}

func TestPauseInterruptsCGOStepWithoutWaitingForSourceStop(t *testing.T) {
	stepping := make(chan struct{})
	service, current, events := sourceStepSession(t, func(s *Service, current *session, request dapEnvelope) any {
		switch request.Command {
		case "stackTrace":
			return map[string]any{"stackFrames": []stepFrame{{ID: 1, Name: "main.main", Line: 1}}}
		case "stepIn":
			close(stepping) // Program keeps running; no stopped event.
		case "pause":
			testStop(s, current, "pause", 7)
		}
		return nil
	})
	testStep(t, service, current, 7)
	select {
	case <-stepping:
	case <-time.After(time.Second):
		t.Fatal("step was not dispatched")
	}
	if _, err := service.Request(context.Background(), "workspace", current.id, "pause", ControlRequest{}); err != nil {
		t.Fatal(err)
	}
	stop := awaitStepStop(t, events)
	if stop.Session.StoppedReason != "pause" {
		t.Fatalf("stop = %+v", stop.Session)
	}
}

func TestCGOStepTraversalIsBounded(t *testing.T) {
	steps := 0
	service, current, events := sourceStepSession(t, func(s *Service, current *session, request dapEnvelope) any {
		if request.Command == "stackTrace" {
			return map[string]any{"stackFrames": []stepFrame{{ID: 1, Name: "main._Cfunc_missing", Line: 1}}}
		}
		if request.Command == "stepIn" {
			steps++
			testStop(s, current, "step", 7)
		}
		return nil
	})
	testStep(t, service, current, 7)
	stop := awaitStepStop(t, events)
	if !strings.Contains(stop.Session.StoppedText, "limit") && !strings.Contains(stop.Session.StoppedText, "deadline") {
		t.Fatalf("missing traversal diagnostic: %+v", stop.Session)
	}
	if steps > cgoStepLimit+1 {
		t.Fatalf("issued %d steps", steps)
	}
}

func TestLateControlAcknowledgementDoesNotResumeNewStop(t *testing.T) {
	service, current, _ := sourceStepSession(t, func(s *Service, current *session, request dapEnvelope) any {
		if request.Command == "next" {
			testStop(s, current, "step", 7)
		}
		if request.Command == "stackTrace" {
			return map[string]any{"stackFrames": []stepFrame{}}
		}
		return nil
	})
	if _, err := service.Request(context.Background(), "workspace", current.id, "next", ControlRequest{StopGeneration: 2}); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if current.status != StatusStopped || current.stopGeneration != 3 {
		t.Fatalf("late ACK changed stop: %s, %d", current.status, current.stopGeneration)
	}
}

func TestGeneratedCGOFramePaths(t *testing.T) {
	for _, filename := range []string{"_cgo_gotypes.go", "/tmp/build/_cgo_export.c", `C:\temp\build\_cgo_export.c`, "/tmp/cgo-gcc-prolog"} {
		if !cgoBridgeFrame(stepFrame{Source: adapterSource{Path: filename}}) {
			t.Errorf("missed CGO source %s", filename)
		}
	}
	if cgoBridgeFrame(stepFrame{Name: "C.add", Source: adapterSource{Path: "/project/native.c"}}) {
		t.Fatal("classified user C code as a bridge")
	}
}

func TestCGORuntimeHelpersFinishWithoutEnteringSystemLibraries(t *testing.T) {
	phase := 0
	service, current, events := sourceStepSession(t, func(s *Service, current *session, request dapEnvelope) any {
		if request.Command == "stackTrace" {
			names := []string{"main.main", "main._Cfunc_add", "runtime.entersyscall", "C.add"}
			return map[string]any{"stackFrames": []stepFrame{{ID: 1, Name: names[phase], Line: 1}}}
		}
		if request.Command == "stepIn" || request.Command == "stepOut" {
			if phase == 2 && request.Command != "stepOut" {
				t.Error("instruction-stepped a runtime helper")
			}
			phase++
			testStop(s, current, "step", 7)
		}
		return nil
	})
	testStep(t, service, current, 7)
	awaitStepStop(t, events)
}

func TestMissingNativeSourceStopsWhenTheCallReturns(t *testing.T) {
	phase := 0
	service, current, events := sourceStepSession(t, func(s *Service, current *session, request dapEnvelope) any {
		if request.Command == "stackTrace" {
			names := []string{"main.main", "main._Cfunc_missing", "main.main"}
			return map[string]any{"stackFrames": []stepFrame{{ID: 1, Name: names[phase], Line: phase + 1}}}
		}
		if request.Command == "stepIn" {
			phase++
			testStop(s, current, "step", 7)
		}
		return nil
	})
	testStep(t, service, current, 7)
	stop := awaitStepStop(t, events)
	if !strings.Contains(stop.Session.StoppedText, "without an available C source stop") {
		t.Fatalf("missing diagnostic: %+v", stop.Session)
	}
}

func TestStopCancelsAnInFlightCGOStep(t *testing.T) {
	stepping := make(chan struct{})
	service, current, events := sourceStepSession(t, func(s *Service, current *session, request dapEnvelope) any {
		if request.Command == "stackTrace" {
			return map[string]any{"stackFrames": []stepFrame{{ID: 1, Name: "main.main", Line: 1}}}
		}
		if request.Command == "stepIn" {
			close(stepping)
		}
		return nil
	})
	testStep(t, service, current, 7)
	select {
	case <-stepping:
	case <-time.After(time.Second):
		t.Fatal("step was not sent")
	}
	if err := service.Stop("workspace", current.id, nil); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	if current.step != nil || current.status != StatusTerminated {
		t.Errorf("step survived stop: %s", current.status)
	}
	service.mu.Unlock()
	for len(events) > 0 {
		if event := <-events; event.Event == "stopped" {
			t.Fatal("stop exposed an intermediate instruction")
		}
	}
}

func TestNativePrologueStopsAtAnAddressInsteadOfCountingDecodedInstructions(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "native.c")
	if err := os.WriteFile(filename, []byte("int add(int value) {\n    return value;\n}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	phase := 0
	service, current, events := sourceStepSession(t, func(s *Service, current *session, request dapEnvelope) any {
		switch request.Command {
		case "stackTrace":
			frame := stepFrame{ID: 1, Name: "main.main", Line: 1}
			if phase == 1 {
				frame.Name = "main._Cfunc_add"
			}
			if phase >= 2 {
				frame.Name, frame.PC, frame.Source = "C.add", "0x1000", adapterSource{Path: filename}
				if phase >= 3 {
					frame.Line, frame.PC = 2, "0x1004"
				}
			}
			return map[string]any{"stackFrames": []stepFrame{frame}}
		case "disassemble":
			return map[string]any{"instructions": []any{
				map[string]any{"address": "0x1000", "instruction": "REP; Op(0)", "line": 1, "location": adapterSource{Path: filename}},
				map[string]any{"address": "0x1001", "instruction": "?"},
				map[string]any{"address": "0x1002", "instruction": "?"},
				map[string]any{"address": "0x1003", "instruction": "CLI"},
				map[string]any{"address": "0x1004", "instruction": "MOV", "line": 2, "location": adapterSource{Path: filename}},
			}}
		case "stepIn":
			phase++
			if phase > 3 {
				t.Error("stepped past the first C statement")
			}
			testStop(s, current, "step", 7)
		}
		return nil
	})
	testStep(t, service, current, 7)
	awaitStepStop(t, events)
}
