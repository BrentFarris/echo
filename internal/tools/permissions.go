package tools

import (
	"path/filepath"
	"strings"
)

// PathMatcher compiles a list of glob patterns into a reusable matcher for
// workspace-relative paths.  An empty pattern list means "allow all".
type PathMatcher struct {
	patterns []string
	matched  []pathPattern
}

type pathPattern struct {
	prefix string // directory prefix (matched with equalFold)
	glob   string // remaining glob pattern for the tail
}

// NewPathMatcher returns a matcher for the given allowlist of glob patterns.
// Patterns are workspace-relative and support ** (match any depth), * (match
// within one segment), and ? (single character).  An empty list allows all
// paths.
func NewPathMatcher(patterns []string) *PathMatcher {
	if len(patterns) == 0 {
		return &PathMatcher{}
	}

	matched := make([]pathPattern, 0, len(patterns))
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Normalize separators to forward slashes for consistent matching.
		p = filepath.ToSlash(p)
		matched = append(matched, pathPattern{glob: p})
	}

	return &PathMatcher{patterns: patterns, matched: matched}
}

// Matches reports whether the workspace-relative path matches any of the
// allowlist patterns.  An empty matcher (no patterns) returns true for all
// paths.
func (m *PathMatcher) Matches(path string) bool {
	if len(m.matched) == 0 {
		return true
	}

	path = filepath.ToSlash(filepath.Clean(path))

	for _, pp := range m.matched {
		if matchGlob(path, pp.glob) {
			return true
		}
	}
	return false
}

// Patterns returns the original allowlist of glob patterns.
func (m *PathMatcher) Patterns() []string {
	if m == nil {
		return nil
	}
	return m.patterns
}

// matchGlob checks whether a workspace-relative path matches a glob pattern.
// It supports:
//
//   - ** : matches any number of path segments (including zero)
//   - *  : matches within a single path segment
//   - ?  : matches a single character within a segment
func matchGlob(path, pattern string) bool {
	// Handle the special ** case by splitting on it.
	if strings.Contains(pattern, "**") {
		return matchDoubleStar(path, pattern)
	}
	return matchSingleGlob(path, pattern)
}

// matchDoubleStar handles patterns containing **.
func matchDoubleStar(path, pattern string) bool {
	parts := strings.Split(pattern, "**")

	switch len(parts) {
	case 1:
		// No ** found, fall through to single glob.
		return matchSingleGlob(path, pattern)
	case 2:
		prefix := strings.TrimRight(parts[0], "/")
		suffix := strings.TrimLeft(parts[1], "/")

		if prefix == "" && suffix == "" {
			return true // ** matches everything
		}

		if prefix != "" {
			// Path must start with the prefix.
			if !strings.HasPrefix(path, prefix) {
				return false
			}
			// If prefix doesn't end with a separator, ensure boundary.
			if !strings.HasSuffix(prefix, "/") && len(path) > len(prefix) {
				if path[len(prefix)] != '/' {
					return false
				}
			}
			path = path[len(prefix):]
			path = strings.TrimLeft(path, "/")
		}

		if suffix == "" {
			return true // prefix/** matches everything under prefix
		}

		// Try matching the suffix against every possible tail of the path.
		for i := 0; i <= len(path); i++ {
			tail := path[i:]
			if tail != "" && tail[0] == '/' {
				tail = tail[1:]
			}
			if matchSingleGlob(tail, suffix) {
				return true
			}
		}
		return false

	default:
		// Multiple ** — handle by recursive splitting.
		mid := strings.Index(pattern, "**")
		prefix := strings.TrimRight(pattern[:mid], "/")
		rest := strings.TrimLeft(pattern[mid+2:], "/")

		if prefix == "" {
			// Match rest against every tail.
			for i := 0; i <= len(path); i++ {
				tail := path[i:]
				if tail != "" && tail[0] == '/' {
					tail = tail[1:]
				}
				if matchDoubleStar(tail, rest) {
					return true
				}
			}
			return false
		}

		if !strings.HasPrefix(path, prefix) {
			return false
		}
		path = path[len(prefix):]
		path = strings.TrimLeft(path, "/")

		for i := 0; i <= len(path); i++ {
			tail := path[i:]
			if tail != "" && tail[0] == '/' {
				tail = tail[1:]
			}
			if matchDoubleStar(tail, rest) {
				return true
			}
		}
		return false
	}
}

