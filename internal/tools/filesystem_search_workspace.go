package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "filesystem_search_workspace",
			Description: "Search text files across the active workspace without needing to know the target file first. Prefer this when locating symbols, strings, files, or code blocks in an unfamiliar repository.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"query"},
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Optional labeled workspace directory or file path to search. Defaults to . for all workspace folders. " + labeledPathSchemaHint,
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
						"description": "When regex is true, match against each whole file so the regex can return a multi-line block. Defaults to false.",
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
						"description": "Maximum total matches to return. Defaults to 20 and is capped at 100.",
						"minimum":     1,
						"maximum":     maxSearchMaxMatches,
					},
					"includeHidden": map[string]any{
						"type":        "boolean",
						"description": "Whether to include dotfiles and dot directories. Defaults to false.",
					},
					"includeIgnored": map[string]any{
						"type":        "boolean",
						"description": "Whether to include common ignored/noisy directories like .git, node_modules, dist, build, target, and coverage. Defaults to false.",
					},
				},
			},
		},
		Run: searchWorkspaceText,
	})
}

type searchWorkspaceTextArgs struct {
	Path           string `json:"path"`
	Query          string `json:"query"`
	Regex          bool   `json:"regex"`
	Multiline      bool   `json:"multiline"`
	CaseSensitive  *bool  `json:"caseSensitive"`
	ContextLines   *int   `json:"contextLines"`
	MaxMatches     *int   `json:"maxMatches"`
	IncludeHidden  bool   `json:"includeHidden"`
	IncludeIgnored bool   `json:"includeIgnored"`
}

type searchWorkspaceTextOutput struct {
	Path            string                           `json:"path"`
	Query           string                           `json:"query"`
	Regex           bool                             `json:"regex"`
	Multiline       bool                             `json:"multiline,omitempty"`
	CaseSensitive   bool                             `json:"caseSensitive"`
	MatchCount      int                              `json:"matchCount"`
	ReturnedMatches int                              `json:"returnedMatches"`
	FilesSearched   int                              `json:"filesSearched"`
	FilesSkipped    int                              `json:"filesSkipped"`
	Truncated       bool                             `json:"truncated"`
	Matches         []searchWorkspaceTextMatchOutput `json:"matches"`
}

type searchWorkspaceTextMatchOutput struct {
	Path      string                 `json:"path"`
	Line      int                    `json:"line"`
	Column    int                    `json:"column"`
	EndLine   int                    `json:"endLine,omitempty"`
	EndColumn int                    `json:"endColumn,omitempty"`
	Match     string                 `json:"match,omitempty"`
	Lines     []searchTextLineOutput `json:"lines"`
}

type workspaceSearchStartPath struct {
	absolute string
	relative string
}

