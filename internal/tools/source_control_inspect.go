package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/brent/echo/internal/sourcecontrol"
)

const SourceControlInspectToolName = "source_control_inspect"

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        SourceControlInspectToolName,
			Description: "Inspect a local Git, Fossil, or future source-control repository without changing files, revisions, or remotes. Supports provider-neutral status, history, revision details, per-file diffs, and annotations.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"operation", "repository"},
				"properties": map[string]any{
					"operation":  map[string]any{"type": "string", "enum": []any{"status", "history", "show", "diff", "annotate"}},
					"repository": map[string]any{"type": "string", "description": "Opaque repository ID or visible repository/workspace-folder label."},
					"provider":   map[string]any{"type": "string", "description": "Optional provider ID, such as git or fossil, used to disambiguate repositories at the same root."},
					"path":       map[string]any{"type": "string", "description": "Repository-relative file path. Required for diff and annotate; optional for show."},
					"revision":   map[string]any{"type": "string", "description": "Revision for show or annotate."},
					"comparison": map[string]any{"type": "string", "description": "For diff: working_tree, included, revision, revisions, or revision_to_worktree.", "enum": []any{"working_tree", "included", "revision", "revisions", "revision_to_worktree"}},
					"base":       map[string]any{"type": "string", "description": "Base revision for revisions or revision_to_worktree diff."},
					"target":     map[string]any{"type": "string", "description": "Target revision for revisions diff."},
					"offset":     map[string]any{"type": "integer", "minimum": 0, "description": "Number of history entries to skip."},
					"maxResults": map[string]any{"type": "integer", "minimum": 1, "maximum": sourcecontrol.HistoryPageSize, "description": "Maximum history entries to return."},
					"startLine":  map[string]any{"type": "integer", "minimum": 1, "description": "First 1-based line for annotate."},
					"endLine":    map[string]any{"type": "integer", "minimum": 1, "description": "Last 1-based line for annotate; at most 500 lines are returned."},
				},
			},
		},
		Run: executeSourceControlInspect,
	})
}

type sourceControlInspectArgs struct {
	Operation  string `json:"operation"`
	Repository string `json:"repository"`
	Provider   string `json:"provider"`
	Path       string `json:"path"`
	Revision   string `json:"revision"`
	Comparison string `json:"comparison"`
	Base       string `json:"base"`
	Target     string `json:"target"`
	Offset     int    `json:"offset"`
	MaxResults int    `json:"maxResults"`
	StartLine  int    `json:"startLine"`
	EndLine    int    `json:"endLine"`
}

type sourceControlInspectOutput struct {
	Operation  string                        `json:"operation"`
	Provider   string                        `json:"provider"`
	Repository sourcecontrol.Repository      `json:"repository"`
	Status     *sourcecontrol.StatusSnapshot `json:"status,omitempty"`
	History    *sourcecontrol.History        `json:"history,omitempty"`
	Detail     *sourcecontrol.RevisionDetail `json:"detail,omitempty"`
	Diff       *sourcecontrol.DiffDocument   `json:"diff,omitempty"`
	Annotation *sourcecontrol.Annotation     `json:"annotation,omitempty"`
}

