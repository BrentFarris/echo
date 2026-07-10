package services

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectKanbanVerificationCommandsUsesWorkspaceBuildCommand(t *testing.T) {
	root := t.TempDir()
	workspace := workspaceFromPath(root)
	workspace.BuildCommand = `go test -tags="debug editor" ./...`

	commands := detectKanbanVerificationCommands(workspace, []string{workspace.Folders[0].Label + "/main.go"})
	if len(commands) != 1 {
		t.Fatalf("expected one custom command, got %#v", commands)
	}
	command := commands[0]
	if command.Kind != "custom" {
		t.Fatalf("expected custom command, got %q", command.Kind)
	}
	if command.Command != workspace.BuildCommand {
		t.Fatalf("expected custom build command, got %q", command.Command)
	}
	if command.Executable != "" || len(command.Args) != 0 {
		t.Fatalf("expected custom command to run through the shell, got executable %q args %#v", command.Executable, command.Args)
	}
	if command.WorkingDirectory != workspace.Folders[0].Label {
		t.Fatalf("expected command to run in workspace root, got %q", command.WorkingDirectory)
	}
}

func TestSystemServiceWorkspaceBuildCommandPersists(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(root, "state.json")
	service := NewSystemServiceWithStorePath(storePath)
	state, err := service.AddWorkspace(workspacePath)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}

	const buildCommand = "go test -tags=debug ./..."
	state, err = service.SetWorkspaceBuildCommand(state.ActiveWorkspaceID, "  "+buildCommand+"  ")
	if err != nil {
		t.Fatalf("set workspace build command: %v", err)
	}
	if got := state.Workspaces[0].BuildCommand; got != buildCommand {
		t.Fatalf("expected normalized build command, got %q", got)
	}

	reloaded := NewSystemServiceWithStorePath(storePath).LoadState()
	if got := reloaded.Workspaces[0].BuildCommand; got != buildCommand {
		t.Fatalf("expected persisted build command, got %q", got)
	}
}
