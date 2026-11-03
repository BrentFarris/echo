package terminal

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"path/filepath"
	"strings"
)

func (s *Service) StartDebugDAP(ctx context.Context, workspaceID, ownerSessionID string, arguments map[string]any) (map[string]any, error) {
	if strings.EqualFold(stringFromAny(arguments["kind"]), "external") {
		return nil, ErrExternalUnsupported
	}
	if interpreted, _ := arguments["argsCanBeInterpretedByShell"].(bool); interpreted {
		return nil, errors.New("debug terminal shell-interpreted arguments are not supported; provide direct argv")
	}
	argv, err := dapArgv(arguments["args"])
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	workspace, err := s.workspace(workspaceID)
	if err != nil {
		return nil, err
	}
	workingDir := strings.TrimSpace(stringFromAny(arguments["cwd"]))
	if workingDir == "" {
		workingDir, err = availableWorkingDirectory(workspace)
		if err != nil {
			return nil, err
		}
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("terminal service is closed")
	}
	factory := s.newBackend
	s.mu.Unlock()
	environment := mergeDAPEnvironment(terminalEnvironment(), arguments["env"])
	cols, rows := ClampSize(120, 30)
	backend, err := factory()
	if err != nil {
		return nil, fmt.Errorf("create debug terminal: %w", err)
	}
	if err := backend.Resize(cols, rows); err != nil {
		_ = backend.Close()
		return nil, fmt.Errorf("size debug terminal: %w", err)
	}
	processContext, cancel := context.WithCancel(context.Background())
	process, err := backend.Start(processContext, CommandSpec{Name: argv[0], Args: argv[1:], Dir: workingDir, Env: environment})
	if err != nil {
		cancel()
		_ = backend.Close()
		return nil, fmt.Errorf("start debug terminal: %w", err)
	}
	title := strings.TrimSpace(stringFromAny(arguments["title"]))
	if title == "" {
		title = "Debug Terminal"
	}
	current := &session{
		botID: workspaceID, id: uuid.NewString(), name: title, kind: "debug", ownerID: ownerSessionID,
		shell: filepath.Base(argv[0]), workingDir: workingDir, backend: backend, process: process,
		cancel: cancel, done: make(chan struct{}), status: "running", output: make([]bufferedChunk, 0, 64),
	}
	s.mu.Lock()
	if s.closed || ctx.Err() != nil {
		s.mu.Unlock()
		cancel()
		_ = process.Kill()
		_ = backend.Close()
		_, _ = process.Wait()
		return nil, errors.New("debug session ended during terminal startup")
	}
	// Retain a bounded number of completed debug terminals for reload replay.
	completed := 0
	for key, old := range s.sessions {
		if old.botID == workspaceID && old.kind == "debug" {
			select {
			case <-old.done:
				completed++
				if completed > 16 {
					delete(s.sessions, key)
				}
			default:
			}
		}
	}
	s.sessions[current.id] = current
	s.mu.Unlock()
	s.emit(Event{Type: "terminal_event", BotID: workspaceID, SessionID: current.id, Name: current.name, Kind: current.kind, OwnerSessionID: current.ownerID, Event: "started"})
	go s.run(current)
	response := map[string]any{"sessionId": current.id}
	if process, ok := process.(interface{ PID() int }); ok && process.PID() > 0 {
		response["processId"] = process.PID()
	}
	return response, nil
}

// StopOwner terminates every integrated terminal created for one debugger
// session without disturbing the workspace's normal shell.
func (s *Service) StopOwner(workspaceID, ownerSessionID string) {
	s.mu.Lock()
	all := make([]*session, 0)
	for _, current := range s.sessions {
		if current.botID == workspaceID && current.ownerID == ownerSessionID && current.kind == "debug" {
			all = append(all, current)
		}
	}
	s.mu.Unlock()
	for _, current := range all {
		current.stopAndWait()
	}
}

func mergeDAPEnvironment(values []string, raw any) []string {
	environment, ok := raw.(map[string]any)
	if !ok {
		return values
	}
	for key, value := range environment {
		if value == nil {
			values = removeEnvironment(values, key)
			continue
		}
		if text, ok := value.(string); ok {
			values = setEnvironment(values, key, text)
		}
	}
	return values
}

func dapArgv(raw any) ([]string, error) {
	values, ok := raw.([]any)
	if !ok || len(values) == 0 {
		return nil, errors.New("debug terminal requires a non-empty args array")
	}
	result := make([]string, len(values))
	for index, value := range values {
		text, ok := value.(string)
		if !ok || strings.ContainsRune(text, 0) || (index == 0 && strings.TrimSpace(text) == "") {
			return nil, fmt.Errorf("debug terminal argument %d is invalid", index)
		}
		result[index] = text
	}
	return result, nil
}

func stringFromAny(value any) string {
	text, _ := value.(string)
	return text
}

func removeEnvironment(values []string, key string) []string {
	prefix := strings.ToUpper(key) + "="
	result := values[:0]
	for _, existing := range values {
		if !strings.HasPrefix(strings.ToUpper(existing), prefix) {
			result = append(result, existing)
		}
	}
	return result
}
