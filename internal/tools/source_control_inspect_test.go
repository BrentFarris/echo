package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/brent/echo/internal/sourcecontrol"
)

type recordingSourceControlInspector struct {
	repositories []sourcecontrol.Repository
	status       sourcecontrol.StatusSnapshot
	statusCalls  []string
	diffTargets  []sourcecontrol.DiffTarget
}

func (f *recordingSourceControlInspector) Repositories(context.Context, string) ([]sourcecontrol.Repository, error) {
	return append([]sourcecontrol.Repository(nil), f.repositories...), nil
}

func (f *recordingSourceControlInspector) Status(_ context.Context, _, repositoryID string) (sourcecontrol.StatusSnapshot, error) {
	f.statusCalls = append(f.statusCalls, repositoryID)
	if f.status.RepositoryID != "" || len(f.status.Groups) > 0 {
		return f.status, nil
	}
	return sourcecontrol.StatusSnapshot{RepositoryID: repositoryID, ProviderID: "fossil", Revision: 7}, nil
}

func TestSourceControlInspectResolvesIncludedRoleForFossil(t *testing.T) {
	inspector := &recordingSourceControlInspector{
		repositories: []sourcecontrol.Repository{{ID: "fossil-id", ProviderID: "fossil", ProviderLabel: "Fossil", Label: "project", Available: true}},
		status: sourcecontrol.StatusSnapshot{RepositoryID: "fossil-id", ProviderID: "fossil", Groups: []sourcecontrol.ChangeGroup{
			{ID: "included-empty", Role: "included"},
			{ID: "protected", Role: "included", Changes: []sourcecontrol.Change{{Path: "src/main.go", GroupID: "protected"}}},
		}},
	}
	execution := ExecutionContext{Context: context.Background(), WorkspaceID: "workspace", SourceControl: inspector}
	result := Execute(execution, SourceControlInspectToolName, json.RawMessage(`{"operation":"diff","repository":"project","path":"src/main.go","comparison":"included"}`))
	if !result.Success || len(inspector.diffTargets) != 1 || inspector.diffTargets[0].GroupID != "protected" {
		t.Fatalf("included Fossil diff was not resolved through the semantic group: result=%#v targets=%#v", result, inspector.diffTargets)
	}
}

func (f *recordingSourceControlInspector) Diff(_ context.Context, _, repositoryID string, target sourcecontrol.DiffTarget) (sourcecontrol.DiffDocument, error) {
	f.diffTargets = append(f.diffTargets, target)
	return sourcecontrol.DiffDocument{RepositoryID: repositoryID, Target: target}, nil
}

func (*recordingSourceControlInspector) History(context.Context, string, string, int, int) (sourcecontrol.History, error) {
	return sourcecontrol.History{}, nil
}

func (*recordingSourceControlInspector) RevisionDetail(context.Context, string, string, string, string) (sourcecontrol.RevisionDetail, error) {
	return sourcecontrol.RevisionDetail{}, nil
}

func (*recordingSourceControlInspector) Annotate(context.Context, string, string, string, string, int, int) (sourcecontrol.Annotation, error) {
	return sourcecontrol.Annotation{}, nil
}

func TestSourceControlInspectDisambiguatesProvidersAndDispatchesStatus(t *testing.T) {
	inspector := &recordingSourceControlInspector{repositories: []sourcecontrol.Repository{
		{ID: "git-id", ProviderID: "git", ProviderLabel: "Git", Label: "project", Available: true},
		{ID: "fossil-id", ProviderID: "fossil", ProviderLabel: "Fossil", Label: "project", Available: true},
	}}
	execution := ExecutionContext{Context: context.Background(), WorkspaceID: "workspace", SourceControl: inspector}

	ambiguous := Execute(execution, SourceControlInspectToolName, json.RawMessage(`{"operation":"status","repository":"project"}`))
	if ambiguous.Success || ambiguous.Error == nil || ambiguous.Error.Code != "ambiguous_repository" {
		t.Fatalf("expected provider ambiguity, got %#v", ambiguous)
	}

	result := Execute(execution, SourceControlInspectToolName, json.RawMessage(`{"operation":"status","repository":"project","provider":"fossil"}`))
	if !result.Success {
		t.Fatalf("status failed: %#v", result.Error)
	}
	output, ok := result.Output.(sourceControlInspectOutput)
	if !ok || output.Provider != "fossil" || output.Repository.ID != "fossil-id" || output.Status == nil {
		t.Fatalf("unexpected source control output: %#v", result.Output)
	}
	if len(inspector.statusCalls) != 1 || inspector.statusCalls[0] != "fossil-id" {
		t.Fatalf("status was dispatched to the wrong repository: %#v", inspector.statusCalls)
	}
}

func TestSourceControlInspectBuildsProviderNeutralDiffTargets(t *testing.T) {
	inspector := &recordingSourceControlInspector{repositories: []sourcecontrol.Repository{
		{ID: "fossil-id", ProviderID: "fossil", ProviderLabel: "Fossil", Label: "project", Available: true},
	}}
	execution := ExecutionContext{Context: context.Background(), WorkspaceID: "workspace", SourceControl: inspector}
	result := Execute(execution, SourceControlInspectToolName, json.RawMessage(`{"operation":"diff","repository":"project","path":"src/main.go","comparison":"revisions","base":"before","target":"after"}`))
	if !result.Success {
		t.Fatalf("diff failed: %#v", result.Error)
	}
	if len(inspector.diffTargets) != 1 {
		t.Fatalf("expected one diff call, got %#v", inspector.diffTargets)
	}
	target := inspector.diffTargets[0]
	if target.Kind != "revisions" || target.Path != "src/main.go" || target.BaseRef != "before" || target.Ref != "after" {
		t.Fatalf("unexpected diff target: %#v", target)
	}

	unsupported := Execute(execution, SourceControlInspectToolName, json.RawMessage(`{"operation":"diff","repository":"project","path":"src/main.go","comparison":"included"}`))
	if unsupported.Success || unsupported.Error == nil || unsupported.Error.Code != "unsupported_source_control_capability" {
		t.Fatalf("expected staging rejection for Fossil, got %#v", unsupported)
	}
}

func TestSourceControlInspectReportsUnavailableRepositoryDiagnostic(t *testing.T) {
	inspector := &recordingSourceControlInspector{repositories: []sourcecontrol.Repository{
		{ID: "fossil-id", ProviderID: "fossil", ProviderLabel: "Fossil", Label: "project", Diagnostic: "Fossil is missing"},
	}}
	result := Execute(ExecutionContext{Context: context.Background(), WorkspaceID: "workspace", SourceControl: inspector}, SourceControlInspectToolName, json.RawMessage(`{"operation":"status","repository":"fossil-id"}`))
	if result.Success || result.Error == nil || result.Error.Code != "source_control_unavailable" || result.Error.Message != "Fossil is missing" {
		t.Fatalf("expected actionable unavailable diagnostic, got %#v", result)
	}
}

func TestSourceControlInspectSelfRegisters(t *testing.T) {
	for _, tool := range Registered() {
		if tool.Metadata().Name == SourceControlInspectToolName {
			return
		}
	}
	t.Fatal("source_control_inspect was not registered")
}
