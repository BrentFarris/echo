package gitservice

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type statusRecord struct {
	kind      byte
	path      string
	oldPath   string
	indexOID  string
	indexMode string
	workMode  string
	index     byte
	worktree  byte
	submodule bool
	conflict  bool
}

type parsedStatus struct {
	head       string
	branch     string
	detached   bool
	upstream   string
	ahead      int
	behind     int
	stashCount int
	records    []statusRecord
}

func (s *Service) Status(ctx context.Context, workspaceID, repositoryID string) (StatusSnapshot, error) {
	state, err := s.repository(ctx, workspaceID, repositoryID)
	if err != nil {
		return StatusSnapshot{}, err
	}
	state.mutationMu.Lock()
	defer state.mutationMu.Unlock()
	return s.loadStatus(ctx, state)
}

func (s *Service) loadStatus(parent context.Context, state *repositoryState) (StatusSnapshot, error) {
	parsed, err := s.readStatus(parent, state)
	if err != nil {
		return StatusSnapshot{}, err
	}
	return buildStatusSnapshot(state, parsed), nil
}

func (s *Service) readStatus(parent context.Context, state *repositoryState) (parsedStatus, error) {
	ctx, cancel := context.WithTimeout(parent, localCommandTimeout)
	defer cancel()
	select {
	case s.statusSlots <- struct{}{}:
		defer func() { <-s.statusSlots }()
	case <-ctx.Done():
		return parsedStatus{}, &Error{Code: "git_timeout", Message: "Git status timed out or was cancelled", Cause: ctx.Err()}
	}
	output, err := runGit(ctx, state.root, nil, true,
		"status", "--porcelain=v2", "--branch", "--show-stash", "-z", "--untracked-files=all")
	if err != nil {
		return parsedStatus{}, err
	}
	parsed, err := parseStatusPorcelainV2(output)
	if err != nil {
		return parsedStatus{}, err
	}
	parsed.records = clearFilterOnlyWorktreeChanges(ctx, state, parsed.records)
	return parsed, nil
}

// A clean filter added after a file was committed can make Git report the
// restored worktree copy as modified forever: checkout writes the raw index
// blob, then status filters those same bytes and compares the transformed
// result with the unfiltered blob in the index. If the raw worktree bytes
// already hash to the index object, there is no worktree edit left to show.
func clearFilterOnlyWorktreeChanges(ctx context.Context, state *repositoryState, records []statusRecord) []statusRecord {
	filterPaths := pathsWithCleanFilters(ctx, state, records)
	if len(filterPaths) == 0 {
		return records
	}
	filtered := records[:0]
	for _, record := range records {
		if filterPaths[pathIdentity(record.path)] && record.worktree == 'M' && record.indexMode == record.workMode &&
			!record.conflict && !record.submodule &&
			rawFileMatchesIndexBlob(state.root, record.path, record.indexOID) {
			record.worktree = '.'
		}
		if record.index == '.' && record.worktree == '.' && !record.conflict {
			continue
		}
		filtered = append(filtered, record)
	}
	return filtered
}

func pathsWithCleanFilters(ctx context.Context, state *repositoryState, records []statusRecord) map[string]bool {
	var input bytes.Buffer
	for _, record := range records {
		if record.worktree == 'M' && record.indexMode == record.workMode && !record.conflict && !record.submodule {
			input.WriteString(record.path)
			input.WriteByte(0)
		}
	}
	if input.Len() == 0 {
		return nil
	}
	output, err := runGit(ctx, state.root, input.Bytes(), true, "check-attr", "-z", "--stdin", "filter")
	if err != nil {
		return nil
	}
	fields := bytes.Split(output, []byte{0})
	paths := make(map[string]bool)
	for index := 0; index+2 < len(fields); index += 3 {
		if string(fields[index+1]) != "filter" {
			continue
		}
		value := string(fields[index+2])
		if value != "" && value != "unspecified" && value != "unset" {
			paths[pathIdentity(string(fields[index]))] = true
		}
	}
	return paths
}

