package gitservice

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/brent/echo/internal/sourcecontrol/hunk"
)

func gitHunkSide(side DiffSide) hunk.Side {
	return hunk.Side{Content: side.Content, Exists: side.Exists, EOL: side.EOL, HasBOM: side.HasBOM}
}

func readHunkGit(ctx context.Context, state *repositoryState, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, localCommandTimeout)
	defer cancel()
	return runGit(ctx, state.root, nil, true, args...)
}

func (s *Service) describeHunks(ctx context.Context, state *repositoryState, document *DiffDocument) {
	if document.Kind != "text" || (document.Scope != "staged" && document.Scope != "unstaged") || state.protectedMetadataPath(document.Path) {
		return
	}
	index, err := readHunkGit(ctx, state, "ls-files", "--stage", "-z", "--", document.Path)
	if err != nil {
		return
	}
	for _, entry := range strings.Split(string(index), "\x00") {
		meta, _, ok := strings.Cut(entry, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 || fields[2] != "0" || (fields[0] != "100644" && fields[0] != "100755") {
			return
		}
	}
	if document.Scope == "unstaged" && document.Ref != nil {
		resolved, resolveErr := s.fs.ResolveProspectiveEntryHostPath(state.workspaceID, *document.Ref)
		if resolveErr != nil {
			return
		}
		info, statErr := os.Lstat(resolved.HostPath)
		if statErr != nil && !os.IsNotExist(statErr) {
			return
		}
		if statErr == nil && !info.Mode().IsRegular() {
			return
		}
	}
	head, _ := readHunkGit(ctx, state, "ls-tree", "-z", "HEAD", "--", document.OldPath)
	if bytes.HasPrefix(head, []byte("120000 ")) || bytes.HasPrefix(head, []byte("160000 ")) {
		return
	}
	attributes, err := readHunkGit(ctx, state, "check-attr", "-z", "--all", "--", document.Path)
	if err != nil {
		return
	}
	document.HunkToken = hunk.Token(document.RepositoryID, document.Scope, document.Path, document.OldPath, document.Original, document.Modified, string(index), string(head), string(attributes))
	if document.Scope == "staged" {
		document.HunkActions = []string{"unstage_hunk"}
	} else {
		document.HunkActions = []string{"stage_hunk", "revert_hunk"}
	}
}

func (s *Service) applyHunk(ctx context.Context, state *repositoryState, request ActionRequest) ([]string, error) {
	block := request.Hunk
	if block == nil || block.Token == "" || (block.Target.Kind != "change" && block.Target.Kind != "") || block.Target.BaseRef != "" || block.Target.Ref != "" {
		return nil, &Error{Code: "invalid_hunk", Message: "a current change block is required"}
	}
	if _, err := state.validatePaths([]string{block.Target.Path}); err != nil {
		return nil, err
	}
	scope := "unstaged"
	if request.Action == "unstage_hunk" {
		scope = "staged"
	}
	group := block.Target.GroupID
	if (scope == "staged" && group != "staged" && group != "included") || (scope == "unstaged" && group != "unstaged" && group != "working" && group != "untracked") {
		return nil, &Error{Code: "invalid_hunk", Message: "the action does not belong to this diff"}
	}
	document, err := s.diffState(ctx, state, state.public().ID, scope, block.Target.Path, block.Target.OldPath, "")
	if err != nil {
		return nil, err
	}
	if document.HunkToken != block.Token || !slices.Contains(document.HunkActions, request.Action) {
		return nil, &Error{Code: "stale_diff", Message: "This diff changed. Refresh it and choose the change again."}
	}
	destination, source := gitHunkSide(document.Original), gitHunkSide(document.Modified)
	destRange, sourceRange := block.Original, block.Modified
	if scope == "staged" {
		destination, source = source, destination
		destRange, sourceRange = sourceRange, destRange
	}
	next, err := hunk.Apply(destination, source, destRange, sourceRange)
	if err != nil {
		return nil, &Error{Code: "invalid_hunk", Message: err.Error()}
	}
	path := document.Path
	index, err := readBlob(ctx, state, ":"+path, "Index")
	if err != nil {
		return nil, err
	}
	oldBytes, newBytes := hunk.Bytes(gitHunkSide(index.side)), hunk.Bytes(next)
	if next.Exists && scope == "unstaged" {
		// Clean the worktree-form candidate exactly as git add would. This also
		// honors .gitattributes, core.autocrlf and repository clean filters.
		oid, hashErr := runGitWithTimeout(ctx, state, false, newBytes, "hash-object", "-w", "--path", path, "--stdin")
		if hashErr != nil {
			return nil, hashErr
		}
		newBytes, err = readHunkGit(ctx, state, "cat-file", "blob", strings.TrimSpace(string(oid)))
		if err != nil {
			return nil, err
		}
	}
	mode := "100644"
	entries, err := readHunkGit(ctx, state, "ls-files", "--stage", "-z", "--", path)
	if err != nil {
		return nil, err
	}
	if fields := strings.Fields(string(entries)); len(fields) > 0 {
		mode = fields[0]
	} else if scope == "staged" {
		head, _ := readHunkGit(ctx, state, "ls-tree", "-z", "HEAD", "--", document.OldPath)
		if fields := strings.Fields(string(head)); len(fields) > 0 {
			mode = fields[0]
		}
	} else if status, statusErr := s.readStatus(ctx, state); statusErr == nil {
		for _, record := range status.records {
			if record.path == path && record.workMode == "100755" {
				mode = "100755"
			}
		}
		if document.Ref != nil {
			if resolved, resolveErr := s.fs.ResolveProspectiveEntryHostPath(state.workspaceID, *document.Ref); resolveErr == nil {
				if info, statErr := os.Lstat(resolved.HostPath); statErr == nil && info.Mode()&0o111 != 0 {
					filemode, _ := readHunkGit(ctx, state, "config", "--get", "core.filemode")
					if strings.TrimSpace(string(filemode)) != "false" {
						mode = "100755"
					}
				}
			}
		}
	}
	patch := indexReplacementPatch(path, mode, oldBytes, newBytes, index.side.Exists, next.Exists)
	if len(patch) == 0 {
		return nil, &Error{Code: "invalid_hunk", Message: "this block has no index changes"}
	}
	// Revalidate after filtering, which may have invoked an external process.
	fresh, err := s.diffState(ctx, state, document.RepositoryID, scope, path, document.OldPath, "")
	if err != nil {
		return nil, err
	}
	if fresh.HunkToken != block.Token {
		return nil, &Error{Code: "stale_diff", Message: "This diff changed. Refresh it and choose the change again."}
	}
	_, err = runGitWithTimeout(ctx, state, false, patch, "apply", "--cached", "--unidiff-zero", "--whitespace=nowarn", "-")
	if err != nil {
		return nil, err
	}
	return []string{path}, nil
}

// A full-context replacement lets Git's index lock and patch matching reject
// concurrent changes to this file without rewriting unrelated index entries.
func indexReplacementPatch(path, mode string, original, modified []byte, originalExists, modifiedExists bool) []byte {
	if originalExists == modifiedExists && bytes.Equal(original, modified) {
		return nil
	}
	a, b := quotePatchPath("a/"+path), quotePatchPath("b/"+path)
	var patch strings.Builder
	fmt.Fprintf(&patch, "diff --git %s %s\n", a, b)
	if !originalExists {
		fmt.Fprintf(&patch, "new file mode %s\n", mode)
		a = "/dev/null"
	}
	if !modifiedExists {
		fmt.Fprintf(&patch, "deleted file mode %s\n", mode)
		b = "/dev/null"
	}
	fmt.Fprintf(&patch, "--- %s\n+++ %s\n", a, b)
	lines := func(data []byte) []string {
		if len(data) == 0 {
			return nil
		}
		return strings.SplitAfter(strings.TrimSuffix(string(data), "\n"), "\n")
	}
	oldCount, newCount := len(lines(original)), len(lines(modified))
	oldStart, newStart := 1, 1
	if oldCount == 0 {
		oldStart = 0
	}
	if newCount == 0 {
		newStart = 0
	}
	fmt.Fprintf(&patch, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
	writeLines := func(data []byte, prefix byte) {
		for len(data) > 0 {
			end := bytes.IndexByte(data, '\n')
			patch.WriteByte(prefix)
			if end < 0 {
				patch.Write(data)
				patch.WriteString("\n\\ No newline at end of file\n")
				break
			}
			patch.Write(data[:end+1])
			data = data[end+1:]
		}
	}
	writeLines(original, '-')
	writeLines(modified, '+')
	return []byte(patch.String())
}

func quotePatchPath(path string) string {
	var quoted strings.Builder
	quoted.WriteByte('"')
	for _, c := range []byte(path) {
		switch {
		case c == '\\' || c == '"':
			quoted.WriteByte('\\')
			quoted.WriteByte(c)
		case c < 32 || c == 127:
			fmt.Fprintf(&quoted, "\\%03o", c)
		default:
			quoted.WriteByte(c)
		}
	}
	quoted.WriteByte('"')
	return quoted.String()
}
