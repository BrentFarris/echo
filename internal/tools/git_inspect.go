package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	gitInspectTimeout          = 15 * time.Second
	gitInspectDefaultLogLimit  = 20
	gitInspectMaxLogLimit      = 100
	gitInspectMaxFiles         = 500
	gitInspectDefaultBlameLine = 200
	gitInspectMaxBlameLines    = 500
	gitInspectDefaultPatchSize = 64 * 1024
	gitInspectMaxPatchSize     = 256 * 1024
	gitInspectMetadataSize     = 2 * 1024 * 1024
	gitInspectMaxMessageSize   = 64 * 1024
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "git_inspect",
			Description: "Inspect local Git repository status and history without changing files, the index, refs, or remotes. Use it to research regressions, commit rationale, ownership, and how code evolved.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"operation", "repository"},
				"properties": map[string]any{
					"operation": map[string]any{
						"type":        "string",
						"description": "Inspection operation.",
						"enum":        []any{"status", "log", "show", "diff", "blame"},
					},
					"repository": map[string]any{
						"type":        "string",
						"description": "Workspace folder label whose folder is the Git repository root, for example echo.",
					},
					"revision": map[string]any{
						"type":        "string",
						"description": "Revision for log, show, or blame. Log and blame default to HEAD; show requires it.",
					},
					"allRefs": map[string]any{
						"type":        "boolean",
						"description": "For log, search every locally available ref instead of HEAD history. Cannot be combined with revision.",
					},
					"query": map[string]any{
						"type":        "string",
						"description": "For log, case-insensitive fixed text to find in complete commit messages.",
					},
					"author": map[string]any{
						"type":        "string",
						"description": "For log, Git author-pattern filter.",
					},
					"since": map[string]any{
						"type":        "string",
						"description": "For log, commits after this Git-compatible date.",
					},
					"until": map[string]any{
						"type":        "string",
						"description": "For log, commits before this Git-compatible date.",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Optional labeled workspace path for log, show, or diff; required for blame. " + labeledPathSchemaHint,
					},
					"skip": map[string]any{
						"type":        "integer",
						"description": "For log pagination, number of matching commits to skip.",
						"minimum":     0,
					},
					"maxResults": map[string]any{
						"type":        "integer",
						"description": "For log, maximum commits to return. Defaults to 20 and is capped at 100.",
						"minimum":     1,
						"maximum":     gitInspectMaxLogLimit,
					},
					"includePatch": map[string]any{
						"type":        "boolean",
						"description": "For show or diff, include the patch. Defaults to true.",
					},
					"maxOutputBytes": map[string]any{
						"type":        "integer",
						"description": "Maximum patch bytes for show or diff. Defaults to 65536 and is capped at 262144.",
						"minimum":     1,
						"maximum":     gitInspectMaxPatchSize,
					},
					"comparison": map[string]any{
						"type":        "string",
						"description": "For diff: working_tree (unstaged), staged, revisions, or revision_to_worktree.",
						"enum":        []any{"working_tree", "staged", "revisions", "revision_to_worktree"},
					},
					"base": map[string]any{
						"type":        "string",
						"description": "Base revision for revisions or revision_to_worktree diff.",
					},
					"target": map[string]any{
						"type":        "string",
						"description": "Target revision for revisions diff.",
					},
					"contextLines": map[string]any{
						"type":        "integer",
						"description": "Patch context lines for show or diff. Defaults to 3 and is capped at 20.",
						"minimum":     0,
						"maximum":     20,
					},
					"startLine": map[string]any{
						"type":        "integer",
						"description": "First 1-based line for blame. Defaults to 1.",
						"minimum":     1,
					},
					"endLine": map[string]any{
						"type":        "integer",
						"description": "Last 1-based line for blame. At most 500 lines are returned.",
						"minimum":     1,
					},
				},
			},
		},
		Run: executeGitInspect,
	})
}

type gitInspectArgs struct {
	Operation      string `json:"operation"`
	Repository     string `json:"repository"`
	Revision       string `json:"revision"`
	AllRefs        bool   `json:"allRefs"`
	Query          string `json:"query"`
	Author         string `json:"author"`
	Since          string `json:"since"`
	Until          string `json:"until"`
	Path           string `json:"path"`
	Skip           int    `json:"skip"`
	MaxResults     int    `json:"maxResults"`
	IncludePatch   *bool  `json:"includePatch"`
	MaxOutputBytes int    `json:"maxOutputBytes"`
	Comparison     string `json:"comparison"`
	Base           string `json:"base"`
	Target         string `json:"target"`
	ContextLines   *int   `json:"contextLines"`
	StartLine      int    `json:"startLine"`
	EndLine        int    `json:"endLine"`
}

type gitInspectRepository struct {
	Label string
	Path  string
}

type gitInspectIdentity struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
	Date  string `json:"date,omitempty"`
}

type gitInspectCommit struct {
	Hash             string             `json:"hash"`
	ShortHash        string             `json:"shortHash"`
	Parents          []string           `json:"parents,omitempty"`
	Subject          string             `json:"subject"`
	Body             string             `json:"body,omitempty"`
	Author           gitInspectIdentity `json:"author"`
	Committer        gitInspectIdentity `json:"committer"`
	MessageTruncated bool               `json:"messageTruncated,omitempty"`
}

