package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	workspaceTextSearchEventName      = "echo:text-search:event"
	maxWorkspaceTextSearchMatches     = 1000
	maxWorkspaceTextSearchLineRunes   = 360
	workspaceTextSearchEventBatchSize = 25
	workspaceTextSearchEventInterval  = 50 * time.Millisecond
)

type WorkspaceTextSearchRequest struct {
	SearchID       string `json:"searchId,omitempty"`
	Query          string `json:"query"`
	Regex          bool   `json:"regex"`
	CaseSensitive  bool   `json:"caseSensitive"`
	WholeWord      bool   `json:"wholeWord"`
	Include        string `json:"include,omitempty"`
	Exclude        string `json:"exclude,omitempty"`
	IncludeIgnored bool   `json:"includeIgnored"`
}

type WorkspaceTextSearchEvent struct {
	WorkspaceID   string                          `json:"workspaceId"`
	SearchID      string                          `json:"searchId"`
	Type          string                          `json:"type"`
	Files         []WorkspaceTextSearchFileResult `json:"files,omitempty"`
	MatchCount    int                             `json:"matchCount,omitempty"`
	FileCount     int                             `json:"fileCount,omitempty"`
	FilesSearched int                             `json:"filesSearched,omitempty"`
	FilesSkipped  int                             `json:"filesSkipped,omitempty"`
	Truncated     bool                            `json:"truncated,omitempty"`
	Result        *WorkspaceTextSearchResult      `json:"result,omitempty"`
}

type WorkspaceTextSearchResult struct {
	WorkspaceID   string                          `json:"workspaceId"`
	Query         string                          `json:"query"`
	Regex         bool                            `json:"regex"`
	CaseSensitive bool                            `json:"caseSensitive"`
	WholeWord     bool                            `json:"wholeWord"`
	Include       string                          `json:"include,omitempty"`
	Exclude       string                          `json:"exclude,omitempty"`
	MatchCount    int                             `json:"matchCount"`
	FileCount     int                             `json:"fileCount"`
	FilesSearched int                             `json:"filesSearched"`
	FilesSkipped  int                             `json:"filesSkipped"`
	Truncated     bool                            `json:"truncated"`
	Files         []WorkspaceTextSearchFileResult `json:"files"`
}

type WorkspaceTextSearchFileResult struct {
	Path    string                     `json:"path"`
	Name    string                     `json:"name"`
	Matches []WorkspaceTextSearchMatch `json:"matches"`
}

type WorkspaceTextSearchMatch struct {
	Line           int    `json:"line"`
	Column         int    `json:"column"`
	EndLine        int    `json:"endLine"`
	EndColumn      int    `json:"endColumn"`
	Offset         int    `json:"offset"`
	EndOffset      int    `json:"endOffset"`
	LineText       string `json:"lineText"`
	MatchText      string `json:"matchText"`
	HighlightStart int    `json:"highlightStart"`
	HighlightEnd   int    `json:"highlightEnd"`
	Truncated      bool   `json:"truncated,omitempty"`
}

type workspaceTextPathFilter struct {
	matcher *regexp.Regexp
}

type workspaceTextSearchRun struct {
	id     uint64
	cancel context.CancelFunc
}

type workspaceTextSearchCandidate struct {
	absolute string
	relative string
}

type workspaceTextSearchFileOutcome struct {
	file      WorkspaceTextSearchFileResult
	searched  bool
	skipped   bool
	truncated bool
}

