package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brent/echo/internal/tools"
)

const (
	workspaceGitCommandTimeout       = 15 * time.Second
	maxWorkspaceGitSyntheticDiffSize = maxWorkspaceEditorFileBytes
	maxWorkspaceGitDiffSize          = 2 * 1024 * 1024
)

type WorkspaceGitChangedFile struct {
	Path           string `json:"path"`
	OldPath        string `json:"oldPath,omitempty"`
	Operation      string `json:"operation"`
	Status         string `json:"status"`
	IndexStatus    string `json:"indexStatus,omitempty"`
	WorktreeStatus string `json:"worktreeStatus,omitempty"`
	Staged         bool   `json:"staged"`
	Unstaged       bool   `json:"unstaged"`
	Diff           string `json:"diff,omitempty"`
	DiffAvailable  bool   `json:"diffAvailable"`
}

type WorkspaceGitChangeReview struct {
	WorkspaceID string                    `json:"workspaceId"`
	FileCount   int                       `json:"fileCount"`
	Files       []WorkspaceGitChangedFile `json:"files"`
}

type gitStatusEntry struct {
	path     string
	oldPath  string
	index    byte
	worktree byte
}

func (s *SystemService) LoadWorkspaceGitChanges(workspaceID string) (WorkspaceGitChangeReview, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return WorkspaceGitChangeReview{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), workspaceGitCommandTimeout)
	defer cancel()

	files := make([]WorkspaceGitChangedFile, 0)
	var firstErr error
	gitFolderCount := 0
	for _, folder := range workspace.Folders {
		if folder.Missing {
			continue
		}
		folderFiles, err := loadGitChangedFilesForFolder(ctx, folder)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		gitFolderCount++
		files = append(files, folderFiles...)
	}
	if gitFolderCount == 0 && firstErr != nil {
		return WorkspaceGitChangeReview{}, firstErr
	}
	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(files[i].Path) < strings.ToLower(files[j].Path)
	})

	return WorkspaceGitChangeReview{
		WorkspaceID: workspace.ID,
		FileCount:   len(files),
		Files:       files,
	}, nil
}

func loadGitChangedFilesForFolder(ctx context.Context, folder WorkspaceFolder) ([]WorkspaceGitChangedFile, error) {
	status, err := runWorkspaceGitCommand(ctx, folder.Path, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	entries, err := parseGitStatusPorcelain(status)
	if err != nil {
		return nil, err
	}

	files := make([]WorkspaceGitChangedFile, 0, len(entries))
	for _, entry := range entries {
		file := WorkspaceGitChangedFile{
			Path:           labeledWorkspacePath(folder.Label, entry.path),
			OldPath:        labeledWorkspacePath(folder.Label, entry.oldPath),
			Operation:      gitStatusOperation(entry.index, entry.worktree),
			Status:         gitRawStatus(entry.index, entry.worktree),
			IndexStatus:    gitStatusChar(entry.index),
			WorktreeStatus: gitStatusChar(entry.worktree),
			Staged:         gitStatusEntryHasStagedChanges(entry),
			Unstaged:       gitStatusEntryHasUnstagedChanges(entry),
		}

		var diff string
		if entry.index == '?' && entry.worktree == '?' {
			diff, err = synthesizeUntrackedGitDiff(folder.Path, entry.path, file.Path)
		} else {
			diff, err = loadGitDiffForPath(ctx, folder.Path, entry.path)
			if err == nil {
				diff = prefixGitDiffPaths(diff, folder.Label)
			}
		}
		if err == nil && strings.TrimSpace(diff) != "" {
			file.Diff = diff
			file.DiffAvailable = true
		}
		files = append(files, file)
	}
	return files, nil
}

func parseGitStatusPorcelain(output []byte) ([]gitStatusEntry, error) {
	if len(output) == 0 {
		return nil, nil
	}
	records := bytes.Split(output, []byte{0})
	entries := make([]gitStatusEntry, 0, len(records))
	for i := 0; i < len(records); i++ {
		if len(records[i]) == 0 {
			continue
		}
		record := string(records[i])
		if len(record) < 4 {
			return nil, fmt.Errorf("parse git status: malformed record")
		}
		entry := gitStatusEntry{
			index:    record[0],
			worktree: record[1],
			path:     record[3:],
		}
		if entry.index == 'R' || entry.index == 'C' || entry.worktree == 'R' || entry.worktree == 'C' {
			i++
			if i >= len(records) || len(records[i]) == 0 {
				return nil, fmt.Errorf("parse git status: missing source path")
			}
			entry.oldPath = string(records[i])
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func loadGitDiffForPath(ctx context.Context, workspacePath string, path string) (string, error) {
	output, err := runWorkspaceGitCommand(ctx, workspacePath, "diff", "--no-ext-diff", "--no-color", "--find-renames", "HEAD", "--", path)
	if err != nil {
		return "", err
	}
	return normalizeGitDiff(output), nil
}

func synthesizeUntrackedGitDiff(workspacePath string, path string, labeledPath string) (string, error) {
	resolved, err := resolveWorkspaceGitPath(workspacePath, path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxWorkspaceGitSyntheticDiffSize {
		return "", nil
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", err
	}
	if !isWorkspaceTextLike(data) || !utf8.Valid(data) {
		return "", nil
	}
	text := string(data)
	lines := splitGitDiffLines(text)

	var builder strings.Builder
	fmt.Fprintf(&builder, "--- /dev/null\n")
	fmt.Fprintf(&builder, "+++ b/%s\n", labeledPath)
	fmt.Fprintf(&builder, "@@ -0,0 +1,%d @@\n", len(lines))
	for _, line := range lines {
		builder.WriteString("+")
		builder.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			builder.WriteString("\n")
		}
	}
	return builder.String(), nil
}

func resolveWorkspaceGitPath(workspacePath string, requestedPath string) (string, error) {
	if filepath.IsAbs(requestedPath) {
		return "", fmt.Errorf("git path must be relative")
	}
	root, err := filepath.Abs(workspacePath)
	if err != nil {
		return "", err
	}
	if realRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = realRoot
	}
	resolved := filepath.Clean(filepath.Join(root, requestedPath))
	relative, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("git path escapes workspace folder")
	}
	return resolved, nil
}

func labeledWorkspacePath(label string, path string) string {
	path = strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
	if path == "" {
		return ""
	}
	return label + "/" + path
}

func prefixGitDiffPaths(diff string, label string) string {
	if strings.TrimSpace(diff) == "" {
		return diff
	}
	lines := strings.Split(strings.ReplaceAll(diff, "\r\n", "\n"), "\n")
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git a/"):
			lines[i] = strings.Replace(strings.Replace(line, " a/", " a/"+label+"/", 1), " b/", " b/"+label+"/", 1)
		case strings.HasPrefix(line, "--- a/"):
			lines[i] = "--- a/" + label + "/" + strings.TrimPrefix(line, "--- a/")
		case strings.HasPrefix(line, "+++ b/"):
			lines[i] = "+++ b/" + label + "/" + strings.TrimPrefix(line, "+++ b/")
		}
	}
	return strings.Join(lines, "\n")
}

func splitGitDiffLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	return lines
}

func normalizeGitDiff(output []byte) string {
	if len(output) == 0 {
		return ""
	}
	truncated := false
	if len(output) > maxWorkspaceGitDiffSize {
		output = output[:maxWorkspaceGitDiffSize]
		truncated = true
	}
	text := strings.ToValidUTF8(string(output), "\uFFFD")
	if truncated {
		text += "\n... diff truncated by Echo ...\n"
	}
	return text
}

func gitStatusOperation(index byte, worktree byte) string {
	switch {
	case index == '?' && worktree == '?':
		return tools.FileChangeCreated
	case gitStatusIsConflict(index, worktree):
		return "conflicted"
	case index == 'R' || worktree == 'R':
		return "renamed"
	case index == 'C' || worktree == 'C':
		return "copied"
	case index == 'D' || worktree == 'D':
		return tools.FileChangeDeleted
	case index == 'A' || worktree == 'A':
		return tools.FileChangeCreated
	default:
		return tools.FileChangeEdited
	}
}

func gitStatusIsConflict(index byte, worktree byte) bool {
	status := string([]byte{index, worktree})
	switch status {
	case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
		return true
	default:
		return index == 'U' || worktree == 'U'
	}
}

func gitRawStatus(index byte, worktree byte) string {
	return string([]byte{index, worktree})
}

func gitStatusChar(value byte) string {
	if value == ' ' {
		return ""
	}
	return string(value)
}

func gitStatusEntryHasStagedChanges(entry gitStatusEntry) bool {
	return entry.index != 0 && entry.index != ' ' && entry.index != '?'
}

func gitStatusEntryHasUnstagedChanges(entry gitStatusEntry) bool {
	return entry.worktree != 0 && entry.worktree != ' ' || entry.index == '?'
}

func runWorkspaceGitCommand(ctx context.Context, workspacePath string, args ...string) ([]byte, error) {
	return runWorkspaceGitCommandWithInput(ctx, workspacePath, nil, args...)
}

func runWorkspaceGitCommandWithInput(ctx context.Context, workspacePath string, input []byte, args ...string) ([]byte, error) {
	commandArgs := append([]string{
		"-c", "safe.directory=*",
		"-c", "core.quotepath=false",
		"-C", workspacePath,
	}, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	configureWorkspaceCommandProcess(cmd)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return stdout.Bytes(), fmt.Errorf("git command timed out")
	}
	if err != nil {
		output := append(stdout.Bytes(), stderr.Bytes()...)
		return output, gitCommandError(args, output, err)
	}
	return stdout.Bytes(), nil
}

func gitCommandError(args []string, output []byte, err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("git executable was not found")
	}
	message := strings.TrimSpace(strings.ToValidUTF8(string(output), "\uFFFD"))
	if message == "" {
		message = err.Error()
	}
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "not a git repository"):
		return fmt.Errorf("workspace is not a git repository")
	case strings.Contains(lower, "detected dubious ownership"):
		return fmt.Errorf("git refused this workspace because of ownership settings")
	}
	name := "git"
	if len(args) > 0 {
		name += " " + args[0]
	}
	return fmt.Errorf("%s failed: %s", name, message)
}