func executeSourceControlInspect(execution ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := execution.context().Err(); err != nil {
		return nil, err
	}
	if execution.SourceControl == nil {
		return nil, SafeError{Code: "source_control_unavailable", Message: "source control inspection is unavailable in this context"}
	}
	var args sourceControlInspectArgs
	if err := DecodeToolArguments(arguments, &args); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	args.Operation = strings.ToLower(strings.TrimSpace(args.Operation))
	args.Repository = strings.TrimSpace(args.Repository)
	args.Provider = strings.ToLower(strings.TrimSpace(args.Provider))
	args.Path = strings.TrimSpace(strings.ReplaceAll(args.Path, "\\", "/"))
	if err := validateSourceControlInspectArgs(args); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(execution.context(), 45*time.Second)
	defer cancel()
	repositories, err := execution.SourceControl.Repositories(ctx, execution.WorkspaceID)
	if err != nil {
		return nil, safeSourceControlInspectError(err)
	}
	repository, err := selectSourceControlRepository(repositories, args.Repository, args.Provider)
	if err != nil {
		return nil, err
	}
	if !repository.Available {
		message := strings.TrimSpace(repository.Diagnostic)
		if message == "" {
			message = repository.ProviderLabel + " repository is unavailable"
		}
		return nil, SafeError{Code: "source_control_unavailable", Message: message}
	}
	result := sourceControlInspectOutput{Operation: args.Operation, Provider: repository.ProviderID, Repository: repository}
	switch args.Operation {
	case "status":
		status, statusErr := execution.SourceControl.Status(ctx, execution.WorkspaceID, repository.ID)
		if statusErr != nil {
			return nil, safeSourceControlInspectError(statusErr)
		}
		result.Status = &status
	case "history":
		limit := args.MaxResults
		if limit <= 0 {
			limit = 20
		}
		history, historyErr := execution.SourceControl.History(ctx, execution.WorkspaceID, repository.ID, args.Offset, limit)
		if historyErr != nil {
			return nil, safeSourceControlInspectError(historyErr)
		}
		result.History = &history
	case "show":
		detail, detailErr := execution.SourceControl.RevisionDetail(ctx, execution.WorkspaceID, repository.ID, strings.TrimSpace(args.Revision), "commit")
		if detailErr != nil {
			return nil, safeSourceControlInspectError(detailErr)
		}
		result.Detail = &detail
		if args.Path != "" {
			diff, diffErr := execution.SourceControl.Diff(ctx, execution.WorkspaceID, repository.ID, sourcecontrol.DiffTarget{Kind: "revision", Path: args.Path, Ref: args.Revision})
			if diffErr != nil {
				return nil, safeSourceControlInspectError(diffErr)
			}
			result.Diff = &diff
		}
	case "diff":
		target, targetErr := sourceControlInspectionDiffTarget(repository.ProviderID, args)
		if targetErr != nil {
			return nil, targetErr
		}
		diff, diffErr := execution.SourceControl.Diff(ctx, execution.WorkspaceID, repository.ID, target)
		if diffErr != nil {
			return nil, safeSourceControlInspectError(diffErr)
		}
		result.Diff = &diff
	case "annotate":
		annotation, annotationErr := execution.SourceControl.Annotate(ctx, execution.WorkspaceID, repository.ID, args.Path, args.Revision, args.StartLine, args.EndLine)
		if annotationErr != nil {
			return nil, safeSourceControlInspectError(annotationErr)
		}
		result.Annotation = &annotation
	}
	return result, nil
}

func validateSourceControlInspectArgs(args sourceControlInspectArgs) error {
	invalid := func(message string) error { return SafeError{Code: "invalid_arguments", Message: message} }
	if args.Repository == "" {
		return invalid("repository is required")
	}
	switch args.Operation {
	case "status":
		if args.Path != "" || args.Revision != "" || args.Comparison != "" || args.Base != "" || args.Target != "" || args.Offset != 0 || args.MaxResults != 0 || args.StartLine != 0 || args.EndLine != 0 {
			return invalid("status only accepts operation, repository, and provider")
		}
	case "history":
		if args.Path != "" || args.Revision != "" || args.Comparison != "" || args.Base != "" || args.Target != "" || args.StartLine != 0 || args.EndLine != 0 {
			return invalid("history received arguments belonging to another operation")
		}
		if args.Offset < 0 || args.MaxResults < 0 || args.MaxResults > sourcecontrol.HistoryPageSize {
			return invalid("history pagination is out of range")
		}
	case "show":
		if strings.TrimSpace(args.Revision) == "" {
			return invalid("revision is required for show")
		}
		if args.Comparison != "" || args.Base != "" || args.Target != "" || args.Offset != 0 || args.MaxResults != 0 || args.StartLine != 0 || args.EndLine != 0 {
			return invalid("show received arguments belonging to another operation")
		}
	case "diff":
		if args.Path == "" {
			return invalid("path is required for diff")
		}
		if args.Revision != "" || args.Offset != 0 || args.MaxResults != 0 || args.StartLine != 0 || args.EndLine != 0 {
			return invalid("diff received arguments belonging to another operation")
		}
	case "annotate":
		if args.Path == "" {
			return invalid("path is required for annotate")
		}
		if args.Comparison != "" || args.Base != "" || args.Target != "" || args.Offset != 0 || args.MaxResults != 0 {
			return invalid("annotate received arguments belonging to another operation")
		}
		if args.StartLine < 0 || args.EndLine < 0 || (args.EndLine > 0 && args.EndLine < args.StartLine) {
			return invalid("annotate line range is invalid")
		}
	default:
		return invalid("operation must be status, history, show, diff, or annotate")
	}
	return nil
}

