package fossil

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/sourcecontrol/checkpoint"
	"github.com/pmezard/go-difflib/difflib"
)

const (
	inspectHistoryDefaultLimit = 20
	inspectHistoryScanLimit    = 10_000
	inspectHistoryChunkSize    = 250
	inspectMaximumContext      = 20
)

const (
	timelineFieldSeparator  = "\x1f"
	timelineRecordSeparator = "\x1e"
)

var timelineFooterPattern = regexp.MustCompile(`^\+\+\+ (?:no more data|end of timeline) \([0-9]+\) \+\+\+$`)

func (p *Provider) acquireInspectionState(ctx context.Context, workspaceID, repositoryID string) (*repositoryState, func(), error) {
	state, err := p.repository(ctx, workspaceID, repositoryID)
	if err != nil {
		return nil, nil, err
	}
	state.rootMu.Lock()
	if err := p.recoverProtectedCommitState(ctx, state); err != nil {
		state.rootMu.Unlock()
		return nil, nil, err
	}
	state.rootMu.Unlock()
	state.rootMu.RLock()
	return state, state.rootMu.RUnlock, nil
}

func normalizeHistoryQuery(query sourcecontrol.HistoryQuery) (sourcecontrol.HistoryQuery, error) {
	query.Revision = strings.TrimSpace(query.Revision)
	query.Branch = strings.TrimSpace(query.Branch)
	query.Path = strings.TrimSpace(strings.ReplaceAll(query.Path, "\\", "/"))
	query.Query = strings.TrimSpace(query.Query)
	query.Author = strings.TrimSpace(query.Author)
	query.Since = strings.TrimSpace(query.Since)
	query.Until = strings.TrimSpace(query.Until)
	if query.Offset < 0 {
		return query, &sourcecontrol.Error{Code: "invalid_history_query", Message: "history offset cannot be negative"}
	}
	if query.Limit <= 0 {
		query.Limit = inspectHistoryDefaultLimit
	}
	if query.Limit > sourcecontrol.HistoryPageSize {
		query.Limit = sourcecontrol.HistoryPageSize
	}
	if query.Revision != "" && query.Branch != "" {
		return query, &sourcecontrol.Error{Code: "invalid_history_query", Message: "revision and branch history filters cannot be combined"}
	}
	if query.Revision != "" {
		if err := requireRef(query.Revision); err != nil {
			return query, err
		}
	}
	if query.Since != "" {
		if _, err := parseInspectDate(query.Since, false); err != nil {
			return query, &sourcecontrol.Error{Code: "invalid_history_query", Message: "since must be an RFC 3339 timestamp or YYYY-MM-DD date", Cause: err}
		}
	}
	if query.Until != "" {
		if _, err := parseInspectDate(query.Until, true); err != nil {
			return query, &sourcecontrol.Error{Code: "invalid_history_query", Message: "until must be an RFC 3339 timestamp or YYYY-MM-DD date", Cause: err}
		}
	}
	return query, nil
}

