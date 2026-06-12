package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/tools"
)

func TestWorkspaceContextBuildsRankedBrief(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceContextFile(t, root, "go.mod", "module example.com/context\n\ngo 1.23\n")
	writeWorkspaceContextFile(t, root, "package.json", `{"scripts":{"test":"vitest","build":"vite build"}}`)
	writeWorkspaceContextFile(t, root, "internal/context_brief.go", "package internal\n\nfunc BuildContextBrief() string { return \"context\" }\n")
	writeWorkspaceContextFile(t, root, "internal/context_brief_test.go", "package internal\n\nfunc TestBuildContextBrief() {}\n")
	writeWorkspaceContextFile(t, root, "internal/other.go", "package internal\n\nfunc Other() {}\n")
	writeWorkspaceContextFile(t, root, "node_modules/ignored.go", "package ignored\n\nfunc BuildContextBrief() {}\n")

	service, workspaceID := newKanbanTestServiceWithStore(t, root, filepath.Join(t.TempDir(), "state.json"))
	workspace, _, err := service.workspaceAndSettings(workspaceID)
	if err != nil {
		t.Fatalf("workspace settings: %v", err)
	}
	restore := stubGoLSPCommand("definitely_missing_gopls_for_context_test")
	defer restore()

	response, err := service.buildWorkspaceContext(context.Background(), workspace, tools.WorkspaceContextRequest{
		Task:     "Implement the context brief builder",
		MaxFiles: 5,
	})
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	label := workspaceRootLabel(t, service, workspaceID)
	if !strings.Contains(response.Brief, "Workspace Context Brief") {
		t.Fatalf("expected rendered brief, got %q", response.Brief)
	}
	if !workspaceContextHasManifest(response.Manifests, label+"/go.mod", "go module") {
		t.Fatalf("expected go.mod manifest, got %#v", response.Manifests)
	}
	if !workspaceContextHasManifest(response.Manifests, label+"/package.json", "node package") {
		t.Fatalf("expected package.json manifest, got %#v", response.Manifests)
	}
	if !workspaceContextHasCommand(response.LikelyCommands, "go test ./...", label) {
		t.Fatalf("expected go test command, got %#v", response.LikelyCommands)
	}
	if !workspaceContextHasCommand(response.LikelyCommands, "npm test", label) {
		t.Fatalf("expected npm test command, got %#v", response.LikelyCommands)
	}
	if !workspaceContextHasFile(response.RelevantFiles, label+"/internal/context_brief.go") {
		t.Fatalf("expected relevant implementation file, got %#v", response.RelevantFiles)
	}
	if workspaceContextHasFile(response.RelevantFiles, label+"/node_modules/ignored.go") {
		t.Fatalf("expected ignored directory to be skipped, got %#v", response.RelevantFiles)
	}
	if !workspaceContextHasFile(response.LikelyTestFiles, label+"/internal/context_brief_test.go") {
		t.Fatalf("expected likely test file, got %#v", response.LikelyTestFiles)
	}
	if !workspaceContextHasWarning(response.Warnings, "Go language server symbols unavailable") {
		t.Fatalf("expected graceful gopls warning, got %#v", response.Warnings)
	}
}

func TestWorkspaceContextBoostsChangedPathsAndVerification(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceContextFile(t, root, "go.mod", "module example.com/context\n\ngo 1.23\n")
	writeWorkspaceContextFile(t, root, "internal/target.go", "package internal\n\nfunc Target() {}\n")
	writeWorkspaceContextFile(t, root, "internal/other.go", "package internal\n\nfunc Other() {}\n")

	service, workspaceID := newKanbanTestServiceWithStore(t, root, filepath.Join(t.TempDir(), "state.json"))
	workspace, _, err := service.workspaceAndSettings(workspaceID)
	if err != nil {
		t.Fatalf("workspace settings: %v", err)
	}
	restore := stubGoLSPCommand("definitely_missing_gopls_for_context_test")
	defer restore()
	label := workspaceRootLabel(t, service, workspaceID)

	response, err := service.buildWorkspaceContext(context.Background(), workspace, tools.WorkspaceContextRequest{
		Task:         "Refactor implementation",
		ChangedPaths: []string{label + "/internal/target.go"},
		MaxFiles:     2,
	})
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	if len(response.RelevantFiles) == 0 || response.RelevantFiles[0].Path != label+"/internal/target.go" {
		t.Fatalf("expected changed path to rank first, got %#v", response.RelevantFiles)
	}
	if !workspaceContextHasCommand(response.VerificationCommands, "go test ./...", label) {
		t.Fatalf("expected verification command from changed Go path, got %#v", response.VerificationCommands)
	}
}

func TestRenderWorkspaceContextBriefCapsOutput(t *testing.T) {
	files := make([]tools.WorkspaceContextFile, 0, 80)
	long := strings.Repeat("x", 1000)
	for i := 0; i < 80; i++ {
		files = append(files, tools.WorkspaceContextFile{
			Path:   "project/file.go",
			Kind:   "go",
			Reason: "content match",
			Matches: []tools.WorkspaceContextMatch{{
				Line: 1,
				Text: long,
			}},
		})
	}

	brief, truncated := renderWorkspaceContextBrief(tools.WorkspaceContextResponse{
		Task:          "large context",
		RelevantFiles: files,
	})

	if !truncated {
		t.Fatal("expected brief to be truncated")
	}
	if len(brief) > tools.WorkspaceContextBriefMaxBytes {
		t.Fatalf("expected capped brief, got %d bytes", len(brief))
	}
	if !strings.Contains(brief, "context brief truncated by Echo") {
		t.Fatalf("expected truncation marker, got %q", brief[len(brief)-80:])
	}
}

func writeWorkspaceContextFile(t *testing.T, root string, relative string, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func workspaceContextHasManifest(manifests []tools.WorkspaceContextManifest, path string, kind string) bool {
	for _, manifest := range manifests {
		if manifest.Path == path && manifest.Kind == kind {
			return true
		}
	}
	return false
}

func workspaceContextHasCommand(commands []tools.WorkspaceContextCommand, command string, workingDirectory string) bool {
	for _, item := range commands {
		if item.Command == command && item.WorkingDirectory == workingDirectory {
			return true
		}
	}
	return false
}

func workspaceContextHasFile(files []tools.WorkspaceContextFile, path string) bool {
	for _, file := range files {
		if file.Path == path {
			return true
		}
	}
	return false
}

func workspaceContextHasWarning(warnings []string, value string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, value) {
			return true
		}
	}
	return false
}

func stubGoLSPCommand(commandName string) func() {
	previous := lspCommandForLanguage
	lspCommandForLanguage = func(languageID string) (lspServerCommand, bool) {
		if languageID == "go" {
			return lspServerCommand{name: commandName}, true
		}
		return previous(languageID)
	}
	return func() {
		lspCommandForLanguage = previous
	}
}
