package fossil

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/sourcecontrol/checkpoint"
	"github.com/brent/echo/internal/workspacefs"
)

func (p *Provider) Diff(ctx context.Context, workspaceID, repositoryID string, target sourcecontrol.DiffTarget) (sourcecontrol.DiffDocument, error) {
	state, err := p.repository(ctx, workspaceID, repositoryID)
	if err != nil {
		return sourcecontrol.DiffDocument{}, err
	}
	state.rootMu.Lock()
	if err := p.recoverProtectedCommitState(ctx, state); err != nil {
		state.rootMu.Unlock()
		return sourcecontrol.DiffDocument{}, err
	}
	state.rootMu.Unlock()
	state.rootMu.RLock()
	defer state.rootMu.RUnlock()
	pathValue, err := cleanPath(target.Path)
	if err != nil || !state.pathAllowed(pathValue) {
		return sourcecontrol.DiffDocument{}, &sourcecontrol.Error{Code: "path_outside_workspace", Message: "source control path is outside this workspace", Cause: sourcecontrol.ErrInvalidPath}
	}
	target.Path = pathValue
	if target.OldPath != "" {
		oldPath, oldErr := cleanPath(target.OldPath)
		if oldErr != nil || !state.pathAllowed(oldPath) {
			return sourcecontrol.DiffDocument{}, &sourcecontrol.Error{Code: "path_outside_workspace", Message: "source control path is outside this workspace", Cause: sourcecontrol.ErrInvalidPath}
		}
		target.OldPath = oldPath
	}
	ref, _ := state.refForPath(pathValue)
	document := sourcecontrol.DiffDocument{
		RepositoryID: repositoryID, ProviderID: ID, Target: target, Ref: ref,
		Revision: state.revision.Load(), Editable: target.Kind == "change" || target.Kind == "",
	}
	manifest, checkpointErr := p.loadCheckpoint(state)
	if checkpointErr != nil {
		return sourcecontrol.DiffDocument{}, checkpointErr
	}
	protectedEntries := checkpointEntries(manifest)
	protectedEntry, isProtected := protectedEntries[pathIdentity(pathValue)]
	if !isProtected && target.GroupID == "working" && target.OldPath != "" {
		protectedEntry, isProtected = protectedEntryForRecord(protectedEntries, statusRecord{
			path: pathValue, oldPath: target.OldPath, kind: "renamed",
		})
	}
	if target.GroupID == protectedGroupID && !isProtected {
		return sourcecontrol.DiffDocument{}, &sourcecontrol.Error{Code: "protected_change_not_found", Message: "the protected file version is no longer available", Cause: sourcecontrol.ErrNotFound}
	}

	var original, modified []byte
	originalExists, modifiedExists := true, true
	if target.Kind == "revisions" {
		if err := requireRef(target.BaseRef); err != nil {
			return sourcecontrol.DiffDocument{}, err
		}
		if err := requireRef(target.Ref); err != nil {
			return sourcecontrol.DiffDocument{}, err
		}
		basePath := pathValue
		if target.OldPath != "" {
			basePath = target.OldPath
		}
		original, originalExists, err = p.revisionFile(ctx, state, target.BaseRef, basePath)
		if err == nil {
			modified, modifiedExists, err = p.revisionFile(ctx, state, target.Ref, pathValue)
		}
		if err != nil {
			return sourcecontrol.DiffDocument{}, err
		}
		document.Editable = false
		document.ModifiedRevision = target.Ref
		document.Original.Label = shortRef(target.BaseRef)
		document.Modified.Label = shortRef(target.Ref)
	} else if target.Kind == "revision_to_worktree" {
		if err := requireRef(target.BaseRef); err != nil {
			return sourcecontrol.DiffDocument{}, err
		}
		basePath := pathValue
		if target.OldPath != "" {
			basePath = target.OldPath
		}
		original, originalExists, err = p.revisionFile(ctx, state, target.BaseRef, basePath)
		if err == nil {
			modified, modifiedExists, err = p.readWorkingFile(state, pathValue)
		}
		if err != nil {
			return sourcecontrol.DiffDocument{}, err
		}
		document.Editable = true
		document.Original.Label = shortRef(target.BaseRef)
		document.Modified.Label = "Working Tree"
	} else if target.Kind == "revision" || target.Kind == "commit" {
		if err := requireRef(target.Ref); err != nil {
			return sourcecontrol.DiffDocument{}, err
		}
		infoOutput, infoErr := p.run(ctx, state.workspaceID, state.root, false, "info", target.Ref)
		if infoErr != nil {
			return sourcecontrol.DiffDocument{}, infoErr
		}
		info := parseInfo(string(infoOutput))
		modified, modifiedExists, err = p.revisionFile(ctx, state, target.Ref, pathValue)
		if err != nil {
			return sourcecontrol.DiffDocument{}, err
		}
		basePath := pathValue
		if target.OldPath != "" {
			basePath = target.OldPath
		}
		if info.Parent == "" {
			originalExists = false
		} else {
			original, originalExists, err = p.revisionFile(ctx, state, info.Parent, basePath)
			if err != nil {
				return sourcecontrol.DiffDocument{}, err
			}
		}
		document.Editable = false
		document.ModifiedRevision = target.Ref
		document.Original.Label = shortRef(info.Parent)
		document.Modified.Label = shortRef(target.Ref)
	} else if target.Kind == "stash" {
		document.Editable = false
		document.Kind = "unavailable"
		document.UnavailableReason = "Fossil stash patches can be reviewed from the stash menu but do not expose stable two-sided file content"
		return document, nil
	} else if target.GroupID == protectedGroupID && isProtected {
		basePath := pathValue
		if protectedEntry.OldPath != "" {
			basePath = protectedEntry.OldPath
		}
		original, originalExists, err = p.revisionFile(ctx, state, manifest.Baseline, basePath)
		if err == nil {
			modified, modifiedExists, err = p.checkpointFileContent(state, protectedEntry)
		}
		if err != nil {
			return sourcecontrol.DiffDocument{}, err
		}
		document.Editable = false
		document.Original.Label = "Checkout"
		document.Modified.Label = "Protected"
	} else if target.GroupID == "working" && isProtected {
		original, originalExists, err = p.checkpointFileContent(state, protectedEntry)
		if err == nil {
			modified, modifiedExists, err = p.readWorkingFile(state, pathValue)
		}
		if err != nil {
			return sourcecontrol.DiffDocument{}, err
		}
		document.Editable = true
		document.Original.Label = "Protected"
		document.Modified.Label = "Working Tree"
	} else {
		basePath := pathValue
		if target.OldPath != "" {
			basePath = target.OldPath
		}
		if target.GroupID == "untracked" {
			originalExists = false
		} else {
			original, originalExists, err = p.revisionFile(ctx, state, "current", basePath)
			if err != nil {
				return sourcecontrol.DiffDocument{}, err
			}
		}
		modified, modifiedExists, err = p.readWorkingFile(state, pathValue)
		if err != nil {
			return sourcecontrol.DiffDocument{}, err
		}
		document.Original.Label = "Checkout"
		document.Modified.Label = "Working Tree"
	}
	document.Original = diffSide(document.Original.Label, original, originalExists)
	document.Modified = diffSide(document.Modified.Label, modified, modifiedExists)
	document.Kind = "text"
	if binaryContent(original) || binaryContent(modified) {
		document.Kind = "binary"
		document.Original.Content = ""
		document.Modified.Content = ""
		document.Editable = false
		document.UnavailableReason = "Binary files cannot be shown in the text diff editor"
	}
	if int64(len(original)) > workspacefs.MaxEditableBytes || int64(len(modified)) > workspacefs.MaxEditableBytes {
		document.Kind = "too-large"
		document.Original.Content = ""
		document.Modified.Content = ""
		document.Editable = false
		document.UnavailableReason = "File is larger than the diff editor limit"
	}
	return document, nil
}