func searchWorkspaceText(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args searchWorkspaceTextArgs
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	args.Path = strings.TrimSpace(args.Path)
	if args.Path == "" {
		args.Path = "."
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
	startPaths, err := workspaceSearchStartPaths(ctx, args.Path)
	if err != nil {
		return nil, err
	}
	output := searchWorkspaceTextOutput{
		Path:          args.Path,
		Query:         args.Query,
		Regex:         args.Regex,
		Multiline:     args.Regex && args.Multiline,
		CaseSensitive: caseSensitive,
		Matches:       []searchWorkspaceTextMatchOutput{},
	}

	for _, start := range startPaths {
		if err := ctx.context().Err(); err != nil {
			return nil, err
		}
		if err := searchWorkspaceStartPath(ctx, start, args, matcher, contextLines, maxMatches, &output); err != nil {
			return nil, err
		}
		if output.Truncated {
			break
		}
	}
	output.ReturnedMatches = len(output.Matches)
	return output, nil
}

func workspaceSearchStartPaths(ctx ExecutionContext, requestedPath string) ([]workspaceSearchStartPath, error) {
	roots := ctx.workspaceRoots()
	if len(roots) == 0 {
		return nil, SafeError{Code: "missing_workspace", Message: "workspace path is required"}
	}
	requestedPath = strings.TrimSpace(requestedPath)
	if requestedPath == "" || requestedPath == "." {
		starts := make([]workspaceSearchStartPath, 0, len(roots))
		for _, root := range roots {
			absolute, err := workspaceRootAbsolutePath(root)
			if err != nil {
				return nil, err
			}
			relative := root.Label
			if relative == "." {
				relative = "."
			}
			starts = append(starts, workspaceSearchStartPath{absolute: absolute, relative: relative})
		}
		return starts, nil
	}

	absolute, err := resolveWorkspacePath(ctx, requestedPath)
	if err != nil {
		return nil, err
	}
	return []workspaceSearchStartPath{{
		absolute: absolute,
		relative: relativeWorkspacePath(ctx, absolute),
	}}, nil
}

func searchWorkspaceStartPath(ctx ExecutionContext, start workspaceSearchStartPath, args searchWorkspaceTextArgs, matcher *regexp.Regexp, contextLines int, maxMatches int, output *searchWorkspaceTextOutput) error {
	info, err := os.Stat(start.absolute)
	if err != nil {
		return SafeError{Code: "path_not_found", Message: fmt.Sprintf("path %s was not found", start.relative)}
	}
	if info.Mode().IsRegular() {
		searchWorkspaceFile(ctx, start.absolute, args, matcher, contextLines, maxMatches, output)
		return nil
	}
	if !info.IsDir() {
		output.FilesSkipped++
		return nil
	}

	err = filepath.WalkDir(start.absolute, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			output.FilesSkipped++
			return nil
		}
		if err := ctx.context().Err(); err != nil {
			return err
		}
		if path != start.absolute && searchWorkspaceEntrySkipped(ctx, path, entry, args) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			output.FilesSkipped++
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if searchWorkspaceFile(ctx, path, args, matcher, contextLines, maxMatches, output) {
			return filepath.SkipAll
		}
		return nil
	})
	if err == filepath.SkipAll {
		return nil
	}
	return err
}

func searchWorkspaceEntrySkipped(ctx ExecutionContext, path string, entry os.DirEntry, args searchWorkspaceTextArgs) bool {
	name := entry.Name()
	if !args.IncludeHidden && strings.HasPrefix(name, ".") {
		return true
	}
	if !args.IncludeIgnored && IsIgnoredChangePath(relativeWorkspacePath(ctx, path)) {
		return true
	}
	return false
}

func searchWorkspaceFile(ctx ExecutionContext, path string, args searchWorkspaceTextArgs, matcher *regexp.Regexp, contextLines int, maxMatches int, output *searchWorkspaceTextOutput) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		output.FilesSkipped++
		return false
	}
	relative := relativeWorkspacePath(ctx, path)
	if searchWorkspacePathSkipped(relative, filepath.Base(path), args) {
		output.FilesSkipped++
		return false
	}
	if info.Size() > maxSearchFileBytes {
		output.FilesSkipped++
		return false
	}

	data, _, err := readSearchFile(path)
	if err != nil || !isTextLike(data) || !utf8.Valid(data) {
		output.FilesSkipped++
		return false
	}
	output.FilesSearched++

	content := string(data)
	lines, lineStarts := searchLines(content)
	ranges := searchTextRanges(content, lines, matcher, args.Regex && args.Multiline)
	if len(ranges) == 0 {
		return false
	}
	output.MatchCount += len(ranges)

	for index, matchRange := range ranges {
		if len(output.Matches) >= maxMatches {
			output.Truncated = true
			return true
		}
		match := searchTextMatch(content, lines, lineStarts, matchRange, contextLines)
		output.Matches = append(output.Matches, searchWorkspaceTextMatchOutput{
			Path:      relative,
			Line:      match.Line,
			Column:    match.Column,
			EndLine:   match.EndLine,
			EndColumn: match.EndColumn,
			Match:     match.Match,
			Lines:     match.Lines,
		})
		if index < len(ranges)-1 && len(output.Matches) >= maxMatches {
			output.Truncated = true
			return true
		}
	}
	return false
}

func searchWorkspacePathSkipped(relative string, name string, args searchWorkspaceTextArgs) bool {
	if !args.IncludeHidden && strings.HasPrefix(name, ".") {
		return true
	}
	if !args.IncludeIgnored && IsIgnoredChangePath(relative) {
		return true
	}
	return false
}
