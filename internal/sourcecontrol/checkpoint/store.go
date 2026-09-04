// Package checkpoint stores provider-owned, transient source-control
// checkpoints outside working copies. It deliberately knows nothing about a
// particular VCS; providers own the meaning of file states and transactions.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

const Version = 1

const (
	manifestName = "manifest.json"
	journalName  = "recovery.json"
)

// FileState is an exact filesystem state. Blob is a SHA-256 reference for a
// regular file; symlink targets are kept inline and absent files use neither.
type FileState struct {
	Path          string `json:"path"`
	OldPath       string `json:"oldPath,omitempty"`
	StatusCode    string `json:"statusCode,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Exists        bool   `json:"exists"`
	Mode          uint32 `json:"mode,omitempty"`
	Symlink       bool   `json:"symlink,omitempty"`
	SymlinkTarget string `json:"symlinkTarget,omitempty"`
	Hash          string `json:"hash,omitempty"`
	Blob          string `json:"blob,omitempty"`
}

type Manifest struct {
	Version             int         `json:"version"`
	WorkspaceID         string      `json:"workspaceId"`
	ProviderID          string      `json:"providerId"`
	RepositoryID        string      `json:"repositoryId"`
	CheckoutFingerprint string      `json:"checkoutFingerprint"`
	Baseline            string      `json:"baseline"`
	Generation          uint64      `json:"generation"`
	Entries             []FileState `json:"entries"`
}

type Journal struct {
	Version             int         `json:"version"`
	WorkspaceID         string      `json:"workspaceId"`
	ProviderID          string      `json:"providerId"`
	RepositoryID        string      `json:"repositoryId"`
	CheckoutFingerprint string      `json:"checkoutFingerprint"`
	Baseline            string      `json:"baseline"`
	NewBaseline         string      `json:"newBaseline,omitempty"`
	Phase               string      `json:"phase"`
	Current             []FileState `json:"current"`
}

// Store serializes updates so a manifest can never observe partially written
// blobs. Provider-level repository locks still protect checkout operations.
type Store struct {
	root string
	mu   sync.Mutex
}

func New(root string) *Store {
	if absolute, err := filepath.Abs(strings.TrimSpace(root)); err == nil {
		root = absolute
	}
	return &Store{root: filepath.Clean(root)}
}

func BlobID(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (s *Store) LoadManifest(workspaceID, providerID, repositoryID string) (*Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var manifest Manifest
	if err := s.loadJSON(s.repositoryDir(workspaceID, providerID, repositoryID), manifestName, &manifest); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("load source-control checkpoint: %w", err)
	}
	if err := validateManifest(manifest, workspaceID, providerID, repositoryID); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (s *Store) LoadJournal(workspaceID, providerID, repositoryID string) (*Journal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var journal Journal
	if err := s.loadJSON(s.repositoryDir(workspaceID, providerID, repositoryID), journalName, &journal); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("load source-control recovery journal: %w", err)
	}
	if err := validateJournal(journal, workspaceID, providerID, repositoryID); err != nil {
		return nil, err
	}
	return &journal, nil
}

func (s *Store) ReplaceManifest(manifest Manifest, blobs map[string][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateManifest(manifest, manifest.WorkspaceID, manifest.ProviderID, manifest.RepositoryID); err != nil {
		return err
	}
	directory := s.repositoryDir(manifest.WorkspaceID, manifest.ProviderID, manifest.RepositoryID)
	if err := s.prepare(directory, manifest.Entries, blobs); err != nil {
		return err
	}
	if err := writeAtomicJSON(directory, manifestName, manifest); err != nil {
		return fmt.Errorf("write source-control checkpoint: %w", err)
	}
	kept := append([]FileState(nil), manifest.Entries...)
	var journal Journal
	if err := s.loadJSON(directory, journalName, &journal); err == nil {
		kept = append(kept, journal.Current...)
	}
	return s.gcLocked(directory, kept)
}

func (s *Store) WriteJournal(journal Journal, blobs map[string][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateJournal(journal, journal.WorkspaceID, journal.ProviderID, journal.RepositoryID); err != nil {
		return err
	}
	directory := s.repositoryDir(journal.WorkspaceID, journal.ProviderID, journal.RepositoryID)
	if err := s.prepare(directory, journal.Current, blobs); err != nil {
		return err
	}
	if err := writeAtomicJSON(directory, journalName, journal); err != nil {
		return fmt.Errorf("write source-control recovery journal: %w", err)
	}
	manifestEntries := []FileState{}
	var manifest Manifest
	if err := s.loadJSON(directory, manifestName, &manifest); err == nil {
		manifestEntries = manifest.Entries
	}
	return s.gcLocked(directory, append(manifestEntries, journal.Current...))
}

func (s *Store) ReadBlob(workspaceID, providerID, repositoryID, blob string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validBlobID(blob) {
		return nil, fmt.Errorf("source-control checkpoint blob reference is invalid")
	}
	data, err := os.ReadFile(filepath.Join(s.repositoryDir(workspaceID, providerID, repositoryID), "blobs", blob))
	if err != nil {
		return nil, fmt.Errorf("read source-control checkpoint blob: %w", err)
	}
	if BlobID(data) != blob {
		return nil, fmt.Errorf("source-control checkpoint blob failed integrity validation")
	}
	return data, nil
}

// Clear removes the checkpoint and journal for a repository. It is used only
// after successful recovery/commit or an explicit user clear.
func (s *Store) Clear(workspaceID, providerID, repositoryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	directory := s.repositoryDir(workspaceID, providerID, repositoryID)
	if directory == s.root || filepath.Dir(directory) == directory {
		return fmt.Errorf("refusing to remove an unsafe checkpoint path")
	}
	if err := os.RemoveAll(directory); err != nil {
		return fmt.Errorf("remove source-control checkpoint: %w", err)
	}
	return syncDirectory(filepath.Dir(directory))
}

func (s *Store) ClearJournal(workspaceID, providerID, repositoryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	directory := s.repositoryDir(workspaceID, providerID, repositoryID)
	err := os.Remove(filepath.Join(directory, journalName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove source-control recovery journal: %w", err)
	}
	var manifest Manifest
	if loadErr := s.loadJSON(directory, manifestName, &manifest); loadErr == nil {
		return s.gcLocked(directory, manifest.Entries)
	}
	return syncDirectory(directory)
}

func (s *Store) RemoveWorkspace(workspaceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	directory := filepath.Join(s.root, identity(workspaceID))
	if directory == s.root || filepath.Dir(directory) != s.root {
		return fmt.Errorf("refusing to remove an unsafe checkpoint workspace path")
	}
	if err := os.RemoveAll(directory); err != nil {
		return fmt.Errorf("remove workspace source-control checkpoints: %w", err)
	}
	return syncDirectory(s.root)
}

func (s *Store) prepare(directory string, states []FileState, blobs map[string][]byte) error {
	if err := os.MkdirAll(filepath.Join(directory, "blobs"), 0o700); err != nil {
		return fmt.Errorf("create source-control checkpoint directory: %w", err)
	}
	_ = os.Chmod(directory, 0o700)
	_ = os.Chmod(filepath.Join(directory, "blobs"), 0o700)
	for _, state := range states {
		if state.Blob == "" {
			continue
		}
		if !validBlobID(state.Blob) || state.Hash != state.Blob {
			return fmt.Errorf("source-control checkpoint blob metadata is invalid for %q", state.Path)
		}
		pathValue := filepath.Join(directory, "blobs", state.Blob)
		if existing, err := os.ReadFile(pathValue); err == nil && BlobID(existing) == state.Blob {
			continue
		}
		data, ok := blobs[state.Blob]
		if !ok || BlobID(data) != state.Blob {
			return fmt.Errorf("source-control checkpoint blob data is missing for %q", state.Path)
		}
		if err := writeAtomic(pathValue, data); err != nil {
			return fmt.Errorf("write source-control checkpoint blob: %w", err)
		}
	}
	return nil
}

func (s *Store) loadJSON(directory, name string, target any) error {
	data, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return err
	}
	return nil
}

func (s *Store) gcLocked(directory string, states []FileState) error {
	keep := make(map[string]bool)
	for _, state := range states {
		if validBlobID(state.Blob) {
			keep[state.Blob] = true
		}
	}
	entries, err := os.ReadDir(filepath.Join(directory, "blobs"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() && validBlobID(entry.Name()) && !keep[entry.Name()] {
			if err := os.Remove(filepath.Join(directory, "blobs", entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return syncDirectory(filepath.Join(directory, "blobs"))
}

func (s *Store) repositoryDir(workspaceID, providerID, repositoryID string) string {
	return filepath.Join(s.root, identity(workspaceID), identity(providerID+"\x00"+repositoryID))
}

func identity(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func validateManifest(manifest Manifest, workspaceID, providerID, repositoryID string) error {
	if manifest.Version != Version || manifest.WorkspaceID != workspaceID || manifest.ProviderID != providerID || manifest.RepositoryID != repositoryID {
		return fmt.Errorf("source-control checkpoint identity is invalid")
	}
	if strings.TrimSpace(manifest.Baseline) == "" || strings.TrimSpace(manifest.CheckoutFingerprint) == "" || len(manifest.Entries) == 0 {
		return fmt.Errorf("source-control checkpoint is incomplete")
	}
	return validateFileStates(manifest.Entries, true)
}

func validateJournal(journal Journal, workspaceID, providerID, repositoryID string) error {
	if journal.Version != Version || journal.WorkspaceID != workspaceID || journal.ProviderID != providerID || journal.RepositoryID != repositoryID {
		return fmt.Errorf("source-control recovery journal identity is invalid")
	}
	if strings.TrimSpace(journal.CheckoutFingerprint) == "" || strings.TrimSpace(journal.Baseline) == "" || strings.TrimSpace(journal.Phase) == "" {
		return fmt.Errorf("source-control recovery journal is incomplete")
	}
	return validateFileStates(journal.Current, true)
}

func validateFileStates(states []FileState, requireEntries bool) error {
	if requireEntries && len(states) == 0 {
		return fmt.Errorf("source-control checkpoint contains no file states")
	}
	seen := make(map[string]bool)
	for _, entry := range states {
		if !validRelativePath(entry.Path) || seen[entry.Path] {
			return fmt.Errorf("source-control checkpoint contains an invalid path")
		}
		seen[entry.Path] = true
		if entry.OldPath != "" && !validRelativePath(entry.OldPath) {
			return fmt.Errorf("source-control checkpoint contains an invalid old path")
		}
		switch {
		case !entry.Exists:
			if entry.Blob != "" || entry.Hash != "" || entry.Symlink || entry.SymlinkTarget != "" {
				return fmt.Errorf("absent source-control checkpoint state has content for %q", entry.Path)
			}
		case entry.Symlink:
			if entry.Blob != "" || entry.Hash != BlobID([]byte("symlink\x00"+entry.SymlinkTarget)) {
				return fmt.Errorf("source-control checkpoint symlink metadata is invalid for %q", entry.Path)
			}
		default:
			if !validBlobID(entry.Blob) || entry.Hash != entry.Blob {
				return fmt.Errorf("source-control checkpoint is missing content for %q", entry.Path)
			}
		}
	}
	return nil
}

func validRelativePath(value string) bool {
	if value == "" || strings.ContainsRune(value, 0) || strings.HasPrefix(value, "/") || filepath.IsAbs(filepath.FromSlash(value)) ||
		(len(value) >= 2 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':') {
		return false
	}
	normalized := strings.ReplaceAll(value, "\\", "/")
	clean := path.Clean(normalized)
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, "../") && clean == normalized
}

func validBlobID(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func writeAtomicJSON(directory, name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(directory, name), data)
}

func writeAtomic(destination string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), filepath.Base(destination)+".*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := replaceFile(temporaryPath, destination); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(destination))
}

func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()
	if err := file.Sync(); err != nil && runtime.GOOS != "windows" && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}

// ReferencedBlobs is useful to providers and tests when checking recovery
// state without depending on storage layout.
func ReferencedBlobs(states []FileState) []string {
	values := make(map[string]bool)
	for _, state := range states {
		if validBlobID(state.Blob) {
			values[state.Blob] = true
		}
	}
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
