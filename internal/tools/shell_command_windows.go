//go:build windows

package tools

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsCreateNoWindow = 0x08000000

func configureShellCommandProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windowsCreateNoWindow,
	}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}

		// CommandContext only terminates PowerShell by default. Compiler, test,
		// and package-manager children can retain the stdout/stderr handles and
		// keep Cmd.Wait blocked indefinitely, so terminate the whole tree first.
		descendants := windowsDescendantProcessIDs(uint32(command.Process.Pid))
		err := command.Process.Kill()
		descendants = append(descendants, windowsDescendantProcessIDs(uint32(command.Process.Pid))...)
		killWindowsProcesses(descendants)
		if errors.Is(err, os.ErrProcessDone) {
			return os.ErrProcessDone
		}
		return err
	}
}

func windowsDescendantProcessIDs(rootPID uint32) []uint32 {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snapshot)

	children := make(map[uint32][]uint32)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil
	}
	for {
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			break
		}
	}

	var descendants []uint32
	queue := append([]uint32(nil), children[rootPID]...)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		descendants = append(descendants, pid)
		queue = append(queue, children[pid]...)
	}
	return descendants
}

func killWindowsProcesses(processIDs []uint32) {
	seen := make(map[uint32]bool, len(processIDs))
	for index := len(processIDs) - 1; index >= 0; index-- {
		pid := processIDs[index]
		if pid == 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		process, err := os.FindProcess(int(pid))
		if err == nil {
			_ = process.Kill()
			_ = process.Release()
		}
	}
}
