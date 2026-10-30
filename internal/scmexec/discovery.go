package scmexec

import (
	"context"
	"fmt"
	ignore "github.com/sabhiram/go-gitignore"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Discover scans directory entries, never file contents or linked directories.
// An incomplete scan is reported instead of presenting an empty workspace.
func Discover(ctx context.Context, root string, marker func(string) bool, allowed ...func(string) bool) ([]string, error) {
	candidates := []string{}
	count := 0
	var matcher *ignore.GitIgnore
	ignorePath := filepath.Join(root, ".gitignore")
	if info, err := os.Lstat(ignorePath); err == nil && info.Mode().IsRegular() && (len(allowed) == 0 || allowed[0](ignorePath)) {
		matcher, _ = ignore.CompileIgnoreFile(ignorePath)
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if len(allowed) > 0 && !allowed[0](path) {
			return filepath.SkipDir
		}
		if path != root {
			relative, _ := filepath.Rel(root, path)
			if matcher != nil && matcher.MatchesPath(filepath.ToSlash(relative)+"/") {
				return filepath.SkipDir
			}
			switch strings.ToLower(entry.Name()) {
			case ".git", ".hg", ".svn", ".echo", "node_modules", ".cache", "vendor", ".venv":
				return filepath.SkipDir
			}
		}
		count++
		if count > 8192 {
			return fmt.Errorf("repository discovery exceeded 8192 folders; add the desired repository as a workspace folder")
		}
		if marker(path) {
			candidates = append(candidates, path)
		}
		return nil
	})
	return candidates, err
}
