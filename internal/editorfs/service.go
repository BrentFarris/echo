// Package editorfs provides the confined filesystem boundary used by the
// Echo code editor. Browser requests identify files by workspace root ID and a
// normalized relative path; absolute client-supplied paths are never accepted.
package editorfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const MaxEditableBytes int64 = 10 << 20

// MaxMediaBytes caps files served to the browser preview surface. It is far
// larger than the editor limit because images and video are only displayed,
// never edited.
const MaxMediaBytes int64 = 500 << 20

var (
	ErrNotFound          = errors.New("file or folder not found")
	ErrOutsideRoot       = errors.New("path escapes the workspace root")
	ErrConflict          = errors.New("file changed on disk")
	ErrAlreadyExists     = errors.New("file or folder already exists")
	ErrUnsupportedFile   = errors.New("file is not editable text")
	ErrTooLarge          = errors.New("file is too large to edit")
	ErrInvalidPath       = errors.New("invalid workspace path")
	ErrNotPreviewable    = errors.New("file is not a supported image or video type")
	ErrProtectedMetadata = errors.New("workspace metadata is managed by Echo")
)

// IsProtectedWorkspaceMetadataPath reports whether a workspace-relative path
// identifies configuration that Echo must keep in place to resolve and render
// the workspace. Other .echo content, such as skills, remains user-editable.
func IsProtectedWorkspaceMetadataPath(value string) bool {
	normalized := path.Clean(strings.TrimSpace(strings.ReplaceAll(value, "\\", "/")))
	parts := strings.Split(normalized, "/")
	for _, part := range parts {
		switch strings.ToLower(part) {
		case ".git", ".fslckout", "_fossil_":
			return true
		}
	}
	return false
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

// ProspectiveEntryPath identifies a confined workspace entry even when some
// of its parent directories do not exist. Resolving a prospective path never
// creates those directories.
type ProspectiveEntryPath struct {
	HostPath           string
	MissingParentCount int
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
	ReadOnly      bool    `json:"readOnly,omitempty"`
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
	lookup   func(string) (domain.Bot, error)
	private  string
	dataPath string
	locksMu  sync.Mutex
	locks    map[string]*referencedPathLock
	index    *Index
}

func New(lookup func(string) (domain.Bot, error), private string) *Service {
	service := &Service{lookup: lookup, private: private, dataPath: filepath.Join(private, "editor"), locks: make(map[string]*referencedPathLock)}
	service.index = newIndex(service)
	return service
}

type Operation struct {
	BotID, Path, Destination string
	Validate                 func() error
}

func (s *Service) mutateContext(ctx context.Context, op Operation, apply func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := s.lookup(op.BotID); err != nil {
		return err
	}
	if err := s.checkMutation(op.Path); err != nil {
		return err
	}
	if op.Destination != "" {
		if err := s.checkMutation(op.Destination); err != nil {
			return err
		}
	}
	if op.Validate != nil {
		if err := op.Validate(); err != nil {
			return err
		}
	}
	return apply()
}

// Close cancels background Quick Open indexing work.
func (s *Service) Close() {
	s.index.Close()
}

// RefreshWorkspace rebuilds cached search state after a workspace registration
// is rebound to a different main folder.
func (s *Service) RefreshWorkspace(botID string) {
	s.index.Invalidate(botID)
}

func (s *Service) isProtectedMetadata(_ string, ref FileRef) bool {
	return IsProtectedWorkspaceMetadataPath(ref.Path)
}
func (s *Service) wouldAffectProtectedMetadata(botID string, ref FileRef) bool {
	return s.isProtectedMetadata(botID, ref)
}

func (s *Service) Roots(botID string) ([]Root, error) {
	resolved, err := s.resolvedRoots(botID)
	if err != nil {
		return nil, err
	}
	result := make([]Root, len(resolved))
	for i := range resolved {
		result[i] = resolved[i].Root
	}
	return result, nil
}

func (s *Service) resolvedRoots(botID string) ([]resolvedRoot, error) {
	bot, err := s.lookup(strings.TrimSpace(botID))
	if err != nil {
		return nil, &Error{Code: "bot_not_found", Message: "bot is unavailable", Cause: err}
	}
	folders := bot.Roots
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

func (s *Service) rootFor(botID, rootID string) (resolvedRoot, error) {
	roots, err := s.resolvedRoots(botID)
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

func (s *Service) resolve(botID string, ref FileRef, allowRoot, allowMissing bool) (resolvedRoot, string, string, error) {
	root, err := s.rootFor(botID, strings.TrimSpace(ref.RootID))
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
	if err := s.checkPrivate(candidate); err != nil {
		return resolvedRoot{}, "", "", err
	}
	return root, candidate, visible, nil
}

// resolveEntry resolves every parent symlink while deliberately not following
// the final path component. Entry mutations must rename/trash a symlink itself,
// never the file or directory it points at.
func (s *Service) resolveEntry(botID string, ref FileRef, allowRoot, allowMissing bool) (resolvedRoot, string, string, error) {
	root, err := s.rootFor(botID, strings.TrimSpace(ref.RootID))
	if err != nil {
		return resolvedRoot{}, "", "", err
	}
	relative, err := normalizeRelative(ref.Path, allowRoot)
	if err != nil {
		return resolvedRoot{}, "", "", err
	}
	visible := filepath.Join(root.HostPath, filepath.FromSlash(relative))
	if relative == "" {
		if err := s.checkPrivate(root.realPath); err != nil {
			return resolvedRoot{}, "", "", err
		}
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
			if err := s.checkPrivate(target); err != nil {
				return resolvedRoot{}, "", "", err
			}
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
	if err := s.checkPrivate(target); err != nil {
		return resolvedRoot{}, "", "", err
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
func (s *Service) ResolveExistingHostPath(botID string, ref FileRef, allowRoot bool) (string, error) {
	_, resolved, _, err := s.resolve(botID, ref, allowRoot, false)
	return resolved, err
}

// ResolveEntryHostPath applies the same canonical parent confinement without
// following the final component, allowing safe create/rename/delete callers.
func (s *Service) ResolveEntryHostPath(botID string, ref FileRef) (string, error) {
	if s.wouldAffectProtectedMetadata(botID, ref) {
		return "", protectedMetadataError()
	}
	_, resolved, _, err := s.resolveEntry(botID, ref, false, true)
	return resolved, err
}

// ResolveProspectiveEntryHostPath applies the entry path boundary while
// permitting a missing suffix of parent directories. It resolves and checks
// every existing ancestor, then returns the confined lexical destination and
// the number of missing parents without creating anything on disk.
func (s *Service) ResolveProspectiveEntryHostPath(botID string, ref FileRef) (ProspectiveEntryPath, error) {
	if s.wouldAffectProtectedMetadata(botID, ref) {
		return ProspectiveEntryPath{}, protectedMetadataError()
	}
	root, err := s.rootFor(botID, strings.TrimSpace(ref.RootID))
	if err != nil {
		return ProspectiveEntryPath{}, err
	}
	relative, err := normalizeRelative(ref.Path, false)
	if err != nil {
		return ProspectiveEntryPath{}, err
	}
	parts := strings.Split(relative, "/")
	lexical := filepath.Join(root.realPath, filepath.FromSlash(relative))
	if err := ensureWithin(root.realPath, lexical); err != nil {
		return ProspectiveEntryPath{}, err
	}

	parent := root.realPath
	missingParents := 0
	for index, name := range parts[:len(parts)-1] {
		candidate := filepath.Join(parent, name)
		info, statErr := os.Lstat(candidate)
		if os.IsNotExist(statErr) {
			missingParents = len(parts) - 1 - index
			parent = filepath.Join(parent, filepath.Join(parts[index:len(parts)-1]...))
			break
		}
		if statErr != nil {
			return ProspectiveEntryPath{}, &Error{Code: "path_unavailable", Message: "path could not be resolved", Cause: statErr}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			realTarget, targetErr := filepath.EvalSymlinks(candidate)
			if targetErr != nil {
				return ProspectiveEntryPath{}, &Error{Code: "symlink_unavailable", Message: "symlink target is unavailable", Cause: targetErr}
			}
			if err := ensureWithin(root.realPath, realTarget); err != nil {
				return ProspectiveEntryPath{}, err
			}
			followed, targetErr := os.Stat(realTarget)
			if targetErr != nil {
				return ProspectiveEntryPath{}, &Error{Code: "symlink_unavailable", Message: "symlink target is unavailable", Cause: targetErr}
			}
			if !followed.IsDir() {
				return ProspectiveEntryPath{}, &Error{Code: "parent_not_found", Message: "parent folder not found", Cause: ErrNotFound}
			}
			parent = realTarget
			continue
		}
		if !info.IsDir() {
			return ProspectiveEntryPath{}, &Error{Code: "parent_not_found", Message: "parent folder not found", Cause: ErrNotFound}
		}
		parent = candidate
	}
	target := filepath.Join(parent, parts[len(parts)-1])
	if err := ensureWithin(root.realPath, target); err != nil {
		return ProspectiveEntryPath{}, err
	}
	if entryInfo, statErr := os.Lstat(target); statErr == nil && entryInfo.Mode()&os.ModeSymlink != 0 {
		realTarget, targetErr := filepath.EvalSymlinks(target)
		if targetErr != nil {
			return ProspectiveEntryPath{}, &Error{Code: "symlink_unavailable", Message: "symlink target is unavailable", Cause: targetErr}
		}
		if err := ensureWithin(root.realPath, realTarget); err != nil {
			return ProspectiveEntryPath{}, err
		}
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return ProspectiveEntryPath{}, &Error{Code: "path_unavailable", Message: "path could not be resolved", Cause: statErr}
	}
	return ProspectiveEntryPath{HostPath: target, MissingParentCount: missingParents}, nil
}

func (s *Service) List(botID string, ref FileRef) ([]Entry, error) {
	root, directory, visible, err := s.resolve(botID, ref, true, false)
	if err != nil {
		return nil, err
	}
	children, err := s.readDir(botID, directory)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &Error{Code: "not_found", Message: "folder not found", Cause: ErrNotFound}
		}
		return nil, err
	}
	baseRelative, _ := normalizeRelative(ref.Path, true)
	entries := make([]Entry, 0, len(children))
	for _, child := range children {
		childRef := FileRef{RootID: root.ID, Path: path.Join(baseRelative, child.Name())}
		if baseRelative == "" {
			childRef.Path = child.Name()
		}
		readOnly := s.isProtectedMetadata(botID, childRef)
		if readOnly {
			continue
		}
		childPath := filepath.Join(directory, child.Name())
		if s.checkPrivate(childPath) != nil {
			continue
		}
		visiblePath := filepath.Join(visible, child.Name())
		info, infoErr := s.lstat(botID, childPath)
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
		relative := childRef.Path
		entries = append(entries, Entry{
			Ref: FileRef{RootID: root.ID, Path: relative}, Name: child.Name(), HostPath: visiblePath,
			Kind: kind, IsSymlink: isSymlink, ReadOnly: readOnly, BlockedReason: blocked,
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

func (s *Service) checkPrivate(p string) error {
	if s.private != "" && broker.Within(s.private, p) {
		return &Error{Code: "path_outside_workspace", Message: "Echo private state is not editor data", Cause: ErrOutsideRoot}
	}
	if real, err := filepath.EvalSymlinks(p); err == nil && s.private != "" && broker.Within(s.private, real) {
		return &Error{Code: "path_outside_workspace", Message: "Echo private state is not editor data", Cause: ErrOutsideRoot}
	}
	return nil
}

func (s *Service) checkMutation(path string) error {
	if err := s.checkPrivate(path); err != nil {
		return err
	}
	if path != "" && s.private != "" && broker.Within(path, s.private) {
		return ErrOutsideRoot
	}
	return nil
}
