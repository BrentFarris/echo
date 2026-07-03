package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitInspectStatusAndLiveDiffsAreReadOnly(t *testing.T) {
	root := newGitInspectTestRepo(t)
	writeGitInspectTestFile(t, root, "tracked.txt", "before\n")
	runGitInspectTestCommand(t, root, "add", ".")
	runGitInspectTestCommand(t, root, "commit", "-m", "initial")
	runGitInspectTestCommand(t, root, "config", "diff.external", "echo-git-inspect-must-not-run")
	runGitInspectTestCommand(t, root, "config", "core.fsmonitor", "echo-git-inspect-fsmonitor-must-not-run")

	writeGitInspectTestFile(t, root, "tracked.txt", "after\n")
	writeGitInspectTestFile(t, root, "staged.txt", "staged\n")
	runGitInspectTestCommand(t, root, "add", "staged.txt")
	writeGitInspectTestFile(t, root, "untracked.txt", "untracked\n")

	beforeHead := strings.TrimSpace(runGitInspectTestCommand(t, root, "rev-parse", "HEAD"))
	beforeStatus := runGitInspectTestCommand(t, root, "-c", "core.fsmonitor=false", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	execution := gitInspectTestContext(root)

	statusResult := Execute(execution, "git_inspect", mustJSON(t, map[string]any{
		"operation":  "status",
		"repository": "repo",
	}))
	if !statusResult.Success {
		t.Fatalf("status failed: %+v", statusResult.Error)
	}
	status := statusResult.Output.(gitInspectStatusOutput)
	if !status.Dirty || status.FileCount != 3 {
		t.Fatalf("unexpected status: %+v", status)
	}
	operations := map[string]bool{}
	for _, file := range status.Files {
		operations[file.Operation] = true
	}
	if !operations["modified"] || !operations["created"] || !operations["untracked"] {
		t.Fatalf("missing status operations: %+v", status.Files)
	}

	workingResult := Execute(execution, "git_inspect", mustJSON(t, map[string]any{
		"operation":    "diff",
		"repository":   "repo",
		"comparison":   "working_tree",
		"includePatch": true,
	}))
	if !workingResult.Success {
		t.Fatalf("working diff failed: %+v", workingResult.Error)
	}
	working := workingResult.Output.(gitInspectDiffOutput)
	if !strings.Contains(working.Patch, "+after") || strings.Contains(working.Patch, "staged.txt") {
		t.Fatalf("unexpected working patch: %q", working.Patch)
	}

	stagedResult := Execute(execution, "git_inspect", mustJSON(t, map[string]any{
		"operation":  "diff",
		"repository": "repo",
		"comparison": "staged",
	}))
	if !stagedResult.Success {
		t.Fatalf("staged diff failed: %+v", stagedResult.Error)
	}
	staged := stagedResult.Output.(gitInspectDiffOutput)
	if !strings.Contains(staged.Patch, "staged.txt") || strings.Contains(staged.Patch, "+after") {
		t.Fatalf("unexpected staged patch: %q", staged.Patch)
	}

	afterHead := strings.TrimSpace(runGitInspectTestCommand(t, root, "rev-parse", "HEAD"))
	afterStatus := runGitInspectTestCommand(t, root, "-c", "core.fsmonitor=false", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if beforeHead != afterHead || beforeStatus != afterStatus {
		t.Fatalf("Git inspection changed repository state\nbefore head=%q status=%q\nafter head=%q status=%q", beforeHead, beforeStatus, afterHead, afterStatus)
	}
}

func TestGitInspectLogSearchAllRefsAndShow(t *testing.T) {
	root := newGitInspectTestRepo(t)
	writeGitInspectTestFile(t, root, "notes.txt", "one\n")
	runGitInspectTestCommand(t, root, "add", ".")
	runGitInspectTestCommand(t, root, "commit", "-m", "initial notes")
	initialHash := strings.TrimSpace(runGitInspectTestCommand(t, root, "rev-parse", "HEAD"))
	mainBranch := strings.TrimSpace(runGitInspectTestCommand(t, root, "branch", "--show-current"))

	runGitInspectTestCommand(t, root, "checkout", "-b", "research")
	writeGitInspectTestFile(t, root, "notes.txt", "two\n")
	runGitInspectTestCommand(t, root, "add", ".")
	runGitInspectTestCommand(t, root, "commit", "-m", "Explain historical behavior", "-m", "The rationale lives in this body.")
	researchHash := strings.TrimSpace(runGitInspectTestCommand(t, root, "rev-parse", "HEAD"))
	runGitInspectTestCommand(t, root, "checkout", mainBranch)

	execution := gitInspectTestContext(root)
	defaultResult := Execute(execution, "git_inspect", mustJSON(t, map[string]any{
		"operation":  "log",
		"repository": "repo",
		"query":      "rationale lives",
	}))
	if !defaultResult.Success {
		t.Fatalf("default log failed: %+v", defaultResult.Error)
	}
	if commits := defaultResult.Output.(gitInspectLogOutput).Commits; len(commits) != 0 {
		t.Fatalf("HEAD history unexpectedly included branch commit: %+v", commits)
	}

	allRefsResult := Execute(execution, "git_inspect", mustJSON(t, map[string]any{
		"operation":  "log",
		"repository": "repo",
		"query":      "RATIONALE LIVES",
		"allRefs":    true,
	}))
	if !allRefsResult.Success {
		t.Fatalf("all-refs log failed: %+v", allRefsResult.Error)
	}
	allRefs := allRefsResult.Output.(gitInspectLogOutput)
	if len(allRefs.Commits) != 1 || allRefs.Commits[0].Hash != researchHash {
		t.Fatalf("unexpected all-refs commits: %+v", allRefs.Commits)
	}
	if !strings.Contains(allRefs.Commits[0].Body, "rationale lives") {
		t.Fatalf("commit body missing: %+v", allRefs.Commits[0])
	}

	showResult := Execute(execution, "git_inspect", mustJSON(t, map[string]any{
		"operation":  "show",
		"repository": "repo",
		"revision":   researchHash[:10],
		"path":       "repo/notes.txt",
	}))
	if !showResult.Success {
		t.Fatalf("show failed: %+v", showResult.Error)
	}
	show := showResult.Output.(gitInspectShowOutput)
	if show.Commit.Hash != researchHash || show.FileCount != 1 || !strings.Contains(show.Patch, "+two") {
		t.Fatalf("unexpected show output: %+v", show)
	}

	rootShowResult := Execute(execution, "git_inspect", mustJSON(t, map[string]any{
		"operation":  "show",
		"repository": "repo",
		"revision":   initialHash,
	}))
	if !rootShowResult.Success {
		t.Fatalf("root show failed: %+v", rootShowResult.Error)
	}
	rootShow := rootShowResult.Output.(gitInspectShowOutput)
	if rootShow.FirstParent != "" || rootShow.FileCount != 1 || !strings.Contains(rootShow.Patch, "+one") {
		t.Fatalf("unexpected root show output: %+v", rootShow)
	}
}

func TestGitInspectRevisionDiffBlameAndPatchTruncation(t *testing.T) {
	root := newGitInspectTestRepo(t)
	writeGitInspectTestFile(t, root, "code.txt", "alpha\nbeta\n")
	runGitInspectTestCommand(t, root, "add", ".")
	runGitInspectTestCommand(t, root, "commit", "-m", "add code")
	base := strings.TrimSpace(runGitInspectTestCommand(t, root, "rev-parse", "HEAD"))

	writeGitInspectTestFile(t, root, "code.txt", "alpha\nchanged\n"+strings.Repeat("long line content\n", 100))
	runGitInspectTestCommand(t, root, "add", ".")
	runGitInspectTestCommand(t, root, "commit", "-m", "change beta")
	target := strings.TrimSpace(runGitInspectTestCommand(t, root, "rev-parse", "HEAD"))
	execution := gitInspectTestContext(root)

	diffResult := Execute(execution, "git_inspect", mustJSON(t, map[string]any{
		"operation":      "diff",
		"repository":     "repo",
		"comparison":     "revisions",
		"base":           base,
		"target":         target,
		"path":           "repo/code.txt",
		"maxOutputBytes": 128,
	}))
	if !diffResult.Success {
		t.Fatalf("revision diff failed: %+v", diffResult.Error)
	}
	diff := diffResult.Output.(gitInspectDiffOutput)
	if diff.Base != base || diff.Target != target || !diff.PatchTruncated || diff.FileCount != 1 {
		t.Fatalf("unexpected revision diff: %+v", diff)
	}

	blameResult := Execute(execution, "git_inspect", mustJSON(t, map[string]any{
		"operation":  "blame",
		"repository": "repo",
		"revision":   target,
		"path":       "repo/code.txt",
		"startLine":  1,
		"endLine":    2,
	}))
	if !blameResult.Success {
		t.Fatalf("blame failed: %+v", blameResult.Error)
	}
	blame := blameResult.Output.(gitInspectBlameOutput)
	if len(blame.Lines) != 2 || blame.Lines[0].Content != "alpha" || blame.Lines[1].Content != "changed" {
		t.Fatalf("unexpected blame lines: %+v", blame.Lines)
	}
	if blame.Lines[1].Hash != target || blame.Lines[1].Subject != "change beta" {
		t.Fatalf("unexpected blame metadata: %+v", blame.Lines[1])
	}
}

func TestGitInspectPaginationAndMergeUsesFirstParent(t *testing.T) {
	root := newGitInspectTestRepo(t)
	writeGitInspectTestFile(t, root, "base.txt", "base\n")
	runGitInspectTestCommand(t, root, "add", ".")
	runGitInspectTestCommand(t, root, "commit", "-m", "base")
	mainBranch := strings.TrimSpace(runGitInspectTestCommand(t, root, "branch", "--show-current"))

	runGitInspectTestCommand(t, root, "checkout", "-b", "feature")
	writeGitInspectTestFile(t, root, "feature.txt", "feature\n")
	runGitInspectTestCommand(t, root, "add", ".")
	runGitInspectTestCommand(t, root, "commit", "-m", "feature change")
	runGitInspectTestCommand(t, root, "checkout", mainBranch)
	writeGitInspectTestFile(t, root, "main.txt", "main\n")
	runGitInspectTestCommand(t, root, "add", ".")
	runGitInspectTestCommand(t, root, "commit", "-m", "main change")
	firstParent := strings.TrimSpace(runGitInspectTestCommand(t, root, "rev-parse", "HEAD"))
	runGitInspectTestCommand(t, root, "merge", "--no-ff", "feature", "-m", "merge feature")
	mergeHash := strings.TrimSpace(runGitInspectTestCommand(t, root, "rev-parse", "HEAD"))

	execution := gitInspectTestContext(root)
	firstPageResult := Execute(execution, "git_inspect", mustJSON(t, map[string]any{
		"operation":  "log",
		"repository": "repo",
		"maxResults": 1,
	}))
	if !firstPageResult.Success {
		t.Fatalf("first log page failed: %+v", firstPageResult.Error)
	}
	firstPage := firstPageResult.Output.(gitInspectLogOutput)
	if len(firstPage.Commits) != 1 || firstPage.Commits[0].Hash != mergeHash || !firstPage.HasMore || firstPage.NextSkip != 1 {
		t.Fatalf("unexpected first log page: %+v", firstPage)
	}
	secondPageResult := Execute(execution, "git_inspect", mustJSON(t, map[string]any{
		"operation":  "log",
		"repository": "repo",
		"maxResults": 1,
		"skip":       firstPage.NextSkip,
	}))
	if !secondPageResult.Success {
		t.Fatalf("second log page failed: %+v", secondPageResult.Error)
	}
	secondPage := secondPageResult.Output.(gitInspectLogOutput)
	if len(secondPage.Commits) != 1 || secondPage.Commits[0].Hash == mergeHash {
		t.Fatalf("unexpected second log page: %+v", secondPage)
	}

	showResult := Execute(execution, "git_inspect", mustJSON(t, map[string]any{
		"operation":  "show",
		"repository": "repo",
		"revision":   mergeHash,
	}))
	if !showResult.Success {
		t.Fatalf("merge show failed: %+v", showResult.Error)
	}
	show := showResult.Output.(gitInspectShowOutput)
	if len(show.Commit.Parents) != 2 || show.FirstParent != firstParent {
		t.Fatalf("unexpected merge parent metadata: %+v", show)
	}
	if show.FileCount != 1 || show.Files[0].Path != "repo/feature.txt" || strings.Contains(show.Patch, "main.txt") {
		t.Fatalf("merge was not compared with first parent: %+v", show)
	}
}

func TestGitInspectRejectsWrongRepositoryPathAndNestedRoot(t *testing.T) {
	first := newGitInspectTestRepo(t)
	second := newGitInspectTestRepo(t)
	execution := ExecutionContext{
		Context: context.Background(),
		WorkspaceRoots: []WorkspaceRoot{
			{Label: "first", Path: first},
			{Label: "second", Path: second},
		},
	}
	pathResult := Execute(execution, "git_inspect", mustJSON(t, map[string]any{
		"operation":  "log",
		"repository": "first",
		"path":       "second/file.txt",
	}))
	if pathResult.Success || pathResult.Error == nil || pathResult.Error.Code != "path_outside_repository" {
		t.Fatalf("expected cross-repository path rejection, got %+v", pathResult)
	}

	nested := filepath.Join(first, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	nestedResult := Execute(ExecutionContext{
		Context:        context.Background(),
		WorkspaceRoots: []WorkspaceRoot{{Label: "nested", Path: nested}},
	}, "git_inspect", mustJSON(t, map[string]any{
		"operation":  "status",
		"repository": "nested",
	}))
	if nestedResult.Success || nestedResult.Error == nil || nestedResult.Error.Code != "repository_not_root" {
		t.Fatalf("expected nested-root rejection, got %+v", nestedResult)
	}
}

func TestGitInspectEmptyHistoryAndArgumentValidation(t *testing.T) {
	root := newGitInspectTestRepo(t)
	execution := gitInspectTestContext(root)
	logResult := Execute(execution, "git_inspect", mustJSON(t, map[string]any{
		"operation":  "log",
		"repository": "repo",
	}))
	if !logResult.Success {
		t.Fatalf("empty log failed: %+v", logResult.Error)
	}
	if commits := logResult.Output.(gitInspectLogOutput).Commits; len(commits) != 0 {
		t.Fatalf("expected empty history, got %+v", commits)
	}

	invalidResult := Execute(execution, "git_inspect", mustJSON(t, map[string]any{
		"operation":    "status",
		"repository":   "repo",
		"includePatch": false,
	}))
	if invalidResult.Success || invalidResult.Error == nil || invalidResult.Error.Code != "invalid_arguments" {
		t.Fatalf("expected unsupported argument rejection, got %+v", invalidResult)
	}
}

func TestGitInspectIsAvailableInReadOnlySchema(t *testing.T) {
	found := false
	for _, tool := range ReadOnlyLLMSchema() {
		if tool.Function.Name == "git_inspect" {
			found = true
			break
		}
	}
	if !found || !IsReadOnlyToolName("git_inspect") || IsMutatingToolName("git_inspect") {
		t.Fatal("git_inspect must be registered as a read-only, non-mutating tool")
	}
}

func gitInspectTestContext(root string) ExecutionContext {
	return ExecutionContext{
		Context:        context.Background(),
		WorkspaceRoots: []WorkspaceRoot{{Label: "repo", Path: root}},
	}
}

func newGitInspectTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable is unavailable")
	}
	root := t.TempDir()
	runGitInspectTestCommand(t, root, "init")
	runGitInspectTestCommand(t, root, "config", "user.name", "Echo Test")
	runGitInspectTestCommand(t, root, "config", "user.email", "echo@example.test")
	runGitInspectTestCommand(t, root, "config", "core.autocrlf", "false")
	return root
}

func runGitInspectTestCommand(t *testing.T, root string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.Command("git", commandArgs...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func writeGitInspectTestFile(t *testing.T, root string, path string, content string) {
	t.Helper()
	absolute := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
