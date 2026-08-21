package workspacefs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

var utf8BOM = []byte{0xef, 0xbb, 0xbf}

func (s *Service) Read(workspaceID string, ref FileRef) (FileSnapshot, error) {
	_, resolved, visible, err := s.resolve(workspaceID, ref, false, false)
	if err != nil {
		return FileSnapshot{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return FileSnapshot{}, &Error{Code: "not_found", Message: "file not found", Cause: ErrNotFound}
	}
	if info.Size() > MaxEditableBytes {
		return FileSnapshot{}, &Error{Code: "file_too_large", Message: "file is larger than the 10 MiB editor limit", Cause: ErrTooLarge}
	}
	file, err := os.Open(resolved)
	if err != nil {
		return FileSnapshot{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxEditableBytes+1))
	if err != nil {
		return FileSnapshot{}, err
	}
	if int64(len(data)) > MaxEditableBytes {
		return FileSnapshot{}, &Error{Code: "file_too_large", Message: "file is larger than the 10 MiB editor limit", Cause: ErrTooLarge}
	}
	hasBOM := bytes.HasPrefix(data, utf8BOM)
	contentBytes := data
	if hasBOM {
		contentBytes = contentBytes[len(utf8BOM):]
	}
	if bytes.IndexByte(contentBytes, 0) >= 0 || !utf8.Valid(contentBytes) {
		return FileSnapshot{}, &Error{Code: "unsupported_file", Message: "file is binary or is not valid UTF-8", Cause: ErrUnsupportedFile}
	}
	revision := contentRevision(data)
	return FileSnapshot{
		Ref: ref, HostPath: visible, Content: string(contentBytes), Revision: revision,
		Size: int64(len(data)), ModifiedAt: info.ModTime().UTC().Format(time.RFC3339Nano),
		Encoding: "utf-8", EOL: detectEOL(contentBytes), HasBOM: hasBOM,
	}, nil
}

func (s *Service) Save(workspaceID string, request SaveRequest) (FileSnapshot, error) {
	if IsProtectedWorkspaceMetadataPath(request.Ref.Path) {
		return FileSnapshot{}, protectedMetadataError()
	}
	if !utf8.ValidString(request.Content) {
		return FileSnapshot{}, &Error{Code: "invalid_utf8", Message: "editor content is not valid UTF-8", Cause: ErrUnsupportedFile}
	}
	data := []byte(request.Content)
	if request.HasBOM {
		data = append(append([]byte(nil), utf8BOM...), data...)
	}
	if int64(len(data)) > MaxEditableBytes {
		return FileSnapshot{}, &Error{Code: "file_too_large", Message: "file is larger than the 10 MiB editor limit", Cause: ErrTooLarge}
	}
	_, resolved, _, err := s.resolve(workspaceID, request.Ref, false, true)
	if err != nil {
		return FileSnapshot{}, err
	}
	unlock := s.lockPaths(resolved)
	defer unlock()

	mode := os.FileMode(0o644)
	existing, statErr := os.Stat(resolved)
	if statErr == nil {
		if request.CreateOnly {
			return FileSnapshot{}, &Error{Code: "already_exists", Message: "a file or folder already exists at that path", Cause: ErrAlreadyExists}
		}
		if !existing.Mode().IsRegular() {
			return FileSnapshot{}, &Error{Code: "not_a_file", Message: "path is not a regular file", Cause: ErrInvalidPath}
		}
		mode = existing.Mode().Perm()
		currentData, readErr := os.ReadFile(resolved)
		if readErr != nil {
			return FileSnapshot{}, readErr
		}
		currentRevision := contentRevision(currentData)
		if request.ExpectedRevision == "" || request.ExpectedRevision != currentRevision {
			current, _ := s.Read(workspaceID, request.Ref)
			return FileSnapshot{}, &Error{Code: "revision_conflict", Message: "file changed on disk", Cause: ErrConflict, Current: &current}
		}
	} else if !os.IsNotExist(statErr) {
		return FileSnapshot{}, statErr
	} else if !request.CreateOnly && request.ExpectedRevision != "" {
		return FileSnapshot{}, &Error{Code: "not_found", Message: "file was deleted from disk", Cause: ErrNotFound}
	}
	write := atomicWrite
	if request.CreateOnly {
		write = atomicCreate
	}
	if err := write(resolved, data, mode); err != nil {
		if request.CreateOnly && os.IsExist(err) {
			return FileSnapshot{}, &Error{Code: "already_exists", Message: "a file or folder already exists at that path", Cause: ErrAlreadyExists}
		}
		return FileSnapshot{}, fmt.Errorf("save file: %w", err)
	}
	op := "write"
	if request.CreateOnly {
		op = "create"
	}
	s.index.ApplyChanges(workspaceID, []Change{{Op: op, Ref: request.Ref}})
	return s.Read(workspaceID, request.Ref)
}

func (s *Service) Create(workspaceID string, request CreateRequest) (Entry, *FileSnapshot, error) {
	if err := validateName(strings.TrimSpace(request.Name)); err != nil {
		return Entry{}, nil, err
	}
	parentRelative, err := normalizeRelative(request.Parent.Path, true)
	if err != nil {
		return Entry{}, nil, err
	}
	child := FileRef{RootID: request.Parent.RootID, Path: path.Join(parentRelative, strings.TrimSpace(request.Name))}
	if IsProtectedWorkspaceMetadataPath(child.Path) {
		return Entry{}, nil, protectedMetadataError()
	}
	if request.Kind == "file" {
		snapshot, err := s.Save(workspaceID, SaveRequest{Ref: child, Content: request.Content, CreateOnly: true, HasBOM: request.HasBOM})
		if err != nil {
			return Entry{}, nil, err
		}
		entry, err := s.entryFor(workspaceID, child)
		return entry, &snapshot, err
	}
	if request.Kind != "directory" {
		return Entry{}, nil, &Error{Code: "invalid_kind", Message: "entry kind must be file or directory", Cause: ErrInvalidPath}
	}
	_, target, _, err := s.resolve(workspaceID, child, false, true)
	if err != nil {
		return Entry{}, nil, err
	}
	if _, err := os.Lstat(target); err == nil {
		return Entry{}, nil, &Error{Code: "already_exists", Message: "a file or folder already exists with that name", Cause: ErrAlreadyExists}
	} else if !os.IsNotExist(err) {
		return Entry{}, nil, err
	}
	unlock := s.lockPaths(target)
	defer unlock()
	if _, err := os.Lstat(target); err == nil {
		return Entry{}, nil, &Error{Code: "already_exists", Message: "a file or folder already exists with that name", Cause: ErrAlreadyExists}
	} else if !os.IsNotExist(err) {
		return Entry{}, nil, err
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		if os.IsExist(err) {
			return Entry{}, nil, &Error{Code: "already_exists", Message: "a file or folder already exists with that name", Cause: ErrAlreadyExists}
		}
		return Entry{}, nil, err
	}
	s.index.ApplyChanges(workspaceID, []Change{{Op: "create", Ref: child}})
	entry, err := s.entryFor(workspaceID, child)
	return entry, nil, err
}

func (s *Service) Rename(workspaceID string, ref FileRef, newName string) (Entry, error) {
	if IsProtectedWorkspaceMetadataPath(ref.Path) {
		return Entry{}, protectedMetadataError()
	}
	if err := validateName(strings.TrimSpace(newName)); err != nil {
		return Entry{}, err
	}
	_, source, _, err := s.resolveEntry(workspaceID, ref, false, false)
	if err != nil {
		return Entry{}, err
	}
	relative, _ := normalizeRelative(ref.Path, false)
	newRef := FileRef{RootID: ref.RootID, Path: path.Join(path.Dir(relative), strings.TrimSpace(newName))}
	if path.Dir(relative) == "." {
		newRef.Path = strings.TrimSpace(newName)
	}
	if IsProtectedWorkspaceMetadataPath(newRef.Path) {
		return Entry{}, protectedMetadataError()
	}
	_, destination, _, err := s.resolveEntry(workspaceID, newRef, false, true)
	if err != nil {
		return Entry{}, err
	}
	unlock := s.lockPaths(source, destination)
	defer unlock()
	if _, statErr := os.Lstat(destination); statErr == nil {
		if !sameFilesystemName(source, destination) {
			return Entry{}, &Error{Code: "already_exists", Message: "a file or folder already exists with that name", Cause: ErrAlreadyExists}
		}
	} else if !os.IsNotExist(statErr) {
		return Entry{}, statErr
	}
	if err := renameCaseSafe(source, destination); err != nil {
		return Entry{}, fmt.Errorf("rename entry: %w", err)
	}
	s.index.ApplyChanges(workspaceID, []Change{
		{Op: "rename", Ref: ref},
		{Op: "create", Ref: newRef},
	})
	return s.entryFor(workspaceID, newRef)
}

func (s *Service) entryFor(workspaceID string, ref FileRef) (Entry, error) {
	root, resolved, visible, err := s.resolveEntry(workspaceID, ref, false, false)
	if err != nil {
		return Entry{}, err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return Entry{}, err
	}
	kind := "file"
	statInfo := info
	isSymlink := info.Mode()&os.ModeSymlink != 0
	if isSymlink {
		if followed, followErr := os.Stat(resolved); followErr == nil {
			statInfo = followed
		}
	}
	if statInfo.IsDir() {
		kind = "directory"
	}
	return Entry{
		Ref: FileRef{RootID: root.ID, Path: filepath.ToSlash(ref.Path)}, Name: filepath.Base(visible), HostPath: visible,
		Kind: kind, IsSymlink: isSymlink, Size: statInfo.Size(), ModifiedAt: statInfo.ModTime().UTC().Format(time.RFC3339Nano),
	}, nil
}

func contentRevision(data []byte) string {
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func detectEOL(data []byte) string {
	crlf := bytes.Count(data, []byte("\r\n"))
	lf := bytes.Count(data, []byte("\n")) - crlf
	if crlf > 0 && crlf >= lf {
		return "crlf"
	}
	return "lf"
}

func sameFilesystemName(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func renameCaseSafe(source, destination string) error {
	if runtime.GOOS != "windows" || !strings.EqualFold(source, destination) || source == destination {
		return os.Rename(source, destination)
	}
	temporary := destination + fmt.Sprintf(".echo-rename-%d", time.Now().UnixNano())
	if err := os.Rename(source, temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Rename(temporary, source)
		return err
	}
	return nil
}
