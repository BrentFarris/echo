package tools

import (
	"path/filepath"
	"strings"
)

// ToolPermission allows one tool, optionally restricted to workspace-relative
// glob paths. Empty Paths allows every path for that tool.
type ToolPermission struct {
	Name  string   `json:"name"`
	Paths []string `json:"paths,omitempty"`
}

// ToolScopeChecker evaluates an agent mode's explicit tool allowlist.
type ToolScopeChecker struct {
	permissions map[string]ToolPermission
	allowAll    bool
}

// NewToolScopeChecker treats an empty permission list as unrestricted, which
// is the persisted contract for Echo's General mode.
func NewToolScopeChecker(permissions []ToolPermission) *ToolScopeChecker {
	if len(permissions) == 0 {
		return &ToolScopeChecker{allowAll: true}
	}
	checker := &ToolScopeChecker{permissions: make(map[string]ToolPermission, len(permissions))}
	for _, permission := range permissions {
		name := strings.TrimSpace(permission.Name)
		if name != "" {
			permission.Name = name
			checker.permissions[name] = permission
		}
	}
	return checker
}

// NewDenyAllToolScopeChecker creates an explicit empty allowlist. It is used
// for child agents when the parent grants no compatible read-only tools.
func NewDenyAllToolScopeChecker() *ToolScopeChecker {
	return &ToolScopeChecker{permissions: make(map[string]ToolPermission)}
}

func (c *ToolScopeChecker) HasTool(name string) bool {
	if c == nil || c.allowAll {
		return true
	}
	_, ok := c.permissions[name]
	return ok
}

func (c *ToolScopeChecker) Allowed(name, path string) bool {
	if c == nil || c.allowAll {
		return true
	}
	permission, ok := c.permissions[name]
	if !ok {
		return false
	}
	if path == "" || len(permission.Paths) == 0 {
		return true
	}
	path = filepath.ToSlash(filepath.Clean(path))
	for _, pattern := range permission.Paths {
		if matchPathGlob(path, filepath.ToSlash(strings.TrimSpace(pattern))) {
			return true
		}
	}
	return false
}

func matchPathGlob(path, pattern string) bool {
	if pattern == "" {
		return false
	}
	if pattern == "**" {
		return true
	}
	if !strings.Contains(pattern, "**") {
		matched, err := filepath.Match(pattern, path)
		return err == nil && matched
	}
	parts := strings.SplitN(pattern, "**", 2)
	prefix := strings.TrimRight(parts[0], "/")
	suffix := strings.TrimLeft(parts[1], "/")
	if prefix != "" && path != prefix && !strings.HasPrefix(path, prefix+"/") {
		return false
	}
	remainder := strings.TrimLeft(strings.TrimPrefix(path, prefix), "/")
	if suffix == "" {
		return true
	}
	for i := 0; i <= len(remainder); i++ {
		if i > 0 && remainder[i-1] != '/' {
			continue
		}
		matched, err := filepath.Match(suffix, remainder[i:])
		if err == nil && matched {
			return true
		}
	}
	return false
}