func rawFileMatchesIndexBlob(root, path, oid string) bool {
	var digest hash.Hash
	switch len(oid) {
	case sha1.Size * 2:
		digest = sha1.New()
	case sha256.Size * 2:
		digest = sha256.New()
	default:
		return false
	}
	clean, err := cleanGitPath(path)
	if err != nil {
		return false
	}
	fullPath := filepath.Join(root, filepath.FromSlash(clean))
	info, err := os.Lstat(fullPath)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	file, err := os.Open(fullPath)
	if err != nil {
		return false
	}
	defer file.Close()
	_, _ = fmt.Fprintf(digest, "blob %d\x00", info.Size())
	if _, err := io.Copy(digest, file); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), oid)
}

func buildStatusSnapshot(state *repositoryState, parsed parsedStatus) StatusSnapshot {
	snapshot := StatusSnapshot{
		WorkspaceID: state.workspaceID, RepositoryID: repositoryID(state.workspaceID, state.root),
		Revision: state.revision.Load(), Branch: parsed.branch, Head: parsed.head,
		Detached: parsed.detached, Upstream: parsed.upstream, Ahead: parsed.ahead,
		Behind: parsed.behind, StashCount: parsed.stashCount,
		Conflicts: []Change{}, Staged: []Change{}, Unstaged: []Change{},
		State: repositoryOperationState(state.gitDir),
	}
	visible := 0
	for _, record := range parsed.records {
		ref, allowed := state.refForPath(record.path)
		if !allowed {
			if record.index != '.' && record.index != '?' && record.index != '!' {
				snapshot.HiddenStagedCount++
			}
			continue
		}
		visible++
		if visible > StatusLimit {
			snapshot.Truncated = true
			continue
		}
		base := Change{
			Path: record.path, OldPath: record.oldPath, Ref: ref,
			IndexStatus: statusCharacter(record.index), WorktreeStatus: statusCharacter(record.worktree),
			Submodule: record.submodule,
		}
		if record.conflict {
			change := base
			change.Status = "conflicted"
			change.StatusCode = "U"
			change.Scope = "conflict"
			snapshot.Conflicts = append(snapshot.Conflicts, change)
			continue
		}
		if record.index != '.' && record.index != '?' && record.index != '!' {
			change := base
			change.Status = statusName(record.index)
			change.StatusCode = statusCharacter(record.index)
			change.Scope = "staged"
			snapshot.Staged = append(snapshot.Staged, change)
		}
		if record.worktree != '.' || record.kind == '?' {
			change := base
			change.Status = statusName(record.worktree)
			change.StatusCode = statusCharacter(record.worktree)
			change.Scope = "unstaged"
			snapshot.Unstaged = append(snapshot.Unstaged, change)
		}
	}
	snapshot.TotalChangeCount = visible
	sortChanges(snapshot.Conflicts)
	sortChanges(snapshot.Staged)
	sortChanges(snapshot.Unstaged)
	return snapshot
}

func parseStatusPorcelainV2(data []byte) (parsedStatus, error) {
	result := parsedStatus{records: []statusRecord{}}
	for position := 0; position < len(data); {
		if data[position] == '\n' || data[position] == 0 {
			position++
			continue
		}
		if data[position] == '#' {
			end := bytes.IndexByte(data[position:], '\n')
			nul := bytes.IndexByte(data[position:], 0)
			if end < 0 || (nul >= 0 && nul < end) {
				end = nul
			}
			if end < 0 {
				end = len(data) - position
			}
			parseStatusHeader(strings.TrimSpace(string(data[position:position+end])), &result)
			position += end
			continue
		}
		end := bytes.IndexByte(data[position:], 0)
		if end < 0 {
			end = len(data) - position
		}
		recordData := string(data[position : position+end])
		position += end
		if position < len(data) && data[position] == 0 {
			position++
		}
		if recordData == "" {
			continue
		}
		record, rename, err := parseStatusRecord(recordData)
		if err != nil {
			return parsedStatus{}, err
		}
		if rename {
			oldEnd := bytes.IndexByte(data[position:], 0)
			if oldEnd < 0 {
				oldEnd = len(data) - position
			}
			record.oldPath = string(data[position : position+oldEnd])
			position += oldEnd
			if position < len(data) && data[position] == 0 {
				position++
			}
		}
		result.records = append(result.records, record)
	}
	if result.branch == "" && result.detached {
		result.branch = "detached"
	}
	return result, nil
}

