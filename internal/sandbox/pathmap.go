package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/brent/echo/internal/workspaces"
)

func WorkspaceMounts(workspace workspaces.Workspace) ([]RootMount, error) {
	folders := append([]string(nil), workspace.Folders...)
	if len(folders) == 0 && strings.TrimSpace(workspace.MainPath) != "" {
		folders = []string{workspace.MainPath}
	}
	seen := make(map[string]bool)
	mounts := make([]RootMount, 0, len(folders))
	for _, folder := range folders {
		absolute, err := filepath.Abs(strings.TrimSpace(folder))
		if err != nil {
			return nil, fmt.Errorf("resolve workspace mount: %w", err)
		}
		absolute = filepath.Clean(absolute)
		identity := absolute
		if realPath, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
			identity = filepath.Clean(realPath)
		}
		key := identity
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		digest := sha256.Sum256([]byte(key))
		id := hex.EncodeToString(digest[:8])
		mounts = append(mounts, RootMount{
			ID: id, HostPath: absolute, GuestPath: "/workspace/" + id,
			Main: sameHostPath(absolute, workspace.MainPath),
		})
	}
	if len(mounts) == 0 {
		return nil, fmt.Errorf("workspace has no available folders")
	}
	return mounts, nil
}

type PathMapper struct{ roots []RootMount }

func NewPathMapper(roots []RootMount) PathMapper {
	return PathMapper{roots: append([]RootMount(nil), roots...)}
}

func (m PathMapper) HostToGuest(hostPath string) (string, error) {
	absolute, err := filepath.Abs(hostPath)
	if err != nil {
		return "", err
	}
	for _, root := range m.roots {
		relative, within := hostRelative(root.HostPath, absolute)
		if within {
			if relative == "." || relative == "" {
				return root.GuestPath, nil
			}
			return path.Join(root.GuestPath, filepath.ToSlash(relative)), nil
		}
	}
	return "", fmt.Errorf("path is outside the registered workspace roots")
}

func (m PathMapper) GuestToHost(guestPath string) (string, error) {
	clean := path.Clean(strings.TrimSpace(strings.ReplaceAll(guestPath, "\\", "/")))
	if clean == "/workspace" || !strings.HasPrefix(clean, "/workspace/") {
		return "", fmt.Errorf("guest path is outside /workspace")
	}
	for _, root := range m.roots {
		if clean == root.GuestPath {
			return root.HostPath, nil
		}
		prefix := root.GuestPath + "/"
		if strings.HasPrefix(clean, prefix) {
			relative := strings.TrimPrefix(clean, prefix)
			host := filepath.Join(root.HostPath, filepath.FromSlash(relative))
			if _, within := hostRelative(root.HostPath, host); !within {
				return "", fmt.Errorf("guest path escapes its workspace root")
			}
			return filepath.Clean(host), nil
		}
	}
	return "", fmt.Errorf("guest workspace root is not registered")
}

func (m PathMapper) HostURIToGuest(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "file" {
		return value, err
	}
	hostPath, err := url.PathUnescape(parsed.Path)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" && len(hostPath) >= 3 && hostPath[0] == '/' && hostPath[2] == ':' {
		hostPath = hostPath[1:]
	}
	guestPath, err := m.HostToGuest(filepath.FromSlash(hostPath))
	if err != nil {
		return "", err
	}
	return (&url.URL{Scheme: "file", Path: guestPath}).String(), nil
}

func (m PathMapper) GuestURIToHost(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "file" {
		return value, err
	}
	guestPath, err := url.PathUnescape(parsed.Path)
	if err != nil {
		return "", err
	}
	hostPath, err := m.GuestToHost(guestPath)
	if err != nil {
		return "", err
	}
	uriPath := filepath.ToSlash(hostPath)
	if runtime.GOOS == "windows" && filepath.VolumeName(hostPath) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	return (&url.URL{Scheme: "file", Path: uriPath}).String(), nil
}

// TranslateJSON recursively rewrites file: URIs while preserving every other
// JSON value verbatim. It is used at the LSP transport boundary for params,
// results, diagnostics, locations, edits, and server requests.
func (m PathMapper) TranslateJSON(data json.RawMessage, hostToGuest bool) (json.RawMessage, error) {
	if len(data) == 0 || string(data) == "null" {
		return data, nil
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	translated, err := m.translateValue(value, hostToGuest)
	if err != nil {
		return nil, err
	}
	return json.Marshal(translated)
}

func (m PathMapper) TranslateValue(value any, hostToGuest bool) (any, error) {
	return m.translateValue(value, hostToGuest)
}

func (m PathMapper) translateValue(value any, hostToGuest bool) (any, error) {
	switch typed := value.(type) {
	case string:
		if !strings.HasPrefix(strings.ToLower(typed), "file:/") {
			return typed, nil
		}
		if hostToGuest {
			return m.HostURIToGuest(typed)
		}
		return m.GuestURIToHost(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			translated, err := m.translateValue(item, hostToGuest)
			if err != nil {
				return nil, err
			}
			result[index] = translated
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			translated, err := m.translateValue(item, hostToGuest)
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

func hostRelative(root, candidate string) (string, bool) {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	comparisonRoot, comparisonCandidate := root, candidate
	if runtime.GOOS == "windows" {
		comparisonRoot = strings.ToLower(root)
		comparisonCandidate = strings.ToLower(candidate)
	}
	comparisonRelative, err := filepath.Rel(comparisonRoot, comparisonCandidate)
	if err != nil || comparisonRelative == ".." || strings.HasPrefix(comparisonRelative, ".."+string(filepath.Separator)) || filepath.IsAbs(comparisonRelative) {
		return "", false
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return "", false
	}
	return relative, true
}

func sameHostPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
