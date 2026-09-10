package sourcecontrol

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type contractProvider struct {
	id            string
	repositories  []Repository
	status        StatusSnapshot
	actions       int
	historyQuery  HistoryQuery
	patchRequest  PatchRequest
	searchRequest RepositorySearchRequest
}

func (p *contractProvider) Descriptor(context.Context, string) ProviderDescriptor {
	return ProviderDescriptor{ID: p.id, Label: p.id, Available: true, Capabilities: []Capability{CapabilityStatus, CapabilityDiff, CapabilityHistory}}
}
func (p *contractProvider) Repositories(context.Context, string) ([]Repository, error) {
	return append([]Repository(nil), p.repositories...), nil
}
func (p *contractProvider) Status(context.Context, string, string) (StatusSnapshot, error) {
	return p.status, nil
}
func (p *contractProvider) Diff(_ context.Context, _, repositoryID string, target DiffTarget) (DiffDocument, error) {
	return DiffDocument{RepositoryID: repositoryID, ProviderID: p.id, Target: target}, nil
}
func (p *contractProvider) History(context.Context, string, string, int, int) (History, error) {
	return History{Commits: []Commit{{Hash: p.id + "-revision"}}}, nil
}
func (p *contractProvider) RevisionDetail(context.Context, string, string, string, string) (RevisionDetail, error) {
	return RevisionDetail{Ref: p.id + "-revision"}, nil
}
func (p *contractProvider) QueryHistory(_ context.Context, _, _ string, query HistoryQuery) (History, error) {
	p.historyQuery = query
	return History{Commits: []Commit{{Hash: p.id + "-query"}}}, nil
}
func (p *contractProvider) Patch(_ context.Context, _, _ string, request PatchRequest) (PatchResult, error) {
	p.patchRequest = request
	return PatchResult{Comparison: request.Comparison}, nil
}
func (p *contractProvider) Search(_ context.Context, _, _ string, request RepositorySearchRequest) (RepositorySearchResult, error) {
	p.searchRequest = request
	return RepositorySearchResult{Query: request.Query}, nil
}
func (p *contractProvider) Annotate(_ context.Context, _, repositoryID, path, ref string, start, end int) (Annotation, error) {
	return Annotation{RepositoryID: repositoryID, ProviderID: p.id, Path: path, Revision: ref, StartLine: start, EndLine: end}, nil
}
func (p *contractProvider) Action(_ context.Context, _, repositoryID string, request ActionRequest) (ActionResult, error) {
	if request.ExpectedRevision != 0 && request.ExpectedRevision != p.status.Revision {
		return ActionResult{}, &Error{Code: "stale_source_control_revision", Message: "stale"}
	}
	p.actions++
	return ActionResult{RequestID: request.RequestID, RepositoryID: repositoryID, Revision: p.status.Revision + 1}, nil
}

func TestRepositoryIDIsProviderQualified(t *testing.T) {
	git := RepositoryID("workspace", "git", `C:\project`)
	fossil := RepositoryID("workspace", "fossil", `C:\project`)
	if git == fossil {
		t.Fatal("same-root repositories from different providers must have different IDs")
	}
	if got := RepositoryID("workspace", "git", `C:\project`); got != git {
		t.Fatal("repository IDs must be stable")
	}
}

func TestRegistryKeepsMixedSameRootRepositoriesAndDispatchesCapabilities(t *testing.T) {
	ctx := context.Background()
	service := New()
	gitID := RepositoryID("workspace", "git", `C:\project`)
	fossilID := RepositoryID("workspace", "fossil", `C:\project`)
	git := &contractProvider{id: "git", repositories: []Repository{{ID: gitID, ProviderID: "git", Label: "project", Available: true}}, status: StatusSnapshot{RepositoryID: gitID, ProviderID: "git", Revision: 4}}
	fossil := &contractProvider{id: "fossil", repositories: []Repository{{ID: fossilID, ProviderID: "fossil", Label: "project", Available: true}}, status: StatusSnapshot{RepositoryID: fossilID, ProviderID: "fossil", Revision: 7}}
	if err := service.Register(git); err != nil {
		t.Fatal(err)
	}
	if err := service.Register(fossil); err != nil {
		t.Fatal(err)
	}
	if err := service.Register(&contractProvider{id: "git"}); err == nil {
		t.Fatal("duplicate provider registration must fail")
	}
	repositories, err := service.Repositories(ctx, "workspace")
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 2 || repositories[0].ID == repositories[1].ID {
		t.Fatalf("mixed repositories = %#v", repositories)
	}
	status, err := service.Status(ctx, "workspace", fossilID)
	if err != nil || status.ProviderID != "fossil" || status.Revision != 7 {
		t.Fatalf("status = %#v, %v", status, err)
	}
	diff, err := service.Diff(ctx, "workspace", gitID, DiffTarget{Kind: "change", Path: "main.go"})
	if err != nil || diff.ProviderID != "git" {
		t.Fatalf("diff = %#v, %v", diff, err)
	}
	annotation, err := service.Annotate(ctx, "workspace", fossilID, "main.go", "current", 1, 10)
	if err != nil || annotation.ProviderID != "fossil" {
		t.Fatalf("annotation = %#v, %v", annotation, err)
	}
	history, err := service.QueryHistory(ctx, "workspace", fossilID, HistoryQuery{Branch: "trunk", Limit: 17})
	if err != nil || len(history.Commits) != 1 || fossil.historyQuery.Branch != "trunk" {
		t.Fatalf("history query = %#v, provider=%#v, %v", history, fossil.historyQuery, err)
	}
	patch, err := service.Patch(ctx, "workspace", fossilID, PatchRequest{Comparison: "protected"})
	if err != nil || patch.Comparison != "protected" || fossil.patchRequest.Comparison != "protected" {
		t.Fatalf("patch = %#v, provider=%#v, %v", patch, fossil.patchRequest, err)
	}
	search, err := service.Search(ctx, "workspace", fossilID, RepositorySearchRequest{Query: "architecture"})
	if err != nil || search.Query != "architecture" || fossil.searchRequest.Query != "architecture" {
		t.Fatalf("search = %#v, provider=%#v, %v", search, fossil.searchRequest, err)
	}
}