func (p *Provider) checkpointFileContent(state *repositoryState, entry checkpoint.FileState) ([]byte, bool, error) {
	if !entry.Exists {
		return nil, false, nil
	}
	if entry.Symlink {
		return []byte(entry.SymlinkTarget), true, nil
	}
	data, err := p.checkpointContent(state, entry)
	return data, true, err
}

func (p *Provider) revisionFile(ctx context.Context, state *repositoryState, revision, pathValue string) ([]byte, bool, error) {
	output, err := p.run(ctx, state.workspaceID, state.root, false, "finfo", "-p", "-r", revision, "./"+pathValue)
	if err != nil {
		message := strings.ToLower(err.Error())
		for _, absentMessage := range []string{
			"no such file",
			"not found",
			"no history for file",
			"does not exist in check-in",
			"not part of the checkout",
		} {
			if strings.Contains(message, absentMessage) {
				return nil, false, nil
			}
		}
		return nil, false, err
	}
	return output, true, nil
}

func (p *Provider) readWorkingFile(state *repositoryState, pathValue string) ([]byte, bool, error) {
	ref, ok := state.refForPath(pathValue)
	if !ok || ref == nil {
		return nil, false, &sourcecontrol.Error{Code: "path_outside_workspace", Message: "source control path is outside this workspace", Cause: sourcecontrol.ErrInvalidPath}
	}
	hostPath, err := p.fs.ResolveEntryHostPath(state.workspaceID, *ref)
	if err != nil {
		return nil, false, &sourcecontrol.Error{Code: "worktree_read_failed", Message: "working file is unavailable", Cause: err}
	}
	info, err := os.Lstat(hostPath)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, &sourcecontrol.Error{Code: "worktree_read_failed", Message: "working file is unavailable", Cause: err}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, readErr := os.Readlink(hostPath)
		if readErr != nil {
			return nil, false, &sourcecontrol.Error{Code: "worktree_read_failed", Message: "working symbolic link is unavailable", Cause: readErr}
		}
		return []byte(target), true, nil
	}
	data, err := os.ReadFile(hostPath)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, &sourcecontrol.Error{Code: "worktree_read_failed", Message: "working file is unavailable", Cause: err}
	}
	return data, true, nil
}

