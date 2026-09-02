//go:build windows

package debugger

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

func hostProcessList(ctx context.Context) ([]ProcessInfo, error) {
	powerShell, err := exec.LookPath("powershell.exe")
	if err != nil {
		powerShell, err = exec.LookPath("pwsh.exe")
	}
	if err != nil {
		return nil, fmt.Errorf("PowerShell is required to list host processes: %w", err)
	}
	command := exec.CommandContext(ctx, powerShell, "-NoProfile", "-NonInteractive", "-Command",
		`Get-CimInstance Win32_Process | ForEach-Object { [Console]::Out.WriteLine(('{0}|{1}|{2}' -f $_.ProcessId, $_.Name, ($_.CommandLine -replace '[\r\n|]', ' '))) }`)
	configureOwnedCommand(command)
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	result := []ProcessInfo{}
	for _, line := range strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(parts) < 2 {
			continue
		}
		pid, parseErr := strconv.Atoi(parts[0])
		if parseErr != nil || pid <= 0 {
			continue
		}
		item := ProcessInfo{PID: pid, Name: parts[1], Execution: "host"}
		if len(parts) == 3 {
			item.CommandLine = parts[2]
		}
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if strings.EqualFold(result[i].Name, result[j].Name) {
			return result[i].PID < result[j].PID
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result, nil
}
