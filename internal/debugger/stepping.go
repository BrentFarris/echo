package debugger

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const cgoStepLimit = 4096
const cgoStepTimeout = 5 * time.Second

type stepOperation struct {
	ctx        context.Context
	cancel     context.CancelFunc
	events     chan dapEnvelope
	connection *dapConnection
	threadID   int
}

type stepFrame struct {
	ID     int           `json:"id"`
	Name   string        `json:"name"`
	Line   int           `json:"line"`
	Column int           `json:"column"`
	PC     string        `json:"instructionPointerReference"`
	Source adapterSource `json:"source"`
}

type stopDetails struct {
	Reason   string `json:"reason"`
	ThreadID int    `json:"threadId"`
}

func (s *Service) startSourceStep(current *session, command string, arguments map[string]any) (RequestResponse, bool) {
	s.mu.Lock()
	if (command != "stepIn" && command != "stepOut") || arguments["granularity"] == "instruction" || !strings.EqualFold(current.profile.AdapterID, "go") || !boolCapability(current.capabilities, "supportsSteppingGranularity") || current.status != StatusStopped || current.step != nil {
		s.mu.Unlock()
		return RequestResponse{}, false
	}
	ctx, cancel := context.WithCancel(current.ctx)
	op := &stepOperation{ctx: ctx, cancel: cancel, events: make(chan dapEnvelope, 8), connection: current.conn, threadID: intValue(arguments["threadId"])}
	current.step = op
	setSessionRunning(current)
	result := RequestResponse{WorkspaceID: current.workspaceID, SessionID: current.id, Revision: current.revision, StopGeneration: current.stopGeneration, Body: json.RawMessage(`{}`)}
	s.mu.Unlock()
	s.publishSession(current.workspaceID, current.id, "continued", nil, "")
	go s.runSourceStep(current, op, command, arguments)
	return result, true
}

// DAP's reader must never wait on a request it is responsible for receiving.
// Intermediate stops are consumed by a separate server-owned operation.
func (s *Service) routeStepEvent(workspaceID, sessionID string, event dapEnvelope) bool {
	if event.Event != "stopped" && event.Event != "continued" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.sessionLocked(workspaceID, sessionID)
	if err != nil || current.step == nil {
		return false
	}
	if event.Event == "stopped" {
		select {
		case current.step.events <- event:
		default:
			current.step.cancel()
		}
	}
	return true
}