func (s *SystemService) SearchWorkspaceText(workspaceID string, request WorkspaceTextSearchRequest) (WorkspaceTextSearchResult, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return WorkspaceTextSearchResult{}, err
	}

	output := WorkspaceTextSearchResult{
		WorkspaceID:   workspace.ID,
		Query:         request.Query,
		Regex:         request.Regex,
		CaseSensitive: request.CaseSensitive,
		WholeWord:     request.WholeWord,
		Include:       strings.TrimSpace(request.Include),
		Exclude:       strings.TrimSpace(request.Exclude),
		Files:         []WorkspaceTextSearchFileResult{},
	}
	if request.Query == "" {
		return output, nil
	}

	matcher, err := workspaceTextSearchMatcher(request)
	if err != nil {
		return WorkspaceTextSearchResult{}, err
	}
	includeFilters, err := compileWorkspaceTextPathFilters(request.Include)
	if err != nil {
		return WorkspaceTextSearchResult{}, err
	}
	excludeFilters, err := compileWorkspaceTextPathFilters(request.Exclude)
	if err != nil {
		return WorkspaceTextSearchResult{}, err
	}

	ctx, runID := s.startWorkspaceTextSearch(workspace.ID)
	defer s.finishWorkspaceTextSearch(workspace.ID, runID)
	searchID := strings.TrimSpace(request.SearchID)
	if searchID == "" {
		searchID = fmt.Sprintf("%d", runID)
	}
	startedResult := output
	s.emitWorkspaceTextSearchEvent(WorkspaceTextSearchEvent{
		WorkspaceID: workspace.ID,
		SearchID:    searchID,
		Type:        "started",
		Result:      &startedResult,
	})

	jobs := make(chan workspaceTextSearchCandidate, 128)
	outcomes := make(chan workspaceTextSearchFileOutcome, 128)
	walkErrors := make(chan error, 1)
	go walkWorkspaceTextSearchCandidates(ctx, workspace, request, includeFilters, excludeFilters, jobs, outcomes, walkErrors)

	workerCount := goruntime.GOMAXPROCS(0) * 2
	if workerCount < 4 {
		workerCount = 4
	}
	if workerCount > 32 {
		workerCount = 32
	}
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for index := 0; index < workerCount; index++ {
		go func() {
			defer workers.Done()
			for candidate := range jobs {
				outcome := searchWorkspaceTextFile(candidate.absolute, candidate.relative, matcher)
				select {
				case outcomes <- outcome:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		workers.Wait()
		close(outcomes)
	}()

	pendingFiles := make([]WorkspaceTextSearchFileResult, 0, workspaceTextSearchEventBatchSize)
	lastEventAt := time.Now()
	emitPendingFiles := func() {
		if len(pendingFiles) == 0 {
			return
		}
		s.emitWorkspaceTextSearchEvent(WorkspaceTextSearchEvent{
			WorkspaceID:   workspace.ID,
			SearchID:      searchID,
			Type:          "matches",
			Files:         pendingFiles,
			MatchCount:    output.MatchCount,
			FileCount:     len(output.Files),
			FilesSearched: output.FilesSearched,
			FilesSkipped:  output.FilesSkipped,
			Truncated:     output.Truncated,
		})
		pendingFiles = make([]WorkspaceTextSearchFileResult, 0, workspaceTextSearchEventBatchSize)
		lastEventAt = time.Now()
	}
	for outcome := range outcomes {
		if outcome.searched {
			output.FilesSearched++
		}
		if outcome.skipped {
			output.FilesSkipped++
		}
		if len(outcome.file.Matches) == 0 {
			if len(pendingFiles) > 0 && time.Since(lastEventAt) >= workspaceTextSearchEventInterval {
				emitPendingFiles()
			}
			continue
		}
		remaining := maxWorkspaceTextSearchMatches - output.MatchCount
		if remaining <= 0 {
			output.Truncated = true
			continue
		}
		if len(outcome.file.Matches) > remaining {
			outcome.file.Matches = outcome.file.Matches[:remaining]
			output.Truncated = true
		}
		output.MatchCount += len(outcome.file.Matches)
		output.Files = append(output.Files, outcome.file)
		pendingFiles = append(pendingFiles, outcome.file)
		if outcome.truncated {
			output.Truncated = true
		}
		if len(pendingFiles) >= workspaceTextSearchEventBatchSize || time.Since(lastEventAt) >= workspaceTextSearchEventInterval {
			emitPendingFiles()
		}
	}
	emitPendingFiles()
	if walkErr := <-walkErrors; walkErr != nil && ctx.Err() == nil {
		return WorkspaceTextSearchResult{}, fmt.Errorf("search workspace text: %w", walkErr)
	}

	sort.Slice(output.Files, func(i, j int) bool {
		return strings.ToLower(output.Files[i].Path) < strings.ToLower(output.Files[j].Path)
	})
	output.FileCount = len(output.Files)
	s.emitWorkspaceTextSearchEvent(WorkspaceTextSearchEvent{
		WorkspaceID: workspace.ID,
		SearchID:    searchID,
		Type:        "complete",
		Result:      &output,
	})
	return output, nil
}

func (s *SystemService) emitWorkspaceTextSearchEvent(event WorkspaceTextSearchEvent) {
	s.emitRuntimeEvent(workspaceTextSearchEventName, event)
	if s.ctx != nil {
		runtime.EventsEmit(s.ctx, workspaceTextSearchEventName, event)
	}
}

func (s *SystemService) startWorkspaceTextSearch(workspaceID string) (context.Context, uint64) {
	s.workspaceTextSearchMu.Lock()
	defer s.workspaceTextSearchMu.Unlock()
	if current, ok := s.workspaceTextSearches[workspaceID]; ok {
		current.cancel()
	}
	s.workspaceTextSearchSeq++
	ctx, cancel := context.WithCancel(context.Background())
	s.workspaceTextSearches[workspaceID] = workspaceTextSearchRun{id: s.workspaceTextSearchSeq, cancel: cancel}
	return ctx, s.workspaceTextSearchSeq
}

func (s *SystemService) finishWorkspaceTextSearch(workspaceID string, runID uint64) {
	s.workspaceTextSearchMu.Lock()
	defer s.workspaceTextSearchMu.Unlock()
	if current, ok := s.workspaceTextSearches[workspaceID]; ok && current.id == runID {
		current.cancel()
		delete(s.workspaceTextSearches, workspaceID)
	}
}

func (s *SystemService) cancelWorkspaceTextSearches() {
	s.workspaceTextSearchMu.Lock()
	runs := make([]workspaceTextSearchRun, 0, len(s.workspaceTextSearches))
	for _, run := range s.workspaceTextSearches {
		runs = append(runs, run)
	}
	s.workspaceTextSearches = make(map[string]workspaceTextSearchRun)
	s.workspaceTextSearchMu.Unlock()
	for _, run := range runs {
		run.cancel()
	}
}

func walkWorkspaceTextSearchCandidates(
	ctx context.Context,
	workspace Workspace,
	request WorkspaceTextSearchRequest,
	includeFilters []workspaceTextPathFilter,
	excludeFilters []workspaceTextPathFilter,
	jobs chan<- workspaceTextSearchCandidate,
	outcomes chan<- workspaceTextSearchFileOutcome,
	walkErrors chan<- error,
) {
	defer close(jobs)
	var resultErr error
	defer func() { walkErrors <- resultErr }()
	for _, folder := range workspace.Folders {
		if folder.Missing || ctx.Err() != nil {
			continue
		}
		root, err := resolveWorkspaceServicePath(workspace, folder.Label)
		if err != nil {
			continue
		}
		err = filepath.WalkDir(root, func(absolute string, entry os.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return filepath.SkipAll
			}
			if walkErr != nil {
				select {
				case outcomes <- workspaceTextSearchFileOutcome{skipped: true}:
				case <-ctx.Done():
				}
				return nil
			}
			if absolute == root {
				return nil
			}

			relative := workspaceRelativePath(workspace, absolute)
			rootRelative := workspaceTextRootRelativePath(folder, relative)
			name := entry.Name()
			if entry.IsDir() {
				if !request.IncludeIgnored && isIgnoredWorkspaceDirectory(name) {
					return filepath.SkipDir
				}
				if workspaceTextPathFilterMatches(excludeFilters, relative, rootRelative, name) {
					return filepath.SkipDir
				}
				return nil
			}

			if (len(includeFilters) > 0 && !workspaceTextPathFilterMatches(includeFilters, relative, rootRelative, name)) ||
				workspaceTextPathFilterMatches(excludeFilters, relative, rootRelative, name) {
				select {
				case outcomes <- workspaceTextSearchFileOutcome{skipped: true}:
				case <-ctx.Done():
					return filepath.SkipAll
				}
				return nil
			}

			select {
			case jobs <- workspaceTextSearchCandidate{absolute: absolute, relative: relative}:
				return nil
			case <-ctx.Done():
				return filepath.SkipAll
			}
		})
		if err != nil && err != filepath.SkipAll {
			resultErr = err
			return
		}
	}
}

func searchWorkspaceTextFile(absolute string, relative string, matcher *regexp.Regexp) workspaceTextSearchFileOutcome {
	outcome := workspaceTextSearchFileOutcome{}
	info, err := os.Stat(absolute)
	if err != nil || !info.Mode().IsRegular() {
		outcome.skipped = true
		return outcome
	}
	if info.Size() > maxWorkspaceEditorFileBytes {
		outcome.skipped = true
		return outcome
	}
	data, err := os.ReadFile(absolute)
	if err != nil || !isWorkspaceTextLike(data) || !utf8.Valid(data) {
		outcome.skipped = true
		return outcome
	}

	outcome.searched = true
	content := string(data)
	file := WorkspaceTextSearchFileResult{
		Path:    relative,
		Name:    filepath.Base(absolute),
		Matches: []WorkspaceTextSearchMatch{},
	}
	lineStart := 0
	lineNumber := 1
	lineUTF16Start := 0
	for lineStart <= len(content) {
		newline := strings.IndexByte(content[lineStart:], '\n')
		lineEnd := len(content)
		nextLineStart := len(content) + 1
		if newline >= 0 {
			lineEnd = lineStart + newline
			nextLineStart = lineEnd + 1
		}
		textEnd := lineEnd
		if textEnd > lineStart && content[textEnd-1] == '\r' {
			textEnd--
		}
		lineTextValue := content[lineStart:textEnd]
		rawMatches := matcher.FindAllStringIndex(lineTextValue, -1)
		for _, raw := range rawMatches {
			if raw[0] == raw[1] {
				continue
			}
			if len(file.Matches) >= maxWorkspaceTextSearchMatches {
				outcome.truncated = true
				break
			}

			startUTF16 := utf16Length(lineTextValue[:raw[0]])
			endUTF16 := startUTF16 + utf16Length(lineTextValue[raw[0]:raw[1]])
			lineText, highlightStart, highlightEnd, truncated := workspaceTextSearchLinePreview(lineTextValue, raw[0], raw[1])
			file.Matches = append(file.Matches, WorkspaceTextSearchMatch{
				Line:           lineNumber,
				Column:         startUTF16 + 1,
				EndLine:        lineNumber,
				EndColumn:      endUTF16 + 1,
				Offset:         lineUTF16Start + startUTF16,
				EndOffset:      lineUTF16Start + endUTF16,
				LineText:       lineText,
				MatchText:      lineTextValue[raw[0]:raw[1]],
				HighlightStart: highlightStart,
				HighlightEnd:   highlightEnd,
				Truncated:      truncated,
			})
		}
		if outcome.truncated || newline < 0 {
			break
		}
		lineUTF16Start += utf16Length(content[lineStart:nextLineStart])
		lineStart = nextLineStart
		lineNumber++
	}

	outcome.file = file
	return outcome
}

func workspaceTextSearchMatcher(request WorkspaceTextSearchRequest) (*regexp.Regexp, error) {
	pattern := request.Query
	if !request.Regex {
		pattern = regexp.QuoteMeta(pattern)
	}
	if request.WholeWord {
		pattern = `\b(?:` + pattern + `)\b`
	}
	if !request.CaseSensitive {
		pattern = `(?i:` + pattern + `)`
	}
	matcher, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("query must be a valid regular expression: %w", err)
	}
	return matcher, nil
}

