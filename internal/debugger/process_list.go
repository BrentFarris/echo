package debugger

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/brent/echo/internal/sandbox"
)

func (s *Service) Processes(ctx context.Context, workspaceID string) ([]ProcessInfo, error) {
	workspace, err := s.workspace(workspaceID)
	if err != nil {
		return nil, err
	}
	if !workspace.Sandbox.Enabled {
		return hostProcessList(ctx)
	}
	manager := s.sandboxManager()
	if manager == nil {
		return nil, fmt.Errorf("sandbox runtime is unavailable")
	}
	options, err := s.executionOptions(workspace, "", nil, "")
	if err != nil {
		return nil, err
	}
	result, err := manager.Execute(ctx, workspaceID, sandbox.ExecRequest{
		Role: "workbench", Command: []string{"ps", "-eo", "pid=,comm=,args="},
		WorkingDirectory: options.WorkspaceFolder, OutputLimit: 4 << 20,
	})
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("sandbox process list exited with code %d", result.ExitCode)
	}
	return parseProcessList(string(result.Stdout), "sandbox"), nil
}

func parseProcessList(output, execution string) []ProcessInfo {
	result := []ProcessInfo{}
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		name := fields[1]
		commandLine := ""
		if len(fields) > 2 {
			commandLine = strings.Join(fields[2:], " ")
		}
		result = append(result, ProcessInfo{PID: pid, Name: name, CommandLine: commandLine, Execution: execution})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if strings.EqualFold(result[i].Name, result[j].Name) {
			return result[i].PID < result[j].PID
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result
}
