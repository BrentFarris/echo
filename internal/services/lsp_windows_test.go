//go:build windows

package services

import "testing"

func TestNewLSPServerProcessHidesWindowsConsole(t *testing.T) {
	command := newLSPServerProcess(lspServerCommand{name: "gopls"}, `C:\tmp`)

	if command.SysProcAttr == nil {
		t.Fatal("expected Windows process attributes to be configured")
	}
	if !command.SysProcAttr.HideWindow {
		t.Fatal("expected LSP server process window to be hidden")
	}
	if command.SysProcAttr.CreationFlags&windowsCreateNoWindow == 0 {
		t.Fatalf("expected CREATE_NO_WINDOW flag, got %#x", command.SysProcAttr.CreationFlags)
	}
}
