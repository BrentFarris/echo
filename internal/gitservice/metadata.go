package gitservice

import (
	"bytes"
	"context"
	"strconv"
	"strings"
)

func (s *Service) Metadata(ctx context.Context, workspaceID, repositoryID string) (Metadata, error) {
	state, err := s.repository(ctx, workspaceID, repositoryID)
	if err != nil {
		return Metadata{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, localCommandTimeout)
	defer cancel()
	branchesOutput, err := runGit(ctx, state.root, nil, true, "for-each-ref", "--format=%(refname:short)%00%(HEAD)", "refs/heads")
	if err != nil {
		return Metadata{}, err
	}
	remoteBranchesOutput, err := runGit(ctx, state.root, nil, true, "for-each-ref", "--format=%(refname:short)%00", "refs/remotes")
	if err != nil {
		return Metadata{}, err
	}
	remotesOutput, err := runGit(ctx, state.root, nil, true, "remote", "-v")
	if err != nil {
		return Metadata{}, err
	}
	tagsOutput, err := runGit(ctx, state.root, nil, true, "tag", "--list", "--sort=-creatordate")
	if err != nil {
		return Metadata{}, err
	}
	stashesOutput, err := runGit(ctx, state.root, nil, true, "stash", "list", "--format=%gd%x00%H%x00%gs%x00")
	if err != nil {
		return Metadata{}, err
	}
	return Metadata{
		Branches:       parseBranches(branchesOutput, false),
		RemoteBranches: parseBranches(remoteBranchesOutput, true),
		Remotes:        parseRemotes(remotesOutput),
		Tags:           nonEmptyLines(tagsOutput),
		Stashes:        parseStashes(stashesOutput),
	}, nil
}

func (s *Service) History(ctx context.Context, workspaceID, repositoryID string, offset, limit int) (History, error) {
	state, err := s.repository(ctx, workspaceID, repositoryID)
	if err != nil {
		return History{}, err
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > HistoryPageSize {
		limit = HistoryPageSize
	}
	ctx, cancel := context.WithTimeout(ctx, localCommandTimeout)
	defer cancel()
	format := "%H%x00%P%x00%an%x00%aI%x00%D%x00%s%x00"
	output, err := runGit(ctx, state.root, nil, true, "log", "--all", "--topo-order", "--date=iso-strict",
		"--pretty=format:"+format, "--skip="+strconv.Itoa(offset), "-n", strconv.Itoa(limit+1))
	if err != nil {
		// An unborn repository has no history and is not an exceptional UI state.
		if strings.Contains(strings.ToLower(err.Error()), "does not have any commits") || strings.Contains(strings.ToLower(err.Error()), "unknown revision") {
			return History{Commits: []Commit{}}, nil
		}
		return History{}, err
	}
	commits := parseHistory(output)
	hasMore := len(commits) > limit
	if hasMore {
		commits = commits[:limit]
	}
	result := History{Commits: commits, HasMore: hasMore}
	if hasMore {
		result.NextOffset = offset + len(commits)
	}
	return result, nil
}

func (s *Service) CommitDetail(ctx context.Context, workspaceID, repositoryID, ref string, stash bool) (CommitDetail, error) {
	state, err := s.repository(ctx, workspaceID, repositoryID)
	if err != nil {
		return CommitDetail{}, err
	}
	validated, err := validateCommitRef(ctx, state, ref)
	if err != nil {
		return CommitDetail{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, localCommandTimeout)
	defer cancel()
	args := []string{"diff-tree", "--no-commit-id", "--name-status", "-r", "-z", "-M", validated}
	if stash {
		args = []string{"diff", "--name-status", "-z", "-M", validated + "^", validated}
	}
	output, err := runGit(ctx, state.root, nil, true, args...)
	if err != nil {
		return CommitDetail{}, err
	}
	files := parseNameStatus(output)
	visible := files[:0]
	for _, file := range files {
		if state.pathAllowed(file.Path) {
			visible = append(visible, file)
		}
	}
	return CommitDetail{Ref: validated, Files: visible}, nil
}

func parseBranches(data []byte, remote bool) []Branch {
	items := []Branch{}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		fields := bytes.Split(line, []byte{0})
		name := strings.TrimSpace(string(fields[0]))
		if name == "" || strings.HasSuffix(name, "/HEAD") {
			continue
		}
		current := len(fields) > 1 && strings.TrimSpace(string(fields[1])) == "*"
		items = append(items, Branch{Name: name, Current: current, Remote: remote})
	}
	return items
}

func parseRemotes(data []byte) []Remote {
	order := []string{}
	items := make(map[string]*Remote)
	for _, line := range nonEmptyLines(data) {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		remote := items[fields[0]]
		if remote == nil {
			remote = &Remote{Name: fields[0]}
			items[fields[0]] = remote
			order = append(order, fields[0])
		}
		kind := strings.Trim(fields[len(fields)-1], "()")
		url := fields[1]
		if kind == "fetch" {
			remote.FetchURL = url
		} else if kind == "push" {
			remote.PushURL = url
		}
	}
	result := make([]Remote, 0, len(order))
	for _, name := range order {
		result = append(result, *items[name])
	}
	return result
}

func parseStashes(data []byte) []Stash {
	fields := bytes.Split(data, []byte{0})
	result := []Stash{}
	for index := 0; index+2 < len(fields); index += 3 {
		ref := strings.TrimSpace(string(fields[index]))
		if ref == "" {
			continue
		}
		result = append(result, Stash{Ref: ref, Hash: string(fields[index+1]), Message: string(fields[index+2])})
	}
	return result
}

func nonEmptyLines(data []byte) []string {
	result := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, line)
		}
	}
	return result
}

func parseHistory(data []byte) []Commit {
	fields := bytes.Split(data, []byte{0})
	result := []Commit{}
	for index := 0; index+5 < len(fields); index += 6 {
		hash := strings.TrimSpace(string(fields[index]))
		if hash == "" {
			continue
		}
		refs := []string{}
		for _, ref := range strings.Split(string(fields[index+4]), ",") {
			if ref = strings.TrimSpace(ref); ref != "" {
				refs = append(refs, ref)
			}
		}
		result = append(result, Commit{
			Hash: hash, Parents: strings.Fields(string(fields[index+1])), Author: string(fields[index+2]),
			AuthoredAt: string(fields[index+3]), Refs: refs, Subject: strings.TrimSuffix(string(fields[index+5]), "\n"),
		})
	}
	return result
}

func parseNameStatus(data []byte) []CommitFile {
	fields := bytes.Split(data, []byte{0})
	result := []CommitFile{}
	for index := 0; index < len(fields); {
		status := strings.TrimSpace(string(fields[index]))
		index++
		if status == "" || index >= len(fields) {
			continue
		}
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if index+1 >= len(fields) {
				break
			}
			oldPath, path := string(fields[index]), string(fields[index+1])
			index += 2
			result = append(result, CommitFile{Path: path, OldPath: oldPath, Status: status[:1]})
			continue
		}
		result = append(result, CommitFile{Path: string(fields[index]), Status: status[:1]})
		index++
	}
	return result
}
