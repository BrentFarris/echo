package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/brent/echo/internal/sourcecontrol"
)

type recordingFossilInspector struct {
	repositories  []sourcecontrol.Repository
	historyQuery  sourcecontrol.HistoryQuery
	searchRequest sourcecontrol.RepositorySearchRequest
	patchRequest  sourcecontrol.PatchRequest
	statusCalls   []string
	annotateCalls []string
}

func (f *recordingFossilInspector) Repositories(context.Context, string) ([]sourcecontrol.Repository, error) {
	return append([]sourcecontrol.Repository(nil), f.repositories...), nil
}

func (f *recordingFossilInspector) Status(_ context.Context, _, repositoryID string) (sourcecontrol.StatusSnapshot, error) {
	f.statusCalls = append(f.statusCalls, repositoryID)
	return sourcecontrol.StatusSnapshot{RepositoryID: repositoryID, ProviderID: "fossil"}, nil
}

func (*recordingFossilInspector) Diff(context.Context, string, string, sourcecontrol.DiffTarget) (sourcecontrol.DiffDocument, error) {
	return sourcecontrol.DiffDocument{}, nil
}

func (*recordingFossilInspector) History(context.Context, string, string, int, int) (sourcecontrol.History, error) {
	return sourcecontrol.History{}, nil
}

func (*recordingFossilInspector) RevisionDetail(_ context.Context, _, _, ref, _ string) (sourcecontrol.RevisionDetail, error) {
	return sourcecontrol.RevisionDetail{Ref: ref, Commit: &sourcecontrol.Commit{Hash: ref}}, nil
}

func (f *recordingFossilInspector) Annotate(_ context.Context, _, repositoryID, path, ref string, start, end int) (sourcecontrol.Annotation, error) {
	f.annotateCalls = append(f.annotateCalls, repositoryID+":"+path+":"+ref)
	return sourcecontrol.Annotation{RepositoryID: repositoryID, ProviderID: "fossil", Path: path, Revision: ref, StartLine: start, EndLine: end}, nil
}

func (f *recordingFossilInspector) QueryHistory(_ context.Context, _, _ string, query sourcecontrol.HistoryQuery) (sourcecontrol.History, error) {
	f.historyQuery = query
	return sourcecontrol.History{Commits: []sourcecontrol.Commit{{Hash: "history"}}}, nil
}

func (f *recordingFossilInspector) Patch(_ context.Context, _, _ string, request sourcecontrol.PatchRequest) (sourcecontrol.PatchResult, error) {
	f.patchRequest = request
	return sourcecontrol.PatchResult{Comparison: request.Comparison, Ref: request.Ref}, nil
}

func (f *recordingFossilInspector) Search(_ context.Context, _, _ string, request sourcecontrol.RepositorySearchRequest) (sourcecontrol.RepositorySearchResult, error) {
	f.searchRequest = request
	return sourcecontrol.RepositorySearchResult{Query: request.Query, Scopes: request.Scopes, Limit: request.Limit}, nil
}

func fossilInspectionContext(inspector *recordingFossilInspector) ExecutionContext {
	return ExecutionContext{Context: context.Background(), WorkspaceID: "workspace", SourceControl: inspector}
}

func TestFossilInspectSelectsOnlyFossilAndDispatchesDefaults(t *testing.T) {
	inspector := &recordingFossilInspector{repositories: []sourcecontrol.Repository{
		{ID: "git-id", ProviderID: "git", ProviderLabel: "Git", Label: "project", Available: true},
		{ID: "fossil-id", ProviderID: "fossil", ProviderLabel: "Fossil", Label: "project", Available: true},
	}}
	result := Execute(fossilInspectionContext(inspector), FossilInspectToolName, json.RawMessage(`{"operation":"log","repository":"project","query":"Fix bug","path":"src/main.go"}`))
	if !result.Success {
		t.Fatalf("log failed: %#v", result.Error)
	}
	if inspector.historyQuery.Limit != 20 || inspector.historyQuery.Query != "Fix bug" || inspector.historyQuery.Path != "src/main.go" {
		t.Fatalf("history query = %#v", inspector.historyQuery)
	}
	output, ok := result.Output.(fossilInspectOutput)
	if !ok || output.Repository.ID != "fossil-id" || output.Provider != "fossil" || output.History == nil {
		t.Fatalf("output = %#v", result.Output)
	}

	search := Execute(fossilInspectionContext(inspector), FossilInspectToolName, json.RawMessage(`{"operation":"search","repository":"fossil-id","query":"architecture"}`))
	if !search.Success {
		t.Fatalf("search failed: %#v", search.Error)
	}
	if inspector.searchRequest.Limit != 20 || inspector.searchRequest.MaxOutputBytes != sourcecontrol.InspectionDefaultOutputMax || len(inspector.searchRequest.Scopes) != 1 || inspector.searchRequest.Scopes[0] != "checkins" {
		t.Fatalf("search request = %#v", inspector.searchRequest)
	}
}

