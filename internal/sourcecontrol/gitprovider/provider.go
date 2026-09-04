// Package gitprovider adapts Echo's mature Git engine to the provider-neutral
// source control contracts.
package gitprovider

import (
	"context"
	"strings"

	"github.com/brent/echo/internal/gitservice"
	"github.com/brent/echo/internal/sourcecontrol"
)

const ID = "git"

var capabilities = []sourcecontrol.Capability{
	sourcecontrol.CapabilityStatus, sourcecontrol.CapabilityDiff, sourcecontrol.CapabilityHistory,
	sourcecontrol.CapabilityStage, sourcecontrol.CapabilityCommitAll, sourcecontrol.CapabilityCommitSelected,
	sourcecontrol.CapabilitySync, sourcecontrol.CapabilityPull, sourcecontrol.CapabilityPush,
	sourcecontrol.CapabilityBranches, sourcecontrol.CapabilityMerge, sourcecontrol.CapabilityStashes,
	sourcecontrol.CapabilityInitialize, sourcecontrol.CapabilityClone,
}

type Provider struct{ service *gitservice.Service }

func New(service *gitservice.Service) *Provider { return &Provider{service: service} }

func (p *Provider) Descriptor(ctx context.Context, workspaceID string) sourcecontrol.ProviderDescriptor {
	descriptor := sourcecontrol.ProviderDescriptor{ID: ID, Label: "Git", Capabilities: append([]sourcecontrol.Capability(nil), capabilities...)}
	if strings.TrimSpace(workspaceID) == "" {
		return descriptor
	}
	version, err := p.service.Version(ctx, workspaceID)
	if err != nil {
		descriptor.Diagnostic = "Git is not installed or is unavailable in this workspace execution environment"
		return descriptor
	}
	descriptor.Available = true
	descriptor.Version = version
	return descriptor
}

func (p *Provider) Repositories(ctx context.Context, workspaceID string) ([]sourcecontrol.Repository, error) {
	repositories, err := p.service.Repositories(ctx, workspaceID)
	if err != nil {
		return nil, convertError(err)
	}
	result := make([]sourcecontrol.Repository, 0, len(repositories))
	for _, repository := range repositories {
		scopes := make([]sourcecontrol.Scope, len(repository.Scopes))
		for index, scope := range repository.Scopes {
			scopes[index] = sourcecontrol.Scope{RootID: scope.RootID, RootLabel: scope.RootLabel, RepoPrefix: scope.RepoPrefix}
		}
		result = append(result, sourcecontrol.Repository{
			ID: repository.ID, ProviderID: ID, ProviderLabel: "Git", Label: repository.Label,
			RootRef: repository.RootRef, Parent: repository.Parent, Scopes: scopes,
			Revision: repository.Revision, Available: true, Capabilities: append([]sourcecontrol.Capability(nil), capabilities...),
		})
	}
	return result, nil
}

func (p *Provider) Status(ctx context.Context, workspaceID, repositoryID string) (sourcecontrol.StatusSnapshot, error) {
	status, err := p.service.Status(ctx, workspaceID, repositoryID)
	if err != nil {
		return sourcecontrol.StatusSnapshot{}, convertError(err)
	}
	return StatusFromGit(status), nil
}

func StatusFromGit(status gitservice.StatusSnapshot) sourcecontrol.StatusSnapshot {
	groups := []sourcecontrol.ChangeGroup{
		{ID: "conflicts", Label: "Merge Changes", Role: "conflicts", Changes: changesFromGit(status.Conflicts, "conflicts"), Actions: []string{"stage", "discard"}},
		{ID: "staged", Label: "Staged Changes", Role: "included", Changes: changesFromGit(status.Staged, "staged"), Actions: []string{"unstage", "commit_staged"}},
		{ID: "unstaged", Label: "Changes", Role: "working", Changes: changesFromGit(status.Unstaged, "unstaged"), Actions: []string{"stage", "discard"}},
	}
	return sourcecontrol.StatusSnapshot{
		WorkspaceID: status.WorkspaceID, RepositoryID: status.RepositoryID, ProviderID: ID,
		Revision: status.Revision, Branch: status.Branch, Head: status.Head, Detached: status.Detached,
		Upstream: status.Upstream, Ahead: status.Ahead, Behind: status.Behind, StashCount: status.StashCount,
		Groups: groups, HiddenChangeCount: status.HiddenStagedCount, Truncated: status.Truncated,
		TotalChangeCount: status.TotalChangeCount,
		State:            sourcecontrol.RepositoryState{MergeInProgress: status.State.MergeInProgress, RebaseInProgress: status.State.RebaseInProgress, CherryPickInProgress: status.State.CherryPickInProgress},
	}
}