type gitInspectFile struct {
	Path           string `json:"path"`
	OldPath        string `json:"oldPath,omitempty"`
	Status         string `json:"status"`
	Operation      string `json:"operation"`
	IndexStatus    string `json:"indexStatus,omitempty"`
	WorktreeStatus string `json:"worktreeStatus,omitempty"`
}

type gitInspectStatusOutput struct {
	Operation      string           `json:"operation"`
	Repository     string           `json:"repository"`
	Branch         string           `json:"branch,omitempty"`
	Detached       bool             `json:"detached"`
	Head           string           `json:"head,omitempty"`
	ShortHead      string           `json:"shortHead,omitempty"`
	Upstream       string           `json:"upstream,omitempty"`
	AheadCount     int              `json:"aheadCount"`
	BehindCount    int              `json:"behindCount"`
	Dirty          bool             `json:"dirty"`
	FileCount      int              `json:"fileCount"`
	Files          []gitInspectFile `json:"files"`
	FilesTruncated bool             `json:"filesTruncated,omitempty"`
}

type gitInspectLogOutput struct {
	Operation  string             `json:"operation"`
	Repository string             `json:"repository"`
	Revision   string             `json:"revision,omitempty"`
	AllRefs    bool               `json:"allRefs"`
	Skip       int                `json:"skip"`
	Commits    []gitInspectCommit `json:"commits"`
	HasMore    bool               `json:"hasMore"`
	NextSkip   int                `json:"nextSkip,omitempty"`
	Truncated  bool               `json:"truncated,omitempty"`
}

type gitInspectShowOutput struct {
	Operation      string           `json:"operation"`
	Repository     string           `json:"repository"`
	Commit         gitInspectCommit `json:"commit"`
	FirstParent    string           `json:"firstParent,omitempty"`
	FileCount      int              `json:"fileCount"`
	Files          []gitInspectFile `json:"files"`
	FilesTruncated bool             `json:"filesTruncated,omitempty"`
	Statistics     string           `json:"statistics,omitempty"`
	Patch          string           `json:"patch,omitempty"`
	PatchTruncated bool             `json:"patchTruncated,omitempty"`
}

type gitInspectDiffOutput struct {
	Operation      string           `json:"operation"`
	Repository     string           `json:"repository"`
	Comparison     string           `json:"comparison"`
	Base           string           `json:"base,omitempty"`
	Target         string           `json:"target,omitempty"`
	FileCount      int              `json:"fileCount"`
	Files          []gitInspectFile `json:"files"`
	FilesTruncated bool             `json:"filesTruncated,omitempty"`
	Statistics     string           `json:"statistics,omitempty"`
	Patch          string           `json:"patch,omitempty"`
	PatchTruncated bool             `json:"patchTruncated,omitempty"`
}

type gitInspectBlameLine struct {
	Line         int    `json:"line"`
	OriginalLine int    `json:"originalLine"`
	Path         string `json:"path"`
	Hash         string `json:"hash"`
	Author       string `json:"author,omitempty"`
	AuthorEmail  string `json:"authorEmail,omitempty"`
	AuthoredAt   string `json:"authoredAt,omitempty"`
	Subject      string `json:"subject,omitempty"`
	Content      string `json:"content"`
}

type gitInspectBlameOutput struct {
	Operation  string                `json:"operation"`
	Repository string                `json:"repository"`
	Revision   string                `json:"revision"`
	Path       string                `json:"path"`
	StartLine  int                   `json:"startLine"`
	EndLine    int                   `json:"endLine"`
	Lines      []gitInspectBlameLine `json:"lines"`
	Truncated  bool                  `json:"truncated,omitempty"`
}

func executeGitInspect(execution ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := execution.context().Err(); err != nil {
		return nil, err
	}
	var args gitInspectArgs
	if err := DecodeToolArguments(arguments, &args); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	args.Operation = strings.ToLower(strings.TrimSpace(args.Operation))
	switch args.Operation {
	case "status", "log", "show", "diff", "blame":
	default:
		return nil, SafeError{Code: "invalid_arguments", Message: "operation must be one of status, log, show, diff, or blame"}
	}
	if err := validateGitInspectArguments(args); err != nil {
		return nil, err
	}
	repository, err := resolveGitInspectRepository(execution, args.Repository)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(execution.context(), gitInspectTimeout)
	defer cancel()
	if err := ensureGitInspectRepositoryRoot(ctx, repository); err != nil {
		return nil, err
	}
	path, err := resolveGitInspectPath(execution, repository, args.Path)
	if err != nil {
		return nil, err
	}

	switch args.Operation {
	case "status":
		return gitInspectStatus(ctx, repository, args, path)
	case "log":
		return gitInspectLog(ctx, repository, args, path)
	case "show":
		return gitInspectShow(ctx, repository, args, path)
	case "diff":
		return gitInspectDiff(ctx, repository, args, path)
	default:
		return gitInspectBlame(ctx, repository, args, path)
	}
}