func parseStatusHeader(header string, status *parsedStatus) {
	fields := strings.Fields(header)
	if len(fields) < 3 || fields[0] != "#" {
		return
	}
	value := strings.Join(fields[2:], " ")
	switch fields[1] {
	case "branch.oid":
		if value != "(initial)" {
			status.head = value
		}
	case "branch.head":
		if value == "(detached)" {
			status.detached = true
		} else {
			status.branch = value
		}
	case "branch.upstream":
		status.upstream = value
	case "branch.ab":
		for _, field := range fields[2:] {
			if strings.HasPrefix(field, "+") {
				status.ahead, _ = strconv.Atoi(strings.TrimPrefix(field, "+"))
			}
			if strings.HasPrefix(field, "-") {
				status.behind, _ = strconv.Atoi(strings.TrimPrefix(field, "-"))
			}
		}
	case "stash":
		status.stashCount, _ = strconv.Atoi(value)
	}
}

func parseStatusRecord(value string) (statusRecord, bool, error) {
	record := statusRecord{index: '.', worktree: '.'}
	switch value[0] {
	case '1':
		fields := strings.SplitN(value, " ", 9)
		if len(fields) != 9 || len(fields[1]) != 2 {
			return statusRecord{}, false, malformedStatus(value)
		}
		record.kind, record.index, record.worktree = '1', fields[1][0], fields[1][1]
		record.submodule = fields[2] != "N..."
		record.indexMode, record.workMode = fields[4], fields[5]
		record.indexOID = fields[7]
		record.path = fields[8]
	case '2':
		fields := strings.SplitN(value, " ", 10)
		if len(fields) != 10 || len(fields[1]) != 2 {
			return statusRecord{}, false, malformedStatus(value)
		}
		record.kind, record.index, record.worktree = '2', fields[1][0], fields[1][1]
		record.submodule = fields[2] != "N..."
		record.indexMode, record.workMode = fields[4], fields[5]
		record.indexOID = fields[7]
		record.path = fields[9]
		return record, true, nil
	case 'u':
		fields := strings.SplitN(value, " ", 11)
		if len(fields) != 11 || len(fields[1]) != 2 {
			return statusRecord{}, false, malformedStatus(value)
		}
		record.kind, record.index, record.worktree = 'u', fields[1][0], fields[1][1]
		record.submodule = fields[2] != "N..."
		record.conflict = true
		record.path = fields[10]
	case '?':
		record.kind, record.worktree = '?', '?'
		record.path = strings.TrimPrefix(value, "? ")
	case '!':
		record.kind, record.worktree = '!', '!'
		record.path = strings.TrimPrefix(value, "! ")
	default:
		return statusRecord{}, false, malformedStatus(value)
	}
	if isConflictPair(record.index, record.worktree) {
		record.conflict = true
	}
	return record, false, nil
}

func malformedStatus(record string) error {
	return &Error{Code: "invalid_git_status", Message: "Git returned an unsupported status record: " + record}
}

func isConflictPair(index, worktree byte) bool {
	switch string([]byte{index, worktree}) {
	case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
		return true
	}
	return false
}

func statusCharacter(status byte) string {
	if status == 0 || status == '.' {
		return ""
	}
	return string(status)
}

func statusName(status byte) string {
	switch status {
	case 'A', '?':
		return "added"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	case 'T':
		return "type-changed"
	case 'U':
		return "conflicted"
	default:
		return "modified"
	}
}

func sortChanges(changes []Change) {
	sort.SliceStable(changes, func(i, j int) bool {
		left, right := strings.ToLower(changes[i].Path), strings.ToLower(changes[j].Path)
		if left == right {
			return changes[i].Path < changes[j].Path
		}
		return left < right
	})
}

func repositoryOperationState(gitDir string) RepositoryState {
	state := RepositoryState{}
	if gitDir == "" {
		return state
	}
	state.MergeInProgress = pathExists(filepath.Join(gitDir, "MERGE_HEAD"))
	state.CherryPickInProgress = pathExists(filepath.Join(gitDir, "CHERRY_PICK_HEAD"))
	state.RebaseInProgress = pathExists(filepath.Join(gitDir, "rebase-merge")) || pathExists(filepath.Join(gitDir, "rebase-apply"))
	return state
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