func (p *Provider) QueryHistory(ctx context.Context, workspaceID, repositoryID string, query sourcecontrol.HistoryQuery) (sourcecontrol.History, error) {
	query, err := normalizeHistoryQuery(query)
	if err != nil {
		return sourcecontrol.History{}, err
	}
	state, release, err := p.acquireInspectionState(ctx, workspaceID, repositoryID)
	if err != nil {
		return sourcecontrol.History{}, err
	}
	defer release()
	if query.Path != "" {
		query.Path, err = cleanPath(query.Path)
		if err != nil || !state.pathAllowed(query.Path) {
			return sourcecontrol.History{}, &sourcecontrol.Error{Code: "path_outside_workspace", Message: "source control path is outside this workspace", Cause: sourcecontrol.ErrInvalidPath}
		}
	}

	var since, until time.Time
	if query.Since != "" {
		since, _ = parseInspectDate(query.Since, false)
	}
	if query.Until != "" {
		until, _ = parseInspectDate(query.Until, true)
	}
	needle := strings.ToLower(query.Query)
	commits := make([]sourcecontrol.Commit, 0, query.Limit+1)
	matched := 0
	scanned := 0
	rawOffset := 0
	exhausted := false

	for scanned < inspectHistoryScanLimit && len(commits) <= query.Limit {
		chunk := inspectHistoryChunkSize
		if remaining := inspectHistoryScanLimit - scanned; chunk > remaining {
			chunk = remaining
		}
		nativeQuery := query
		if !until.IsZero() {
			// Fossil's "before" boundary is exclusive. Move the native
			// prefilter to the next whole second, then enforce the exact
			// inclusive boundary in memory.
			nativeQuery.Until = until.UTC().Add(time.Second).Truncate(time.Second).Format("2006-01-02T15:04:05")
		}
		args := timelineInspectArgs(nativeQuery, rawOffset, chunk+1)
		output, runErr := p.run(ctx, state.workspaceID, state.root, false, args...)
		if runErr != nil {
			return sourcecontrol.History{}, runErr
		}
		page, parseErr := parseInspectionTimeline(string(output))
		if parseErr != nil {
			return sourcecontrol.History{}, parseErr
		}
		if len(page) > chunk {
			page = page[:chunk]
		} else {
			exhausted = true
		}
		rawOffset += len(page)
		scanned += len(page)
		for _, commit := range page {
			when, hasTime := parseTimelineTime(commit.AuthoredAt)
			if (!since.IsZero() || !until.IsZero()) && !hasTime {
				return sourcecontrol.History{}, &sourcecontrol.Error{Code: "malformed_fossil_output", Message: "Fossil returned a timeline date that could not be interpreted"}
			}
			if !since.IsZero() && hasTime && when.Before(since) {
				exhausted = true
				break
			}
			if !until.IsZero() && hasTime && when.After(until) {
				continue
			}
			// Fossil 2.27 has no timeline --for-user option. Filter before
			// counting matches so pagination is over the requested author.
			if query.Author != "" && commit.Author != query.Author {
				continue
			}
			if needle != "" && !strings.Contains(strings.ToLower(commit.Message), needle) {
				continue
			}
			if matched < query.Offset {
				matched++
				continue
			}
			matched++
			commits = append(commits, commit)
			if len(commits) > query.Limit {
				break
			}
		}
		if exhausted || len(page) == 0 {
			break
		}
	}

	hasMore := len(commits) > query.Limit
	if hasMore {
		commits = commits[:query.Limit]
	}
	truncated := !exhausted && scanned >= inspectHistoryScanLimit
	return sourcecontrol.History{
		Commits: commits, NextOffset: query.Offset + len(commits), HasMore: hasMore || truncated, Truncated: truncated,
	}, nil
}

func (p *Provider) inspectCommit(ctx context.Context, state *repositoryState, ref string, existingInfo ...string) (*sourcecontrol.Commit, error) {
	query := sourcecontrol.HistoryQuery{Revision: ref, Limit: 1}
	output, err := p.run(ctx, state.workspaceID, state.root, false, timelineInspectArgs(query, 0, 2)...)
	if err != nil {
		return nil, err
	}
	commits, err := parseInspectionTimeline(string(output))
	if err != nil {
		return nil, err
	}
	if len(commits) == 0 {
		return nil, &sourcecontrol.Error{Code: "revision_not_found", Message: "Fossil check-in was not found", Cause: sourcecontrol.ErrNotFound}
	}
	commit := commits[0]
	infoOutput := ""
	if len(existingInfo) > 0 {
		infoOutput = existingInfo[0]
	} else {
		info, infoErr := p.run(ctx, state.workspaceID, state.root, false, "info", ref)
		if infoErr != nil {
			return nil, infoErr
		}
		infoOutput = string(info)
	}
	revision := parseRevisionInfo(infoOutput)
	if revision.Hash != "" {
		commit.Hash = revision.Hash
	}
	commit.Parents = revision.Parents
	if len(revision.Tags) > 0 {
		commit.Tags = revision.Tags
		commit.Refs = append([]string(nil), revision.Tags...)
		if commit.Branch != "" && !containsFold(commit.Refs, commit.Branch) {
			commit.Refs = append([]string{commit.Branch}, commit.Refs...)
		}
	}
	if commit.ShortHash == "" {
		commit.ShortHash = shortRef(commit.Hash)
	}
	return &commit, nil
}

