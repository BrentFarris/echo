package editorfs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/brent/echo/internal/mutation"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type TrashItem struct {
	Registration mutation.Result `json:"registration,omitempty"`
	ID           string          `json:"id"`
	BotID        string          `json:"botId"`
	Ref          FileRef         `json:"ref"`
	Name         string          `json:"name"`
	Kind         string          `json:"kind"`
	HostPath     string          `json:"hostPath"`
	DeletedAt    time.Time       `json:"deletedAt"`
}

type trashMetadata struct {
	TrashItem
	RootHostPath string `json:"rootHostPath"`
}

func (s *Service) Trash(botID string, ref FileRef) (TrashItem, error) {
	return s.TrashContext(context.Background(), botID, ref)
}

func (s *Service) TrashContext(ctx context.Context, botID string, ref FileRef) (TrashItem, error) {
	if s.wouldAffectProtectedMetadata(botID, ref) {
		return TrashItem{}, protectedMetadataError()
	}
	root, source, visible, err := s.resolveEntry(botID, ref, false, false)
	if err != nil {
		return TrashItem{}, err
	}
	unlock := s.lockPaths(source)
	defer unlock()
	info, err := s.lstat(botID, source)
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
		ID: id, BotID: botID, Ref: ref, Name: filepath.Base(visible),
		Kind: kind, HostPath: visible, DeletedAt: time.Now().UTC(),
	}
	directory := s.trashItemDir(botID, id)
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
	mutationErr := s.mutateContext(ctx, Operation{BotID: botID, Path: source, Kind: "delete", Origin: "trash", RecoveryID: id, Registration: &item.Registration}, func() error {
		sourceRoot, name, err := s.access(botID, source)
		if err != nil {
			return err
		}
		defer sourceRoot.Close()
		trashRoot, err := os.OpenRoot(directory)
		if err != nil {
			return err
		}
		defer trashRoot.Close()
		if err := copyRootPath(sourceRoot, name, trashRoot, "payload"); err != nil {
			return err
		}
		if err := verifyRootCopy(sourceRoot, name, trashRoot, "payload"); err != nil {
			return err
		}
		// Keep recovery data even if removing the original fails partway.
		cleanup = false
		return sourceRoot.RemoveAll(name)
	})
	if mutationErr != nil {
		return item, mutationErr
	}
	cleanup = false
	s.index.ApplyChanges(botID, []Change{{Op: "delete", Ref: ref}})
	return item, nil
}

func (s *Service) ListTrash(botID string) ([]TrashItem, error) {
	base := s.trashWorkspaceDir(botID)
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
		metadata, err := s.readTrashMetadata(botID, child.Name())
		if err == nil {
			items = append(items, metadata.TrashItem)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DeletedAt.After(items[j].DeletedAt) })
	return items, nil
}

func (s *Service) Restore(botID, id string) (Entry, error) {
	return s.RestoreContext(context.Background(), botID, id)
}

func (s *Service) RestoreContext(ctx context.Context, botID, id string) (Entry, error) {
	if !validTrashID(id) {
		return Entry{}, &Error{Code: "trash_not_found", Message: "trash item not found", Cause: ErrNotFound}
	}
	metadata, err := s.readTrashMetadata(botID, id)
	if err != nil {
		return Entry{}, &Error{Code: "trash_not_found", Message: "trash item not found", Cause: err}
	}
	_, destination, _, err := s.resolveEntry(botID, metadata.Ref, false, true)
	if err != nil {
		return Entry{}, err
	}
	itemDirectory := s.trashItemDir(botID, id)
	unlock := s.lockPaths(destination, itemDirectory)
	defer unlock()
	if _, err := s.lstat(botID, destination); err == nil {
		return Entry{}, &Error{Code: "restore_collision", Message: "the original path is already occupied", Cause: ErrAlreadyExists}
	} else if !os.IsNotExist(err) {
		return Entry{}, err
	}
	payload := filepath.Join(itemDirectory, "payload")
	if _, err := os.Lstat(payload); err != nil {
		return Entry{}, &Error{Code: "trash_not_found", Message: "trash payload is missing", Cause: err}
	}
	var registration mutation.Result
	mutationErr := s.mutateContext(ctx, Operation{BotID: botID, Path: destination, Kind: "restore", Origin: "trash", RecoveryID: id, Registration: &registration}, func() error {
		targetRoot, name, err := s.access(botID, destination)
		if err != nil {
			return err
		}
		defer targetRoot.Close()
		trashRoot, err := os.OpenRoot(itemDirectory)
		if err != nil {
			return err
		}
		defer trashRoot.Close()
		if err := copyRootPath(trashRoot, "payload", targetRoot, name); err != nil {
			return err
		}
		if err := verifyRootCopy(trashRoot, "payload", targetRoot, name); err != nil {
			return err
		}
		return nil
	})
	if mutationErr != nil {
		return Entry{}, mutationErr
	}
	_ = os.RemoveAll(itemDirectory)
	s.index.ApplyChanges(botID, []Change{{Op: "create", Ref: metadata.Ref}})
	entry, err := s.entryFor(botID, metadata.Ref)
	entry.Registration = registration
	return entry, err
}