func changesFromGit(changes []gitservice.Change, groupID string) []sourcecontrol.Change {
	result := make([]sourcecontrol.Change, 0, len(changes))
	for _, change := range changes {
		result = append(result, sourcecontrol.Change{
			Path: change.Path, OldPath: change.OldPath, Ref: change.Ref, Status: change.Status,
			StatusCode: change.StatusCode, Kind: gitChangeKind(change), GroupID: groupID, Submodule: change.Submodule,
		})
	}
	return result
}

func gitChangeKind(change gitservice.Change) string {
	if change.StatusCode == "?" {
		return "untracked"
	}
	if change.StatusCode == "A" {
		return "added"
	}
	if change.StatusCode == "D" {
		return "deleted"
	}
	if change.OldPath != "" {
		return "renamed"
	}
	return "modified"
}

func (p *Provider) Diff(ctx context.Context, workspaceID, repositoryID string, target sourcecontrol.DiffTarget) (sourcecontrol.DiffDocument, error) {
	if target.Kind == "revisions" || target.Kind == "revision_to_worktree" {
		targetRef := target.Ref
		if target.Kind == "revision_to_worktree" {
			targetRef = ""
		}
		document, err := p.service.DiffBetween(ctx, workspaceID, repositoryID, target.Path, target.OldPath, target.BaseRef, targetRef)
		if err != nil {
			return sourcecontrol.DiffDocument{}, convertError(err)
		}
		return sourcecontrol.DiffDocument{
			RepositoryID: document.RepositoryID, ProviderID: ID, Target: target, Ref: document.Ref,
			Revision: document.Revision, ModifiedRevision: document.ModifiedRevision,
			Original: sourcecontrol.DiffSide(document.Original), Modified: sourcecontrol.DiffSide(document.Modified),
			Editable: document.Editable, Kind: document.Kind, UnavailableReason: document.UnavailableReason,
		}, nil
	}
	scope := target.GroupID
	switch scope {
	case "conflicts":
		scope = "conflict"
	case "working", "untracked":
		scope = "unstaged"
	case "included":
		scope = "staged"
	}
	if target.Kind == "revision" || target.Kind == "commit" {
		scope = "commit"
	} else if target.Kind == "stash" {
		scope = "stash"
	}
	document, err := p.service.Diff(ctx, workspaceID, repositoryID, scope, target.Path, target.OldPath, target.Ref)
	if err != nil {
		return sourcecontrol.DiffDocument{}, convertError(err)
	}
	return sourcecontrol.DiffDocument{
		RepositoryID: document.RepositoryID, ProviderID: ID, Target: target, Ref: document.Ref,
		Revision: document.Revision, ModifiedRevision: document.ModifiedRevision,
		Original: sourcecontrol.DiffSide(document.Original), Modified: sourcecontrol.DiffSide(document.Modified),
		Editable: document.Editable, Kind: document.Kind, UnavailableReason: document.UnavailableReason,
	}, nil
}

func (p *Provider) Annotate(ctx context.Context, workspaceID, repositoryID, path, ref string, startLine, endLine int) (sourcecontrol.Annotation, error) {
	annotation, err := p.service.Annotate(ctx, workspaceID, repositoryID, path, ref, startLine, endLine)
	if err != nil {
		return sourcecontrol.Annotation{}, convertError(err)
	}
	return sourcecontrol.Annotation{
		RepositoryID: repositoryID, ProviderID: ID, Revision: annotation.Revision, Path: annotation.Path,
		StartLine: annotation.StartLine, EndLine: annotation.EndLine, Text: annotation.Text, Truncated: annotation.Truncated,
	}, nil
}

