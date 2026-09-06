package tools

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/brent/echo/internal/sourcecontrol"
)

const FossilInspectToolName = "fossil_inspect"

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        FossilInspectToolName,
			Description: "Inspect a local Fossil repository without changing its checkout, checkpoints, stashes, or network state. Supports status, repository-wide history, native Fossil content search, revision details, bounded patches, and blame.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"operation", "repository"},
				"properties": map[string]any{
					"operation": map[string]any{
						"type": "string", "description": "Read-only Fossil inspection operation.",
						"enum": []any{"status", "log", "search", "show", "diff", "blame"},
					},
					"repository": map[string]any{
						"type": "string", "description": "Opaque Fossil repository ID or visible repository/workspace-folder label.",
					},
					"revision": map[string]any{
						"type": "string", "description": "Check-in for log ancestry, show, or blame. Blame defaults to current.",
					},
					"branch": map[string]any{
						"type": "string", "description": "For log, restrict history to this Fossil branch. Cannot be combined with revision.",
					},
					"query": map[string]any{
						"type": "string", "description": "Case-insensitive fixed text for log comments, or the required native Fossil search query.",
					},
					"scopes": map[string]any{
						"type": "array", "description": "For search, Fossil content classes. Defaults to checkins.",
						"items": map[string]any{"type": "string", "enum": []any{"checkins", "docs", "forum", "tickets", "technotes", "wiki", "help", "all"}},
					},
					"author": map[string]any{
						"type": "string", "description": "For log, restrict check-ins to this Fossil user.",
					},
					"since": map[string]any{
						"type": "string", "description": "For log, inclusive lower date bound as RFC 3339 or YYYY-MM-DD.",
					},
					"until": map[string]any{
						"type": "string", "description": "For log, inclusive upper date bound as RFC 3339 or YYYY-MM-DD.",
					},
					"path": map[string]any{
						"type": "string", "description": "Optional repository-relative path for log, show, or diff; required for blame.",
					},
					"skip": map[string]any{
						"type": "integer", "description": "For log, number of matching check-ins to skip.", "minimum": 0,
					},
					"maxResults": map[string]any{
						"type": "integer", "description": "Maximum log or search results. Defaults to 20 and is capped at 100.", "minimum": 1, "maximum": 100,
					},
					"includePatch": map[string]any{
						"type": "boolean", "description": "For show or diff, include a bounded unified patch. Defaults to true.",
					},
					"maxOutputBytes": map[string]any{
						"type": "integer", "description": "Maximum patch or search output bytes. Defaults to 65536 and is capped at 262144.", "minimum": 1, "maximum": sourcecontrol.InspectionMaximumOutputMax,
					},
					"comparison": map[string]any{
						"type": "string", "description": "For diff: working_tree, protected, revisions, or revision_to_worktree.",
						"enum": []any{"working_tree", "protected", "revisions", "revision_to_worktree"},
					},
					"base": map[string]any{
						"type": "string", "description": "Base check-in for revisions or revision_to_worktree diff.",
					},
					"target": map[string]any{
						"type": "string", "description": "Target check-in for revisions diff.",
					},
					"contextLines": map[string]any{
						"type": "integer", "description": "Patch context lines for show or diff. Defaults to 3 and is capped at 20.", "minimum": 0, "maximum": 20,
					},
					"startLine": map[string]any{
						"type": "integer", "description": "First 1-based line for blame. Defaults to 1.", "minimum": 1,
					},
					"endLine": map[string]any{
						"type": "integer", "description": "Last 1-based line for blame. At most 500 lines are returned.", "minimum": 1,
					},
				},
			},
		},
		Run: executeFossilInspect,
	})
}

