package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxTextFileBytes = 256 * 1024

func resolveWorkspacePath(workspacePath, requestedPath string) (string, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return "", SafeError{Code: "missing_workspace", Message: "workspace path is required"}
	}
	workspaceAbs, err := filepath.Abs(workspacePath)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path: %w", err)
	}
	if realWorkspace, err := filepath.EvalSymlinks(workspaceAbs); err == nil {
		workspaceAbs = realWorkspace
	}
	requestedPath = strings.TrimSpace(requestedPath)
	if requestedPath == "" {
		requestedPath = "."
	}
	if filepath.IsAbs(requestedPath) {
		return "", SafeError{Code: "path_outside_workspace", Message: "path must be relative to the workspace"}
	}

	resolved := filepath.Clean(filepath.Join(workspaceAbs, requestedPath))
	relative, err := filepath.Rel(workspaceAbs, resolved)
	if err != nil {
		return "", fmt.Errorf("resolve relative path: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", SafeError{Code: "path_outside_workspace", Message: "path escapes the workspace"}
	}
	if realResolved, err := filepath.EvalSymlinks(resolved); err == nil {
		relative, err := filepath.Rel(workspaceAbs, realResolved)
		if err != nil {
			return "", fmt.Errorf("resolve real path: %w", err)
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", SafeError{Code: "path_outside_workspace", Message: "path escapes the workspace"}
		}
		resolved = realResolved
	}
	return resolved, nil
}

func resolveWorkspaceChildPath(workspacePath, requestedPath string) (string, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return "", SafeError{Code: "missing_workspace", Message: "workspace path is required"}
	}
	workspaceAbs, err := filepath.Abs(workspacePath)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path: %w", err)
	}
	if realWorkspace, err := filepath.EvalSymlinks(workspaceAbs); err == nil {
		workspaceAbs = realWorkspace
	}
	requestedPath = strings.TrimSpace(requestedPath)
	if requestedPath == "" {
		return "", SafeError{Code: "invalid_arguments", Message: "path is required"}
	}
	if filepath.IsAbs(requestedPath) {
		return "", SafeError{Code: "path_outside_workspace", Message: "path must be relative to the workspace"}
	}

	resolved := filepath.Clean(filepath.Join(workspaceAbs, requestedPath))
	relative, err := filepath.Rel(workspaceAbs, resolved)
	if err != nil {
		return "", fmt.Errorf("resolve relative path: %w", err)
	}
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", SafeError{Code: "path_outside_workspace", Message: "path escapes the workspace"}
	}

	parent := filepath.Dir(resolved)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return "", SafeError{Code: "parent_not_found", Message: "parent directory was not found"}
	}
	if !parentInfo.IsDir() {
		return "", SafeError{Code: "parent_not_directory", Message: "parent path is not a directory"}
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("resolve parent path: %w", err)
	}
	relativeParent, err := filepath.Rel(workspaceAbs, realParent)
	if err != nil {
		return "", fmt.Errorf("resolve parent relative path: %w", err)
	}
	if relativeParent == ".." || strings.HasPrefix(relativeParent, ".."+string(filepath.Separator)) {
		return "", SafeError{Code: "path_outside_workspace", Message: "path escapes the workspace"}
	}

	return filepath.Join(realParent, filepath.Base(resolved)), nil
}

func relativeWorkspacePath(workspacePath, absolutePath string) string {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return filepath.ToSlash(absolutePath)
	}
	absPath := strings.TrimSpace(absolutePath)
	if absPath == "" {
		return filepath.ToSlash(absolutePath)
	}
	// Normalize both paths to their real absolute forms so filepath.Rel
	// produces correct results even when symlinks or mixed casing are involved.
	workspaceAbs, err := filepath.Abs(workspacePath)
	if err != nil {
		return filepath.ToSlash(absPath)
	}
	if realWS, err := filepath.EvalSymlinks(workspaceAbs); err == nil {
		workspaceAbs = realWS
	}
	absAbs, err := filepath.Abs(absPath)
	if err != nil {
		return filepath.ToSlash(absPath)
	}
	if realAbs, err := filepath.EvalSymlinks(absAbs); err == nil {
		absAbs = realAbs
	}
	relative, err := filepath.Rel(workspaceAbs, absAbs)
	if err != nil {
		return filepath.ToSlash(absPath)
	}
	if relative == "." {
		return "."
	}
	return filepath.ToSlash(relative)
}

func isTextLike(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	for _, b := range data {
		if b == 0 {
			return false
		}
	}
	return true
}

func normalizeToolTextLineBreaks(text string) string {
	// XML-ish inline tool calls can decode newline character references to
	// Unicode line controls that editors render as glyphs instead of lines.
	return strings.NewReplacer(
		"\u0085", "\n",
		"\u2028", "\n",
		"\u2029", "\n",
	).Replace(text)
}

func normalizeToolTextLineBreaksForFile(text, fileContent string) string {
	return normalizeTextLineBreaks(text, preferredTextLineBreak(fileContent))
}

func normalizeTextLineBreaks(text, lineBreak string) string {
	text = normalizeToolTextLineBreaks(text)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if lineBreak == "\r\n" {
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text
}

func preferredTextLineBreak(text string) string {
	crlf := 0
	lf := 0
	for index := 0; index < len(text); index++ {
		switch text[index] {
		case '\r':
			if index+1 < len(text) && text[index+1] == '\n' {
				crlf++
				index++
			} else {
				lf++
			}
		case '\n':
			lf++
		}
	}
	if crlf > 0 && crlf >= lf {
		return "\r\n"
	}
	return "\n"
}

func fileKind(info os.FileInfo) string {
	if info.IsDir() {
		return "directory"
	}
	if info.Mode().IsRegular() {
		return "file"
	}
	return "other"
}