func TestFossilInspectBuildsShowAndRepositoryWideDiffRequests(t *testing.T) {
	inspector := &recordingFossilInspector{repositories: []sourcecontrol.Repository{
		{ID: "fossil-id", ProviderID: "fossil", ProviderLabel: "Fossil", Label: "project", Available: true},
	}}
	show := Execute(fossilInspectionContext(inspector), FossilInspectToolName, json.RawMessage(`{"operation":"show","repository":"project","revision":"abc123"}`))
	if !show.Success {
		t.Fatalf("show failed: %#v", show.Error)
	}
	if inspector.patchRequest.Comparison != "revision" || inspector.patchRequest.Ref != "abc123" || inspector.patchRequest.Path != "" || !inspector.patchRequest.IncludePatch || inspector.patchRequest.ContextLines != 3 || inspector.patchRequest.MaxOutputBytes != sourcecontrol.InspectionDefaultOutputMax {
		t.Fatalf("show patch = %#v", inspector.patchRequest)
	}

	diff := Execute(fossilInspectionContext(inspector), FossilInspectToolName, json.RawMessage(`{"operation":"diff","repository":"project","comparison":"revisions","base":"before","target":"after","includePatch":false,"contextLines":0}`))
	if !diff.Success {
		t.Fatalf("diff failed: %#v", diff.Error)
	}
	if inspector.patchRequest.Comparison != "revisions" || inspector.patchRequest.BaseRef != "before" || inspector.patchRequest.Ref != "after" || inspector.patchRequest.Path != "" || inspector.patchRequest.IncludePatch || inspector.patchRequest.ContextLines != 0 {
		t.Fatalf("diff patch = %#v", inspector.patchRequest)
	}
}

func TestFossilInspectValidationAndUnavailableDiagnostics(t *testing.T) {
	inspector := &recordingFossilInspector{repositories: []sourcecontrol.Repository{
		{ID: "fossil-id", ProviderID: "fossil", ProviderLabel: "Fossil", Label: "project", Available: true},
	}}
	for name, arguments := range map[string]string{
		"search query":    `{"operation":"search","repository":"project"}`,
		"revision branch": `{"operation":"log","repository":"project","revision":"abc","branch":"trunk"}`,
		"diff revisions":  `{"operation":"diff","repository":"project","comparison":"revisions","base":"abc"}`,
		"blame path":      `{"operation":"blame","repository":"project"}`,
		"mixed all scope": `{"operation":"search","repository":"project","query":"x","scopes":["all","wiki"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			result := Execute(fossilInspectionContext(inspector), FossilInspectToolName, json.RawMessage(arguments))
			if result.Success || result.Error == nil || result.Error.Code != "invalid_arguments" {
				t.Fatalf("result = %#v", result)
			}
		})
	}

	inspector.repositories[0].Available = false
	inspector.repositories[0].Diagnostic = "Fossil executable is unavailable"
	result := Execute(fossilInspectionContext(inspector), FossilInspectToolName, json.RawMessage(`{"operation":"status","repository":"project"}`))
	if result.Success || result.Error == nil || result.Error.Message != "Fossil executable is unavailable" {
		t.Fatalf("result = %#v", result)
	}
}

func TestFossilInspectRegistrationAndResearchAvailability(t *testing.T) {
	registered := false
	for _, tool := range Registered() {
		if tool.Metadata().Name == FossilInspectToolName {
			registered = true
			break
		}
	}
	if !registered {
		t.Fatal("fossil_inspect was not registered")
	}
	if !IsResearchWorkerToolName(FossilInspectToolName) {
		t.Fatal("research workers must expose fossil_inspect")
	}
}