func validateGitInspectArguments(args gitInspectArgs) error {
	invalid := func(message string) error {
		return SafeError{Code: "invalid_arguments", Message: message}
	}
	if args.ContextLines != nil && (*args.ContextLines < 0 || *args.ContextLines > 20) {
		return invalid("contextLines must be between 0 and 20")
	}
	if args.MaxOutputBytes < 0 {
		return invalid("maxOutputBytes cannot be negative")
	}
	switch args.Operation {
	case "status":
		if strings.TrimSpace(args.Revision) != "" || args.AllRefs || strings.TrimSpace(args.Query) != "" ||
			strings.TrimSpace(args.Author) != "" || strings.TrimSpace(args.Since) != "" || strings.TrimSpace(args.Until) != "" ||
			strings.TrimSpace(args.Path) != "" || args.Skip != 0 || args.MaxResults != 0 || args.IncludePatch != nil ||
			args.MaxOutputBytes != 0 || strings.TrimSpace(args.Comparison) != "" || strings.TrimSpace(args.Base) != "" ||
			strings.TrimSpace(args.Target) != "" || args.ContextLines != nil || args.StartLine != 0 || args.EndLine != 0 {
			return invalid("status only accepts operation and repository")
		}
	case "log":
		if args.IncludePatch != nil || args.MaxOutputBytes != 0 || strings.TrimSpace(args.Comparison) != "" ||
			strings.TrimSpace(args.Base) != "" || strings.TrimSpace(args.Target) != "" || args.ContextLines != nil ||
			args.StartLine != 0 || args.EndLine != 0 {
			return invalid("log received arguments belonging to another operation")
		}
	case "show":
		if args.AllRefs || strings.TrimSpace(args.Query) != "" || strings.TrimSpace(args.Author) != "" ||
			strings.TrimSpace(args.Since) != "" || strings.TrimSpace(args.Until) != "" || args.Skip != 0 ||
			args.MaxResults != 0 || strings.TrimSpace(args.Comparison) != "" || strings.TrimSpace(args.Base) != "" ||
			strings.TrimSpace(args.Target) != "" || args.StartLine != 0 || args.EndLine != 0 {
			return invalid("show received arguments belonging to another operation")
		}
	case "diff":
		if strings.TrimSpace(args.Revision) != "" || args.AllRefs || strings.TrimSpace(args.Query) != "" ||
			strings.TrimSpace(args.Author) != "" || strings.TrimSpace(args.Since) != "" || strings.TrimSpace(args.Until) != "" ||
			args.Skip != 0 || args.MaxResults != 0 || args.StartLine != 0 || args.EndLine != 0 {
			return invalid("diff received arguments belonging to another operation")
		}
	case "blame":
		if args.AllRefs || strings.TrimSpace(args.Query) != "" || strings.TrimSpace(args.Author) != "" ||
			strings.TrimSpace(args.Since) != "" || strings.TrimSpace(args.Until) != "" || args.Skip != 0 ||
			args.MaxResults != 0 || args.IncludePatch != nil || args.MaxOutputBytes != 0 ||
			strings.TrimSpace(args.Comparison) != "" || strings.TrimSpace(args.Base) != "" ||
			strings.TrimSpace(args.Target) != "" || args.ContextLines != nil {
			return invalid("blame received arguments belonging to another operation")
		}
	}
	return nil
}

func resolveGitInspectRepository(execution ExecutionContext, requested string) (gitInspectRepository, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return gitInspectRepository{}, SafeError{Code: "invalid_arguments", Message: "repository is required"}
	}
	for _, root := range execution.workspaceRoots() {
		if strings.EqualFold(root.Label, requested) {
			path, err := workspaceRootAbsolutePath(root)
			if err != nil {
				return gitInspectRepository{}, err
			}
			return gitInspectRepository{Label: root.Label, Path: path}, nil
		}
	}
	return gitInspectRepository{}, SafeError{Code: "repository_not_found", Message: fmt.Sprintf("workspace repository %q was not found", requested)}
}

func ensureGitInspectRepositoryRoot(ctx context.Context, repository gitInspectRepository) error {
	output, _, err := runGitInspectCommand(ctx, repository.Path, 64*1024, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	top := strings.TrimSpace(string(output))
	topInfo, topErr := os.Stat(top)
	rootInfo, rootErr := os.Stat(repository.Path)
	if topErr != nil || rootErr != nil || !os.SameFile(topInfo, rootInfo) {
		return SafeError{Code: "repository_not_root", Message: "workspace folder must be the Git repository root"}
	}
	return nil
}

func resolveGitInspectPath(execution ExecutionContext, repository gitInspectRepository, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", nil
	}
	resolved, err := resolveWorkspacePath(execution, requested)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(repository.Path, resolved)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", SafeError{Code: "path_outside_repository", Message: "path must belong to the selected repository"}
	}
	return filepath.ToSlash(relative), nil
}

