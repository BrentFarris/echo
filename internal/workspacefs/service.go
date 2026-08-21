// Package workspacefs provides the confined filesystem boundary used by the
// Echo code editor. Browser requests identify files by workspace root ID and a
// normalized relative path; absolute client-supplied paths are never accepted.
package workspacefs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/brent/echo/internal/workspaces"
)

const MaxEditableBytes int64 = 10 << 20

// MaxMediaBytes caps files served to the browser preview surface. It is far
// larger than the editor limit because images and video are only displayed,
// never edited.
const MaxMediaBytes int64 = 500 << 20

var (
	ErrNotFound        = errors.New("file or folder not found")
	ErrOutsideRoot     = errors.New("path escapes the workspace root")
	ErrConflict        = errors.New("file changed on disk")
	ErrAlreadyExists   = errors.New("file or folder already exists")
	ErrUnsupportedFile = errors.New("file is not editable text")
	ErrTooLarge        = errors.New("file is too large to edit")
	ErrInvalidPath     = errors.New("invalid workspace path")
	ErrNotPreviewable  = errors.New("file is not a supported image or video type")
	ErrProtectedMetadata = errors.New("workspace metadata is managed by Echo")
)

// IsProtectedWorkspaceMetadataPath reports whether a workspace-relative path
// identifies configuration that Echo must keep in place to resolve and render
// the workspace. Other .echo content, such as skills, remains user-editable.
func IsProtectedWorkspaceMetadataPath(value string) bool {
	normalized := path.Clean(strings.TrimSpace(strings.ReplaceAll(value, "\\", "/")))
	parts := strings.Split(normalized, "/")
	if len(parts) == 1 {
		return strings.EqualFold(parts[0], workspaces.EchoDirName)
	}
	if len(parts) != 2 || !strings.EqualFold(parts[0], workspaces.EchoDirName) {
		return false
	}
	name := strings.ToLower(parts[1])
	return name == "workspace.json" || (strings.HasPrefix(name, "icon.") && len(name) > len("icon."))
}

func protectedMetadataError() error {
	return &Error{
		Code: "protected_workspace_metadata", Message: "workspace metadata is managed by Echo and cannot be modified here",
		Cause: ErrProtectedMetadata,
	}
}

// Error carries a stable API error code without exposing unsafe filesystem
// implementation details to the browser.
type Error struct {
	Code    string
	Message string
	Cause   error
	Current *FileSnapshot
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

type FileRef struct {
	RootID string `json:"rootId"`
	Path   string `json:"path"`
}

type Root struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	ReferenceLabel string `json:"referenceLabel"`
	HostPath       string `json:"hostPath"`
	BlockedReason  string `json:"blockedReason,omitempty"`
}

type Entry struct {
	Ref           FileRef `json:"ref"`
	Name          string  `json:"name"`
	HostPath      string  `json:"hostPath"`
	Kind          string  `json:"kind"`
	IsSymlink     bool    `json:"isSymlink"`
	BlockedReason string  `json:"blockedReason,omitempty"`
	Size          int64   `json:"size,omitempty"`
	ModifiedAt    string  `json:"modifiedAt"`
}

type FileSnapshot struct {
	Ref        FileRef `json:"ref"`
	HostPath   string  `json:"hostPath"`
	Content    string  `json:"content"`
	Revision   string  `json:"revision"`
	Size       int64   `json:"size"`
	ModifiedAt string  `json:"modifiedAt"`
	Encoding   string  `json:"encoding"`
	EOL        string  `json:"eol"`
	HasBOM     bool    `json:"hasBom"`
}

type SaveRequest struct {
	Ref              FileRef
	Content          string
	ExpectedRevision string
	CreateOnly       bool
	HasBOM           bool
}

type CreateRequest struct {
	Parent  FileRef
	Name    string
	Kind    string
	Content string
	HasBOM  bool
}

type resolvedRoot struct {
	Root
	realPath   string
	resolveErr error
}

type referencedPathLock struct {
	mutex sync.Mutex
	refs  int
}