func timelineInspectArgs(query sourcecontrol.HistoryQuery, offset, limit int) []string {
	format := strings.Join([]string{"%H", "%h", "%p", "%a", "%d", "%b", "%t", "%c"}, timelineFieldSeparator) + timelineRecordSeparator
	// Fossil 2.27 does not support -q; the parser handles its result footer.
	args := []string{"timeline", "-t", "ci", "-n", strconv.Itoa(limit), "--offset", strconv.Itoa(offset), "-W", "0", "--format", format}
	if query.Branch != "" {
		args = append(args, "--branch", query.Branch)
	}
	if query.Path != "" {
		args = append(args, "--path", "./"+query.Path)
	}
	if query.Revision != "" {
		args = append(args, "ancestors", query.Revision)
	} else if query.Until != "" {
		args = append(args, "before", query.Until)
	}
	return args
}

func parseInspectionTimeline(output string) ([]sourcecontrol.Commit, error) {
	normalized := strings.ReplaceAll(output, "\r\n", "\n")
	// Older Fossil versions append a notification even with --format. Only
	// accept the known footer after the last record, never inside a comment.
	footerStart := strings.LastIndex(normalized, timelineRecordSeparator) + len(timelineRecordSeparator)
	if timelineFooterPattern.MatchString(strings.TrimSpace(normalized[footerStart:])) {
		normalized = normalized[:footerStart]
	}
	result := make([]sourcecontrol.Commit, 0)
	for _, raw := range strings.Split(normalized, timelineRecordSeparator) {
		raw = strings.Trim(raw, "\r\n \t")
		if raw == "" {
			continue
		}
		fields := strings.Split(raw, timelineFieldSeparator)
		if len(fields) < 8 {
			return nil, &sourcecontrol.Error{Code: "malformed_fossil_output", Message: "Fossil returned malformed timeline output"}
		}
		message := normalizeCommitMessage(strings.Join(fields[7:], timelineFieldSeparator))
		branch := strings.TrimSpace(fields[5])
		tags := splitFossilList(fields[6])
		refs := append([]string(nil), tags...)
		if branch != "" && !containsFold(refs, branch) {
			refs = append([]string{branch}, refs...)
		}
		result = append(result, sourcecontrol.Commit{
			Hash: strings.TrimSpace(fields[0]), ShortHash: strings.TrimSpace(fields[1]),
			Author: strings.TrimSpace(fields[3]), AuthoredAt: strings.TrimSpace(fields[4]),
			Branch: branch, Tags: tags, Refs: refs, Subject: firstLine(message), Message: message,
		})
	}
	if len(result) == 0 && strings.TrimSpace(normalized) != "" {
		return nil, &sourcecontrol.Error{Code: "malformed_fossil_output", Message: "Fossil returned malformed timeline output"}
	}
	return result, nil
}

type revisionInfo struct {
	Hash    string
	Parents []string
	Tags    []string
}

func parseRevisionInfo(output string) revisionInfo {
	var result revisionInfo
	for _, raw := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		key, value, ok := strings.Cut(raw, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch {
		case key == "hash" || key == "uuid" || key == "checkout":
			if result.Hash == "" {
				result.Hash = firstField(value)
			}
		case key == "parent" || key == "merge-parent":
			if parent := firstField(value); parent != "" && !containsFold(result.Parents, parent) {
				result.Parents = append(result.Parents, parent)
			}
		case key == "tags":
			result.Tags = splitFossilList(value)
		}
	}
	return result
}