func gitInspectStatus(ctx context.Context, repository gitInspectRepository, args gitInspectArgs, path string) (any, error) {
	output, outputTruncated, err := runGitInspectCommand(ctx, repository.Path, gitInspectMetadataSize, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	if outputTruncated {
		output = completeGitInspectNULRecords(output)
	}
	files, err := parseGitInspectStatus(output, repository.Label)
	if err != nil {
		return nil, SafeError{Code: "git_parse_failed", Message: err.Error()}
	}
	totalFiles := len(files)
	filesTruncated := outputTruncated || totalFiles > gitInspectMaxFiles
	if filesTruncated {
		files = files[:gitInspectMaxFiles]
	}
	branchOutput, _, _ := runGitInspectCommand(ctx, repository.Path, 4096, "branch", "--show-current")
	branch := strings.TrimSpace(string(branchOutput))
	head, shortHead := gitInspectHead(ctx, repository.Path)
	upstream, ahead, behind := gitInspectUpstream(ctx, repository.Path)
	return gitInspectStatusOutput{
		Operation:      "status",
		Repository:     repository.Label,
		Branch:         branch,
		Detached:       branch == "" && head != "",
		Head:           head,
		ShortHead:      shortHead,
		Upstream:       upstream,
		AheadCount:     ahead,
		BehindCount:    behind,
		Dirty:          totalFiles > 0,
		FileCount:      totalFiles,
		Files:          files,
		FilesTruncated: filesTruncated,
	}, nil
}

func gitInspectLog(ctx context.Context, repository gitInspectRepository, args gitInspectArgs, path string) (any, error) {
	if args.AllRefs && strings.TrimSpace(args.Revision) != "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "allRefs cannot be combined with revision"}
	}
	if strings.TrimSpace(args.Comparison) != "" || strings.TrimSpace(args.Base) != "" || strings.TrimSpace(args.Target) != "" || args.StartLine != 0 || args.EndLine != 0 {
		return nil, SafeError{Code: "invalid_arguments", Message: "log received arguments belonging to another operation"}
	}
	if args.Skip < 0 {
		return nil, SafeError{Code: "invalid_arguments", Message: "skip cannot be negative"}
	}
	limit := args.MaxResults
	if limit <= 0 {
		limit = gitInspectDefaultLogLimit
	}
	if limit > gitInspectMaxLogLimit {
		limit = gitInspectMaxLogLimit
	}
	revision := ""
	if !args.AllRefs {
		revision = strings.TrimSpace(args.Revision)
		if revision == "" {
			revision = "HEAD"
		}
		if strings.EqualFold(revision, "HEAD") && !gitInspectHasHead(ctx, repository.Path) {
			return gitInspectLogOutput{
				Operation:  "log",
				Repository: repository.Label,
				Revision:   revision,
				Skip:       args.Skip,
				Commits:    []gitInspectCommit{},
			}, nil
		}
		canonical, err := canonicalGitInspectRevision(ctx, repository.Path, revision)
		if err != nil {
			return nil, err
		}
		revision = canonical
	}
	command := []string{
		"log",
		fmt.Sprintf("-n%d", limit+1),
		fmt.Sprintf("--skip=%d", args.Skip),
		"--date=iso-strict",
		"--format=%H%x00%h%x00%P%x00%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI%x00%s%x00%b%x1e",
	}
	if args.AllRefs {
		command = append(command, "--all")
	} else {
		command = append(command, revision)
	}
	if query := strings.TrimSpace(args.Query); query != "" {
		command = append(command, "--regexp-ignore-case", "--fixed-strings", "--grep="+query)
	}
	if author := strings.TrimSpace(args.Author); author != "" {
		command = append(command, "--author="+author)
	}
	if since := strings.TrimSpace(args.Since); since != "" {
		command = append(command, "--since="+since)
	}
	if until := strings.TrimSpace(args.Until); until != "" {
		command = append(command, "--until="+until)
	}
	if path != "" {
		command = append(command, "--", path)
	}
	output, outputTruncated, err := runGitInspectCommand(ctx, repository.Path, gitInspectMetadataSize, command...)
	if err != nil {
		if gitInspectNoCommits(err) {
			return gitInspectLogOutput{Operation: "log", Repository: repository.Label, Revision: revision, AllRefs: args.AllRefs, Skip: args.Skip, Commits: []gitInspectCommit{}}, nil
		}
		return nil, err
	}
	commits := parseGitInspectCommits(output)
	hasMore := len(commits) > limit || outputTruncated
	if len(commits) > limit {
		commits = commits[:limit]
	}
	nextSkip := 0
	if hasMore {
		nextSkip = args.Skip + len(commits)
	}
	return gitInspectLogOutput{
		Operation:  "log",
		Repository: repository.Label,
		Revision:   revision,
		AllRefs:    args.AllRefs,
		Skip:       args.Skip,
		Commits:    commits,
		HasMore:    hasMore,
		NextSkip:   nextSkip,
		Truncated:  outputTruncated,
	}, nil
}

func gitInspectShow(ctx context.Context, repository gitInspectRepository, args gitInspectArgs, path string) (any, error) {
	if strings.TrimSpace(args.Revision) == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "revision is required for show"}
	}
	if args.AllRefs || strings.TrimSpace(args.Query) != "" || strings.TrimSpace(args.Author) != "" || strings.TrimSpace(args.Since) != "" || strings.TrimSpace(args.Until) != "" || strings.TrimSpace(args.Comparison) != "" || strings.TrimSpace(args.Base) != "" || strings.TrimSpace(args.Target) != "" || args.StartLine != 0 || args.EndLine != 0 {
		return nil, SafeError{Code: "invalid_arguments", Message: "show received arguments belonging to another operation"}
	}
	revision, err := canonicalGitInspectRevision(ctx, repository.Path, args.Revision)
	if err != nil {
		return nil, err
	}
	commit, err := loadGitInspectCommit(ctx, repository.Path, revision)
	if err != nil {
		return nil, err
	}
	firstParent := ""
	if len(commit.Parents) > 0 {
		firstParent = commit.Parents[0]
	}
	base := gitInspectCommitDiffBase(firstParent, revision)
	files, filesTruncated, totalFiles, err := loadGitInspectChangedFiles(ctx, repository, base, path)
	if err != nil {
		return nil, err
	}
	statistics, _ := loadGitInspectStatistics(ctx, repository.Path, base, path)
	result := gitInspectShowOutput{
		Operation:      "show",
		Repository:     repository.Label,
		Commit:         commit,
		FirstParent:    firstParent,
		FileCount:      totalFiles,
		Files:          files,
		FilesTruncated: filesTruncated,
		Statistics:     statistics,
	}
	if gitInspectPatchEnabled(args) {
		patch, truncated, err := loadGitInspectPatch(ctx, repository.Path, base, path, gitInspectContextLines(args), gitInspectPatchLimit(args))
		if err != nil {
			return nil, err
		}
		result.Patch = patch
		result.PatchTruncated = truncated
	}
	return result, nil
}