// Service owns editor filesystem operations and per-path write locks.
type Service struct {
	workspaces *workspaces.Manager
	dataPath   string
	locksMu    sync.Mutex
	locks      map[string]*referencedPathLock
	index      *Index
}

func New(workspaces *workspaces.Manager, dataPath string) *Service {
	service := &Service{workspaces: workspaces, dataPath: dataPath, locks: make(map[string]*referencedPathLock)}
	service.index = newIndex(service)
	return service
}

// Close cancels background Quick Open indexing work.
func (s *Service) Close() {
	s.index.Close()
}

// RefreshWorkspace rebuilds cached search state after a workspace registration
// is rebound to a different main folder.
func (s *Service) RefreshWorkspace(workspaceID string) {
	s.index.Invalidate(workspaceID)
}

func (s *Service) Roots(workspaceID string) ([]Root, error) {
	resolved, err := s.resolvedRoots(workspaceID)
	if err != nil {
		return nil, err
	}
	result := make([]Root, len(resolved))
	for i := range resolved {
		result[i] = resolved[i].Root
	}
	return result, nil
}

func (s *Service) resolvedRoots(workspaceID string) ([]resolvedRoot, error) {
	workspace, ok, err := s.workspaces.Get(strings.TrimSpace(workspaceID))
	if err != nil {
		var configErr *workspaces.ConfigError
		if errors.As(err, &configErr) {
			return nil, &Error{Code: configErr.Code, Message: configErr.Message, Cause: err}
		}
		return nil, err
	}
	if !ok {
		return nil, &Error{Code: "workspace_not_found", Message: "workspace not found", Cause: ErrNotFound}
	}
	folders := make([]string, 0, len(workspace.Folders))
	for _, folder := range workspace.Folders {
		if folder = strings.TrimSpace(folder); folder != "" {
			folders = append(folders, folder)
		}
	}
	if len(folders) == 0 && strings.TrimSpace(workspace.MainPath) != "" {
		folders = []string{workspace.MainPath}
	}
	labels := make(map[string]bool)
	referenceLabels := make(map[string]bool)
	seenPaths := make(map[string]bool)
	result := make([]resolvedRoot, 0, len(folders))
	for _, folder := range folders {
		absolute, err := filepath.Abs(strings.TrimSpace(folder))
		if err != nil {
			return nil, fmt.Errorf("resolve workspace root: %w", err)
		}
		absolute = filepath.Clean(absolute)
		realPath, resolveErr := filepath.EvalSymlinks(absolute)
		if resolveErr == nil {
			if info, statErr := os.Stat(realPath); statErr != nil {
				resolveErr = statErr
			} else if !info.IsDir() {
				resolveErr = fmt.Errorf("workspace root is not a directory")
			}
		}
		identityPath := realPath
		if resolveErr != nil {
			identityPath = absolute
		}
		pathKey := identityPath
		if runtime.GOOS == "windows" {
			pathKey = strings.ToLower(pathKey)
		}
		if seenPaths[pathKey] {
			continue
		}
		seenPaths[pathKey] = true
		label := filepath.Base(absolute)
		if label == "." || label == string(filepath.Separator) || strings.TrimSpace(label) == "" {
			label = "workspace"
		}
		baseLabel := label
		for suffix := 2; labels[strings.ToLower(label)]; suffix++ {
			label = fmt.Sprintf("%s-%d", baseLabel, suffix)
		}
		labels[strings.ToLower(label)] = true
		referenceLabel := NormalizeReferenceLabel(label)
		if referenceLabel == "" {
			referenceLabel = "workspace"
		}
		baseReferenceLabel := referenceLabel
		for suffix := 2; referenceLabels[strings.ToLower(referenceLabel)]; suffix++ {
			referenceLabel = fmt.Sprintf("%s-%d", baseReferenceLabel, suffix)
		}
		referenceLabels[strings.ToLower(referenceLabel)] = true
		digestInput := identityPath
		if runtime.GOOS == "windows" {
			digestInput = strings.ToLower(digestInput)
		}
		digest := sha256.Sum256([]byte(digestInput))
		root := resolvedRoot{Root: Root{
			ID: hex.EncodeToString(digest[:8]), Label: label, ReferenceLabel: referenceLabel, HostPath: absolute,
		}, realPath: realPath, resolveErr: resolveErr}
		if resolveErr != nil {
			root.BlockedReason = "Workspace folder is unavailable"
		}
		result = append(result, root)
	}
	return result, nil
}

