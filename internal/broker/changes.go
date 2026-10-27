package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/brent/echo/internal/domain"
)

// Snapshots contain fingerprints, not file contents. Limits keep a shell command
// in a large directory from indexing the host or delaying Stop indefinitely.
const changeEntryLimit = 20000
const changeScanTimeout = 2 * time.Second

type FileSnapshot struct {
	Files    map[string]string
	Complete bool
}

func changePath(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

func ignoredChangeEntry(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".hg", ".svn", ".fslckout", "_fossil_", "node_modules", ".cache", ".vs":
		return true
	}
	return false
}

// SnapshotFiles inspects explicit paths or a command's working directory through
// os.Root. Links in recursive scans are skipped; private state is never scanned.
func (f *Files) SnapshotFiles(ctx context.Context, bot domain.Bot, paths []string) FileSnapshot {
	ctx, cancel := context.WithTimeout(ctx, changeScanTimeout)
	defer cancel()
	snapshot := FileSnapshot{Files: map[string]string{}, Complete: true}
	visited := 0
	for _, path := range paths {
		root, relative, err := f.resolve(bot, path)
		if err != nil {
			snapshot.Complete = false
			continue
		}
		func() {
			defer root.Close()
			var visit func(string, int)
			visit = func(relative string, depth int) {
				if ctx.Err() != nil || visited >= changeEntryLimit || depth > 64 {
					snapshot.Complete = false
					return
				}
				visited++
				absolute := filepath.Join(root.Name(), relative)
				if f.Private != "" && Within(f.Private, absolute) {
					return
				}
				info, err := root.Lstat(relative)
				if os.IsNotExist(err) {
					return
				}
				if err != nil {
					snapshot.Complete = false
					return
				}
				if info.Mode()&os.ModeSymlink != 0 {
					return
				}
				if info.IsDir() {
					dir, err := root.Open(relative)
					if err != nil {
						snapshot.Complete = false
						return
					}
					defer dir.Close()
					for {
						entries, err := dir.ReadDir(128)
						for _, entry := range entries {
							if ignoredChangeEntry(entry.Name()) {
								continue
							}
							visit(filepath.Join(relative, entry.Name()), depth+1)
							if ctx.Err() != nil || visited >= changeEntryLimit {
								snapshot.Complete = false
								return
							}
						}
						if err == io.EOF {
							return
						}
						if err != nil {
							snapshot.Complete = false
							return
						}
					}
				}
				if !info.Mode().IsRegular() {
					return
				}
				fingerprint := fmt.Sprintf("metadata:%d:%d:%o", info.Size(), info.ModTime().UnixNano(), info.Mode().Perm())
				if info.Size() <= 1<<20 {
					file, err := root.Open(relative)
					if err != nil {
						snapshot.Complete = false
						return
					}
					hash := sha256.New()
					n, err := io.Copy(hash, io.LimitReader(file, (1<<20)+1))
					file.Close()
					if err != nil || n != info.Size() {
						snapshot.Complete = false
						return
					}
					fingerprint = "sha256:" + hex.EncodeToString(hash.Sum(nil)) + fmt.Sprintf(":%o", info.Mode().Perm())
				}
				snapshot.Files[changePath(absolute)] = fingerprint
			}
			visit(relative, 0)
		}()
	}
	return snapshot
}

func DiffFiles(bot domain.Bot, before, after FileSnapshot) []domain.FileChange {
	keys := map[string]bool{}
	for path := range before.Files {
		keys[path] = true
	}
	for path := range after.Files {
		keys[path] = true
	}
	paths := make([]string, 0, len(keys))
	for path := range keys {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	changes := []domain.FileChange{}
	for _, path := range paths {
		old, had := before.Files[path]
		next, has := after.Files[path]
		// An incomplete scan must never invent a creation or deletion.
		if old == next || !had && !before.Complete || !has && !after.Complete {
			continue
		}
		operation := "edited"
		if !had {
			operation = "created"
		} else if !has {
			operation = "deleted"
		}
		label := filepath.ToSlash(path)
		for _, root := range bot.Roots {
			if Within(root, path) {
				relative, _ := filepath.Rel(root, path)
				label = filepath.ToSlash(filepath.Join(filepath.Base(root), relative))
				break
			}
		}
		changes = append(changes, domain.FileChange{Path: path, DisplayPath: label, Operation: operation, Before: old, After: next})
	}
	return changes
}
