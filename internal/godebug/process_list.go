package godebug

import (
	"context"
	"debug/buildinfo"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

type ProcessInfo struct {
	Executable  string `json:"-"`
	PID         int    `json:"pid"`
	Name        string `json:"name"`
	CommandLine string `json:"commandLine,omitempty"`
	Execution   string `json:"execution"`
}

func Processes(ctx context.Context, filter string, goOnly bool) ([]ProcessInfo, error) {
	list, err := hostProcessList(ctx)
	if err != nil {
		return nil, err
	}
	result := []ProcessInfo{}
	for _, item := range list {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if filter != "" && !strings.Contains(strings.ToLower(item.Name+" "+item.CommandLine), strings.ToLower(filter)) {
			continue
		}
		if goOnly {
			executable := item.Executable
			if runtime.GOOS == "linux" {
				executable, _ = os.Readlink(filepath.Join("/proc", strconv.Itoa(item.PID), "exe"))
			}
			if executable == "" {
				executable = item.Name
			}
			if _, err := buildinfo.ReadFile(executable); err != nil {
				continue
			}
		}
		result = append(result, item)
	}
	return result, nil
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