func splitFossilList(value string) []string {
	result := []string{}
	seen := make(map[string]bool)
	for _, field := range strings.Fields(strings.NewReplacer(",", " ", "[", " ", "]", " ").Replace(value)) {
		field = strings.TrimSpace(field)
		if field != "" && !seen[strings.ToLower(field)] {
			seen[strings.ToLower(field)] = true
			result = append(result, field)
		}
	}
	return result
}

func containsFold(values []string, candidate string) bool {
	for _, value := range values {
		if strings.EqualFold(value, candidate) {
			return true
		}
	}
	return false
}

func normalizeCommitMessage(value string) string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	for index := range lines {
		lines[index] = strings.TrimSpace(lines[index])
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func parseInspectDate(value string, endOfDay bool) (time.Time, error) {
	value = strings.TrimSpace(value)
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, err
	}
	if endOfDay {
		parsed = parsed.Add(24*time.Hour - time.Nanosecond)
	}
	return parsed, nil
}

func parseTimelineTime(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(value)); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func normalizeSearchRequest(request sourcecontrol.RepositorySearchRequest) (sourcecontrol.RepositorySearchRequest, error) {
	request.Query = strings.TrimSpace(request.Query)
	if request.Query == "" {
		return request, &sourcecontrol.Error{Code: "invalid_search_query", Message: "query is required for Fossil search"}
	}
	if request.Limit <= 0 {
		request.Limit = inspectHistoryDefaultLimit
	}
	if request.Limit > sourcecontrol.HistoryPageSize {
		request.Limit = sourcecontrol.HistoryPageSize
	}
	request.MaxOutputBytes = normalizeInspectOutputLimit(request.MaxOutputBytes)
	if len(request.Scopes) == 0 {
		request.Scopes = []string{"checkins"}
	}
	valid := map[string]bool{"checkins": true, "docs": true, "forum": true, "tickets": true, "technotes": true, "wiki": true, "help": true, "all": true}
	seen := make(map[string]bool)
	normalized := make([]string, 0, len(request.Scopes))
	for _, scope := range request.Scopes {
		scope = strings.ToLower(strings.TrimSpace(scope))
		if !valid[scope] {
			return request, &sourcecontrol.Error{Code: "invalid_search_scope", Message: "unsupported Fossil search scope " + strconv.Quote(scope)}
		}
		if !seen[scope] {
			seen[scope] = true
			normalized = append(normalized, scope)
		}
	}
	if seen["all"] && len(normalized) > 1 {
		return request, &sourcecontrol.Error{Code: "invalid_search_scope", Message: "the all search scope cannot be combined with other scopes"}
	}
	request.Scopes = normalized
	return request, nil
}

func (p *Provider) Search(ctx context.Context, workspaceID, repositoryID string, request sourcecontrol.RepositorySearchRequest) (sourcecontrol.RepositorySearchResult, error) {
	request, err := normalizeSearchRequest(request)
	if err != nil {
		return sourcecontrol.RepositorySearchResult{}, err
	}
	state, release, err := p.acquireInspectionState(ctx, workspaceID, repositoryID)
	if err != nil {
		return sourcecontrol.RepositorySearchResult{}, err
	}
	defer release()
	args := fossilSearchArgs(request)
	output, truncated, err := p.runLimited(ctx, state.workspaceID, state.root, false, request.MaxOutputBytes, args...)
	if err != nil {
		return sourcecontrol.RepositorySearchResult{}, err
	}
	return sourcecontrol.RepositorySearchResult{
		Query: request.Query, Scopes: append([]string(nil), request.Scopes...), Limit: request.Limit,
		Output: strings.TrimSpace(validBoundedUTF8(output, request.MaxOutputBytes)), Truncated: truncated,
	}, nil
}

