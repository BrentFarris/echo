package debugger

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/workspacefs"
)

func (s *Service) resolveSource(workspaceID string, ref debugconfig.SourceRef) (string, error) {
	return s.fs.ResolveExistingHostPath(workspaceID, workspacefs.FileRef{RootID: ref.RootID, Path: ref.Path}, false)
}

func (s *Service) fileRefForPath(workspaceID, path string) *debugconfig.SourceRef {
	roots, err := s.fs.Roots(workspaceID)
	if err != nil {
		return nil
	}
	clean, err := filepath.Abs(path)
	if err != nil {
		return nil
	}
	for _, root := range roots {
		base, baseErr := filepath.Abs(root.HostPath)
		if baseErr != nil {
			continue
		}
		relative, relErr := filepath.Rel(base, clean)
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		return &debugconfig.SourceRef{RootID: root.ID, Path: filepath.ToSlash(relative)}
	}
	return nil
}

func (s *Service) adapterPathToHost(workspaceID, path string) string {
	if path == "" {
		return ""
	}
	workspace, err := s.workspace(workspaceID)
	if err == nil && workspace.Sandbox.Enabled {
		if manager := s.sandboxManager(); manager != nil {
			if host, mapErr := manager.GuestToHost(workspaceID, path); mapErr == nil {
				return host
			}
		}
	}
	return path
}

func (s *Service) hostPathToAdapter(workspaceID, path string) string {
	workspace, err := s.workspace(workspaceID)
	if err == nil && workspace.Sandbox.Enabled {
		if manager := s.sandboxManager(); manager != nil {
			if guest, mapErr := manager.HostToGuest(workspaceID, path); mapErr == nil {
				return guest
			}
		}
	}
	return path
}

// translateDAPBody adds stable Echo file references while retaining ordinary
// DAP source objects, and converts guest paths back to host paths.
func (s *Service) translateDAPBody(workspaceID string, body json.RawMessage) json.RawMessage {
	if len(body) == 0 {
		return body
	}
	var value any
	if json.Unmarshal(body, &value) != nil {
		return body
	}
	translateSourceNodes(value, func(path string) (string, *debugconfig.SourceRef) {
		host := s.adapterPathToHost(workspaceID, path)
		return host, s.fileRefForPath(workspaceID, host)
	})
	translated, err := json.Marshal(value)
	if err != nil {
		return body
	}
	return translated
}

func (s *Service) translateDAPArguments(workspaceID string, value any) (any, error) {
	switch current := value.(type) {
	case []any:
		result := make([]any, len(current))
		for index, item := range current {
			translated, err := s.translateDAPArguments(workspaceID, item)
			if err != nil {
				return nil, err
			}
			result[index] = translated
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(current))
		for key, item := range current {
			result[key] = item
		}
		if rawRef, ok := result["echoRef"].(map[string]any); ok {
			rootID, _ := rawRef["rootId"].(string)
			path, _ := rawRef["path"].(string)
			host, err := s.resolveSource(workspaceID, debugconfig.SourceRef{RootID: rootID, Path: path})
			if err != nil {
				return nil, err
			}
			result["path"] = s.hostPathToAdapter(workspaceID, host)
			delete(result, "echoRef")
		} else if path, ok := result["path"].(string); ok && (result["name"] != nil || result["sourceReference"] != nil) {
			result["path"] = s.hostPathToAdapter(workspaceID, path)
		}
		for key, item := range result {
			translated, err := s.translateDAPArguments(workspaceID, item)
			if err != nil {
				return nil, err
			}
			result[key] = translated
		}
		return result, nil
	default:
		return value, nil
	}
}

func translateSourceNodes(value any, resolve func(string) (string, *debugconfig.SourceRef)) {
	switch current := value.(type) {
	case []any:
		for _, entry := range current {
			translateSourceNodes(entry, resolve)
		}
	case map[string]any:
		if path, ok := current["path"].(string); ok && (current["sourceReference"] != nil || current["name"] != nil) {
			host, ref := resolve(path)
			current["path"] = host
			if ref != nil {
				current["echoRef"] = ref
			}
		}
		for _, entry := range current {
			translateSourceNodes(entry, resolve)
		}
	}
}
