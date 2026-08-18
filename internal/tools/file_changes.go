package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"unicode/utf8"
)

const (
	FileChangeCreated = "created"
	FileChangeEdited  = "edited"
	FileChangeDeleted = "deleted"
)

// FileChange describes a single file mutation produced by a tool so the caller
// (e.g. the chat loop) can surface workspace changes to the user.
type FileChange struct {
	Path      string        `json:"path"`
	Operation string        `json:"operation"`
	Before    *FileSnapshot `json:"before,omitempty"`
	After     *FileSnapshot `json:"after,omitempty"`
}

// FileSnapshot captures the state of a file before or after a mutation.
type FileSnapshot struct {
	Path          string `json:"path"`
	Exists        bool   `json:"exists"`
	Bytes         int64  `json:"bytes,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	Text          string `json:"text,omitempty"`
	TextAvailable bool   `json:"textAvailable,omitempty"`
	Binary        bool   `json:"binary,omitempty"`
	Large         bool   `json:"large,omitempty"`
}

// FileChangeSink receives the file changes a tool recorded during execution.
type FileChangeSink func([]FileChange)

func (c ExecutionContext) recordFileChanges(changes ...FileChange) {
	if c.FileChanges == nil || len(changes) == 0 {
		return
	}
	filtered := make([]FileChange, 0, len(changes))
	for _, change := range changes {
		if change.Path == "" || IsIgnoredChangePath(change.Path) || changeSnapshotsEqual(change.Before, change.After) {
			continue
		}
		filtered = append(filtered, change)
	}
	if len(filtered) > 0 {
		c.FileChanges(filtered)
	}
}

func fileChangeForPath(ctx ExecutionContext, absolutePath string, before *FileSnapshot, after *FileSnapshot) FileChange {
	path := ""
	if after != nil {
		path = after.Path
	} else if before != nil {
		path = before.Path
	} else {
		path = relativeWorkspacePath(ctx, absolutePath)
	}
	operation := FileChangeEdited
	switch {
	case before == nil && after != nil:
		operation = FileChangeCreated
	case before != nil && after == nil:
		operation = FileChangeDeleted
	}
	return FileChange{
		Path:      path,
		Operation: operation,
		Before:    before,
		After:     after,
	}
}

func snapshotExistingFile(ctx ExecutionContext, absolutePath string) (*FileSnapshot, error) {
	info, err := os.Stat(absolutePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil
	}
	return readFileSnapshot(ctx, absolutePath, info)
}

func readFileSnapshot(ctx ExecutionContext, absolutePath string, info os.FileInfo) (*FileSnapshot, error) {
	return readFileSnapshotContext(ctx.context(), ctx, absolutePath, info)
}

func readFileSnapshotContext(runContext context.Context, ctx ExecutionContext, absolutePath string, info os.FileInfo) (*FileSnapshot, error) {
	path := relativeWorkspacePath(ctx, absolutePath)
	snapshot := &FileSnapshot{
		Path:   path,
		Exists: true,
		Bytes:  info.Size(),
	}
	if info.Size() > maxTextFileBytes {
		hash, err := hashFile(runContext, absolutePath)
		if err != nil {
			return nil, err
		}
		snapshot.SHA256 = hash
		snapshot.Large = true
		return snapshot, nil
	}

	data, err := os.ReadFile(absolutePath)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	snapshot.SHA256 = hex.EncodeToString(sum[:])
	if !isTextLike(data) || !utf8.Valid(data) {
		snapshot.Binary = true
		return snapshot, nil
	}
	snapshot.Text = string(data)
	snapshot.TextAvailable = true
	return snapshot, nil
}

func hashFile(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			if _, err := hash.Write(buffer[:count]); err != nil {
				return "", err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func changeSnapshotsEqual(before *FileSnapshot, after *FileSnapshot) bool {
	if before == nil && after == nil {
		return true
	}
	if before == nil || after == nil {
		return false
	}
	return before.Exists == after.Exists && before.SHA256 != "" && before.SHA256 == after.SHA256
}

func snapshotHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
