package services

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/brent/echo/internal/tools"
)

const workspaceIgnoreCheckTimeout = 3 * time.Second

type rootGitignorePattern struct {
	pattern  string
	negated  bool
	dirOnly  bool
	anchored bool
	hasSlash bool
}

func cleanChangePath(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	for strings.Contains(path, "//") {
		path = strings.ReplaceAll(path, "//", "/")
	}
	path = strings.TrimPrefix(path, "./")
	return path
}

func ignoredWorkspaceChangePaths(workspacePath string, changes []tools.FileChange) map[string]bool {
	paths := make([]string, 0, len(changes))
	seen := map[string]bool{}
	for _, change := range changes {
		path := cleanChangePath(change.Path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	if len(paths) == 0 || strings.TrimSpace(workspacePath) == "" {
		return nil
	}
	if ignored, err := gitIgnoredWorkspacePaths(workspacePath, paths); err == nil {
		return ignored
	}
	return rootGitignoreIgnoredPaths(workspacePath, paths)
}

func gitIgnoredWorkspacePaths(workspacePath string, paths []string) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), workspaceIgnoreCheckTimeout)
	defer cancel()

	commandArgs := []string{
		"-c", "safe.directory=*",
		"-c", "core.quotepath=false",
		"-C", workspacePath,
		"check-ignore",
		"--no-index",
		"--stdin",
	}
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\n") + "\n")
	configureWorkspaceCommandProcess(cmd)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return map[string]bool{}, nil
		}
		return nil, err
	}

	ignored := map[string]bool{}
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		path := cleanChangePath(string(bytes.TrimSpace(line)))
		if path != "" {
			ignored[path] = true
		}
	}
	return ignored, nil
}

func rootGitignoreIgnoredPaths(workspacePath string, paths []string) map[string]bool {
	patterns, err := loadRootGitignorePatterns(workspacePath)
	if err != nil || len(patterns) == 0 {
		return map[string]bool{}
	}
	ignored := map[string]bool{}
	for _, candidate := range paths {
		matched := false
		for _, pattern := range patterns {
			if rootGitignorePatternMatches(pattern, candidate) {
				matched = !pattern.negated
			}
		}
		if matched {
			ignored[candidate] = true
		}
	}
	return ignored
}

func loadRootGitignorePatterns(workspacePath string) ([]rootGitignorePattern, error) {
	data, err := os.ReadFile(filepath.Join(workspacePath, ".gitignore"))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	patterns := make([]rootGitignorePattern, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, `\#`) {
			line = line[1:]
		}
		negated := false
		if strings.HasPrefix(line, "!") {
			negated = true
			line = strings.TrimSpace(line[1:])
		} else if strings.HasPrefix(line, `\!`) {
			line = line[1:]
		}
		line = strings.ReplaceAll(line, "\\", "/")
		anchored := strings.HasPrefix(line, "/")
		line = strings.TrimPrefix(line, "/")
		dirOnly := strings.HasSuffix(line, "/")
		line = strings.TrimSuffix(line, "/")
		if line == "" {
			continue
		}
		patterns = append(patterns, rootGitignorePattern{
			pattern:  line,
			negated:  negated,
			dirOnly:  dirOnly,
			anchored: anchored,
			hasSlash: strings.Contains(line, "/"),
		})
	}
	return patterns, nil
}

func rootGitignorePatternMatches(pattern rootGitignorePattern, candidate string) bool {
	candidate = cleanChangePath(candidate)
	if candidate == "" {
		return false
	}
	if pattern.dirOnly {
		for _, dir := range candidateDirectories(candidate) {
			if rootGitignorePatternMatches(rootGitignorePattern{
				pattern:  pattern.pattern,
				anchored: pattern.anchored,
				hasSlash: pattern.hasSlash,
			}, dir) {
				return true
			}
		}
		return false
	}
	if !pattern.hasSlash && !pattern.anchored {
		for _, part := range strings.Split(candidate, "/") {
			if gitignoreGlobMatch(pattern.pattern, part) {
				return true
			}
		}
		return false
	}
	if gitignoreGlobMatch(pattern.pattern, candidate) {
		return true
	}
	if !pattern.anchored && pattern.hasSlash {
		parts := strings.Split(candidate, "/")
		for i := 1; i < len(parts); i++ {
			if gitignoreGlobMatch(pattern.pattern, strings.Join(parts[i:], "/")) {
				return true
			}
		}
	}
	return false
}

func candidateDirectories(candidate string) []string {
	parts := strings.Split(candidate, "/")
	if len(parts) <= 1 {
		return nil
	}
	dirs := make([]string, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		dirs = append(dirs, strings.Join(parts[:i], "/"))
	}
	return dirs
}

func gitignoreGlobMatch(pattern string, candidate string) bool {
	expression := gitignoreGlobRegexp(pattern)
	matched, err := regexp.MatchString(expression, candidate)
	return err == nil && matched
}

func gitignoreGlobRegexp(pattern string) string {
	var builder strings.Builder
	builder.WriteString("^")
	for i := 0; i < len(pattern); {
		char := pattern[i]
		switch char {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i += 2
				if i < len(pattern) && pattern[i] == '/' {
					i++
					builder.WriteString("(?:.*/)?")
				} else {
					builder.WriteString(".*")
				}
				continue
			}
			builder.WriteString("[^/]*")
		case '?':
			builder.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(pattern[i+1:], ']')
			if end >= 0 {
				class := pattern[i : i+end+2]
				builder.WriteString(class)
				i += end + 2
				continue
			}
			builder.WriteString(regexp.QuoteMeta(string(char)))
		default:
			builder.WriteString(regexp.QuoteMeta(string(char)))
		}
		i++
	}
	builder.WriteString("$")
	return builder.String()
}