func (s *Service) PurgeTrash(botID, id string) error {
	if !validTrashID(id) {
		return &Error{Code: "trash_not_found", Message: "trash item not found", Cause: ErrNotFound}
	}
	directory := s.trashItemDir(botID, id)
	unlock := s.lockPaths(directory)
	defer unlock()
	if _, err := os.Stat(filepath.Join(directory, "meta.json")); err != nil {
		return &Error{Code: "trash_not_found", Message: "trash item not found", Cause: err}
	}
	return os.RemoveAll(directory)
}

func (s *Service) readTrashMetadata(botID, id string) (trashMetadata, error) {
	data, err := os.ReadFile(filepath.Join(s.trashItemDir(botID, id), "meta.json"))
	if err != nil {
		return trashMetadata{}, err
	}
	var metadata trashMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return trashMetadata{}, err
	}
	if metadata.BotID != botID || metadata.ID != id {
		return trashMetadata{}, errorsNew("trash metadata does not match its container")
	}
	return metadata, nil
}

func (s *Service) trashWorkspaceDir(botID string) string {
	digest := sha256.Sum256([]byte(botID))
	return filepath.Join(filepath.Dir(s.dataPath), "trash", hex.EncodeToString(digest[:16]))
}

func (s *Service) trashItemDir(botID, id string) string {
	return filepath.Join(s.trashWorkspaceDir(botID), id)
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

// Copies use pinned roots on both sides; no recursive path operation can follow
// a newly introduced link outside its grant. Exclusive creation protects restores.
func copyRootPath(source *os.Root, from string, target *os.Root, to string) error {
	info, err := source.Lstat(from)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link, err := source.Readlink(from)
		if err != nil {
			return err
		}
		return target.Symlink(link, to)
	}
	if info.IsDir() {
		if err := target.Mkdir(to, info.Mode().Perm()); err != nil {
			return err
		}
		directory, err := source.Open(from)
		if err != nil {
			return err
		}
		children, err := directory.ReadDir(-1)
		directory.Close()
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := copyRootPath(source, filepath.Join(from, child.Name()), target, filepath.Join(to, child.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return ErrUnsupportedFile
	}
	input, err := source.Open(from)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := target.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(output, input)
	if err == nil {
		err = output.Sync()
	}
	closeErr := output.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func rootDigest(root *os.Root, name string) (string, error) {
	hash := sha256.New()
	var visit func(string, string) error
	visit = func(current, relative string) error {
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s:%s\x00", relative, info.Mode().Type())
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := root.Readlink(current)
			if err != nil {
				return err
			}
			io.WriteString(hash, link)
		case info.IsDir():
			file, err := root.Open(current)
			if err != nil {
				return err
			}
			children, err := file.ReadDir(-1)
			file.Close()
			if err != nil {
				return err
			}
			sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
			for _, child := range children {
				if err := visit(filepath.Join(current, child.Name()), filepath.Join(relative, child.Name())); err != nil {
					return err
				}
			}
		case info.Mode().IsRegular():
			file, err := root.Open(current)
			if err != nil {
				return err
			}
			_, err = io.Copy(hash, file)
			file.Close()
			if err != nil {
				return err
			}
		default:
			return ErrUnsupportedFile
		}
		return nil
	}
	if err := visit(name, ""); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func verifyRootCopy(source *os.Root, from string, target *os.Root, to string) error {
	left, err := rootDigest(source, from)
	if err != nil {
		return err
	}
	right, err := rootDigest(target, to)
	if err != nil {
		return err
	}
	if left != right {
		return fmt.Errorf("copy verification failed")
	}
	return nil
}
func errorsNew(message string) error { return fmt.Errorf("%s", message) }