func workspaceTextSearchLinePreview(line string, matchStartByte int, matchEndByte int) (string, int, int, bool) {
	runes := []rune(line)
	if len(runes) <= maxWorkspaceTextSearchLineRunes {
		return line, utf16Length(line[:matchStartByte]), utf16Length(line[:matchEndByte]), false
	}

	matchStartRune := utf8.RuneCountInString(line[:matchStartByte])
	matchEndRune := utf8.RuneCountInString(line[:matchEndByte])
	previewStart := workspaceTextMaxInt(0, matchStartRune-(maxWorkspaceTextSearchLineRunes/3))
	previewEnd := workspaceTextMinInt(len(runes), previewStart+maxWorkspaceTextSearchLineRunes)
	if previewEnd < matchEndRune {
		previewEnd = workspaceTextMinInt(len(runes), matchEndRune+(maxWorkspaceTextSearchLineRunes/3))
		previewStart = workspaceTextMaxInt(0, previewEnd-maxWorkspaceTextSearchLineRunes)
	}

	prefix := ""
	suffix := ""
	if previewStart > 0 {
		prefix = "..."
	}
	if previewEnd < len(runes) {
		suffix = "..."
	}
	preview := string(runes[previewStart:previewEnd])
	highlightStart := utf16Length(string(runes[previewStart:matchStartRune])) + utf16Length(prefix)
	highlightEnd := highlightStart + utf16Length(string(runes[matchStartRune:matchEndRune]))
	return prefix + preview + suffix, highlightStart, highlightEnd, true
}

