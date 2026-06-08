package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	defaultSearchContextLines = 2
	maxSearchContextLines     = 80
	defaultSearchMaxMatches   = 20
	maxSearchMaxMatches       = 100
	maxSearchFileBytes        = 4 * 1024 * 1024
	maxSearchLineBytes        = 2000
	maxSearchMatchBytes       = 512
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "filesystem_search_text",
			Description: "Search a UTF-8 or plain text file inside the active workspace without returning the whole file. Prefer this before filesystem_read_text when locating symbols, strings, or code blocks in large or unfamiliar files.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"path", "query"},
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Workspace-relative file path to search.",
					},
					"query": map[string]any{
						"type":        "string",
						"description": "Literal text to find, or a Go regular expression when regex is true.",
					},
					"regex": map[string]any{
						"type":        "boolean",
						"description": "Treat query as a Go regular expression. Defaults to false.",
					},
					"multiline": map[string]any{
						"type":        "boolean",
						"description": "When regex is true, match against the whole file so the regex can return a multi-line block. Defaults to false.",
					},
					"caseSensitive": map[string]any{
						"type":        "boolean",
						"description": "Whether matching is case-sensitive. Defaults to true.",
					},
					"contextLines": map[string]any{
						"type":        "integer",
						"description": "Number of lines to include before and after each match. Defaults to 2 and is capped at 80.",
						"minimum":     0,
						"maximum":     maxSearchContextLines,
					},
					"maxMatches": map[string]any{
						"type":        "integer",
						"description": "Maximum matches to return. Defaults to 20 and is capped at 100.",
						"minimum":     1,
						"maximum":     maxSearchMaxMatches,
					},
				},
			},
		},
		Run: searchTextFile,
	})
}

type searchTextFileArgs struct {
	Path          string `json:"path"`
	Query         string `json:"query"`
	Regex         bool   `json:"regex"`
	Multiline     bool   `json:"multiline"`
	CaseSensitive *bool  `json:"caseSensitive"`
	ContextLines  *int   `json:"contextLines"`
	MaxMatches    *int   `json:"maxMatches"`
}

type searchTextFileOutput struct {
	Path            string                  `json:"path"`
	Query           string                  `json:"query"`
	Regex           bool                    `json:"regex"`
	Multiline       bool                    `json:"multiline,omitempty"`
	CaseSensitive   bool                    `json:"caseSensitive"`
	MatchCount      int                     `json:"matchCount"`
	ReturnedMatches int                     `json:"returnedMatches"`
	FileTruncated   bool                    `json:"fileTruncated"`
	Matches         []searchTextMatchOutput `json:"matches"`
}

type searchTextMatchOutput struct {
	Line      int                    `json:"line"`
	Column    int                    `json:"column"`
	EndLine   int                    `json:"endLine,omitempty"`
	EndColumn int                    `json:"endColumn,omitempty"`
	Match     string                 `json:"match,omitempty"`
	Lines     []searchTextLineOutput `json:"lines"`
}

