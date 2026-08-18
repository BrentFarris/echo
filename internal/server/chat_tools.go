package server

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/brent/echo/internal/tools"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
	"github.com/brent/echo/internal/workspaceskills"
)

// workspaceToolRoots converts a workspace's folders into labeled tool roots so
// tools can resolve labeled workspace paths (e.g. "echo/frontend/src/main.ts").
// Labels are derived from each folder's base name, normalized to a safe
// lowercase slug, matching the convention used by the legacy Echo.
func workspaceToolRoots(workspace workspaces.Workspace) []tools.WorkspaceRoot {
	roots := make([]tools.WorkspaceRoot, 0, len(workspace.Folders))
	labels := make(map[string]bool)
	for _, folder := range workspace.Folders {
		folder = strings.TrimSpace(folder)
		if folder == "" {
			continue
		}
		label := normalizeWorkspaceFolderLabel(filepath.Base(folder))
		if label == "" {
			label = "workspace"
		}
		baseLabel := label
		for suffix := 2; labels[strings.ToLower(label)]; suffix++ {
			label = baseLabel + "-" + strconv.Itoa(suffix)
		}
		labels[strings.ToLower(label)] = true
		roots = append(roots, tools.WorkspaceRoot{
			Label: label,
			Path:  folder,
		})
	}
	return roots
}

func (s *Server) workspaceSkills(workspace workspaces.Workspace) *workspaceskills.Service {
	s.skillsMu.Lock()
	defer s.skillsMu.Unlock()
	if s.skills == nil {
		s.skills = make(map[string]*workspaceskills.Service)
	}
	if service := s.skills[workspace.ID]; service != nil {
		return service
	}
	service := workspaceskills.New(s.confinedToolRoots(workspace))
	s.skills[workspace.ID] = service
	return service
}

func (s *Server) confinedToolRoots(workspace workspaces.Workspace) []tools.WorkspaceRoot {
	filesystemRoots, err := s.fs.Roots(workspace.ID)
	if err != nil {
		return workspaceToolRoots(workspace)
	}
	roots := make([]tools.WorkspaceRoot, 0, len(filesystemRoots))
	labels := make(map[string]bool)
	for _, filesystemRoot := range filesystemRoots {
		label := normalizeWorkspaceFolderLabel(filesystemRoot.Label)
		if label == "" {
			label = "workspace"
		}
		baseLabel := label
		for suffix := 2; labels[strings.ToLower(label)]; suffix++ {
			label = baseLabel + "-" + strconv.Itoa(suffix)
		}
		labels[strings.ToLower(label)] = true
		roots = append(roots, tools.WorkspaceRoot{ID: filesystemRoot.ID, Label: label, Path: filesystemRoot.HostPath})
	}
	return roots
}

func (s *Server) toolPathResolver(workspaceID string, roots []tools.WorkspaceRoot, child bool) func(string) (string, error) {
	return func(requested string) (string, error) {
		requested = strings.TrimSpace(strings.ReplaceAll(requested, "\\", "/"))
		requested = strings.TrimPrefix(requested, "./")
		requested = strings.Trim(requested, "/")
		parts := strings.SplitN(requested, "/", 2)
		if requested == "" || requested == "." || len(parts) == 0 {
			return "", tools.SafeError{Code: "path_outside_workspace", Message: "path must include a workspace folder label"}
		}
		var root tools.WorkspaceRoot
		for _, candidate := range roots {
			if strings.EqualFold(candidate.Label, parts[0]) {
				root = candidate
				break
			}
		}
		if root.ID == "" {
			return "", tools.SafeError{Code: "path_outside_workspace", Message: "workspace folder was not found"}
		}
		relative := ""
		if len(parts) == 2 {
			relative = parts[1]
		}
		ref := workspacefs.FileRef{RootID: root.ID, Path: relative}
		var resolved string
		var err error
		if child {
			resolved, err = s.fs.ResolveEntryHostPath(workspaceID, ref)
		} else {
			resolved, err = s.fs.ResolveExistingHostPath(workspaceID, ref, true)
		}
		if err == nil {
			return resolved, nil
		}
		var confined *workspacefs.Error
		if errors.As(err, &confined) {
			return "", tools.SafeError{Code: confined.Code, Message: confined.Message}
		}
		return "", err
	}
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