type gitInspectDiffBase struct {
	command []string
	base    string
	target  string
}

func gitInspectDiff(ctx context.Context, repository gitInspectRepository, args gitInspectArgs, path string) (any, error) {
	if args.AllRefs || strings.TrimSpace(args.Revision) != "" || strings.TrimSpace(args.Query) != "" || strings.TrimSpace(args.Author) != "" || strings.TrimSpace(args.Since) != "" || strings.TrimSpace(args.Until) != "" || args.StartLine != 0 || args.EndLine != 0 {
		return nil, SafeError{Code: "invalid_arguments", Message: "diff received arguments belonging to another operation"}
	}
	comparison := strings.ToLower(strings.TrimSpace(args.Comparison))
	var base gitInspectDiffBase
	switch comparison {
	case "working_tree":
		if strings.TrimSpace(args.Base) != "" || strings.TrimSpace(args.Target) != "" {
			return nil, SafeError{Code: "invalid_arguments", Message: "working_tree diff does not accept base or target"}
		}
		base.command = []string{"diff"}
	case "staged":
		if strings.TrimSpace(args.Base) != "" || strings.TrimSpace(args.Target) != "" {
			return nil, SafeError{Code: "invalid_arguments", Message: "staged diff does not accept base or target"}
		}
		base.command = []string{"diff", "--cached"}
	case "revisions":
		if strings.TrimSpace(args.Base) == "" || strings.TrimSpace(args.Target) == "" {
			return nil, SafeError{Code: "invalid_arguments", Message: "revisions diff requires base and target"}
		}
		canonicalBase, err := canonicalGitInspectRevision(ctx, repository.Path, args.Base)
		if err != nil {
			return nil, err
		}
		canonicalTarget, err := canonicalGitInspectRevision(ctx, repository.Path, args.Target)
		if err != nil {
			return nil, err
		}
		base = gitInspectDiffBase{command: []string{"diff", canonicalBase, canonicalTarget}, base: canonicalBase, target: canonicalTarget}
	case "revision_to_worktree":
		if strings.TrimSpace(args.Base) == "" || strings.TrimSpace(args.Target) != "" {
			return nil, SafeError{Code: "invalid_arguments", Message: "revision_to_worktree diff requires base and does not accept target"}
		}
		canonicalBase, err := canonicalGitInspectRevision(ctx, repository.Path, args.Base)
		if err != nil {
			return nil, err
		}
		base = gitInspectDiffBase{command: []string{"diff", canonicalBase}, base: canonicalBase}
	default:
		return nil, SafeError{Code: "invalid_arguments", Message: "comparison must be working_tree, staged, revisions, or revision_to_worktree"}
	}
	files, filesTruncated, totalFiles, err := loadGitInspectChangedFiles(ctx, repository, base, path)
	if err != nil {
		return nil, err
	}
	statistics, _ := loadGitInspectStatistics(ctx, repository.Path, base, path)
	result := gitInspectDiffOutput{
		Operation:      "diff",
		Repository:     repository.Label,
		Comparison:     comparison,
		Base:           base.base,
		Target:         base.target,
		FileCount:      totalFiles,
		Files:          files,
		FilesTruncated: filesTruncated,
		Statistics:     statistics,
	}
	if gitInspectPatchEnabled(args) {
		patch, truncated, err := loadGitInspectPatch(ctx, repository.Path, base, path, gitInspectContextLines(args), gitInspectPatchLimit(args))
		if err != nil {
			return nil, err
		}
		result.Patch = patch
		result.PatchTruncated = truncated
	}
	return result, nil
}

