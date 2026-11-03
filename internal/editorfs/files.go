package editorfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/brent/echo/internal/mutation"
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

func (s *Service) Read(botID string, ref FileRef) (FileSnapshot, error) {
	_, resolved, visible, err := s.resolve(botID, ref, false, false)
	if err != nil {
		return FileSnapshot{}, err
	}
	info, err := s.stat(botID, resolved)
	if err != nil || !info.Mode().IsRegular() {
		return FileSnapshot{}, &Error{Code: "not_found", Message: "file not found", Cause: ErrNotFound}
	}
	if info.Size() > MaxEditableBytes {
		return FileSnapshot{}, &Error{Code: "file_too_large", Message: "file is larger than the 10 MiB editor limit", Cause: ErrTooLarge}
	}
	file, err := s.open(botID, resolved)
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

func (s *Service) Save(botID string, request SaveRequest) (FileSnapshot, error) {
	return s.SaveContext(context.Background(), botID, request)
}

func (s *Service) SaveContext(ctx context.Context, botID string, request SaveRequest) (FileSnapshot, error) {
	if s.isProtectedMetadata(botID, request.Ref) {
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
	_, resolved, _, err := s.resolve(botID, request.Ref, false, true)
	if err != nil {
		return FileSnapshot{}, err
	}
	unlock := s.lockPaths(resolved)
	defer unlock()

	mode := os.FileMode(0o644)
	existing, statErr := s.stat(botID, resolved)
	if statErr == nil {
		if request.CreateOnly {
			return FileSnapshot{}, &Error{Code: "already_exists", Message: "a file or folder already exists at that path", Cause: ErrAlreadyExists}
		}
		if !existing.Mode().IsRegular() {
			return FileSnapshot{}, &Error{Code: "not_a_file", Message: "path is not a regular file", Cause: ErrInvalidPath}
		}
		mode = existing.Mode().Perm()
		currentData, readErr := s.readFile(botID, resolved)
		if readErr != nil {
			return FileSnapshot{}, readErr
		}
		currentRevision := contentRevision(currentData)
		if request.ExpectedRevision == "" || request.ExpectedRevision != currentRevision {
			current, _ := s.Read(botID, request.Ref)
			return FileSnapshot{}, &Error{Code: "revision_conflict", Message: "file changed on disk", Cause: ErrConflict, Current: &current}
		}
	} else if !os.IsNotExist(statErr) {
		return FileSnapshot{}, statErr
	} else if !request.CreateOnly && request.ExpectedRevision != "" {
		return FileSnapshot{}, &Error{Code: "not_found", Message: "file was deleted from disk", Cause: ErrNotFound}
	}
	write := func(path string, data []byte, mode os.FileMode) error {
		return s.write(botID, path, data, mode, request.CreateOnly)
	}
	var registration mutation.Result
	writeErr := s.mutateContext(ctx, Operation{BotID: botID, Path: resolved, Registration: &registration, Origin: "editor", Validate: func() error {
		if statErr == nil {
			current, err := s.readFile(botID, resolved)
			if err != nil {
				return err
			}
			if contentRevision(current) != request.ExpectedRevision {
				return &Error{Code: "revision_conflict", Message: "file changed on disk", Cause: ErrConflict}
			}
		}
		return nil
	}}, func() error {
		// Preserve the current file permissions for the atomic replacement.
		if info, err := s.stat(botID, resolved); err == nil {
			mode = info.Mode().Perm()
		}
		return write(resolved, data, mode)
	})
	if err := writeErr; err != nil {
		if request.CreateOnly && os.IsExist(err) {
			return FileSnapshot{}, &Error{Code: "already_exists", Message: "a file or folder already exists at that path", Cause: ErrAlreadyExists}
		}
		return FileSnapshot{}, fmt.Errorf("save file: %w", err)
	}
	op := "write"
	if request.CreateOnly {
		op = "create"
	}
	s.index.ApplyChanges(botID, []Change{{Op: op, Ref: request.Ref}})
	snapshot, err := s.Read(botID, request.Ref)
	snapshot.Registration = registration
	return snapshot, err
}

func (s *Service) Create(botID string, request CreateRequest) (Entry, *FileSnapshot, error) {
	return s.CreateContext(context.Background(), botID, request)
}

func (s *Service) CreateContext(ctx context.Context, botID string, request CreateRequest) (Entry, *FileSnapshot, error) {
	if err := validateName(strings.TrimSpace(request.Name)); err != nil {
		return Entry{}, nil, err
	}
	parentRelative, err := normalizeRelative(request.Parent.Path, true)
	if err != nil {
		return Entry{}, nil, err
	}
	child := FileRef{RootID: request.Parent.RootID, Path: path.Join(parentRelative, strings.TrimSpace(request.Name))}
	if s.isProtectedMetadata(botID, child) {
		return Entry{}, nil, protectedMetadataError()
	}
	if request.Kind == "file" {
		snapshot, err := s.SaveContext(ctx, botID, SaveRequest{Ref: child, Content: request.Content, CreateOnly: true, HasBOM: request.HasBOM})
		if err != nil {
			return Entry{}, nil, err
		}
		entry, err := s.entryFor(botID, child)
		return entry, &snapshot, err
	}
	if request.Kind != "directory" {
		return Entry{}, nil, &Error{Code: "invalid_kind", Message: "entry kind must be file or directory", Cause: ErrInvalidPath}
	}
	_, target, _, err := s.resolve(botID, child, false, true)
	if err != nil {
		return Entry{}, nil, err
	}
	if _, err := s.lstat(botID, target); err == nil {
		return Entry{}, nil, &Error{Code: "already_exists", Message: "a file or folder already exists with that name", Cause: ErrAlreadyExists}
	} else if !os.IsNotExist(err) {
		return Entry{}, nil, err
	}
	unlock := s.lockPaths(target)
	defer unlock()
	if _, err := s.lstat(botID, target); err == nil {
		return Entry{}, nil, &Error{Code: "already_exists", Message: "a file or folder already exists with that name", Cause: ErrAlreadyExists}
	} else if !os.IsNotExist(err) {
		return Entry{}, nil, err
	}
	if err := s.mkdir(botID, target, 0o755); err != nil {
		if os.IsExist(err) {
			return Entry{}, nil, &Error{Code: "already_exists", Message: "a file or folder already exists with that name", Cause: ErrAlreadyExists}
		}
		return Entry{}, nil, err
	}
	s.index.ApplyChanges(botID, []Change{{Op: "create", Ref: child}})
	entry, err := s.entryFor(botID, child)
	return entry, nil, err
}

func (s *Service) Rename(botID string, ref FileRef, newName string) (Entry, error) {
	return s.RenameContext(context.Background(), botID, ref, newName)
}

func (s *Service) RenameContext(ctx context.Context, botID string, ref FileRef, newName string) (Entry, error) {
	newName = strings.TrimSpace(newName)
	if err := validateName(newName); err != nil {
		return Entry{}, err
	}
	relative, err := normalizeRelative(ref.Path, false)
	if err != nil {
		return Entry{}, err
	}
	parentPath := path.Dir(relative)
	if parentPath == "." {
		parentPath = ""
	}
	return s.moveEntry(ctx, botID, ref, FileRef{RootID: ref.RootID, Path: parentPath}, newName)
}

// Move relocates an entry to another directory in the same workspace root.
// The entry keeps its current name; Rename uses the same confined operation
// with a different name and its existing parent.
func (s *Service) Move(botID string, ref, destinationParent FileRef) (Entry, error) {
	return s.MoveContext(context.Background(), botID, ref, destinationParent)
}

func (s *Service) MoveContext(ctx context.Context, botID string, ref, destinationParent FileRef) (Entry, error) {
	relative, err := normalizeRelative(ref.Path, false)
	if err != nil {
		return Entry{}, err
	}
	return s.moveEntry(ctx, botID, ref, destinationParent, path.Base(relative))
}

func (s *Service) moveEntry(ctx context.Context, botID string, ref, destinationParent FileRef, newName string) (Entry, error) {
	if s.wouldAffectProtectedMetadata(botID, ref) {
		return Entry{}, protectedMetadataError()
	}
	if strings.TrimSpace(ref.RootID) != strings.TrimSpace(destinationParent.RootID) {
		return Entry{}, &Error{Code: "cross_root_move_unsupported", Message: "files cannot be moved between workspace folders", Cause: ErrInvalidPath}
	}
	if err := validateName(newName); err != nil {
		return Entry{}, err
	}
	parentRelative, err := normalizeRelative(destinationParent.Path, true)
	if err != nil {
		return Entry{}, err
	}
	newRef := FileRef{RootID: destinationParent.RootID, Path: path.Join(parentRelative, newName)}
	if s.wouldAffectProtectedMetadata(botID, newRef) {
		return Entry{}, protectedMetadataError()
	}
	_, source, _, err := s.resolveEntry(botID, ref, false, false)
	if err != nil {
		return Entry{}, err
	}
	_, destinationDirectory, _, err := s.resolve(botID, destinationParent, true, false)
	if err != nil {
		return Entry{}, err
	}
	directoryInfo, err := s.stat(botID, destinationDirectory)
	if err != nil {
		return Entry{}, err
	}
	if !directoryInfo.IsDir() {
		return Entry{}, &Error{Code: "invalid_move_destination", Message: "move destination must be a folder", Cause: ErrInvalidPath}
	}
	sourceInfo, err := s.lstat(botID, source)
	if err != nil {
		return Entry{}, err
	}
	if sourceInfo.IsDir() {
		if relative, relativeErr := filepath.Rel(source, destinationDirectory); relativeErr == nil &&
			(relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))) {
			return Entry{}, &Error{Code: "invalid_move_destination", Message: "a folder cannot be moved into itself", Cause: ErrInvalidPath}
		}
	}
	_, destination, _, err := s.resolveEntry(botID, newRef, false, true)
	if err != nil {
		return Entry{}, err
	}
	if source == destination {
		return s.entryFor(botID, ref)
	}
	unlock := s.lockPaths(source, destination)
	defer unlock()
	if _, statErr := s.lstat(botID, destination); statErr == nil {
		if !sameFilesystemName(source, destination) {
			return Entry{}, &Error{Code: "already_exists", Message: "a file or folder already exists with that name", Cause: ErrAlreadyExists}
		}
	} else if !os.IsNotExist(statErr) {
		return Entry{}, statErr
	}
	var registration mutation.Result
	moveErr := s.mutateContext(ctx, Operation{BotID: botID, Path: source, Destination: destination, Kind: "move", Origin: "explorer", Registration: &registration, Validate: func() error {
		current, err := s.lstat(botID, source)
		if err != nil {
			return err
		}
		if !os.SameFile(sourceInfo, current) {
			return &Error{Code: "revision_conflict", Message: "move source changed on disk", Cause: ErrConflict}
		}
		if _, err := s.lstat(botID, destination); err == nil && !sameFilesystemName(source, destination) {
			return &Error{Code: "already_exists", Message: "move destination was created concurrently", Cause: ErrAlreadyExists}
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}}, func() error { return s.rename(botID, source, destination) })
	if err := moveErr; err != nil {
		return Entry{}, fmt.Errorf("move entry: %w", err)
	}
	s.index.ApplyChanges(botID, []Change{
		{Op: "rename", Ref: ref},
		{Op: "create", Ref: newRef},
	})
	entry, err := s.entryFor(botID, newRef)
	entry.Registration = registration
	return entry, err
}

func (s *Service) entryFor(botID string, ref FileRef) (Entry, error) {
	root, resolved, visible, err := s.resolveEntry(botID, ref, false, false)
	if err != nil {
		return Entry{}, err
	}
	info, err := s.lstat(botID, resolved)
	if err != nil {
		return Entry{}, err
	}
	kind := "file"
	statInfo := info
	isSymlink := info.Mode()&os.ModeSymlink != 0
	if isSymlink {
		if followed, followErr := s.stat(botID, resolved); followErr == nil {
			statInfo = followed
		}
	}
	if statInfo.IsDir() {
		kind = "directory"
	}
	return Entry{
		Ref: FileRef{RootID: root.ID, Path: filepath.ToSlash(ref.Path)}, Name: filepath.Base(visible), HostPath: visible,
		Kind: kind, IsSymlink: isSymlink, ReadOnly: s.isProtectedMetadata(botID, ref),
		Size: statInfo.Size(), ModifiedAt: statInfo.ModTime().UTC().Format(time.RFC3339Nano),
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

// MediaTypeForName maps a file name to the media type the preview surface can
// display. Empty means the browser cannot render this file as image, video, or
// audio.
func MediaTypeForName(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".bmp":
		return "image/bmp"
	case ".ico":
		return "image/x-icon"
	case ".avif":
		return "image/avif"
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".ogv":
		return "video/ogg"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg", ".oga", ".opus":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".m4a":
		return "audio/mp4"
	case ".aac":
		return "audio/aac"
	case ".weba":
		return "audio/webm"
	default:
		return ""
	}
}

// Previewable reports whether name identifies a file the browser preview
// surface can display and returns its media type.
func Previewable(name string) (bool, string) {
	mediaType := MediaTypeForName(name)
	return mediaType != "", mediaType
}

// MediaMeta resolves a previewable file and reports how much of it fits under
// the preview cap. Truncated marks oversized files whose first chunk is still
// useful for images; video players need the complete file so truncated videos
// should not be served.
func (s *Service) MediaMeta(botID string, ref FileRef) (path string, size int64, mediaType string, truncated bool, err error) {
	_, resolved, _, resolveErr := s.resolve(botID, ref, false, false)
	if resolveErr != nil {
		err = resolveErr
		return
	}
	info, statErr := s.stat(botID, resolved)
	if statErr != nil || !info.Mode().IsRegular() {
		err = &Error{Code: "not_found", Message: "file not found", Cause: ErrNotFound}
		return
	}
	previewable, mapped := Previewable(filepath.Base(info.Name()))
	if !previewable {
		err = &Error{Code: "unsupported_preview", Message: "file is not a supported image, video, or audio type", Cause: ErrNotPreviewable}
		return
	}
	size = info.Size()
	truncated = size > MaxMediaBytes
	if truncated && (strings.HasPrefix(mapped, "video/") || strings.HasPrefix(mapped, "audio/")) {
		err = &Error{Code: "file_too_large", Message: "file is larger than the 500 MiB preview limit", Cause: ErrTooLarge}
		return
	}
	path = resolved
	mediaType = mapped
	return
}