func (p *Provider) Metadata(ctx context.Context, workspaceID, repositoryID string) (sourcecontrol.Metadata, error) {
	metadata, err := p.service.Metadata(ctx, workspaceID, repositoryID)
	if err != nil {
		return sourcecontrol.Metadata{}, convertError(err)
	}
	result := sourcecontrol.Metadata{Tags: append([]string(nil), metadata.Tags...)}
	for _, branch := range metadata.Branches {
		result.Branches = append(result.Branches, sourcecontrol.Branch{Name: branch.Name, Current: branch.Current, Remote: branch.Remote})
	}
	for _, branch := range metadata.RemoteBranches {
		result.RemoteBranches = append(result.RemoteBranches, sourcecontrol.Branch{Name: branch.Name, Current: branch.Current, Remote: branch.Remote})
	}
	for _, remote := range metadata.Remotes {
		result.Remotes = append(result.Remotes, sourcecontrol.Remote{Name: remote.Name, FetchURL: remote.FetchURL, PushURL: remote.PushURL})
	}
	for _, stash := range metadata.Stashes {
		result.Stashes = append(result.Stashes, sourcecontrol.Stash{Ref: stash.Ref, Hash: stash.Hash, Message: stash.Message})
	}
	return result, nil
}

func (p *Provider) History(ctx context.Context, workspaceID, repositoryID string, offset, limit int) (sourcecontrol.History, error) {
	history, err := p.service.History(ctx, workspaceID, repositoryID, offset, limit)
	if err != nil {
		return sourcecontrol.History{}, convertError(err)
	}
	result := sourcecontrol.History{NextOffset: history.NextOffset, HasMore: history.HasMore}
	for _, commit := range history.Commits {
		result.Commits = append(result.Commits, sourcecontrol.Commit(commit))
	}
	return result, nil
}

func (p *Provider) RevisionDetail(ctx context.Context, workspaceID, repositoryID, ref, kind string) (sourcecontrol.RevisionDetail, error) {
	detail, err := p.service.CommitDetail(ctx, workspaceID, repositoryID, ref, kind == "stash")
	if err != nil {
		return sourcecontrol.RevisionDetail{}, convertError(err)
	}
	result := sourcecontrol.RevisionDetail{Ref: detail.Ref}
	for _, file := range detail.Files {
		result.Files = append(result.Files, sourcecontrol.RevisionFile(file))
	}
	return result, nil
}

func (p *Provider) Action(ctx context.Context, workspaceID, repositoryID string, request sourcecontrol.ActionRequest) (sourcecontrol.ActionResult, error) {
	result, err := p.service.Action(ctx, workspaceID, repositoryID, gitservice.ActionRequest{
		RequestID: request.RequestID, Action: request.Action, ExpectedRevision: request.ExpectedRevision,
		Paths: request.Paths, Message: request.Message,
		Ref: request.Ref, StartPoint: request.StartPoint, Name: request.Name, Remote: request.Remote,
		Branch: request.Branch, URL: request.URL, Confirmed: request.Confirmed,
	})
	if err != nil {
		return sourcecontrol.ActionResult{}, convertError(err)
	}
	return sourcecontrol.ActionResult{RequestID: result.RequestID, RepositoryID: result.RepositoryID, Revision: result.Revision, AffectedPaths: result.AffectedPaths, TrashIDs: result.TrashIDs}, nil
}

func (p *Provider) Subscribe(ctx context.Context, workspaceID string) error {
	return p.service.Subscribe(ctx, workspaceID)
}
func (p *Provider) Unsubscribe(workspaceID string) { p.service.Unsubscribe(workspaceID) }
func (p *Provider) InvalidateWorkspace(workspaceID string) {
	p.service.InvalidateWorkspace(workspaceID)
}
func (p *Provider) ResetWorkspace(ctx context.Context, workspaceID string) error {
	return p.service.ResetWorkspace(ctx, workspaceID)
}
func (p *Provider) RemoveWorkspace(workspaceID string) { p.service.RemoveWorkspace(workspaceID) }
func (p *Provider) StopWorkspaceProcesses(workspaceID string) {
	p.service.StopWorkspaceProcesses(workspaceID)
}
func (p *Provider) Close() { p.service.Close() }

func convertError(err error) error {
	if gitError, ok := err.(*gitservice.Error); ok {
		return &sourcecontrol.Error{Code: gitError.Code, Message: gitError.Message, Cause: gitError.Cause, Details: gitError.Details}
	}
	return err
}
