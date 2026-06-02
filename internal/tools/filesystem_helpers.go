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
	relative, err := filepath.Rel(workspacePath, absolutePath)
	if err != nil {
		return filepath.ToSlash(absolutePath)
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

func fileKind(info os.FileInfo) string {
	if info.IsDir() {
		return "directory"
	}
	if info.Mode().IsRegular() {
		return "file"
	}
	return "other"
}