func gitInspectBlame(ctx context.Context, repository gitInspectRepository, args gitInspectArgs, path string) (any, error) {
	if path == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "path is required for blame"}
	}
	if args.AllRefs || strings.TrimSpace(args.Query) != "" || strings.TrimSpace(args.Author) != "" || strings.TrimSpace(args.Since) != "" || strings.TrimSpace(args.Until) != "" || strings.TrimSpace(args.Comparison) != "" || strings.TrimSpace(args.Base) != "" || strings.TrimSpace(args.Target) != "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "blame received arguments belonging to another operation"}
	}
	revisionName := strings.TrimSpace(args.Revision)
	if revisionName == "" {
		revisionName = "HEAD"
	}
	revision, err := canonicalGitInspectRevision(ctx, repository.Path, revisionName)
	if err != nil {
		return nil, err
	}
	start := args.StartLine
	if start <= 0 {
		start = 1
	}
	end := args.EndLine
	truncated := false
	probeExtra := end == 0
	if end == 0 {
		end = start + gitInspectDefaultBlameLine
	} else if end < start {
		return nil, SafeError{Code: "invalid_arguments", Message: "endLine must be greater than or equal to startLine"}
	} else if end-start+1 > gitInspectMaxBlameLines {
		end = start + gitInspectMaxBlameLines - 1
		truncated = true
	}
	output, outputTruncated, err := runGitInspectCommand(ctx, repository.Path, gitInspectMetadataSize, "blame", "--line-porcelain", "-L", fmt.Sprintf("%d,%d", start, end), revision, "--", path)
	if err != nil {
		return nil, err
	}
	lines := parseGitInspectBlame(output, repository.Label)
	if probeExtra && len(lines) > gitInspectDefaultBlameLine {
		lines = lines[:gitInspectDefaultBlameLine]
		truncated = true
	}
	if len(lines) > gitInspectMaxBlameLines {
		lines = lines[:gitInspectMaxBlameLines]
		truncated = true
	}
	actualEnd := start - 1
	if len(lines) > 0 {
		actualEnd = lines[len(lines)-1].Line
	}
	return gitInspectBlameOutput{
		Operation:  "blame",
		Repository: repository.Label,
		Revision:   revision,
		Path:       labeledGitInspectPath(repository.Label, path),
		StartLine:  start,
		EndLine:    actualEnd,
		Lines:      lines,
		Truncated:  truncated || outputTruncated,
	}, nil
}

func canonicalGitInspectRevision(ctx context.Context, repositoryPath string, revision string) (string, error) {
	revision = strings.TrimSpace(revision)
	if revision == "" || strings.ContainsAny(revision, "\x00\r\n") {
		return "", SafeError{Code: "invalid_revision", Message: "revision is invalid"}
	}
	output, _, err := runGitInspectCommand(ctx, repositoryPath, 4096, "rev-parse", "--verify", "--end-of-options", revision+"^{commit}")
	if err != nil {
		return "", SafeError{Code: "invalid_revision", Message: fmt.Sprintf("Git revision %q was not found", revision)}
	}
	canonical := strings.TrimSpace(string(output))
	if len(canonical) != 40 && len(canonical) != 64 {
		return "", SafeError{Code: "invalid_revision", Message: fmt.Sprintf("Git revision %q did not resolve to a commit", revision)}
	}
	return canonical, nil
}

func loadGitInspectCommit(ctx context.Context, repositoryPath string, revision string) (gitInspectCommit, error) {
	output, _, err := runGitInspectCommand(ctx, repositoryPath, gitInspectMetadataSize,
		"log", "-1", "--date=iso-strict",
		"--format=%H%x00%h%x00%P%x00%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI%x00%s%x00%b%x1e",
		revision,
	)
	if err != nil {
		return gitInspectCommit{}, err
	}
	commits := parseGitInspectCommits(output)
	if len(commits) == 0 {
		return gitInspectCommit{}, SafeError{Code: "commit_not_found", Message: "commit was not found"}
	}
	return commits[0], nil
}

func parseGitInspectCommits(output []byte) []gitInspectCommit {
	records := strings.Split(string(output), "\x1e")
	commits := make([]gitInspectCommit, 0, len(records))
	for _, record := range records {
		record = strings.Trim(record, "\r\n")
		if record == "" {
			continue
		}
		fields := strings.SplitN(record, "\x00", 11)
		if len(fields) != 11 {
			continue
		}
		subject, subjectTruncated := truncateGitInspectString(fields[9], gitInspectMaxMessageSize)
		body, bodyTruncated := truncateGitInspectString(strings.TrimRight(fields[10], "\r\n"), gitInspectMaxMessageSize)
		commits = append(commits, gitInspectCommit{
			Hash:      fields[0],
			ShortHash: fields[1],
			Parents:   strings.Fields(fields[2]),
			Author: gitInspectIdentity{
				Name: fields[3], Email: fields[4], Date: fields[5],
			},
			Committer: gitInspectIdentity{
				Name: fields[6], Email: fields[7], Date: fields[8],
			},
			Subject:          subject,
			Body:             body,
			MessageTruncated: subjectTruncated || bodyTruncated,
		})
	}
	return commits
}

func gitInspectCommitDiffBase(parent string, revision string) gitInspectDiffBase {
	if parent == "" {
		return gitInspectDiffBase{command: []string{"diff-tree", "--root", "--no-commit-id", "-r", revision}, target: revision}
	}
	return gitInspectDiffBase{command: []string{"diff", parent, revision}, base: parent, target: revision}
}

func loadGitInspectChangedFiles(ctx context.Context, repository gitInspectRepository, base gitInspectDiffBase, path string) ([]gitInspectFile, bool, int, error) {
	command := append([]string{}, base.command...)
	command = append(command, "--name-status", "-z", "--find-renames", "--no-ext-diff", "--no-textconv", "--no-color")
	if path != "" {
		command = append(command, "--", path)
	}
	output, outputTruncated, err := runGitInspectCommand(ctx, repository.Path, gitInspectMetadataSize, command...)
	if err != nil {
		return nil, false, 0, err
	}
	if outputTruncated {
		output = completeGitInspectNULRecords(output)
	}
	files, err := parseGitInspectNameStatus(output, repository.Label)
	if err != nil {
		return nil, false, 0, SafeError{Code: "git_parse_failed", Message: err.Error()}
	}
	total := len(files)
	truncated := outputTruncated || total > gitInspectMaxFiles
	if len(files) > gitInspectMaxFiles {
		files = files[:gitInspectMaxFiles]
	}
	return files, truncated, total, nil
}