func diffSide(label string, data []byte, exists bool) sourcecontrol.DiffSide {
	hasBOM := bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if hasBOM {
		data = data[3:]
	}
	eol := "lf"
	if bytes.Contains(data, []byte("\r\n")) {
		eol = "crlf"
	}
	content := ""
	if utf8.Valid(data) {
		content = string(data)
	}
	return sourcecontrol.DiffSide{Label: label, Content: content, Exists: exists, EOL: eol, HasBOM: hasBOM}
}

func binaryContent(data []byte) bool { return bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) }

func shortRef(ref string) string {
	if len(ref) > 10 {
		return ref[:10]
	}
	return ref
}

func (p *Provider) Metadata(ctx context.Context, workspaceID, repositoryID string) (sourcecontrol.Metadata, error) {
	state, err := p.repository(ctx, workspaceID, repositoryID)
	if err != nil {
		return sourcecontrol.Metadata{}, err
	}
	statusInfo, err := p.checkoutInfo(ctx, state.workspaceID, state.root)
	if err != nil {
		return sourcecontrol.Metadata{}, err
	}
	metadata := sourcecontrol.Metadata{}
	if output, branchErr := p.run(ctx, state.workspaceID, state.root, false, "branch", "list", "--all"); branchErr == nil {
		for _, raw := range strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n") {
			line := strings.TrimSpace(raw)
			if line == "" {
				continue
			}
			current := strings.HasPrefix(line, "*")
			line = strings.TrimSpace(strings.TrimPrefix(line, "*"))
			closed := strings.Contains(strings.ToLower(line), "closed")
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			name := fields[0]
			metadata.Branches = append(metadata.Branches, sourcecontrol.Branch{Name: name, Current: current || name == statusInfo.Branch, Closed: closed})
		}
	}
	if output, remoteErr := p.run(ctx, state.workspaceID, state.root, false, "remote"); remoteErr == nil {
		remote := redactRemote(strings.TrimSpace(string(output)))
		if remote != "" && !strings.EqualFold(remote, "off") {
			metadata.Remotes = append(metadata.Remotes, sourcecontrol.Remote{Name: "default", FetchURL: remote, PushURL: remote})
		}
	}
	if output, stashErr := p.run(ctx, state.workspaceID, state.root, false, "stash", "list"); stashErr == nil {
		metadata.Stashes = parseStashes(string(output))
	}
	return metadata, nil
}

func redactRemote(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return credentialURLPattern.ReplaceAllString(value, "$1***@")
	}
	if parsed.User != nil {
		parsed.User = url.User("***")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func parseStashes(output string) []sourcecontrol.Stash {
	result := []sourcecontrol.Stash{}
	for _, raw := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		idText, rest, ok := strings.Cut(line, ":")
		idText = strings.TrimSpace(idText)
		if !ok {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			idText = strings.TrimSuffix(fields[0], ":")
			rest = strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
		}
		if _, err := strconv.Atoi(idText); err != nil {
			continue
		}
		result = append(result, sourcecontrol.Stash{Ref: idText, Message: strings.TrimSpace(rest)})
	}
	return result
}

