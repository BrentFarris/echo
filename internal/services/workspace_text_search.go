package services

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxWorkspaceTextSearchMatches   = 1000
	maxWorkspaceTextSearchLineRunes = 360
)

type WorkspaceTextSearchRequest struct {
	Query          string `json:"query"`
	Regex          bool   `json:"regex"`
	CaseSensitive  bool   `json:"caseSensitive"`
	WholeWord      bool   `json:"wholeWord"`
	Include        string `json:"include,omitempty"`
	Exclude        string `json:"exclude,omitempty"`
	IncludeIgnored bool   `json:"includeIgnored"`
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

type workspaceTextSearchLine struct {
	number int
	start  int
	text   string
}

type workspaceTextPathFilter struct {
	matcher *regexp.Regexp
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

	for _, folder := range workspace.Folders {
		if folder.Missing {
			continue
		}
		root, err := resolveWorkspaceServicePath(workspace, folder.Label)
		if err != nil {
			continue
		}
		err = filepath.WalkDir(root, func(absolute string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				output.FilesSkipped++
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

			if len(includeFilters) > 0 && !workspaceTextPathFilterMatches(includeFilters, relative, rootRelative, name) {
				output.FilesSkipped++
				return nil
			}
			if workspaceTextPathFilterMatches(excludeFilters, relative, rootRelative, name) {
				output.FilesSkipped++
				return nil
			}

			if searchWorkspaceTextFile(workspace, absolute, relative, matcher, &output) {
				return filepath.SkipAll
			}
			return nil
		})
		if err != nil && err != filepath.SkipAll {
			return WorkspaceTextSearchResult{}, fmt.Errorf("search workspace text: %w", err)
		}
		if output.Truncated {
			break
		}
	}

	sort.Slice(output.Files, func(i, j int) bool {
		return strings.ToLower(output.Files[i].Path) < strings.ToLower(output.Files[j].Path)
	})
	output.FileCount = len(output.Files)
	return output, nil
}

func searchWorkspaceTextFile(workspace Workspace, absolute string, relative string, matcher *regexp.Regexp, output *WorkspaceTextSearchResult) bool {
	info, err := os.Stat(absolute)
	if err != nil || !info.Mode().IsRegular() {
		output.FilesSkipped++
		return false
	}
	if info.Size() > maxWorkspaceEditorFileBytes {
		output.FilesSkipped++
		return false
	}
	data, err := os.ReadFile(absolute)
	if err != nil || !isWorkspaceTextLike(data) || !utf8.Valid(data) {
		output.FilesSkipped++
		return false
	}

	output.FilesSearched++
	content := string(data)
	lines := workspaceTextSearchLines(content)
	file := WorkspaceTextSearchFileResult{
		Path:    relative,
		Name:    filepath.Base(absolute),
		Matches: []WorkspaceTextSearchMatch{},
	}

	for _, line := range lines {
		rawMatches := matcher.FindAllStringIndex(line.text, -1)
		for _, raw := range rawMatches {
			if raw[0] == raw[1] {
				continue
			}
			if output.MatchCount >= maxWorkspaceTextSearchMatches {
				output.Truncated = true
				if len(file.Matches) > 0 {
					output.Files = append(output.Files, file)
				}
				return true
			}

			startByte := line.start + raw[0]
			endByte := line.start + raw[1]
			lineText, highlightStart, highlightEnd, truncated := workspaceTextSearchLinePreview(line.text, raw[0], raw[1])
			file.Matches = append(file.Matches, WorkspaceTextSearchMatch{
				Line:           line.number,
				Column:         utf16Length(line.text[:raw[0]]) + 1,
				EndLine:        line.number,
				EndColumn:      utf16Length(line.text[:raw[1]]) + 1,
				Offset:         utf16Length(content[:startByte]),
				EndOffset:      utf16Length(content[:endByte]),
				LineText:       lineText,
				MatchText:      line.text[raw[0]:raw[1]],
				HighlightStart: highlightStart,
				HighlightEnd:   highlightEnd,
				Truncated:      truncated,
			})
			output.MatchCount++
		}
	}

	if len(file.Matches) > 0 {
		output.Files = append(output.Files, file)
	}
	return false
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

func workspaceTextSearchLines(content string) []workspaceTextSearchLine {
	lines := []workspaceTextSearchLine{}
	start := 0
	number := 1
	for index, char := range content {
		if char != '\n' {
			continue
		}
		end := index
		if end > start && content[end-1] == '\r' {
			end--
		}
		lines = append(lines, workspaceTextSearchLine{number: number, start: start, text: content[start:end]})
		start = index + 1
		number++
	}
	if start <= len(content) {
		lines = append(lines, workspaceTextSearchLine{number: number, start: start, text: content[start:]})
	}
	return lines
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