func fossilSearchArgs(request sourcecontrol.RepositorySearchRequest) []string {
	args := []string{"search", "--highlight", "0", "-W", "0", "-n", strconv.Itoa(request.Limit)}
	flags := map[string]string{"checkins": "-c", "docs": "--docs", "forum": "--forum", "tickets": "--tickets", "technotes": "--technotes", "wiki": "--wiki", "help": "-h", "all": "-a"}
	for _, scope := range request.Scopes {
		args = append(args, flags[scope])
	}
	return append(args, request.Query)
}

func normalizeInspectOutputLimit(value int) int {
	if value <= 0 {
		return sourcecontrol.InspectionDefaultOutputMax
	}
	if value > sourcecontrol.InspectionMaximumOutputMax {
		return sourcecontrol.InspectionMaximumOutputMax
	}
	return value
}

func normalizePatchRequest(request sourcecontrol.PatchRequest) (sourcecontrol.PatchRequest, error) {
	request.Comparison = strings.ToLower(strings.TrimSpace(request.Comparison))
	request.Path = strings.TrimSpace(strings.ReplaceAll(request.Path, "\\", "/"))
	request.BaseRef = strings.TrimSpace(request.BaseRef)
	request.Ref = strings.TrimSpace(request.Ref)
	if request.ContextLines < 0 {
		return request, &sourcecontrol.Error{Code: "invalid_patch_request", Message: "context lines cannot be negative"}
	}
	if request.ContextLines > inspectMaximumContext {
		request.ContextLines = inspectMaximumContext
	}
	request.MaxOutputBytes = normalizeInspectOutputLimit(request.MaxOutputBytes)
	switch request.Comparison {
	case "working_tree", "protected":
		if request.BaseRef != "" || request.Ref != "" {
			return request, &sourcecontrol.Error{Code: "invalid_patch_request", Message: request.Comparison + " does not accept revisions"}
		}
	case "revisions":
		if err := requireRef(request.BaseRef); err != nil {
			return request, &sourcecontrol.Error{Code: "invalid_patch_request", Message: "base and target revisions are required for revisions diff", Cause: err}
		}
		if err := requireRef(request.Ref); err != nil {
			return request, &sourcecontrol.Error{Code: "invalid_patch_request", Message: "base and target revisions are required for revisions diff", Cause: err}
		}
	case "revision_to_worktree":
		if err := requireRef(request.BaseRef); err != nil || request.Ref != "" {
			return request, &sourcecontrol.Error{Code: "invalid_patch_request", Message: "base revision is required for revision_to_worktree diff"}
		}
	case "revision":
		if err := requireRef(request.Ref); err != nil || request.BaseRef != "" {
			return request, &sourcecontrol.Error{Code: "invalid_patch_request", Message: "target revision is required for revision diff"}
		}
	default:
		return request, &sourcecontrol.Error{Code: "invalid_patch_request", Message: "unsupported Fossil patch comparison"}
	}
	return request, nil
}