// NormalizeReferenceLabel converts a workspace folder name into the stable
// label accepted by agent filesystem tools and emitted by chat references.
func NormalizeReferenceLabel(label string) string {
	label = strings.TrimSpace(strings.ToLower(label))
	var builder strings.Builder
	lastDash := false
	for _, character := range label {
		valid := (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-'
		if valid {
			builder.WriteRune(character)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-_.")
}

func availableResolvedRoots(roots []resolvedRoot) []resolvedRoot {
	available := make([]resolvedRoot, 0, len(roots))
	for _, root := range roots {
		if root.resolveErr == nil {
			available = append(available, root)
		}
	}
	return available
}

func (s *Service) rootFor(workspaceID, rootID string) (resolvedRoot, error) {
	roots, err := s.resolvedRoots(workspaceID)
	if err != nil {
		return resolvedRoot{}, err
	}
	for _, root := range roots {
		if subtleStringEqual(root.ID, rootID) {
			if root.resolveErr != nil {
				return resolvedRoot{}, &Error{Code: "workspace_root_unavailable", Message: "workspace folder is unavailable", Cause: root.resolveErr}
			}
			return root, nil
		}
	}
	return resolvedRoot{}, &Error{Code: "root_not_found", Message: "workspace folder not found", Cause: ErrNotFound}
}

func normalizeRelative(input string, allowRoot bool) (string, error) {
	if strings.ContainsRune(input, 0) || filepath.IsAbs(input) || path.IsAbs(strings.ReplaceAll(input, "\\", "/")) {
		return "", &Error{Code: "invalid_path", Message: "path must be relative to the workspace folder", Cause: ErrInvalidPath}
	}
	normalized := strings.ReplaceAll(input, "\\", "/")
	if strings.Contains(normalized, ":") && runtime.GOOS == "windows" {
		return "", &Error{Code: "invalid_path", Message: "path contains invalid characters", Cause: ErrInvalidPath}
	}
	normalized = path.Clean(normalized)
	if normalized == "." || normalized == "" {
		if allowRoot {
			return "", nil
		}
		return "", &Error{Code: "root_mutation_forbidden", Message: "the workspace folder itself cannot be modified", Cause: ErrInvalidPath}
	}
	if normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", &Error{Code: "path_outside_workspace", Message: "path escapes the workspace folder", Cause: ErrOutsideRoot}
	}
	for _, segment := range strings.Split(normalized, "/") {
		if err := validateName(segment); err != nil {
			return "", err
		}
	}
	return normalized, nil
}

func validateName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return &Error{Code: "invalid_name", Message: "name is not valid", Cause: ErrInvalidPath}
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return &Error{Code: "invalid_name", Message: "name contains control characters", Cause: ErrInvalidPath}
		}
	}
	if runtime.GOOS == "windows" {
		if strings.ContainsAny(name, `<>:"|?*`) || strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
			return &Error{Code: "invalid_name", Message: "name contains characters that Windows does not allow", Cause: ErrInvalidPath}
		}
		base := strings.ToUpper(strings.TrimSuffix(name, filepath.Ext(name)))
		switch base {
		case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
			return &Error{Code: "invalid_name", Message: "name is reserved by Windows", Cause: ErrInvalidPath}
		}
	}
	return nil
}