func selectSourceControlRepository(repositories []sourcecontrol.Repository, requested, providerID string) (sourcecontrol.Repository, error) {
	matches := make([]sourcecontrol.Repository, 0, 2)
	for _, repository := range repositories {
		if providerID != "" && !strings.EqualFold(repository.ProviderID, providerID) {
			continue
		}
		matched := repository.ID == requested || strings.EqualFold(repository.Label, requested)
		if !matched {
			for _, scope := range repository.Scopes {
				if strings.EqualFold(scope.RootLabel, requested) && scope.RepoPrefix == "" {
					matched = true
					break
				}
			}
		}
		if matched {
			matches = append(matches, repository)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) == 0 {
		return sourcecontrol.Repository{}, SafeError{Code: "repository_not_found", Message: fmt.Sprintf("source control repository %q was not found", requested)}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ProviderID < matches[j].ProviderID })
	labels := make([]string, 0, len(matches))
	for _, repository := range matches {
		labels = append(labels, repository.Label+" ("+repository.ProviderID+")")
	}
	return sourcecontrol.Repository{}, SafeError{Code: "ambiguous_repository", Message: "repository matches multiple providers; pass provider: " + strings.Join(labels, ", ")}
}

func sourceControlInspectionDiffTarget(providerID string, args sourceControlInspectArgs) (sourcecontrol.DiffTarget, error) {
	comparison := strings.ToLower(strings.TrimSpace(args.Comparison))
	if comparison == "" {
		comparison = "working_tree"
	}
	target := sourcecontrol.DiffTarget{Path: args.Path}
	switch comparison {
	case "working_tree":
		if args.Base != "" || args.Target != "" {
			return target, SafeError{Code: "invalid_arguments", Message: "working_tree diff does not accept base or target"}
		}
		target.Kind, target.GroupID = "change", "working"
	case "included":
		if providerID != "git" {
			return target, SafeError{Code: "unsupported_source_control_capability", Message: "this provider does not have an included/staging area"}
		}
		if args.Base != "" || args.Target != "" {
			return target, SafeError{Code: "invalid_arguments", Message: "included diff does not accept base or target"}
		}
		target.Kind, target.GroupID = "change", "staged"
	case "revision":
		if strings.TrimSpace(args.Target) == "" || args.Base != "" {
			return target, SafeError{Code: "invalid_arguments", Message: "revision diff requires target and does not accept base"}
		}
		target.Kind, target.Ref = "revision", args.Target
	case "revisions":
		if strings.TrimSpace(args.Base) == "" || strings.TrimSpace(args.Target) == "" {
			return target, SafeError{Code: "invalid_arguments", Message: "revisions diff requires base and target"}
		}
		target.Kind, target.BaseRef, target.Ref = "revisions", args.Base, args.Target
	case "revision_to_worktree":
		if strings.TrimSpace(args.Base) == "" || args.Target != "" {
			return target, SafeError{Code: "invalid_arguments", Message: "revision_to_worktree diff requires base and does not accept target"}
		}
		target.Kind, target.BaseRef = "revision_to_worktree", args.Base
	default:
		return target, SafeError{Code: "invalid_arguments", Message: "comparison must be working_tree, included, revision, revisions, or revision_to_worktree"}
	}
	return target, nil
}

func safeSourceControlInspectError(err error) error {
	var sourceError *sourcecontrol.Error
	if errors.As(err, &sourceError) {
		return SafeError{Code: sourceError.Code, Message: sourceError.Message}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return SafeError{Code: "source_control_timeout", Message: "source control inspection timed out"}
	}
	return SafeError{Code: "source_control_failed", Message: "source control inspection failed"}
}
