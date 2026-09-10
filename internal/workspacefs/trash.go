package workspacefs

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type TrashItem struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	Ref         FileRef   `json:"ref"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	HostPath    string    `json:"hostPath"`
	DeletedAt   time.Time `json:"deletedAt"`
}

type trashMetadata struct {
	TrashItem
	RootHostPath string `json:"rootHostPath"`
}

func (s *Service) Trash(workspaceID string, ref FileRef) (TrashItem, error) {
	if s.wouldAffectProtectedMetadata(workspaceID, ref) {
		return TrashItem{}, protectedMetadataError()
	}
	root, source, visible, err := s.resolveEntry(workspaceID, ref, false, false)
	if err != nil {
		return TrashItem{}, err
	}
	unlock := s.lockPaths(source)
	defer unlock()
	info, err := os.Lstat(source)
	if err != nil {
		return TrashItem{}, err
	}
	id, err := trashID()
	if err != nil {
		return TrashItem{}, err
	}
	kind := "file"
	if info.IsDir() {
		kind = "directory"
	}
	item := TrashItem{
		ID: id, WorkspaceID: workspaceID, Ref: ref, Name: filepath.Base(visible),
		Kind: kind, HostPath: visible, DeletedAt: time.Now().UTC(),
	}
	directory := s.trashItemDir(workspaceID, id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return TrashItem{}, fmt.Errorf("create trash item: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(directory)
		}
	}()
	metadata, err := json.MarshalIndent(trashMetadata{TrashItem: item, RootHostPath: root.HostPath}, "", "  ")
	if err != nil {
		return TrashItem{}, err
	}
	if err := atomicWrite(filepath.Join(directory, "meta.json"), metadata, 0o600); err != nil {
		return TrashItem{}, fmt.Errorf("write trash metadata: %w", err)
	}
	payload := filepath.Join(directory, "payload")
	if err := os.Rename(source, payload); err != nil {
		if copyErr := copyPath(source, payload); copyErr != nil {
			return TrashItem{}, fmt.Errorf("move item to trash: %w", copyErr)
		}
		if err := verifyCopy(source, payload); err != nil {
			return TrashItem{}, fmt.Errorf("verify trash copy: %w", err)
		}
		// The verified payload must survive even if source cleanup is only
		// partially successful. This prevents a cross-volume delete failure
		// from destroying the last complete copy of an item.
		cleanup = false
		if err := os.RemoveAll(source); err != nil {
			return item, fmt.Errorf("remove original after verified trash copy: %w", err)
		}
	}
	cleanup = false
	s.index.ApplyChanges(workspaceID, []Change{{Op: "delete", Ref: ref}})
	return item, nil
}

func (s *Service) ListTrash(workspaceID string) ([]TrashItem, error) {
	base := s.trashWorkspaceDir(workspaceID)
	children, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return []TrashItem{}, nil
	}
	if err != nil {
		return nil, err
	}
	items := make([]TrashItem, 0, len(children))
	for _, child := range children {
		if !child.IsDir() || !validTrashID(child.Name()) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(base, child.Name(), "payload")); err != nil {
			continue
		}
		metadata, err := s.readTrashMetadata(workspaceID, child.Name())
		if err == nil {
			items = append(items, metadata.TrashItem)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DeletedAt.After(items[j].DeletedAt) })
	return items, nil
}

func (s *Service) Restore(workspaceID, id string) (Entry, error) {
	if !validTrashID(id) {
		return Entry{}, &Error{Code: "trash_not_found", Message: "trash item not found", Cause: ErrNotFound}
	}
	metadata, err := s.readTrashMetadata(workspaceID, id)
	if err != nil {
		return Entry{}, &Error{Code: "trash_not_found", Message: "trash item not found", Cause: err}
	}
	_, destination, _, err := s.resolveEntry(workspaceID, metadata.Ref, false, true)
	if err != nil {
		return Entry{}, err
	}
	itemDirectory := s.trashItemDir(workspaceID, id)
	unlock := s.lockPaths(destination, itemDirectory)
	defer unlock()
	if _, err := os.Lstat(destination); err == nil {
		return Entry{}, &Error{Code: "restore_collision", Message: "the original path is already occupied", Cause: ErrAlreadyExists}
	} else if !os.IsNotExist(err) {
		return Entry{}, err
	}
	payload := filepath.Join(itemDirectory, "payload")
	if _, err := os.Lstat(payload); err != nil {
		return Entry{}, &Error{Code: "trash_not_found", Message: "trash payload is missing", Cause: err}
	}
	if err := os.Rename(payload, destination); err != nil {
		if copyErr := copyPath(payload, destination); copyErr != nil {
			return Entry{}, fmt.Errorf("restore trash item: %w", copyErr)
		}
		if err := verifyCopy(payload, destination); err != nil {
			_ = os.RemoveAll(destination)
			return Entry{}, fmt.Errorf("verify restored item: %w", err)
		}
		if err := os.RemoveAll(payload); err != nil {
			// The destination has already been verified. Keep that complete copy
			// and make cleanup best-effort rather than deleting it and risking
			// data loss after a partially successful payload removal.
			_ = os.RemoveAll(itemDirectory)
		}
	}
	_ = os.RemoveAll(itemDirectory)
	s.index.ApplyChanges(workspaceID, []Change{{Op: "create", Ref: metadata.Ref}})
	return s.entryFor(workspaceID, metadata.Ref)
}

func (s *Service) PurgeTrash(workspaceID, id string) error {
	if !validTrashID(id) {
		return &Error{Code: "trash_not_found", Message: "trash item not found", Cause: ErrNotFound}
	}
	directory := s.trashItemDir(workspaceID, id)
	unlock := s.lockPaths(directory)
	defer unlock()
	if _, err := os.Stat(filepath.Join(directory, "meta.json")); err != nil {
		return &Error{Code: "trash_not_found", Message: "trash item not found", Cause: err}
	}
	return os.RemoveAll(directory)
}

func (s *Service) readTrashMetadata(workspaceID, id string) (trashMetadata, error) {
	data, err := os.ReadFile(filepath.Join(s.trashItemDir(workspaceID, id), "meta.json"))
	if err != nil {
		return trashMetadata{}, err
	}
	var metadata trashMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return trashMetadata{}, err
	}
	if metadata.WorkspaceID != workspaceID || metadata.ID != id {
		return trashMetadata{}, errorsNew("trash metadata does not match its container")
	}
	return metadata, nil
}

func (s *Service) trashWorkspaceDir(workspaceID string) string {
	digest := sha256.Sum256([]byte(workspaceID))
	return filepath.Join(filepath.Dir(s.dataPath), "trash", hex.EncodeToString(digest[:16]))
}

func (s *Service) trashItemDir(workspaceID, id string) string {
	return filepath.Join(s.trashWorkspaceDir(workspaceID), id)
}

func trashID() (string, error) {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d-%s", time.Now().UTC().UnixMilli(), hex.EncodeToString(random)), nil
}

func validTrashID(id string) bool {
	if id == "" || strings.ContainsAny(id, `/\\.`) {
		return false
	}
	for _, character := range id {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') && character != '-' {
			return false
		}
	}
	return true
}

func copyPath(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		return os.Symlink(target, destination)
	}
	if info.IsDir() {
		if err := os.Mkdir(destination, info.Mode().Perm()); err != nil {
			return err
		}
		children, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := copyPath(filepath.Join(source, child.Name()), filepath.Join(destination, child.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func verifyCopy(source, destination string) error {
	sourceManifest, err := pathManifest(source)
	if err != nil {
		return err
	}
	destinationManifest, err := pathManifest(destination)
	if err != nil {
		return err
	}
	if len(sourceManifest) != len(destinationManifest) {
		return fmt.Errorf("entry count differs")
	}
	for key, value := range sourceManifest {
		if destinationManifest[key] != value {
			return fmt.Errorf("entry %q differs", key)
		}
	}
	return nil
}

func pathManifest(root string) (map[string]string, error) {
	manifest := make(map[string]string)
	err := filepath.Walk(root, func(current string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		value := fmt.Sprintf("%s:%04o:%d", info.Mode().Type().String(), info.Mode().Perm(), info.Size())
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, readErr := os.Readlink(current)
			if readErr != nil {
				return readErr
			}
			digest := sha256.Sum256([]byte(target))
			value += ":" + hex.EncodeToString(digest[:])
		case info.Mode().IsRegular():
			file, openErr := os.Open(current)
			if openErr != nil {
				return openErr
			}
			hash := sha256.New()
			_, copyErr := io.Copy(hash, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			value += ":" + hex.EncodeToString(hash.Sum(nil))
		}
		manifest[filepath.ToSlash(relative)] = value
		return nil
	})
	return manifest, err
}

func errorsNew(message string) error { return fmt.Errorf("%s", message) }