func (s *Service) resolve(workspaceID string, ref FileRef, allowRoot, allowMissing bool) (resolvedRoot, string, string, error) {
	root, err := s.rootFor(workspaceID, strings.TrimSpace(ref.RootID))
	if err != nil {
		return resolvedRoot{}, "", "", err
	}
	relative, err := normalizeRelative(ref.Path, allowRoot)
	if err != nil {
		return resolvedRoot{}, "", "", err
	}
	visible := filepath.Join(root.HostPath, filepath.FromSlash(relative))
	candidate := filepath.Join(root.realPath, filepath.FromSlash(relative))
	if err := ensureWithin(root.realPath, candidate); err != nil {
		return resolvedRoot{}, "", "", err
	}
	realCandidate, evalErr := filepath.EvalSymlinks(candidate)
	if evalErr == nil {
		if err := ensureWithin(root.realPath, realCandidate); err != nil {
			return resolvedRoot{}, "", "", err
		}
		candidate = realCandidate
	} else if !allowMissing || !os.IsNotExist(evalErr) {
		if os.IsNotExist(evalErr) {
			return resolvedRoot{}, "", "", &Error{Code: "not_found", Message: "file or folder not found", Cause: ErrNotFound}
		}
		return resolvedRoot{}, "", "", &Error{Code: "path_unavailable", Message: "path could not be resolved", Cause: evalErr}
	} else {
		parent := filepath.Dir(candidate)
		realParent, parentErr := filepath.EvalSymlinks(parent)
		if parentErr != nil {
			return resolvedRoot{}, "", "", &Error{Code: "parent_not_found", Message: "parent folder not found", Cause: parentErr}
		}
		if err := ensureWithin(root.realPath, realParent); err != nil {
			return resolvedRoot{}, "", "", err
		}
		candidate = filepath.Join(realParent, filepath.Base(candidate))
	}
	return root, candidate, visible, nil
}

// resolveEntry resolves every parent symlink while deliberately not following
// the final path component. Entry mutations must rename/trash a symlink itself,
// never the file or directory it points at.
func (s *Service) resolveEntry(workspaceID string, ref FileRef, allowRoot, allowMissing bool) (resolvedRoot, string, string, error) {
	root, err := s.rootFor(workspaceID, strings.TrimSpace(ref.RootID))
	if err != nil {
		return resolvedRoot{}, "", "", err
	}
	relative, err := normalizeRelative(ref.Path, allowRoot)
	if err != nil {
		return resolvedRoot{}, "", "", err
	}
	visible := filepath.Join(root.HostPath, filepath.FromSlash(relative))
	if relative == "" {
		return root, root.realPath, visible, nil
	}
	lexical := filepath.Join(root.realPath, filepath.FromSlash(relative))
	if err := ensureWithin(root.realPath, lexical); err != nil {
		return resolvedRoot{}, "", "", err
	}
	realParent, err := filepath.EvalSymlinks(filepath.Dir(lexical))
	if err != nil {
		if os.IsNotExist(err) {
			return resolvedRoot{}, "", "", &Error{Code: "parent_not_found", Message: "parent folder not found", Cause: err}
		}
		return resolvedRoot{}, "", "", err
	}
	if err := ensureWithin(root.realPath, realParent); err != nil {
		return resolvedRoot{}, "", "", err
	}
	target := filepath.Join(realParent, filepath.Base(lexical))
	info, statErr := os.Lstat(target)
	if statErr != nil {
		if allowMissing && os.IsNotExist(statErr) {
			return root, target, visible, nil
		}
		if os.IsNotExist(statErr) {
			return resolvedRoot{}, "", "", &Error{Code: "not_found", Message: "file or folder not found", Cause: ErrNotFound}
		}
		return resolvedRoot{}, "", "", statErr
	}
	if info.Mode()&os.ModeSymlink != 0 {
		realTarget, evalErr := filepath.EvalSymlinks(target)
		if evalErr != nil {
			return resolvedRoot{}, "", "", &Error{Code: "symlink_unavailable", Message: "symlink target is unavailable", Cause: evalErr}
		}
		if err := ensureWithin(root.realPath, realTarget); err != nil {
			return resolvedRoot{}, "", "", err
		}
	}
	return root, target, visible, nil
}

