package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

	status, err := runWorkspaceGitCommand(ctx, workspace.FolderPath, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return WorkspaceGitChangeReview{}, err
	}
	entries, err := parseGitStatusPorcelain(status)
	if err != nil {
		return WorkspaceGitChangeReview{}, err
	}

	files := make([]WorkspaceGitChangedFile, 0, len(entries))
	for _, entry := range entries {
		file := WorkspaceGitChangedFile{
			Path:           entry.path,
			OldPath:        entry.oldPath,
			Operation:      gitStatusOperation(entry.index, entry.worktree),
			Status:         gitRawStatus(entry.index, entry.worktree),
			IndexStatus:    gitStatusChar(entry.index),
			WorktreeStatus: gitStatusChar(entry.worktree),
		}

		var diff string
		if entry.index == '?' && entry.worktree == '?' {
			diff, err = synthesizeUntrackedGitDiff(workspace.FolderPath, entry.path)
		} else {
			diff, err = loadGitDiffForPath(ctx, workspace.FolderPath, entry.path)
		}
		if err == nil && strings.TrimSpace(diff) != "" {
			file.Diff = diff
			file.DiffAvailable = true
		}
		files = append(files, file)
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

func synthesizeUntrackedGitDiff(workspacePath string, path string) (string, error) {
	resolved, err := resolveWorkspaceServicePath(workspacePath, path)
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
	fmt.Fprintf(&builder, "+++ b/%s\n", path)
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

func runWorkspaceGitCommand(ctx context.Context, workspacePath string, args ...string) ([]byte, error) {
	commandArgs := append([]string{
		"-c", "safe.directory=*",
		"-c", "core.quotepath=false",
		"-C", workspacePath,
	}, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	configureWorkspaceCommandProcess(cmd)

	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return output, fmt.Errorf("git command timed out")
	}
	if err != nil {
		return output, gitCommandError(args, output, err)
	}
	return output, nil
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