type fossilInspectArgs struct {
	Operation      string   `json:"operation"`
	Repository     string   `json:"repository"`
	Revision       string   `json:"revision"`
	Branch         string   `json:"branch"`
	Query          string   `json:"query"`
	Scopes         []string `json:"scopes"`
	Author         string   `json:"author"`
	Since          string   `json:"since"`
	Until          string   `json:"until"`
	Path           string   `json:"path"`
	Skip           int      `json:"skip"`
	MaxResults     int      `json:"maxResults"`
	IncludePatch   *bool    `json:"includePatch"`
	MaxOutputBytes int      `json:"maxOutputBytes"`
	Comparison     string   `json:"comparison"`
	Base           string   `json:"base"`
	Target         string   `json:"target"`
	ContextLines   *int     `json:"contextLines"`
	StartLine      int      `json:"startLine"`
	EndLine        int      `json:"endLine"`
}

type fossilInspectOutput struct {
	Operation  string                                `json:"operation"`
	Provider   string                                `json:"provider"`
	Repository sourcecontrol.Repository              `json:"repository"`
	Status     *sourcecontrol.StatusSnapshot         `json:"status,omitempty"`
	History    *sourcecontrol.History                `json:"history,omitempty"`
	Search     *sourcecontrol.RepositorySearchResult `json:"search,omitempty"`
	Detail     *sourcecontrol.RevisionDetail         `json:"detail,omitempty"`
	Diff       *sourcecontrol.PatchResult            `json:"diff,omitempty"`
	Annotation *sourcecontrol.Annotation             `json:"annotation,omitempty"`
}

func executeFossilInspect(execution ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := execution.context().Err(); err != nil {
		return nil, err
	}
	if execution.SourceControl == nil {
		return nil, SafeError{Code: "source_control_unavailable", Message: "source control inspection is unavailable in this context"}
	}
	advanced, ok := execution.SourceControl.(SourceControlAdvancedInspector)
	if !ok {
		return nil, SafeError{Code: "unsupported_source_control_capability", Message: "advanced Fossil inspection is unavailable in this context"}
	}
	var args fossilInspectArgs
	if err := DecodeToolArguments(arguments, &args); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	normalizeFossilInspectArgs(&args)
	if err := validateFossilInspectArgs(args); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(execution.context(), 45*time.Second)
	defer cancel()
	repositories, err := execution.SourceControl.Repositories(ctx, execution.WorkspaceID)
	if err != nil {
		return nil, safeSourceControlInspectError(err)
	}
	repository, err := selectSourceControlRepository(repositories, args.Repository, "fossil")
	if err != nil {
		return nil, err
	}
	if !repository.Available {
		message := strings.TrimSpace(repository.Diagnostic)
		if message == "" {
			message = "Fossil repository is unavailable"
		}
		return nil, SafeError{Code: "source_control_unavailable", Message: message}
	}

	result := fossilInspectOutput{Operation: args.Operation, Provider: "fossil", Repository: repository}
	switch args.Operation {
	case "status":
		status, callErr := execution.SourceControl.Status(ctx, execution.WorkspaceID, repository.ID)
		if callErr != nil {
			return nil, safeSourceControlInspectError(callErr)
		}
		result.Status = &status
	case "log":
		if args.MaxResults == 0 {
			args.MaxResults = 20
		}
		history, callErr := advanced.QueryHistory(ctx, execution.WorkspaceID, repository.ID, sourcecontrol.HistoryQuery{
			Offset: args.Skip, Limit: args.MaxResults, Revision: args.Revision, Branch: args.Branch,
			Path: args.Path, Query: args.Query, Author: args.Author, Since: args.Since, Until: args.Until,
		})
		if callErr != nil {
			return nil, safeSourceControlInspectError(callErr)
		}
		result.History = &history
	case "search":
		if args.MaxResults == 0 {
			args.MaxResults = 20
		}
		if len(args.Scopes) == 0 {
			args.Scopes = []string{"checkins"}
		}
		if args.MaxOutputBytes == 0 {
			args.MaxOutputBytes = sourcecontrol.InspectionDefaultOutputMax
		}
		search, callErr := advanced.Search(ctx, execution.WorkspaceID, repository.ID, sourcecontrol.RepositorySearchRequest{
			Query: args.Query, Scopes: args.Scopes, Limit: args.MaxResults, MaxOutputBytes: args.MaxOutputBytes,
		})
		if callErr != nil {
			return nil, safeSourceControlInspectError(callErr)
		}
		result.Search = &search
	case "show":
		detail, callErr := execution.SourceControl.RevisionDetail(ctx, execution.WorkspaceID, repository.ID, args.Revision, "commit")
		if callErr != nil {
			return nil, safeSourceControlInspectError(callErr)
		}
		result.Detail = &detail
		patch, callErr := advanced.Patch(ctx, execution.WorkspaceID, repository.ID, fossilPatchRequest(args, "revision", "", args.Revision))
		if callErr != nil {
			return nil, safeSourceControlInspectError(callErr)
		}
		result.Diff = &patch
	case "diff":
		patch, callErr := advanced.Patch(ctx, execution.WorkspaceID, repository.ID, fossilPatchRequest(args, args.Comparison, args.Base, args.Target))
		if callErr != nil {
			return nil, safeSourceControlInspectError(callErr)
		}
		result.Diff = &patch
	case "blame":
		annotation, callErr := execution.SourceControl.Annotate(ctx, execution.WorkspaceID, repository.ID, args.Path, args.Revision, args.StartLine, args.EndLine)
		if callErr != nil {
			return nil, safeSourceControlInspectError(callErr)
		}
		result.Annotation = &annotation
	}
	return result, nil
}

