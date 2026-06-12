package tools

import (
	"context"
	"testing"
)

type fakeWorkspaceContextProvider struct {
	request WorkspaceContextRequest
}

func (f *fakeWorkspaceContextProvider) QueryWorkspaceContext(ctx context.Context, request WorkspaceContextRequest) (WorkspaceContextResponse, error) {
	f.request = request
	return WorkspaceContextResponse{
		Task:  request.Task,
		Path:  request.Path,
		Brief: "context brief",
	}, nil
}

func TestWorkspaceContextCallsProviderWithNormalizedRequest(t *testing.T) {
	provider := &fakeWorkspaceContextProvider{}
	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspaceContext: provider},
		"workspace_context",
		mustToolJSON(t, map[string]any{
			"task":         "  Implement context briefs  ",
			"path":         `app\internal`,
			"changedPaths": []string{`app\foo.go`, "app/foo.go", "app/bar.go"},
			"maxFiles":     999,
		}),
	)

	if !result.Success {
		t.Fatalf("workspace_context failed: %#v", result)
	}
	if provider.request.Task != "Implement context briefs" {
		t.Fatalf("expected trimmed task, got %#v", provider.request)
	}
	if provider.request.Path != "app/internal" {
		t.Fatalf("expected normalized path, got %#v", provider.request)
	}
	if provider.request.MaxFiles != MaxWorkspaceContextMaxFiles {
		t.Fatalf("expected maxFiles cap, got %#v", provider.request)
	}
	if got := provider.request.ChangedPaths; len(got) != 2 || got[0] != "app/bar.go" || got[1] != "app/foo.go" {
		t.Fatalf("expected deduplicated changed paths, got %#v", got)
	}
	output, ok := result.Output.(WorkspaceContextResponse)
	if !ok || output.Brief != "context brief" {
		t.Fatalf("unexpected output: %#v", result.Output)
	}
}

func TestWorkspaceContextDefaultsMaxFiles(t *testing.T) {
	provider := &fakeWorkspaceContextProvider{}
	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspaceContext: provider},
		"workspace_context",
		mustToolJSON(t, map[string]any{"task": "Find relevant files"}),
	)

	if !result.Success {
		t.Fatalf("workspace_context failed: %#v", result)
	}
	if provider.request.Path != "." || provider.request.MaxFiles != DefaultWorkspaceContextMaxFiles {
		t.Fatalf("expected default path and maxFiles, got %#v", provider.request)
	}
}

func TestWorkspaceContextRejectsMissingTask(t *testing.T) {
	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspaceContext: &fakeWorkspaceContextProvider{}},
		"workspace_context",
		mustToolJSON(t, map[string]any{"task": "  "}),
	)

	if result.Success || result.Error == nil || result.Error.Code != "invalid_arguments" {
		t.Fatalf("expected invalid_arguments error, got %#v", result)
	}
}

func TestWorkspaceContextRequiresProvider(t *testing.T) {
	result := Execute(
		ExecutionContext{Context: context.Background()},
		"workspace_context",
		mustToolJSON(t, map[string]any{"task": "Find relevant files"}),
	)

	if result.Success || result.Error == nil || result.Error.Code != "workspace_context_unavailable" {
		t.Fatalf("expected workspace_context_unavailable error, got %#v", result)
	}
}