func (p *Provider) Patch(ctx context.Context, workspaceID, repositoryID string, request sourcecontrol.PatchRequest) (sourcecontrol.PatchResult, error) {
	request, err := normalizePatchRequest(request)
	if err != nil {
		return sourcecontrol.PatchResult{}, err
	}
	state, release, err := p.acquireInspectionState(ctx, workspaceID, repositoryID)
	if err != nil {
		return sourcecontrol.PatchResult{}, err
	}
	defer release()
	if request.Path != "" {
		request.Path, err = cleanPath(request.Path)
		if err != nil || !state.pathAllowed(request.Path) {
			return sourcecontrol.PatchResult{}, &sourcecontrol.Error{Code: "path_outside_workspace", Message: "source control path is outside this workspace", Cause: sourcecontrol.ErrInvalidPath}
		}
	}
	paths, err := inspectionPathArgs(state, request.Path)
	if err != nil {
		return sourcecontrol.PatchResult{}, err
	}
	if request.Comparison == "protected" {
		return p.protectedPatch(ctx, state, request)
	}
	if request.Comparison == "revision" {
		infoOutput, infoErr := p.run(ctx, state.workspaceID, state.root, false, "info", request.Ref)
		if infoErr != nil {
			return sourcecontrol.PatchResult{}, infoErr
		}
		if parseInfo(string(infoOutput)).Parent == "" {
			return p.rootRevisionPatch(ctx, state, request)
		}
	}

	comparisonArgs := []string{}
	switch request.Comparison {
	case "revisions":
		comparisonArgs = append(comparisonArgs, "--from", request.BaseRef, "--to", request.Ref)
	case "revision_to_worktree":
		comparisonArgs = append(comparisonArgs, "--from", request.BaseRef)
	case "revision":
		comparisonArgs = append(comparisonArgs, "--checkin", request.Ref)
	}
	baseArgs := append([]string{"diff", "--internal", "--new-file"}, comparisonArgs...)
	briefArgs := append(append([]string(nil), baseArgs...), "--brief")
	briefArgs = append(briefArgs, paths...)
	briefOutput, briefTruncated, err := p.runLimited(ctx, state.workspaceID, state.root, false, maximumCommandOutput, briefArgs...)
	if err != nil {
		return sourcecontrol.PatchResult{}, err
	}
	files := filterVisibleRevisionFiles(state, parseBriefDiff(string(briefOutput)))
	result := sourcecontrol.PatchResult{
		Comparison: request.Comparison, BaseRef: request.BaseRef, Ref: request.Ref,
		FileCount: len(files), Files: files, FilesTruncated: briefTruncated,
	}
	if len(result.Files) > sourcecontrol.InspectionFileLimit {
		result.Files = result.Files[:sourcecontrol.InspectionFileLimit]
		result.FilesTruncated = true
	}
	numstatArgs := append(append([]string(nil), baseArgs...), "--numstat")
	numstatArgs = append(numstatArgs, paths...)
	statistics, statisticsTruncated, statErr := p.runLimited(ctx, state.workspaceID, state.root, false, sourcecontrol.InspectionDefaultOutputMax, numstatArgs...)
	if statErr != nil {
		return sourcecontrol.PatchResult{}, statErr
	}
	result.Statistics = strings.TrimSpace(validBoundedUTF8(statistics, sourcecontrol.InspectionDefaultOutputMax))
	if statisticsTruncated {
		result.Statistics += "\n…"
	}
	if request.IncludePatch {
		patchArgs := append(append([]string(nil), baseArgs...), "--unified", "-c", strconv.Itoa(request.ContextLines))
		patchArgs = append(patchArgs, paths...)
		patch, truncated, patchErr := p.runLimited(ctx, state.workspaceID, state.root, false, request.MaxOutputBytes, patchArgs...)
		if patchErr != nil {
			return sourcecontrol.PatchResult{}, patchErr
		}
		result.Patch = strings.TrimSpace(validBoundedUTF8(patch, request.MaxOutputBytes))
		result.PatchTruncated = truncated
	}
	return result, nil
}