func normalizeFossilInspectArgs(args *fossilInspectArgs) {
	args.Operation = strings.ToLower(strings.TrimSpace(args.Operation))
	args.Repository = strings.TrimSpace(args.Repository)
	args.Revision = strings.TrimSpace(args.Revision)
	args.Branch = strings.TrimSpace(args.Branch)
	args.Query = strings.TrimSpace(args.Query)
	args.Author = strings.TrimSpace(args.Author)
	args.Since = strings.TrimSpace(args.Since)
	args.Until = strings.TrimSpace(args.Until)
	args.Path = strings.TrimSpace(strings.ReplaceAll(args.Path, "\\", "/"))
	args.Comparison = strings.ToLower(strings.TrimSpace(args.Comparison))
	if args.Operation == "diff" && args.Comparison == "" {
		args.Comparison = "working_tree"
	}
	args.Base = strings.TrimSpace(args.Base)
	args.Target = strings.TrimSpace(args.Target)
	for index := range args.Scopes {
		args.Scopes[index] = strings.ToLower(strings.TrimSpace(args.Scopes[index]))
	}
}

func validateFossilInspectArgs(args fossilInspectArgs) error {
	invalid := func(message string) error { return SafeError{Code: "invalid_arguments", Message: message} }
	if args.Repository == "" {
		return invalid("repository is required")
	}
	if args.Skip < 0 || args.MaxResults < 0 || args.MaxResults > sourcecontrol.HistoryPageSize {
		return invalid("result pagination is out of range")
	}
	if args.MaxOutputBytes < 0 || args.MaxOutputBytes > sourcecontrol.InspectionMaximumOutputMax {
		return invalid("maxOutputBytes is out of range")
	}
	if args.ContextLines != nil && (*args.ContextLines < 0 || *args.ContextLines > 20) {
		return invalid("contextLines is out of range")
	}
	if args.StartLine < 0 || args.EndLine < 0 || (args.EndLine > 0 && args.EndLine < args.StartLine) {
		return invalid("blame line range is invalid")
	}

	commonPatchFields := args.IncludePatch != nil || args.MaxOutputBytes != 0 || args.ContextLines != nil
	switch args.Operation {
	case "status":
		if args.Revision != "" || args.Branch != "" || args.Query != "" || len(args.Scopes) != 0 || args.Author != "" || args.Since != "" || args.Until != "" || args.Path != "" || args.Skip != 0 || args.MaxResults != 0 || commonPatchFields || args.Comparison != "" || args.Base != "" || args.Target != "" || args.StartLine != 0 || args.EndLine != 0 {
			return invalid("status only accepts operation and repository")
		}
	case "log":
		if args.Revision != "" && args.Branch != "" {
			return invalid("revision and branch cannot be combined for log")
		}
		if len(args.Scopes) != 0 || commonPatchFields || args.Comparison != "" || args.Base != "" || args.Target != "" || args.StartLine != 0 || args.EndLine != 0 {
			return invalid("log received arguments belonging to another operation")
		}
	case "search":
		if args.Query == "" {
			return invalid("query is required for search")
		}
		if args.Revision != "" || args.Branch != "" || args.Author != "" || args.Since != "" || args.Until != "" || args.Path != "" || args.Skip != 0 || args.IncludePatch != nil || args.ContextLines != nil || args.Comparison != "" || args.Base != "" || args.Target != "" || args.StartLine != 0 || args.EndLine != 0 {
			return invalid("search received arguments belonging to another operation")
		}
		validScopes := map[string]bool{"checkins": true, "docs": true, "forum": true, "tickets": true, "technotes": true, "wiki": true, "help": true, "all": true}
		seenAll := false
		for _, scope := range args.Scopes {
			if !validScopes[scope] {
				return invalid("search scope is invalid")
			}
			seenAll = seenAll || scope == "all"
		}
		if seenAll && len(args.Scopes) > 1 {
			return invalid("the all search scope cannot be combined with other scopes")
		}
	case "show":
		if args.Revision == "" {
			return invalid("revision is required for show")
		}
		if args.Branch != "" || args.Query != "" || len(args.Scopes) != 0 || args.Author != "" || args.Since != "" || args.Until != "" || args.Skip != 0 || args.MaxResults != 0 || args.Comparison != "" || args.Base != "" || args.Target != "" || args.StartLine != 0 || args.EndLine != 0 {
			return invalid("show received arguments belonging to another operation")
		}
	case "diff":
		if args.Revision != "" || args.Branch != "" || args.Query != "" || len(args.Scopes) != 0 || args.Author != "" || args.Since != "" || args.Until != "" || args.Skip != 0 || args.MaxResults != 0 || args.StartLine != 0 || args.EndLine != 0 {
			return invalid("diff received arguments belonging to another operation")
		}
		switch args.Comparison {
		case "working_tree", "protected":
			if args.Base != "" || args.Target != "" {
				return invalid(args.Comparison + " diff does not accept base or target")
			}
		case "revisions":
			if args.Base == "" || args.Target == "" {
				return invalid("revisions diff requires base and target")
			}
		case "revision_to_worktree":
			if args.Base == "" || args.Target != "" {
				return invalid("revision_to_worktree diff requires base and does not accept target")
			}
		default:
			return invalid("comparison must be working_tree, protected, revisions, or revision_to_worktree")
		}
	case "blame":
		if args.Path == "" {
			return invalid("path is required for blame")
		}
		if args.Branch != "" || args.Query != "" || len(args.Scopes) != 0 || args.Author != "" || args.Since != "" || args.Until != "" || args.Skip != 0 || args.MaxResults != 0 || commonPatchFields || args.Comparison != "" || args.Base != "" || args.Target != "" {
			return invalid("blame received arguments belonging to another operation")
		}
	default:
		return invalid("operation must be status, log, search, show, diff, or blame")
	}
	return nil
}

func fossilPatchRequest(args fossilInspectArgs, comparison, base, target string) sourcecontrol.PatchRequest {
	contextLines := 3
	if args.ContextLines != nil {
		contextLines = *args.ContextLines
	}
	includePatch := args.IncludePatch == nil || *args.IncludePatch
	maxOutputBytes := args.MaxOutputBytes
	if maxOutputBytes == 0 {
		maxOutputBytes = sourcecontrol.InspectionDefaultOutputMax
	}
	return sourcecontrol.PatchRequest{
		Comparison: comparison, Path: args.Path, BaseRef: base, Ref: target,
		ContextLines: contextLines, IncludePatch: includePatch, MaxOutputBytes: maxOutputBytes,
	}
}