type searchTextLineOutput struct {
	Number    int    `json:"number"`
	Text      string `json:"text"`
	Match     bool   `json:"match,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type searchIndexedLine struct {
	Number int
	Start  int
	Text   string
}

type searchTextRange struct {
	start int
	end   int
}

func searchTextFile(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args searchTextFileArgs
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	args.Path = strings.TrimSpace(args.Path)
	if args.Path == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "path is required"}
	}
	if args.Query == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "query is required"}
	}

	caseSensitive := true
	if args.CaseSensitive != nil {
		caseSensitive = *args.CaseSensitive
	}
	contextLines := defaultSearchContextLines
	if args.ContextLines != nil {
		contextLines = *args.ContextLines
		if contextLines < 0 {
			contextLines = 0
		}
	}
	if contextLines > maxSearchContextLines {
		contextLines = maxSearchContextLines
	}
	maxMatches := defaultSearchMaxMatches
	if args.MaxMatches != nil {
		maxMatches = *args.MaxMatches
		if maxMatches <= 0 {
			maxMatches = 1
		}
	}
	if maxMatches > maxSearchMaxMatches {
		maxMatches = maxSearchMaxMatches
	}

	matcher, err := searchMatcher(args.Query, args.Regex, caseSensitive)
	if err != nil {
		return nil, err
	}

	path, err := resolveWorkspacePath(ctx.WorkspacePath, args.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, SafeError{Code: "path_not_found", Message: fmt.Sprintf("file %s was not found", relativeWorkspacePath(ctx.WorkspacePath, path))}
	}
	if !info.Mode().IsRegular() {
		return nil, SafeError{Code: "not_file", Message: "path is not a regular file"}
	}

	data, fileTruncated, err := readSearchFile(path)
	if err != nil {
		return nil, err
	}
	if !isTextLike(data) {
		return nil, SafeError{Code: "binary_file", Message: "file appears to be binary"}
	}
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}

	content := string(data)
	lines, lineStarts := searchLines(content)
	ranges := searchTextRanges(content, lines, matcher, args.Regex && args.Multiline)
	returned := minInt(len(ranges), maxMatches)
	matches := make([]searchTextMatchOutput, 0, returned)
	for _, match := range ranges[:returned] {
		matches = append(matches, searchTextMatch(content, lines, lineStarts, match, contextLines))
	}

	return searchTextFileOutput{
		Path:            relativeWorkspacePath(ctx.WorkspacePath, path),
		Query:           args.Query,
		Regex:           args.Regex,
		Multiline:       args.Regex && args.Multiline,
		CaseSensitive:   caseSensitive,
		MatchCount:      len(ranges),
		ReturnedMatches: len(matches),
		FileTruncated:   fileTruncated,
		Matches:         matches,
	}, nil
}

func searchMatcher(query string, regex bool, caseSensitive bool) (*regexp.Regexp, error) {
	pattern := query
	if !regex {
		pattern = regexp.QuoteMeta(query)
	}
	if !caseSensitive {
		pattern = "(?i)" + pattern
	}
	matcher, err := regexp.Compile(pattern)
	if err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: fmt.Sprintf("query must be a valid regular expression: %v", err)}
	}
	return matcher, nil
}

func readSearchFile(path string) ([]byte, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, int64(maxSearchFileBytes+1)))
	if err != nil {
		return nil, false, fmt.Errorf("read file: %w", err)
	}
	if len(data) <= maxSearchFileBytes {
		return data, false, nil
	}
	return data[:maxSearchFileBytes], true, nil
}

func searchLines(content string) ([]searchIndexedLine, []int) {
	lineStarts := lineStartIndexes(content)
	lines := make([]searchIndexedLine, 0, len(lineStarts))
	for index, start := range lineStarts {
		end := len(content)
		if index+1 < len(lineStarts) {
			end = lineStarts[index+1]
		}
		textEnd := end
		if textEnd > start && content[textEnd-1] == '\n' {
			textEnd--
		}
		if textEnd > start && content[textEnd-1] == '\r' {
			textEnd--
		}
		lines = append(lines, searchIndexedLine{
			Number: index + 1,
			Start:  start,
			Text:   content[start:textEnd],
		})
	}
	return lines, lineStarts
}

func searchTextRanges(content string, lines []searchIndexedLine, matcher *regexp.Regexp, multiline bool) []searchTextRange {
	if multiline {
		return searchRegexpRanges(content, 0, matcher)
	}

	var ranges []searchTextRange
	for _, line := range lines {
		for _, match := range searchRegexpRanges(line.Text, line.Start, matcher) {
			ranges = append(ranges, match)
		}
	}
	return ranges
}

func searchRegexpRanges(content string, offset int, matcher *regexp.Regexp) []searchTextRange {
	rawMatches := matcher.FindAllStringIndex(content, -1)
	ranges := make([]searchTextRange, 0, len(rawMatches))
	for _, raw := range rawMatches {
		if raw[0] == raw[1] {
			continue
		}
		ranges = append(ranges, searchTextRange{
			start: offset + raw[0],
			end:   offset + raw[1],
		})
	}
	return ranges
}

func searchTextMatch(content string, lines []searchIndexedLine, lineStarts []int, match searchTextRange, contextLines int) searchTextMatchOutput {
	startLineIndex := lineIndexForOffset(lineStarts, boundedSearchOffset(content, match.start))
	endLineIndex := lineIndexForOffset(lineStarts, boundedSearchOffset(content, match.end-1))
	startLine := lines[startLineIndex]
	endLine := lines[endLineIndex]
	fromLine := maxInt(0, startLineIndex-contextLines)
	toLine := minInt(len(lines)-1, endLineIndex+contextLines)

	outputLines := make([]searchTextLineOutput, 0, toLine-fromLine+1)
	for index := fromLine; index <= toLine; index++ {
		text, truncated := truncateSearchText(lines[index].Text, maxSearchLineBytes)
		outputLines = append(outputLines, searchTextLineOutput{
			Number:    lines[index].Number,
			Text:      text,
			Match:     index >= startLineIndex && index <= endLineIndex,
			Truncated: truncated,
		})
	}

	matchText, _ := truncateSearchText(content[match.start:match.end], maxSearchMatchBytes)
	return searchTextMatchOutput{
		Line:      startLine.Number,
		Column:    searchColumn(content, startLine.Start, match.start),
		EndLine:   endLine.Number,
		EndColumn: searchColumn(content, endLine.Start, match.end),
		Match:     matchText,
		Lines:     outputLines,
	}
}

func boundedSearchOffset(content string, offset int) int {
	if len(content) == 0 {
		return 0
	}
	if offset < 0 {
		return 0
	}
	if offset >= len(content) {
		return len(content) - 1
	}
	return offset
}

func searchColumn(content string, lineStart int, offset int) int {
	if offset <= lineStart {
		return 1
	}
	if offset > len(content) {
		offset = len(content)
	}
	return utf8.RuneCountInString(content[lineStart:offset]) + 1
}

func truncateSearchText(text string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text, false
	}
	return text[:maxBytes] + "...", true
}