func loadGitInspectStatistics(ctx context.Context, repositoryPath string, base gitInspectDiffBase, path string) (string, error) {
	command := append([]string{}, base.command...)
	command = append(command, "--shortstat", "--find-renames", "--no-ext-diff", "--no-textconv", "--no-color")
	if path != "" {
		command = append(command, "--", path)
	}
	output, _, err := runGitInspectCommand(ctx, repositoryPath, 64*1024, command...)
	return strings.TrimSpace(string(output)), err
}

func loadGitInspectPatch(ctx context.Context, repositoryPath string, base gitInspectDiffBase, path string, contextLines int, limit int) (string, bool, error) {
	command := append([]string{}, base.command...)
	command = append(command,
		"--patch",
		"--no-ext-diff",
		"--no-textconv",
		"--no-color",
		"--find-renames",
		fmt.Sprintf("--unified=%d", contextLines),
	)
	if path != "" {
		command = append(command, "--", path)
	}
	output, truncated, err := runGitInspectCommand(ctx, repositoryPath, limit, command...)
	if err != nil {
		return "", false, err
	}
	return strings.ToValidUTF8(string(output), "\uFFFD"), truncated, nil
}

func gitInspectPatchEnabled(args gitInspectArgs) bool {
	return args.IncludePatch == nil || *args.IncludePatch
}

func gitInspectPatchLimit(args gitInspectArgs) int {
	limit := args.MaxOutputBytes
	if limit <= 0 {
		return gitInspectDefaultPatchSize
	}
	if limit > gitInspectMaxPatchSize {
		return gitInspectMaxPatchSize
	}
	return limit
}

func gitInspectContextLines(args gitInspectArgs) int {
	if args.ContextLines == nil {
		return 3
	}
	if *args.ContextLines < 0 {
		return 0
	}
	if *args.ContextLines > 20 {
		return 20
	}
	return *args.ContextLines
}

func parseGitInspectStatus(output []byte, label string) ([]gitInspectFile, error) {
	records := strings.Split(string(output), "\x00")
	files := make([]gitInspectFile, 0, len(records))
	for i := 0; i < len(records); i++ {
		record := records[i]
		if record == "" {
			continue
		}
		if len(record) < 4 {
			return nil, fmt.Errorf("parse Git status: malformed record")
		}
		index, worktree := record[0], record[1]
		file := gitInspectFile{
			Path:           labeledGitInspectPath(label, record[3:]),
			Status:         string([]byte{index, worktree}),
			Operation:      gitInspectStatusOperation(index, worktree),
			IndexStatus:    gitInspectStatusCharacter(index),
			WorktreeStatus: gitInspectStatusCharacter(worktree),
		}
		if index == 'R' || index == 'C' || worktree == 'R' || worktree == 'C' {
			i++
			if i >= len(records) || records[i] == "" {
				return nil, fmt.Errorf("parse Git status: missing source path")
			}
			file.OldPath = labeledGitInspectPath(label, records[i])
		}
		files = append(files, file)
	}
	return files, nil
}

func parseGitInspectNameStatus(output []byte, label string) ([]gitInspectFile, error) {
	records := strings.Split(string(output), "\x00")
	files := make([]gitInspectFile, 0, len(records))
	for i := 0; i < len(records); i++ {
		status := strings.TrimSpace(records[i])
		if status == "" {
			continue
		}
		if i+1 >= len(records) || records[i+1] == "" {
			return nil, fmt.Errorf("parse Git name-status: missing path")
		}
		file := gitInspectFile{Status: status, Operation: gitInspectNameStatusOperation(status)}
		if status[0] == 'R' || status[0] == 'C' {
			file.OldPath = labeledGitInspectPath(label, records[i+1])
			i++
			if i+1 >= len(records) || records[i+1] == "" {
				return nil, fmt.Errorf("parse Git name-status: missing target path")
			}
			file.Path = labeledGitInspectPath(label, records[i+1])
			i++
		} else {
			file.Path = labeledGitInspectPath(label, records[i+1])
			i++
		}
		files = append(files, file)
	}
	return files, nil
}

func gitInspectStatusOperation(index byte, worktree byte) string {
	status := string([]byte{index, worktree})
	switch {
	case status == "??":
		return "untracked"
	case status == "DD", status == "AU", status == "UD", status == "UA", status == "DU", status == "AA", status == "UU":
		return "conflicted"
	case index == 'R' || worktree == 'R':
		return "renamed"
	case index == 'C' || worktree == 'C':
		return "copied"
	case index == 'D' || worktree == 'D':
		return "deleted"
	case index == 'A' || worktree == 'A':
		return "created"
	default:
		return "modified"
	}
}

func gitInspectNameStatusOperation(status string) string {
	if status == "" {
		return "modified"
	}
	switch status[0] {
	case 'A':
		return "created"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	default:
		return "modified"
	}
}

func gitInspectStatusCharacter(value byte) string {
	if value == ' ' {
		return ""
	}
	return string(value)
}

