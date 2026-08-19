//go:build windows

package tools

import (
	"context"
	"testing"
)

func TestNewGitInspectCommandHidesWindowsCommand(t *testing.T) {
	command := newGitInspectCommand(context.Background(), "--version")

	if command.SysProcAttr == nil {
		t.Fatal("expected Windows process attributes to be configured")
	}
	if !command.SysProcAttr.HideWindow {
		t.Fatal("expected git inspect command window to be hidden")
	}
	if command.SysProcAttr.CreationFlags&windowsCreateNoWindow == 0 {
		t.Fatalf("expected CREATE_NO_WINDOW flag, got %#x", command.SysProcAttr.CreationFlags)
	}
}