func (op *stepOperation) request(ctx context.Context, command string, arguments map[string]any) (dapEnvelope, error) {
	if err := ctx.Err(); err != nil {
		return dapEnvelope{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	return op.connection.request(ctx, command, arguments)
}

func (op *stepOperation) topFrame(ctx context.Context, threadID int) (stepFrame, error) {
	frames, err := op.stackFrames(ctx, threadID, 1)
	if err != nil {
		return stepFrame{}, err
	}
	return frames[0], nil
}

func (op *stepOperation) stackFrames(ctx context.Context, threadID, levels int) ([]stepFrame, error) {
	response, err := op.request(ctx, "stackTrace", map[string]any{"threadId": threadID, "startFrame": 0, "levels": levels})
	if err != nil {
		return nil, err
	}
	var body struct {
		Frames []stepFrame `json:"stackFrames"`
	}
	if err = json.Unmarshal(response.Body, &body); err != nil {
		return nil, err
	}
	if len(body.Frames) == 0 {
		return nil, fmt.Errorf("the debugger returned no stack frame")
	}
	return body.Frames, nil
}

func (op *stepOperation) execute(ctx context.Context, command string, arguments map[string]any) (*dapEnvelope, error) {
	if _, err := op.request(ctx, command, arguments); err != nil {
		return nil, err
	}
	select {
	case event := <-op.events:
		return &event, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Service) runSourceStep(current *session, op *stepOperation, command string, arguments map[string]any) {
	defer op.cancel()
	originFrames, err := op.stackFrames(op.ctx, op.threadID, 50)
	if err != nil {
		s.finishSourceStep(current, op, &dapEnvelope{Event: "stopped", Body: mustJSON(map[string]any{"reason": "pause", "threadId": op.threadID, "allThreadsStopped": true})}, err.Error())
		return
	}
	origin := originFrames[0]
	returnTarget := ""
	if command == "stepOut" {
		for _, frame := range originFrames[1:] {
			if !cgoBridgeFrame(frame) && !runtimeBridgeFrame(frame) && frame.Line > 0 {
				returnTarget = frame.Name
				break
			}
		}
	}
	stop, err := op.execute(op.ctx, command, arguments)
	if err != nil {
		s.recoverSourceStep(current, op, stop, err)
		return
	}
	traversing := false
	targetGo := false
	targetC := ""
	targetGoBase := ""
	var bridgeCtx context.Context
	var cancel context.CancelFunc
	defer func() {
		if cancel != nil {
			cancel()
		}
	}()
	prologueTarget := ""
	prologueLine := 0
	prologueChecked := false
	for count := 0; ; count++ {
		var details stopDetails
		_ = json.Unmarshal(stop.Body, &details)
		// Never hide user breakpoints, exceptions, or stops on another thread.
		if details.Reason != "step" || (details.ThreadID != op.threadID && details.ThreadID != 0) {
			s.finishSourceStep(current, op, stop, "")
			return
		}
		ctx := op.ctx
		if traversing {
			ctx = bridgeCtx
		}
		if details.ThreadID == 0 {
			details.ThreadID = op.threadID
		}
		frame, frameErr := op.topFrame(ctx, details.ThreadID)
		if frameErr != nil {
			s.recoverSourceStep(current, op, stop, frameErr)
			return
		}
		if !traversing {
			if !cgoBridgeFrame(frame) && !(command == "stepOut" && nativeCFrame(origin) && runtimeBridgeFrame(frame)) {
				s.finishSourceStep(current, op, stop, "")
				return
			}
			traversing = true
			targetGo = command == "stepIn" && (sourceBase(frame.Source) == "_cgo_export.c" || strings.Contains(frame.Name, "_cgoexp_"))
			if targetGo {
				targetGoBase = strings.TrimPrefix(frame.Name, "C.")
				if _, suffix, ok := strings.Cut(frame.Name, "_cgoexp_"); ok {
					_, targetGoBase, _ = strings.Cut(suffix, "_")
				}
			}
			if command == "stepIn" {
				for _, prefix := range []string{"._Cfunc_", "._C2func_"} {
					if _, name, ok := strings.Cut(frame.Name, prefix); ok {
						targetC = "C." + name
					}
				}
			}
			bridgeCtx, cancel = context.WithTimeout(op.ctx, cgoStepTimeout)
			ctx = bridgeCtx
		}
		if command == "stepIn" && targetC != "" && !cgoBridgeFrame(origin) && frame.Name == origin.Name && frame.Source.Path == origin.Source.Path {
			s.finishSourceStep(current, op, stop, "The native call returned without an available C source stop; build its C code with debug information and optimization disabled.")
			return
		}
		reached := !cgoBridgeFrame(frame) && !runtimeBridgeFrame(frame)
		if targetGo {
			reached = reached && strings.HasSuffix(frame.Source.Path, ".go") && strings.HasSuffix(frame.Name, "."+targetGoBase)
		}
		if targetC != "" {
			reached = reached && frame.Name == targetC
		}
		if command == "stepOut" && returnTarget != "" {
			reached = reached && frame.Name == returnTarget
		}
		if reached {
			if nativeCFrame(frame) && !prologueChecked && command == "stepIn" {
				prologueChecked = true
				prologueTarget = s.nativePrologueTarget(ctx, current, op, frame)
				prologueLine = frame.Line
			}
			if prologueTarget == "" || frame.PC == prologueTarget || frame.Line != prologueLine {
				s.finishSourceStep(current, op, stop, "")
				return
			}
		}
		if count >= cgoStepLimit || ctx.Err() != nil {
			s.recoverSourceStep(current, op, stop, fmt.Errorf("CGO bridge traversal reached its limit; stopped at the current instruction"))
			return
		}
		if op.ctx.Err() != nil {
			s.recoverSourceStep(current, op, stop, op.ctx.Err())
			return
		}
		nextCommand := "stepIn"
		nextArguments := map[string]any{"threadId": details.ThreadID, "granularity": "instruction"}
		finishingGoBridge := command == "stepOut" && frame.Line > 0 && strings.HasSuffix(frame.Source.Path, ".go") && (runtimeBridgeFrame(frame) || cgoBridgeFrame(frame))
		if finishingGoBridge || cgoRuntimeHelperFrame(frame) {
			// Once C has returned to Go, let Delve finish the runtime function.
			// Instruction-stepping exitsyscall can enter Linux's vDSO clock
			// seqlock, which continually retries when each instruction is paused.
			nextCommand = "stepOut"
			delete(nextArguments, "granularity")
		}
		stop, err = op.execute(ctx, nextCommand, nextArguments)
		if err != nil {
			s.recoverSourceStep(current, op, stop, err)
			return
		}
	}
}

func cgoRuntimeHelperFrame(frame stepFrame) bool {
	switch frame.Name {
	case "runtime.nanotime", "runtime.nanotime1", "runtime.walltime", "runtime.walltime1", "runtime.cputicks",
		"runtime.entersyscall", "runtime.reentersyscall", "runtime.exitsyscall",
		"C._cgo_wait_runtime_init_done", "C._cgo_release_context":
		return true
	}
	return false
}

func nativeCFrame(frame stepFrame) bool {
	return strings.HasPrefix(frame.Name, "C.") && !cgoBridgeFrame(frame)
}

func cgoBridgeFrame(frame stepFrame) bool {
	base := sourceBase(frame.Source)
	return strings.Contains(frame.Name, "._Cfunc_") || strings.Contains(frame.Name, "._C2func_") || strings.Contains(frame.Name, "_cgoexp_") || strings.HasPrefix(frame.Name, "C._cgo_") || base == "_cgo_export.c" || base == "_cgo_gotypes.go" || base == "cgo-gcc-prolog"
}

func sourceBase(source adapterSource) string {
	filename := strings.ReplaceAll(source.Path, "\\", "/")
	if filename == "" {
		return source.Name
	}
	return filename[strings.LastIndex(filename, "/")+1:]
}

func runtimeBridgeFrame(frame stepFrame) bool {
	// During a goroutine/system-stack switch Delve can briefly report a
	// synthetic unknown frame. This is only skipped within a CGO traversal.
	if frame.Name == "???" && frame.Line < 1 {
		return true
	}
	filename := strings.ReplaceAll(frame.Source.Path, "\\", "/")
	return strings.HasPrefix(frame.Name, "runtime.") || strings.Contains(filename, "/src/runtime/") || strings.Contains(filename, "/src/internal/runtime/")
}

var controlInstruction = regexp.MustCompile(`(?i)^(CALL|RET|J[A-Z]*|B|BL|BLR|BR|CBZ|CBNZ|TBZ|TBNZ)\b`)

// Stop after the native prologue, so parameter locations are initialized. Only
// advance a straight-line declaration/brace line, never a one-line C body or a
// call/branch/return. Source-level next could otherwise execute the whole call.
func (s *Service) nativePrologueTarget(ctx context.Context, current *session, op *stepOperation, frame stepFrame) string {
	if frame.PC == "" || frame.Line < 1 {
		return ""
	}
	content, err := s.readAdapterSource(ctx, current.workspaceID, frame.Source.Path)
	if err != nil {
		return ""
	}
	lines := strings.Split(content, "\n")
	if frame.Line > len(lines) {
		return ""
	}
	line := strings.TrimSpace(lines[frame.Line-1])
	if !strings.HasSuffix(line, "{") || strings.Contains(line, ";") {
		return ""
	}
	response, err := op.request(ctx, "disassemble", map[string]any{"memoryReference": frame.PC, "instructionCount": 64})
	if err != nil {
		return ""
	}
	var body struct {
		Instructions []struct {
			Address     string        `json:"address"`
			Instruction string        `json:"instruction"`
			Line        int           `json:"line"`
			Location    adapterSource `json:"location"`
		} `json:"instructions"`
	}
	if json.Unmarshal(response.Body, &body) != nil {
		return ""
	}
	for i, instruction := range body.Instructions {
		if i > 0 && instruction.Line > 0 && instruction.Line != frame.Line {
			if instruction.Location.Path == frame.Source.Path {
				// Use the address, not a count. Delve can decode ENDBR64 as
				// multiple entries even though hardware steps it once.
				return instruction.Address
			}
			return ""
		}
		if controlInstruction.MatchString(strings.TrimSpace(instruction.Instruction)) {
			return ""
		}
	}
	return ""
}

func (s *Service) recoverSourceStep(current *session, op *stepOperation, stop *dapEnvelope, failure error) {
	if current.ctx.Err() != nil {
		return
	}
	message := "CGO stepping stopped: " + failure.Error()
	if op.ctx.Err() != nil {
		message = "CGO stepping paused."
	}
	if stop == nil {
		ctx, cancel := context.WithTimeout(current.ctx, 2*time.Second)
		defer cancel()
		_, _ = op.connection.request(ctx, "pause", map[string]any{"threadId": op.threadID})
		select {
		case event := <-op.events:
			stop = &event
		case <-ctx.Done():
		}
	}
	s.finishSourceStep(current, op, stop, message)
}

func (s *Service) finishSourceStep(current *session, op *stepOperation, stop *dapEnvelope, message string) {
	s.mu.Lock()
	if current.step != op || isStoppingOrTerminal(current.status) {
		s.mu.Unlock()
		return
	}
	current.step = nil
	s.mu.Unlock()
	if message != "" {
		s.appendOutput(current.workspaceID, current.id, "adapter", message+"\n", nil)
	}
	if stop != nil {
		if message != "" {
			body := map[string]any{}
			_ = json.Unmarshal(stop.Body, &body)
			body["description"] = message
			stop.Body = mustJSON(body)
		}
		s.handleDAPEvent(current.workspaceID, current.id, *stop)
	}
}

func mustJSON(value any) json.RawMessage { data, _ := json.Marshal(value); return data }
