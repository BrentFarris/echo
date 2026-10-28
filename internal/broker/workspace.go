package broker

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/brent/echo/internal/domain"
)

// OpenWorkspace opens exactly one explicitly selected workspace. The returned
// handle confines every relative operation to that workspace, even when another
// bot root is its parent. The caller must close it.
func (f *Files) OpenWorkspace(bot domain.Bot, workspace string) (*os.Root, error) {
	if !filepath.IsAbs(workspace) {
		return nil, errors.New("workspace must be an explicit absolute directory")
	}
	for _, part := range strings.FieldsFunc(workspace, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return nil, errors.New("workspace cannot contain parent traversal")
		}
	}
	roots, err := CanonicalRoots([]string{workspace})
	if err != nil {
		return nil, err
	}
	workspace = roots[0]
	if len(bot.Roots) > 0 {
		found := false
		for _, allowed := range bot.Roots {
			if workspace == allowed || runtime.GOOS == "windows" && strings.EqualFold(workspace, allowed) {
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("workspace must match a configured bot root")
		}
	}
	if f.Private != "" && (Within(workspace, f.Private) || Within(f.Private, workspace)) {
		return nil, errors.New("workspace cannot contain Echo private state")
	}
	// Use the same rooted access boundary as file tools before narrowing it.
	r, p, err := f.resolve(bot, workspace)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.OpenRoot(p)
}

// WriteWorkspaceFile is a conditional save shared by every bot using this
// broker. An empty revision means create-only, unlike the general file tool.
func (f *Files) WriteWorkspaceFile(bot domain.Bot, workspace, relative, content, expected string) (bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !filepath.IsLocal(relative) || len(content) > 1<<20 {
		return false, false, errors.New("invalid workspace file or content size")
	}
	r, err := f.OpenWorkspace(bot, workspace)
	if err != nil {
		return false, false, err
	}
	defer r.Close()
	if info, statErr := r.Lstat(relative); statErr == nil {
		if !info.Mode().IsRegular() {
			return false, false, errors.New("skill destination must be a regular file, not a link or directory")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return false, false, statErr
	}
	file, err := r.Open(relative)
	created := errors.Is(err, os.ErrNotExist)
	if err != nil && !created {
		return false, false, err
	}
	if !created {
		defer file.Close()
		if expected == "" {
			return false, false, errors.New("skill already exists; read it and supply its revision before updating")
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return false, false, errors.New("skill must be a regular file")
		}
		data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		if err != nil {
			return false, false, err
		}
		sum := sha256.Sum256(data)
		if len(data) > 1<<20 || hex.EncodeToString(sum[:]) != expected {
			return false, false, errors.New("skill changed since it was read; read it again before updating")
		}
		if string(data) == content {
			return false, true, nil
		}
		file.Close()
	} else if expected != "" {
		return false, false, errors.New("skill no longer exists; create it without a revision")
	}
	if err := r.MkdirAll(filepath.Dir(relative), 0755); err != nil {
		return false, false, err
	}
	_, err = writeRootFile(r, relative, filepath.Join(workspace, relative), content)
	return created, false, err
}