func ensureWithin(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return &Error{Code: "path_outside_workspace", Message: "path escapes the workspace folder", Cause: ErrOutsideRoot}
	}
	return nil
}

func (s *Service) lockPaths(paths ...string) func() {
	keys := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, item := range paths {
		key := filepath.Clean(item)
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	s.locksMu.Lock()
	locks := make([]*referencedPathLock, 0, len(keys))
	for _, key := range keys {
		lock := s.locks[key]
		if lock == nil {
			lock = &referencedPathLock{}
			s.locks[key] = lock
		}
		lock.refs++
		locks = append(locks, lock)
	}
	s.locksMu.Unlock()
	for _, lock := range locks {
		lock.mutex.Lock()
	}
	return func() {
		for index := len(locks) - 1; index >= 0; index-- {
			locks[index].mutex.Unlock()
		}
		s.locksMu.Lock()
		for index, key := range keys {
			locks[index].refs--
			if locks[index].refs == 0 && s.locks[key] == locks[index] {
				delete(s.locks, key)
			}
		}
		s.locksMu.Unlock()
	}
}

// ResolveExistingHostPath exposes the editor's canonical path boundary to
// trusted in-process consumers such as chat filesystem tools.
func (s *Service) ResolveExistingHostPath(workspaceID string, ref FileRef, allowRoot bool) (string, error) {
	_, resolved, _, err := s.resolve(workspaceID, ref, allowRoot, false)
	return resolved, err
}

// ResolveEntryHostPath applies the same canonical parent confinement without
// following the final component, allowing safe create/rename/delete callers.
func (s *Service) ResolveEntryHostPath(workspaceID string, ref FileRef) (string, error) {
	if IsProtectedWorkspaceMetadataPath(ref.Path) {
		return "", protectedMetadataError()
	}
	_, resolved, _, err := s.resolveEntry(workspaceID, ref, false, true)
	return resolved, err
}

func (s *Service) List(workspaceID string, ref FileRef) ([]Entry, error) {
	root, directory, visible, err := s.resolve(workspaceID, ref, true, false)
	if err != nil {
		return nil, err
	}
	children, err := os.ReadDir(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &Error{Code: "not_found", Message: "folder not found", Cause: ErrNotFound}
		}
		return nil, err
	}
	baseRelative, _ := normalizeRelative(ref.Path, true)
	entries := make([]Entry, 0, len(children))
	for _, child := range children {
		childPath := filepath.Join(directory, child.Name())
		visiblePath := filepath.Join(visible, child.Name())
		info, infoErr := os.Lstat(childPath)
		if infoErr != nil {
			continue
		}
		isSymlink := info.Mode()&os.ModeSymlink != 0
		kind := "file"
		blocked := ""
		statInfo := info
		if isSymlink {
			real, evalErr := filepath.EvalSymlinks(childPath)
			if evalErr != nil {
				blocked = "Symlink target is unavailable"
			} else if containErr := ensureWithin(root.realPath, real); containErr != nil {
				blocked = "Symlink target is outside this workspace folder"
			} else if followed, statErr := os.Stat(childPath); statErr == nil {
				statInfo = followed
			}
		}
		if statInfo.IsDir() {
			kind = "directory"
		}
		relative := path.Join(baseRelative, child.Name())
		if baseRelative == "" {
			relative = child.Name()
		}
		entries = append(entries, Entry{
			Ref: FileRef{RootID: root.ID, Path: relative}, Name: child.Name(), HostPath: visiblePath,
			Kind: kind, IsSymlink: isSymlink, BlockedReason: blocked,
			Size: statInfo.Size(), ModifiedAt: statInfo.ModTime().UTC().Format(time.RFC3339Nano),
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind == "directory"
		}
		left, right := strings.ToLower(entries[i].Name), strings.ToLower(entries[j].Name)
		if left == right {
			return entries[i].Name < entries[j].Name
		}
		return left < right
	})
	return entries, nil
}

func subtleStringEqual(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
