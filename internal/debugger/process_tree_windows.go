//go:build windows

package debugger

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	debugCreateNoWindow     = 0x08000000
	debugCreateProcessGroup = 0x00000200
)

func configureOwnedCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: debugCreateNoWindow | debugCreateProcessGroup}
	command.Cancel = func() error { return killOwnedCommand(command) }
}

func killOwnedCommand(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return os.ErrProcessDone
	}
	root := uint32(command.Process.Pid)
	descendants := debugDescendantProcessIDs(root)
	err := command.Process.Kill()
	// Some processes create children while handling termination. Take one more
	// snapshot before killing deepest-first so none retain adapter pipes.
	descendants = append(descendants, debugDescendantProcessIDs(root)...)
	seen := map[uint32]bool{}
	for index := len(descendants) - 1; index >= 0; index-- {
		pid := descendants[index]
		if pid == 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		if process, findErr := os.FindProcess(int(pid)); findErr == nil {
			_ = process.Kill()
			_ = process.Release()
		}
	}
	if errors.Is(err, os.ErrProcessDone) {
		return os.ErrProcessDone
	}
	return err
}

func debugDescendantProcessIDs(root uint32) []uint32 {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snapshot)
	children := map[uint32][]uint32{}
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if windows.Process32First(snapshot, &entry) != nil {
		return nil
	}
	for {
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
		if windows.Process32Next(snapshot, &entry) != nil {
			break
		}
	}
	result := []uint32{}
	queue := append([]uint32(nil), children[root]...)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		result = append(result, pid)
		queue = append(queue, children[pid]...)
	}
	return result
}