// matchSingleGlob matches a path against a single-segment glob pattern using
// filepath.Match semantics but with forward slashes.
func matchSingleGlob(path, pattern string) bool {
	matched, err := filepath.Match(pattern, path)
	return matched && err == nil
}

// ToolPermission defines the path scope for a single tool.
// An empty Paths list means "allow all paths" for that tool.
type ToolPermission struct {
	Name  string   `json:"name"`
	Paths []string `json:"paths,omitempty"` // glob patterns; nil/empty means allow all
}

// PathMatcher returns the compiled matcher for this permission's path patterns.
// Returns nil if no paths are configured (allow all).
func (p *ToolPermission) PathMatcher() *PathMatcher {
	if len(p.Paths) == 0 {
		return nil
	}
	return NewPathMatcher(p.Paths)
}

// ToolScopeChecker evaluates per-tool permissions with optional path constraints.
// An empty checker (no permissions set) allows all tools and paths.
type ToolScopeChecker struct {
	permissions map[string]*ToolPermission // tool name -> permission config
	allowAll    bool                       // true when no restrictions were configured
}

// NewToolScopeChecker creates a checker from the given per-tool permissions.
// An empty list means "allow all tools with unrestricted paths".
func NewToolScopeChecker(permissions []ToolPermission) *ToolScopeChecker {
	if len(permissions) == 0 {
		return &ToolScopeChecker{allowAll: true}
	}

	m := make(map[string]*ToolPermission, len(permissions))
	for i := range permissions {
		p := &permissions[i]
		name := strings.TrimSpace(p.Name)
		if name != "" {
			m[name] = p
		}
	}

	return &ToolScopeChecker{permissions: m}
}

// NewDenyAllToolScopeChecker creates an explicit empty allowlist. It differs
// from NewToolScopeChecker(nil), which intentionally preserves legacy allow-all behavior.
func NewDenyAllToolScopeChecker() *ToolScopeChecker {
	return &ToolScopeChecker{permissions: make(map[string]*ToolPermission)}
}

// Allowed reports whether the given tool name and optional workspace-relative
// path are permitted.  An empty path argument skips path evaluation and checks
// only the tool name.  A nil or empty checker allows everything.
func (c *ToolScopeChecker) Allowed(toolName string, path string) bool {
	if c == nil || c.allowAll {
		return true
	}

	perm, ok := c.permissions[toolName]
	if !ok {
		return false
	}

	// Tool is allowed; now check path scope if a path was provided.
	if path != "" && len(perm.Paths) > 0 {
		return NewPathMatcher(perm.Paths).Matches(path)
	}
	return true
}

// HasTool reports whether the checker has an explicit entry for the given tool name.
func (c *ToolScopeChecker) HasTool(toolName string) bool {
	if c == nil || c.allowAll {
		return true
	}
	_, ok := c.permissions[toolName]
	return ok
}

// ToolPermissionChecker determines whether a tool name is allowed by a set of
// tool permissions.  An empty permission list means "allow all".
type ToolPermissionChecker struct {
	allowed map[string]bool
	all     bool
}

// NewToolPermissionChecker returns a checker for the given list of allowed
// tool names.  An empty list allows all tools.
func NewToolPermissionChecker(permissions []string) *ToolPermissionChecker {
	if len(permissions) == 0 {
		return &ToolPermissionChecker{all: true}
	}

	allowed := make(map[string]bool, len(permissions))
	for _, name := range permissions {
		name = strings.TrimSpace(name)
		if name != "" {
			allowed[name] = true
		}
	}

	return &ToolPermissionChecker{allowed: allowed}
}

// Allowed reports whether the given tool name is permitted.
func (c *ToolPermissionChecker) Allowed(toolName string) bool {
	if c.all {
		return true
	}
	return c.allowed[toolName]
}