func (p *Provider) History(ctx context.Context, workspaceID, repositoryID string, offset, limit int) (sourcecontrol.History, error) {
	state, err := p.repository(ctx, workspaceID, repositoryID)
	if err != nil {
		return sourcecontrol.History{}, err
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > sourcecontrol.HistoryPageSize {
		limit = sourcecontrol.HistoryPageSize
	}
	separator := "\x1f"
	format := strings.Join([]string{"%H", "%p", "%a", "%d", "%b", "%t", "%c"}, separator)
	output, err := p.run(ctx, state.workspaceID, state.root, false,
		"timeline", "-t", "ci", "-n", strconv.Itoa(limit+1), "--offset", strconv.Itoa(offset), "-W", "0", "--format", format)
	if err != nil {
		return sourcecontrol.History{}, err
	}
	commits := parseTimeline(string(output), separator)
	hasMore := len(commits) > limit
	if hasMore {
		commits = commits[:limit]
	}
	return sourcecontrol.History{Commits: commits, NextOffset: offset + len(commits), HasMore: hasMore}, nil
}

func parseTimeline(output, separator string) []sourcecontrol.Commit {
	commits := []sourcecontrol.Commit{}
	for _, line := range strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n") {
		fields := strings.Split(line, separator)
		if len(fields) < 7 || strings.TrimSpace(fields[0]) == "" {
			continue
		}
		refs := []string{}
		for _, value := range []string{fields[1], fields[4], fields[5]} {
			for _, ref := range strings.Fields(strings.ReplaceAll(value, ",", " ")) {
				if ref != "" {
					refs = append(refs, ref)
				}
			}
		}
		commits = append(commits, sourcecontrol.Commit{
			Hash:   strings.TrimSpace(fields[0]),
			Author: strings.TrimSpace(fields[2]), AuthoredAt: strings.TrimSpace(fields[3]), Refs: refs,
			Subject: firstLine(strings.TrimSpace(strings.Join(fields[6:], separator))),
		})
	}
	return commits
}

func (p *Provider) RevisionDetail(ctx context.Context, workspaceID, repositoryID, ref, kind string) (sourcecontrol.RevisionDetail, error) {
	state, err := p.repository(ctx, workspaceID, repositoryID)
	if err != nil {
		return sourcecontrol.RevisionDetail{}, err
	}
	if kind == "stash" {
		if _, parseErr := strconv.Atoi(ref); parseErr != nil {
			return sourcecontrol.RevisionDetail{}, &sourcecontrol.Error{Code: "invalid_stash", Message: "Fossil stash ID is invalid"}
		}
		output, showErr := p.run(ctx, state.workspaceID, state.root, false, "stash", "show", ref)
		if showErr != nil {
			return sourcecontrol.RevisionDetail{}, showErr
		}
		return sourcecontrol.RevisionDetail{Ref: ref, Files: parsePatchFiles(string(output))}, nil
	}
	if err := requireRef(ref); err != nil {
		return sourcecontrol.RevisionDetail{}, err
	}
	infoOutput, err := p.run(ctx, state.workspaceID, state.root, false, "info", ref)
	if err != nil {
		return sourcecontrol.RevisionDetail{}, err
	}
	info := parseInfo(string(infoOutput))
	if info.Parent == "" {
		output, listErr := p.run(ctx, state.workspaceID, state.root, false, "ls", "-r", ref)
		if listErr != nil {
			return sourcecontrol.RevisionDetail{}, listErr
		}
		files := []sourcecontrol.RevisionFile{}
		for _, pathValue := range strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n") {
			if pathValue = filepathClean(pathValue); pathValue != "" && state.pathAllowed(pathValue) {
				files = append(files, sourcecontrol.RevisionFile{Path: pathValue, Status: "A"})
			}
		}
		return sourcecontrol.RevisionDetail{Ref: ref, Files: files}, nil
	}
	output, err := p.run(ctx, state.workspaceID, state.root, false, "diff", "--from", info.Parent, "--to", ref, "--internal", "--brief")
	if err != nil {
		return sourcecontrol.RevisionDetail{}, err
	}
	files := parseBriefDiff(string(output))
	filtered := files[:0]
	for _, file := range files {
		if state.pathAllowed(file.Path) {
			filtered = append(filtered, file)
		}
	}
	return sourcecontrol.RevisionDetail{Ref: ref, Files: filtered}, nil
}

func parseBriefDiff(output string) []sourcecontrol.RevisionFile {
	result := []sourcecontrol.RevisionFile{}
	seen := make(map[string]bool)
	for _, raw := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "Index:") {
			pathValue := filepathClean(strings.TrimSpace(strings.TrimPrefix(line, "Index:")))
			if pathValue != "" && !seen[pathValue] {
				seen[pathValue] = true
				result = append(result, sourcecontrol.RevisionFile{Path: pathValue, Status: "M"})
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		code := strings.ToUpper(strings.TrimSuffix(fields[0], ":"))
		if code != "ADDED" && code != "DELETED" && code != "CHANGED" && code != "RENAMED" {
			continue
		}
		pathValue := filepathClean(strings.TrimSpace(line[len(fields[0]):]))
		if pathValue == "" || seen[pathValue] {
			continue
		}
		seen[pathValue] = true
		status := map[string]string{"ADDED": "A", "DELETED": "D", "CHANGED": "M", "RENAMED": "R"}[code]
		result = append(result, sourcecontrol.RevisionFile{Path: pathValue, Status: status})
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}

func parsePatchFiles(output string) []sourcecontrol.RevisionFile { return parseBriefDiff(output) }

func firstLine(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	if before, _, ok := strings.Cut(value, "\n"); ok {
		return strings.TrimSpace(before)
	}
	return value
}