func inspectionPathArgs(state *repositoryState, requested string) ([]string, error) {
	if requested != "" {
		pathValue, err := cleanPath(requested)
		if err != nil || !state.pathAllowed(pathValue) {
			return nil, &sourcecontrol.Error{Code: "path_outside_workspace", Message: "source control path is outside this workspace", Cause: sourcecontrol.ErrInvalidPath}
		}
		return []string{"./" + pathValue}, nil
	}
	seen := make(map[string]bool)
	paths := []string{}
	for _, scope := range state.scopes {
		prefix := strings.Trim(filepathClean(scope.RepoPrefix), "/")
		if prefix == "" {
			return nil, nil
		}
		key := pathIdentity(prefix)
		if !seen[key] {
			seen[key] = true
			paths = append(paths, "./"+prefix)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func filterVisibleRevisionFiles(state *repositoryState, files []sourcecontrol.RevisionFile) []sourcecontrol.RevisionFile {
	filtered := files[:0]
	for _, file := range files {
		if state.pathAllowed(file.Path) && (file.OldPath == "" || state.pathAllowed(file.OldPath)) {
			filtered = append(filtered, file)
		}
	}
	return filtered
}

func (p *Provider) rootRevisionPatch(ctx context.Context, state *repositoryState, request sourcecontrol.PatchRequest) (sourcecontrol.PatchResult, error) {
	output, err := p.run(ctx, state.workspaceID, state.root, false, "ls", "-r", request.Ref)
	if err != nil {
		return sourcecontrol.PatchResult{}, err
	}
	allFiles := make([]sourcecontrol.RevisionFile, 0)
	for _, raw := range strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n") {
		pathValue := filepathClean(raw)
		if pathValue == "" || !state.pathAllowed(pathValue) {
			continue
		}
		if request.Path != "" && pathIdentity(request.Path) != pathIdentity(pathValue) {
			continue
		}
		allFiles = append(allFiles, sourcecontrol.RevisionFile{Path: pathValue, Status: "A"})
	}
	sort.SliceStable(allFiles, func(i, j int) bool { return strings.ToLower(allFiles[i].Path) < strings.ToLower(allFiles[j].Path) })
	result := sourcecontrol.PatchResult{
		Comparison: request.Comparison, Ref: request.Ref, FileCount: len(allFiles), Files: append([]sourcecontrol.RevisionFile(nil), allFiles...),
	}
	if len(result.Files) > sourcecontrol.InspectionFileLimit {
		result.Files = result.Files[:sourcecontrol.InspectionFileLimit]
		result.FilesTruncated = true
	}
	if !request.IncludePatch {
		result.Statistics = fmt.Sprintf("%d file(s) changed", result.FileCount)
		return result, nil
	}
	var patch strings.Builder
	insertions := 0
	for _, file := range allFiles {
		content, exists, readErr := p.revisionFile(ctx, state, request.Ref, file.Path)
		if readErr != nil {
			return sourcecontrol.PatchResult{}, readErr
		}
		if !exists || binaryContent(content) {
			continue
		}
		filePatch, added, _ := unifiedFilePatch(file.Path, file.Path, nil, content, false, true, request.ContextLines)
		insertions += added
		if result.PatchTruncated {
			continue
		}
		remaining := request.MaxOutputBytes - patch.Len()
		if remaining <= 0 {
			result.PatchTruncated = true
			continue
		}
		if len(filePatch) > remaining {
			patch.WriteString(validUTF8Prefix(filePatch, remaining))
			result.PatchTruncated = true
			continue
		}
		patch.WriteString(filePatch)
	}
	result.Statistics = fmt.Sprintf("%d file(s) changed, %d insertion(s), 0 deletion(s)", result.FileCount, insertions)
	result.Patch = strings.TrimSpace(patch.String())
	return result, nil
}

func (p *Provider) protectedPatch(ctx context.Context, state *repositoryState, request sourcecontrol.PatchRequest) (sourcecontrol.PatchResult, error) {
	manifest, err := p.loadCheckpoint(state)
	if err != nil {
		return sourcecontrol.PatchResult{}, err
	}
	result := sourcecontrol.PatchResult{Comparison: request.Comparison, Files: []sourcecontrol.RevisionFile{}}
	if manifest == nil {
		return result, nil
	}
	entries := append([]checkpoint.FileState(nil), manifest.Entries...)
	sort.SliceStable(entries, func(i, j int) bool { return strings.ToLower(entries[i].Path) < strings.ToLower(entries[j].Path) })
	var patch strings.Builder
	insertions, deletions := 0, 0
	for _, entry := range entries {
		if !state.pathAllowed(entry.Path) || (entry.OldPath != "" && !state.pathAllowed(entry.OldPath)) {
			continue
		}
		if request.Path != "" && pathIdentity(request.Path) != pathIdentity(entry.Path) && pathIdentity(request.Path) != pathIdentity(entry.OldPath) {
			continue
		}
		status := protectedFileStatus(entry)
		result.Files = append(result.Files, sourcecontrol.RevisionFile{Path: entry.Path, OldPath: entry.OldPath, Status: status})
		basePath := entry.Path
		if entry.OldPath != "" {
			basePath = entry.OldPath
		}
		original, originalExists, readErr := p.revisionFile(ctx, state, manifest.Baseline, basePath)
		if readErr != nil {
			return sourcecontrol.PatchResult{}, readErr
		}
		modified, modifiedExists, readErr := p.checkpointFileContent(state, entry)
		if readErr != nil {
			return sourcecontrol.PatchResult{}, readErr
		}
		if binaryContent(original) || binaryContent(modified) {
			continue
		}
		filePatch, added, removed := unifiedFilePatch(basePath, entry.Path, original, modified, originalExists, modifiedExists, request.ContextLines)
		insertions += added
		deletions += removed
		if request.IncludePatch && !result.PatchTruncated {
			remaining := request.MaxOutputBytes - patch.Len()
			if remaining <= 0 {
				result.PatchTruncated = true
			} else if len(filePatch) > remaining {
				patch.WriteString(validUTF8Prefix(filePatch, remaining))
				result.PatchTruncated = true
			} else {
				patch.WriteString(filePatch)
			}
		}
	}
	result.FileCount = len(result.Files)
	if len(result.Files) > sourcecontrol.InspectionFileLimit {
		result.Files = result.Files[:sourcecontrol.InspectionFileLimit]
		result.FilesTruncated = true
	}
	result.Statistics = fmt.Sprintf("%d file(s) changed, %d insertion(s), %d deletion(s)", result.FileCount, insertions, deletions)
	result.Patch = strings.TrimSpace(patch.String())
	return result, nil
}

func protectedFileStatus(entry checkpoint.FileState) string {
	if entry.StatusCode != "" {
		code := strings.ToUpper(entry.StatusCode)
		if strings.Contains(code, "ADDED") || code == "A" {
			return "A"
		}
		if strings.Contains(code, "DELETED") || strings.Contains(code, "MISSING") || code == "D" {
			return "D"
		}
		if strings.Contains(code, "RENAMED") || code == "R" {
			return "R"
		}
	}
	switch strings.ToLower(entry.Kind) {
	case "added", "untracked":
		return "A"
	case "deleted", "missing":
		return "D"
	case "renamed":
		return "R"
	default:
		return "M"
	}
}

func boundedUnifiedFilePatch(oldPath, newPath string, original, modified []byte, originalExists, modifiedExists bool, contextLines, limit int) (string, int, int, bool) {
	if limit <= 0 {
		return "", 0, 0, true
	}
	text, added, removed := unifiedFilePatch(oldPath, newPath, original, modified, originalExists, modifiedExists, contextLines)
	if len(text) <= limit {
		return text, added, removed, false
	}
	return validUTF8Prefix(text, limit), added, removed, true
}

func validUTF8Prefix(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	prefix := value[:limit]
	for len(prefix) > 0 && !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix
}

func validBoundedUTF8(data []byte, limit int) string {
	value := strings.ToValidUTF8(string(data), "\uFFFD")
	return validUTF8Prefix(value, limit)
}

func unifiedFilePatch(oldPath, newPath string, original, modified []byte, originalExists, modifiedExists bool, contextLines int) (string, int, int) {
	oldName, newName := "a/"+oldPath, "b/"+newPath
	if !originalExists {
		oldName = "/dev/null"
	}
	if !modifiedExists {
		newName = "/dev/null"
	}
	text, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: difflib.SplitLines(string(original)), B: difflib.SplitLines(string(modified)),
		FromFile: oldName, ToFile: newName, Context: contextLines,
	})
	added, removed := countPatchLines(text)
	return text, added, removed
}

func countPatchLines(patch string) (int, int) {
	added, removed := 0, 0
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			added++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			removed++
		}
	}
	return added, removed
}