func TestActionEventsAndStaleRevision(t *testing.T) {
	ctx := context.Background()
	service := New()
	repositoryID := RepositoryID("workspace", "git", "/project")
	provider := &contractProvider{id: "git", repositories: []Repository{{ID: repositoryID, ProviderID: "git", Label: "project", Available: true}}, status: StatusSnapshot{WorkspaceID: "workspace", RepositoryID: repositoryID, ProviderID: "git", Revision: 9}}
	if err := service.Register(provider); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Repositories(ctx, "workspace"); err != nil {
		t.Fatal(err)
	}
	events := []Event{}
	service.SetNotifier(func(event Event) { events = append(events, event) })
	if _, err := service.Action(ctx, "workspace", repositoryID, ActionRequest{RequestID: "stale", Action: "commit", ExpectedRevision: 8}); err == nil {
		t.Fatal("stale action should fail")
	} else {
		var sourceError *Error
		if !errors.As(err, &sourceError) || sourceError.Code != "stale_source_control_revision" {
			t.Fatalf("unexpected stale error: %v", err)
		}
	}
	if provider.actions != 0 {
		t.Fatal("stale action reached mutation")
	}
	events = nil
	if _, err := service.Action(ctx, "workspace", repositoryID, ActionRequest{RequestID: "fresh", Action: "commit", ExpectedRevision: 9}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Type != "source_control_operation" || events[1].Type != "source_control_operation" || events[2].Type != "source_control_status" {
		t.Fatalf("events = %#v", events)
	}
}

type statusOnlyProvider struct{ contractProvider }

func TestUnsupportedCapabilityFailsExplicitly(t *testing.T) {
	service := New()
	repositoryID := RepositoryID("workspace", "status-only", "/project")
	provider := &statusOnlyDiscovery{id: "status-only", repositoryID: repositoryID}
	if err := service.Register(provider); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Repositories(context.Background(), "workspace"); err != nil {
		t.Fatal(err)
	}
	_, err := service.Diff(context.Background(), "workspace", repositoryID, DiffTarget{Path: "x"})
	var sourceError *Error
	if !errors.As(err, &sourceError) || sourceError.Code != "unsupported_source_control_capability" {
		t.Fatalf("unexpected error: %v", err)
	}
}

type statusOnlyDiscovery struct {
	id           string
	repositoryID string
}

func (p *statusOnlyDiscovery) Descriptor(context.Context, string) ProviderDescriptor {
	return ProviderDescriptor{ID: p.id, Label: p.id, Available: true, Capabilities: []Capability{CapabilityStatus}}
}
func (p *statusOnlyDiscovery) Repositories(context.Context, string) ([]Repository, error) {
	return []Repository{{ID: p.repositoryID, ProviderID: p.id, Label: "project", Available: true}}, nil
}
func (p *statusOnlyDiscovery) Status(context.Context, string, string) (StatusSnapshot, error) {
	return StatusSnapshot{RepositoryID: p.repositoryID, ProviderID: p.id}, nil
}

type legacyHistoryProvider struct {
	id           string
	repositoryID string
}

func (p *legacyHistoryProvider) Descriptor(context.Context, string) ProviderDescriptor {
	return ProviderDescriptor{ID: p.id, Label: p.id, Available: true, Capabilities: []Capability{CapabilityHistory}}
}
func (p *legacyHistoryProvider) Repositories(context.Context, string) ([]Repository, error) {
	return []Repository{{ID: p.repositoryID, ProviderID: p.id, Label: "project", Available: true}}, nil
}
func (p *legacyHistoryProvider) History(_ context.Context, _, _ string, offset, limit int) (History, error) {
	return History{Commits: []Commit{{Hash: fmt.Sprintf("%d-%d", offset, limit)}}}, nil
}
func (p *legacyHistoryProvider) RevisionDetail(context.Context, string, string, string, string) (RevisionDetail, error) {
	return RevisionDetail{}, nil
}

func TestAdvancedHistoryQueryFallsBackWithoutChangingLegacyProviders(t *testing.T) {
	service := New()
	repositoryID := RepositoryID("workspace", "legacy", "/project")
	provider := &legacyHistoryProvider{id: "legacy", repositoryID: repositoryID}
	if err := service.Register(provider); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Repositories(context.Background(), "workspace"); err != nil {
		t.Fatal(err)
	}
	history, err := service.QueryHistory(context.Background(), "workspace", repositoryID, HistoryQuery{Offset: 3, Limit: 7})
	if err != nil || len(history.Commits) != 1 || history.Commits[0].Hash != "3-7" {
		t.Fatalf("fallback history = %#v, %v", history, err)
	}
	_, err = service.QueryHistory(context.Background(), "workspace", repositoryID, HistoryQuery{Query: "filtered"})
	var sourceError *Error
	if !errors.As(err, &sourceError) || sourceError.Code != "unsupported_source_control_capability" {
		t.Fatalf("filtered fallback error = %v", err)
	}
}