func workspaceTextRootRelativePath(folder WorkspaceFolder, relative string) string {
	relative = filepath.ToSlash(strings.TrimSpace(relative))
	label := filepath.ToSlash(strings.TrimSpace(folder.Label))
	if strings.EqualFold(relative, label) {
		return "."
	}
	prefix := label + "/"
	if strings.HasPrefix(strings.ToLower(relative), strings.ToLower(prefix)) {
		return relative[len(prefix):]
	}
	return relative
}

func compileWorkspaceTextPathFilters(value string) ([]workspaceTextPathFilter, error) {
	parts := strings.FieldsFunc(value, func(char rune) bool {
		return char == ',' || char == ';' || char == '\n' || char == '\r'
	})
	filters := make([]workspaceTextPathFilter, 0, len(parts))
	for _, part := range parts {
		pattern := normalizeWorkspaceTextPathFilter(part)
		if pattern == "" {
			continue
		}
		expression := workspaceTextGlobExpression(pattern)
		matcher, err := regexp.Compile(expression)
		if err != nil {
			return nil, fmt.Errorf("file filter %q is invalid: %w", pattern, err)
		}
		filters = append(filters, workspaceTextPathFilter{matcher: matcher})
	}
	return filters, nil
}

func normalizeWorkspaceTextPathFilter(pattern string) string {
	pattern = strings.TrimSpace(strings.ReplaceAll(pattern, "\\", "/"))
	pattern = strings.Trim(pattern, "\"'`")
	pattern = strings.TrimPrefix(pattern, "./")
	trailingSlash := strings.HasSuffix(pattern, "/")
	pattern = strings.Trim(pattern, "/")
	if trailingSlash && pattern != "" {
		pattern += "/**"
	}
	return pattern
}

