package server

import (
	"path/filepath"
	"strings"

	"github.com/brent/echo/internal/tools"
	"github.com/brent/echo/internal/workspaces"
)

// workspaceToolRoots converts a workspace's folders into labeled tool roots so
// tools can resolve labeled workspace paths (e.g. "echo/frontend/src/main.ts").
// Labels are derived from each folder's base name, normalized to a safe
// lowercase slug, matching the convention used by the legacy Echo.
func workspaceToolRoots(workspace workspaces.Workspace) []tools.WorkspaceRoot {
	roots := make([]tools.WorkspaceRoot, 0, len(workspace.Folders))
	for _, folder := range workspace.Folders {
		folder = strings.TrimSpace(folder)
		if folder == "" {
			continue
		}
		roots = append(roots, tools.WorkspaceRoot{
			Label: normalizeWorkspaceFolderLabel(filepath.Base(folder)),
			Path:  folder,
		})
	}
	return roots
}

// normalizeWorkspaceFolderLabel converts a folder name into a safe, lowercase
// label suitable for use in labeled workspace paths.
func normalizeWorkspaceFolderLabel(label string) string {
	label = strings.TrimSpace(strings.ToLower(label))
	var builder strings.Builder
	lastDash := false
	for _, r := range label {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if valid {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-_.")
}
