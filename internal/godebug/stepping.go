package godebug

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/brent/echo/internal/debugger"
	"regexp"
	"strings"
	"time"
)

const cgoStepLimit = 4096
const cgoStepTimeout = 5 * time.Second

func sourceStep(stepCtx context.Context, op debugger.StepClient, command string, arguments map[string]any) (*debugger.StopEvent, string, error) {
	threadID := intValue(arguments["threadId"])
	originFrames, err := op.Frames(stepCtx, threadID, 50)
	if err != nil {
		return nil, "", err
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
	stop, err := op.Execute(stepCtx, command, arguments)
	if err != nil {
		return stop, "", err
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
		details := *stop
		// Never hide user breakpoints, exceptions, or stops on another thread.
		if details.Reason != "step" || (details.ThreadID != threadID && details.ThreadID != 0) {
			return stop, "", nil
		}
		ctx := stepCtx
		if traversing {
			ctx = bridgeCtx
		}
		if details.ThreadID == 0 {
			details.ThreadID = threadID
		}
		frames, frameErr := op.Frames(ctx, details.ThreadID, 1)
		var frame debugger.Frame
		if frameErr == nil {
			frame = frames[0]
		}
		if frameErr != nil {
			return stop, "", frameErr
		}
		if !traversing {
			if !cgoBridgeFrame(frame) && !(command == "stepOut" && nativeCFrame(origin) && runtimeBridgeFrame(frame)) {
				return stop, "", nil
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
			bridgeCtx, cancel = context.WithTimeout(stepCtx, cgoStepTimeout)
			ctx = bridgeCtx
		}
		if command == "stepIn" && targetC != "" && !cgoBridgeFrame(origin) && frame.Name == origin.Name && frame.Source.Path == origin.Source.Path {
			return stop, "The native call returned without an available C source stop; build its C code with debug information and optimization disabled.", nil
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
				prologueTarget = nativePrologueTarget(ctx, op, frame)
				prologueLine = frame.Line
			}
			if prologueTarget == "" || frame.PC == prologueTarget || frame.Line != prologueLine {
				return stop, "", nil
			}
		}
		if count >= cgoStepLimit || ctx.Err() != nil {
			return stop, "", fmt.Errorf("CGO bridge traversal reached its limit; stopped at the current instruction")
		}
		if stepCtx.Err() != nil {
			return stop, "", stepCtx.Err()
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
		stop, err = op.Execute(ctx, nextCommand, nextArguments)
		if err != nil {
			return stop, "", err
		}
	}
}

func cgoRuntimeHelperFrame(frame debugger.Frame) bool {
	switch frame.Name {
	case "runtime.nanotime", "runtime.nanotime1", "runtime.walltime", "runtime.walltime1", "runtime.cputicks",
		"runtime.entersyscall", "runtime.reentersyscall", "runtime.exitsyscall",
		"C._cgo_wait_runtime_init_done", "C._cgo_release_context":
		return true
	}
	return false
}

func nativeCFrame(frame debugger.Frame) bool {
	return strings.HasPrefix(frame.Name, "C.") && !cgoBridgeFrame(frame)
}

func cgoBridgeFrame(frame debugger.Frame) bool {
	base := sourceBase(frame.Source)
	return strings.Contains(frame.Name, "._Cfunc_") || strings.Contains(frame.Name, "._C2func_") || strings.Contains(frame.Name, "_cgoexp_") || strings.HasPrefix(frame.Name, "C._cgo_") || base == "_cgo_export.c" || base == "_cgo_gotypes.go" || base == "cgo-gcc-prolog"
}

func sourceBase(source debugger.Source) string {
	filename := strings.ReplaceAll(source.Path, "\\", "/")
	if filename == "" {
		return source.Name
	}
	return filename[strings.LastIndex(filename, "/")+1:]
}

func runtimeBridgeFrame(frame debugger.Frame) bool {
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
func nativePrologueTarget(ctx context.Context, op debugger.StepClient, frame debugger.Frame) string {
	if frame.PC == "" || frame.Line < 1 {
		return ""
	}
	content, err := op.Source(ctx, frame.Source)
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
	response, err := op.Request(ctx, "disassemble", map[string]any{"memoryReference": frame.PC, "instructionCount": 64})
	if err != nil {
		return ""
	}
	var body struct {
		Instructions []struct {
			Address     string          `json:"address"`
			Instruction string          `json:"instruction"`
			Line        int             `json:"line"`
			Location    debugger.Source `json:"location"`
		} `json:"instructions"`
	}
	if json.Unmarshal(response, &body) != nil {
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

// Delve evaluates C values with its Go expression parser. Preserve strings and
// character literals; only adapt C's pointer-member operator.
func delveCExpression(expression string) string {
	var out strings.Builder
	var quote byte
	for i := 0; i < len(expression); i++ {
		c := expression[i]
		if quote != 0 {
			out.WriteByte(c)
			if c == '\\' && quote != '`' && i+1 < len(expression) {
				i++
				out.WriteByte(expression[i])
			} else if c == quote {
				quote = 0
			}
		} else if c == '"' || c == '\'' || c == '`' {
			quote = c
			out.WriteByte(c)
		} else if c == '-' && i+1 < len(expression) && expression[i+1] == '>' {
			out.WriteByte('.')
			i++
		} else {
			out.WriteByte(c)
		}
	}
	return out.String()
}
