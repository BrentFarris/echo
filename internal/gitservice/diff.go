package gitservice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/brent/echo/internal/workspacefs"
)

type blobResult struct {
	side     DiffSide
	kind     string
	revision string
}

func (s *Service) Diff(ctx context.Context, workspaceID, repositoryID, scope, path, oldPath, ref string) (DiffDocument, error) {
	state, err := s.repository(ctx, workspaceID, repositoryID)
	if err != nil {
		return DiffDocument{}, err
	}
	path, err = cleanGitPath(path)
	if err != nil {
		return DiffDocument{}, err
	}
	if !state.pathAllowed(path) {
		return DiffDocument{}, &Error{Code: "path_outside_workspace", Message: "Git path is outside this workspace", Cause: ErrInvalidPath}
	}
	if strings.TrimSpace(oldPath) == "" {
		oldPath = path
	} else {
		oldPath, err = cleanGitPath(oldPath)
		if err != nil {
			return DiffDocument{}, err
		}
	}
	fileRef, _ := state.refForPath(path)
	document := DiffDocument{
		RepositoryID: repositoryID, Scope: scope, Path: path, OldPath: oldPath,
		Ref: fileRef, Revision: state.revision.Load(), Kind: "text",
	}

	if submodule, subErr := isGitlink(ctx, state, path); subErr == nil && submodule {
		document.Kind = "submodule"
		document.UnavailableReason = "Submodule changes are represented by commit IDs and cannot be edited as text."
		return document, nil
	}

	var original, modified blobResult
	switch scope {
	case "staged":
		original, err = readBlob(ctx, state, "HEAD:"+oldPath, "HEAD")
		if err == nil {
			modified, err = readBlob(ctx, state, ":"+path, "Index")
		}
		document.Editable = false
	case "unstaged":
		original, err = readBlob(ctx, state, ":"+path, "Index")
		if err == nil && !original.side.Exists && oldPath != path {
			original, err = readBlob(ctx, state, ":"+oldPath, "Index")
		}
		if err == nil {
			modified, err = s.readWorktreeFile(workspaceID, fileRef, path)
		}
		document.Editable = true
	case "commit", "stash":
		validated, validateErr := validateCommitRef(ctx, state, ref)
		if validateErr != nil {
			return DiffDocument{}, validateErr
		}
		original, err = readBlob(ctx, state, validated+"^:"+oldPath, validated+"^")
		if err == nil {
			modified, err = readBlob(ctx, state, validated+":"+path, validated)
		}
		document.Editable = false
	default:
		return DiffDocument{}, &Error{Code: "invalid_diff_scope", Message: "unsupported Git diff scope"}
	}
	if err != nil {
		return DiffDocument{}, err
	}
	document.Original = original.side
	document.Modified = modified.side
	document.ModifiedRevision = modified.revision
	for _, result := range []blobResult{original, modified} {
		if result.kind == "too-large" {
			document.Kind = "too-large"
			document.UnavailableReason = "This diff exceeds Echo Code's 10 MiB editor limit."
			document.Original.Content = ""
			document.Modified.Content = ""
			break
		}
		if result.kind == "binary" {
			document.Kind = "binary"
			document.UnavailableReason = "Binary or non-UTF-8 files cannot be displayed as a text diff."
			document.Original.Content = ""
			document.Modified.Content = ""
		}
	}
	return document, nil
}

func readBlob(parent context.Context, state *repositoryState, specification, label string) (blobResult, error) {
	ctx, cancel := context.WithTimeout(parent, localCommandTimeout)
	defer cancel()
	sizeOutput, err := runGit(ctx, state.root, nil, true, "cat-file", "-s", specification)
	if err != nil {
		// A missing side is expected for additions, deletions, unborn HEADs, and
		// the first parent of a root commit.
		return blobResult{side: DiffSide{Label: label, Exists: false, EOL: "lf"}}, nil
	}
	size, parseErr := strconv.ParseInt(strings.TrimSpace(string(sizeOutput)), 10, 64)
	if parseErr != nil {
		return blobResult{}, &Error{Code: "invalid_git_blob", Message: "Git returned an invalid object size", Cause: parseErr}
	}
	if size > workspacefs.MaxEditableBytes {
		return blobResult{side: DiffSide{Label: label, Exists: true, EOL: "lf"}, kind: "too-large"}, nil
	}
	content, err := runGit(ctx, state.root, nil, true, "cat-file", "blob", specification)
	if err != nil {
		return blobResult{}, err
	}
	return textBlob(label, content), nil
}

func (s *Service) readWorktreeFile(workspaceID string, ref *workspacefs.FileRef, path string) (blobResult, error) {
	if ref == nil {
		return blobResult{}, &Error{Code: "path_outside_workspace", Message: "Git path is outside this workspace", Cause: ErrInvalidPath}
	}
	snapshot, err := s.fs.Read(workspaceID, *ref)
	if err == nil {
		return blobResult{side: DiffSide{
			Label: "Working Tree", Content: snapshot.Content, Exists: true,
			EOL: snapshot.EOL, HasBOM: snapshot.HasBOM,
		}, revision: snapshot.Revision}, nil
	}
	var fsError *workspacefs.Error
	if errors.As(err, &fsError) {
		switch fsError.Code {
		case "not_found":
			return blobResult{side: DiffSide{Label: "Working Tree", Exists: false, EOL: "lf"}}, nil
		case "file_too_large":
			return blobResult{side: DiffSide{Label: "Working Tree", Exists: true, EOL: "lf"}, kind: "too-large"}, nil
		case "unsupported_file", "invalid_utf8":
			return blobResult{side: DiffSide{Label: "Working Tree", Exists: true, EOL: "lf"}, kind: "binary"}, nil
		}
	}
	return blobResult{}, &Error{Code: "worktree_read_failed", Message: fmt.Sprintf("could not read %s from the working tree", path), Cause: err}
}

func textBlob(label string, content []byte) blobResult {
	if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
		return blobResult{side: DiffSide{Label: label, Exists: true, EOL: "lf"}, kind: "binary"}
	}
	hasBOM := bytes.HasPrefix(content, []byte{0xef, 0xbb, 0xbf})
	if hasBOM {
		content = content[3:]
	}
	eol := "lf"
	if bytes.Contains(content, []byte("\r\n")) {
		eol = "crlf"
	}
	return blobResult{side: DiffSide{Label: label, Content: string(content), Exists: true, EOL: eol, HasBOM: hasBOM}}
}

func validateCommitRef(parent context.Context, state *repositoryState, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.ContainsRune(ref, 0) {
		return "", &Error{Code: "invalid_git_ref", Message: "Git revision is required"}
	}
	ctx, cancel := context.WithTimeout(parent, localCommandTimeout)
	defer cancel()
	output, err := runGit(ctx, state.root, nil, true, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", &Error{Code: "invalid_git_ref", Message: "Git revision was not found", Cause: err}
	}
	return strings.TrimSpace(string(output)), nil
}

func isGitlink(parent context.Context, state *repositoryState, path string) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, localCommandTimeout)
	defer cancel()
	output, err := runGit(ctx, state.root, nil, true, "ls-files", "--stage", "--", path)
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(string(output), "160000 "), nil
}