func workspaceTextPathFilterMatches(filters []workspaceTextPathFilter, relative string, rootRelative string, name string) bool {
	if len(filters) == 0 {
		return false
	}
	relative = filepath.ToSlash(relative)
	rootRelative = filepath.ToSlash(rootRelative)
	name = filepath.ToSlash(name)
	for _, filter := range filters {
		if filter.matcher.MatchString(relative) || filter.matcher.MatchString(rootRelative) || filter.matcher.MatchString(name) {
			return true
		}
	}
	return false
}

func workspaceTextGlobExpression(pattern string) string {
	hasSlash := strings.Contains(pattern, "/")
	hasGlob := strings.ContainsAny(pattern, "*?[")
	if !hasGlob {
		quoted := regexp.QuoteMeta(pattern)
		if hasSlash {
			return `(?i)(?:^|/)` + quoted + `(?:$|/)`
		}
		return `(?i)(?:^|/)` + quoted + `(?:$|/)`
	}

	glob := workspaceTextGlobToRegexp(pattern)
	if hasSlash {
		return `(?i)^` + glob + `$`
	}
	return `(?i)(?:^|/)` + glob + `(?:$|/)`
}

func workspaceTextGlobToRegexp(pattern string) string {
	var builder strings.Builder
	for index := 0; index < len(pattern); index++ {
		char := pattern[index]
		switch char {
		case '*':
			if index+2 < len(pattern) && pattern[index+1] == '*' && pattern[index+2] == '/' {
				builder.WriteString(`(?:.*/)?`)
				index += 2
			} else if index+1 < len(pattern) && pattern[index+1] == '*' {
				builder.WriteString(".*")
				index++
			} else {
				builder.WriteString(`[^/]*`)
			}
		case '?':
			builder.WriteString(`[^/]`)
		case '[':
			end := strings.IndexByte(pattern[index+1:], ']')
			if end >= 0 {
				class := pattern[index+1 : index+1+end]
				if strings.HasPrefix(class, "!") {
					class = "^" + regexp.QuoteMeta(class[1:])
				}
				builder.WriteString("[")
				builder.WriteString(class)
				builder.WriteString("]")
				index += end + 1
			} else {
				builder.WriteString(regexp.QuoteMeta(string(char)))
			}
		default:
			builder.WriteString(regexp.QuoteMeta(string(char)))
		}
	}
	return builder.String()
}

func workspaceTextMinInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func workspaceTextMaxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}
