package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxTextFileBytes = 256 * 1024

func (ctx ExecutionContext) workspaceRoots() []WorkspaceRoot {
	if len(ctx.WorkspaceRoots) > 0 {
		roots := make([]WorkspaceRoot, 0, len(ctx.WorkspaceRoots))
		for _, root := range ctx.WorkspaceRoots {
			root.Label = strings.TrimSpace(root.Label)
			root.Path = strings.TrimSpace(root.Path)
			if root.Label == "" || root.Path == "" {
				continue
			}
			roots = append(roots, root)
		}
		return roots
	}
	workspacePath := strings.TrimSpace(ctx.WorkspacePath)
	if workspacePath == "" {
		return nil
	}
	return []WorkspaceRoot{{Label: ".", Path: workspacePath}}
}

func resolveWorkspacePath(ctx ExecutionContext, requestedPath string) (string, error) {
	roots := ctx.workspaceRoots()
	if len(roots) == 0 {
		return "", SafeError{Code: "missing_workspace", Message: "workspace path is required"}
	}
	requestedPath = strings.TrimSpace(requestedPath)
	if requestedPath == "" {
		requestedPath = "."
	}
	if filepath.IsAbs(requestedPath) {
		return "", SafeError{Code: "path_outside_workspace", Message: "path must be relative to the workspace"}
	}
	root, relativePath, err := resolveWorkspaceRoot(roots, requestedPath, true)
	if err != nil {
		return "", err
	}
	workspaceAbs, err := workspaceRootAbsolutePath(root)
	if err != nil {
		return "", err
	}

	resolved := filepath.Clean(filepath.Join(workspaceAbs, relativePath))
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

func resolveWorkspaceChildPath(ctx ExecutionContext, requestedPath string) (string, error) {
	roots := ctx.workspaceRoots()
	if len(roots) == 0 {
		return "", SafeError{Code: "missing_workspace", Message: "workspace path is required"}
	}
	requestedPath = strings.TrimSpace(requestedPath)
	if requestedPath == "" {
		return "", SafeError{Code: "invalid_arguments", Message: "path is required"}
	}
	if filepath.IsAbs(requestedPath) {
		return "", SafeError{Code: "path_outside_workspace", Message: "path must be relative to the workspace"}
	}
	root, relativePath, err := resolveWorkspaceRoot(roots, requestedPath, false)
	if err != nil {
		return "", err
	}
	workspaceAbs, err := workspaceRootAbsolutePath(root)
	if err != nil {
		return "", err
	}

	resolved := filepath.Clean(filepath.Join(workspaceAbs, relativePath))
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

func relativeWorkspacePath(ctx ExecutionContext, absolutePath string) string {
	absPath := strings.TrimSpace(absolutePath)
	if absPath == "" {
		return filepath.ToSlash(absolutePath)
	}
	absAbs, err := filepath.Abs(absPath)
	if err != nil {
		return filepath.ToSlash(absPath)
	}
	if realAbs, err := filepath.EvalSymlinks(absAbs); err == nil {
		absAbs = realAbs
	}
	for _, root := range workspaceRootsByPathDepth(ctx.workspaceRoots()) {
		workspaceAbs, err := workspaceRootAbsolutePath(root)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(workspaceAbs, absAbs)
		if err != nil {
			continue
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		if root.Label == "." {
			if relative == "." {
				return "."
			}
			return filepath.ToSlash(relative)
		}
		if relative == "." {
			return root.Label
		}
		return root.Label + "/" + filepath.ToSlash(relative)
	}
	return filepath.ToSlash(absPath)
}

func resolveWorkspaceRoot(roots []WorkspaceRoot, requestedPath string, allowVirtualRoot bool) (WorkspaceRoot, string, error) {
	if len(roots) == 1 && roots[0].Label == "." {
		path := strings.TrimSpace(requestedPath)
		if path == "" {
			path = "."
		}
		return roots[0], path, nil
	}
	if requestedPath == "." {
		if allowVirtualRoot {
			return WorkspaceRoot{}, "", SafeError{Code: "virtual_workspace_root", Message: "path must include a workspace folder label"}
		}
		return WorkspaceRoot{}, "", SafeError{Code: "path_outside_workspace", Message: "path must include a workspace folder label"}
	}
	label, relativePath := splitWorkspaceLabeledPath(requestedPath)
	if label == "" {
		return WorkspaceRoot{}, "", SafeError{Code: "path_outside_workspace", Message: "path must include a workspace folder label"}
	}
	for _, root := range roots {
		if strings.EqualFold(root.Label, label) {
			return root, relativePath, nil
		}
	}
	return WorkspaceRoot{}, "", SafeError{Code: "path_outside_workspace", Message: fmt.Sprintf("workspace folder %q was not found", label)}
}

func splitWorkspaceLabeledPath(requestedPath string) (string, string) {
	path := strings.TrimSpace(strings.ReplaceAll(requestedPath, "\\", "/"))
	path = strings.TrimPrefix(path, "./")
	path = strings.Trim(path, "/")
	if path == "" || path == "." {
		return "", "."
	}
	parts := strings.SplitN(path, "/", 2)
	relativePath := "."
	if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
		relativePath = filepath.FromSlash(parts[1])
	}
	return parts[0], relativePath
}

func workspaceRootAbsolutePath(root WorkspaceRoot) (string, error) {
	workspaceAbs, err := filepath.Abs(root.Path)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path: %w", err)
	}
	if realWorkspace, err := filepath.EvalSymlinks(workspaceAbs); err == nil {
		workspaceAbs = realWorkspace
	}
	return workspaceAbs, nil
}

func workspaceRootsByPathDepth(roots []WorkspaceRoot) []WorkspaceRoot {
	ordered := append([]WorkspaceRoot{}, roots...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return len(filepath.Clean(ordered[i].Path)) > len(filepath.Clean(ordered[j].Path))
	})
	return ordered
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