func parseGitInspectBlame(output []byte, label string) []gitInspectBlameLine {
	rawLines := strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n")
	lines := make([]gitInspectBlameLine, 0)
	var current gitInspectBlameLine
	var timestamp int64
	var timezone string
	for _, line := range rawLines {
		if line == "" {
			continue
		}
		if line[0] == '\t' {
			current.Content = line[1:]
			current.AuthoredAt = formatGitInspectTimestamp(timestamp, timezone)
			if current.Path != "" {
				current.Path = labeledGitInspectPath(label, current.Path)
			}
			lines = append(lines, current)
			current = gitInspectBlameLine{}
			timestamp = 0
			timezone = ""
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 3 && isGitInspectHash(fields[0]) {
			current.Hash = fields[0]
			current.OriginalLine, _ = strconv.Atoi(fields[1])
			current.Line, _ = strconv.Atoi(fields[2])
			continue
		}
		key, value, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		switch key {
		case "author":
			current.Author = value
		case "author-mail":
			current.AuthorEmail = strings.Trim(value, "<>")
		case "author-time":
			timestamp, _ = strconv.ParseInt(value, 10, 64)
		case "author-tz":
			timezone = value
		case "summary":
			current.Subject = value
		case "filename":
			current.Path = value
		}
	}
	return lines
}

func formatGitInspectTimestamp(timestamp int64, timezone string) string {
	if timestamp == 0 {
		return ""
	}
	offset := 0
	if len(timezone) == 5 {
		hours, hourErr := strconv.Atoi(timezone[1:3])
		minutes, minuteErr := strconv.Atoi(timezone[3:5])
		if hourErr == nil && minuteErr == nil {
			offset = (hours*60 + minutes) * 60
			if timezone[0] == '-' {
				offset = -offset
			}
		}
	}
	return time.Unix(timestamp, 0).In(time.FixedZone("", offset)).Format(time.RFC3339)
}

func gitInspectHead(ctx context.Context, repositoryPath string) (string, string) {
	headOutput, _, err := runGitInspectCommand(ctx, repositoryPath, 4096, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", ""
	}
	head := strings.TrimSpace(string(headOutput))
	shortOutput, _, _ := runGitInspectCommand(ctx, repositoryPath, 4096, "rev-parse", "--short", "HEAD")
	return head, strings.TrimSpace(string(shortOutput))
}

func gitInspectHasHead(ctx context.Context, repositoryPath string) bool {
	_, _, err := runGitInspectCommand(ctx, repositoryPath, 4096, "rev-parse", "--verify", "HEAD")
	return err == nil
}

func gitInspectUpstream(ctx context.Context, repositoryPath string) (string, int, int) {
	output, _, err := runGitInspectCommand(ctx, repositoryPath, 4096, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil {
		return "", 0, 0
	}
	upstream := strings.TrimSpace(string(output))
	counts, _, err := runGitInspectCommand(ctx, repositoryPath, 4096, "rev-list", "--left-right", "--count", "HEAD...@{upstream}")
	if err != nil {
		return upstream, 0, 0
	}
	fields := strings.Fields(string(counts))
	if len(fields) != 2 {
		return upstream, 0, 0
	}
	ahead, _ := strconv.Atoi(fields[0])
	behind, _ := strconv.Atoi(fields[1])
	return upstream, ahead, behind
}

func labeledGitInspectPath(label string, path string) string {
	path = strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
	if label == "." {
		return path
	}
	return label + "/" + path
}

func truncateGitInspectString(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	return strings.ToValidUTF8(value[:limit], "\uFFFD"), true
}

func completeGitInspectNULRecords(output []byte) []byte {
	last := strings.LastIndexByte(string(output), 0)
	if last < 0 {
		return nil
	}
	return output[:last+1]
}

func isGitInspectHash(value string) bool {
	if len(value) < 7 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", character) {
			return false
		}
	}
	return true
}

func gitInspectNoCommits(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "does not have any commits") ||
		strings.Contains(message, "bad default revision") ||
		strings.Contains(message, "unknown revision")
}

func runGitInspectCommand(ctx context.Context, repositoryPath string, outputLimit int, args ...string) ([]byte, bool, error) {
	commandArgs := append([]string{
		"--no-pager",
		"-c", "safe.directory=*",
		"-c", "core.quotepath=false",
		"-c", "color.ui=false",
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "submodule.recurse=false",
		"-C", repositoryPath,
	}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Env = append(os.Environ(),
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
	)
	stdout := newLimitedBuffer(outputLimit)
	stderr := newLimitedBuffer(64 * 1024)
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if ctx.Err() != nil {
		return []byte(stdout.String()), stdout.Truncated(), SafeError{Code: "git_timeout", Message: "Git inspection timed out or was canceled"}
	}
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, false, SafeError{Code: "git_unavailable", Message: "Git executable was not found"}
		}
		message := strings.TrimSpace(strings.ToValidUTF8(stderr.String(), "\uFFFD"))
		if message == "" {
			message = err.Error()
		}
		lower := strings.ToLower(message)
		code := "git_failed"
		switch {
		case strings.Contains(lower, "not a git repository"):
			code = "not_git_repository"
			message = "workspace folder is not a Git repository"
		case strings.Contains(lower, "dubious ownership"):
			code = "git_ownership_refused"
			message = "Git refused the workspace because of ownership settings"
		}
		return []byte(stdout.String()), stdout.Truncated(), SafeError{Code: code, Message: message}
	}
	return []byte(stdout.String()), stdout.Truncated(), nil
}
