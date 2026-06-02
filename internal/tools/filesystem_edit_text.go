package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "filesystem_edit_text",
			Description: "Replace a unique text span in a UTF-8 or plain text file inside the active workspace. Exact matches are preferred; when no exact match exists, whitespace runs in oldText may match equivalent runs with different spaces, tabs, indentation, or line endings.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"path", "oldText", "newText"},
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Workspace-relative file path to edit.",
					},
					"oldText": map[string]any{
						"type":        "string",
						"description": "Text to replace. It must identify exactly one location. Prefer copying literal text; if indentation or line endings are uncertain, include enough surrounding non-whitespace text because whitespace runs can be matched flexibly.",
					},
					"newText": map[string]any{
						"type":        "string",
						"description": "Replacement text.",
					},
				},
			},
		},
		Run: editTextFile,
	})
}

type editTextFileArgs struct {
	Path    string `json:"path"`
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

type editTextFileOutput struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
	BytesWritten int64  `json:"bytesWritten"`
}

const maxAmbiguousMatchCandidates = 5

type textMatch struct {
	start int
	end   int
}

func editTextFile(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args editTextFileArgs
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	if args.Path == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "path is required"}
	}
	if args.OldText == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "oldText is required"}
	}

	path, err := resolveWorkspacePath(ctx.WorkspacePath, args.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, SafeError{Code: "path_not_found", Message: "file was not found"}
	}
	if !info.Mode().IsRegular() {
		return nil, SafeError{Code: "not_file", Message: "path is not a regular file"}
	}
	if info.Size() > maxTextFileBytes {
		return nil, SafeError{Code: "file_too_large", Message: fmt.Sprintf("file is larger than the %d byte editing limit", maxTextFileBytes)}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	if !isTextLike(data) || !utf8.Valid(data) {
		return nil, SafeError{Code: "binary_file", Message: "file appears to be binary"}
	}
	content := string(data)
	matches := literalMatches(content, args.OldText)
	switch len(matches) {
	case 0:
		matches = flexibleWhitespaceMatches(content, args.OldText)
		if len(matches) == 0 {
			return nil, SafeError{Code: "match_not_found", Message: "oldText was not found in the file"}
		}
	case 1:
	default:
		return nil, SafeError{
			Code:    "ambiguous_match",
			Message: ambiguousMatchMessage(content, matches),
		}
	}
	if len(matches) > 1 {
		return nil, SafeError{
			Code:    "ambiguous_match",
			Message: ambiguousMatchMessage(content, matches),
		}
	}

	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	match := matches[0]
	updated := content[:match.start] + args.NewText + content[match.end:]
	if err := os.WriteFile(path, []byte(updated), info.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}
	return editTextFileOutput{
		Path:         relativeWorkspacePath(ctx.WorkspacePath, path),
		Replacements: 1,
		BytesWritten: int64(len(updated)),
	}, nil
}

func literalMatches(content, needle string) []textMatch {
	if needle == "" {
		return nil
	}
	var matches []textMatch
	searchFrom := 0
	for {
		index := strings.Index(content[searchFrom:], needle)
		if index < 0 {
			return matches
		}
		absolute := searchFrom + index
		matches = append(matches, textMatch{start: absolute, end: absolute + len(needle)})
		searchFrom = absolute + len(needle)
	}
}

type flexibleSegment struct {
	text       string
	whitespace bool
	hasNewline bool
}

func flexibleWhitespaceMatches(content, needle string) []textMatch {
	if needle == "" || !containsWhitespace(needle) {
		return nil
	}
	segments := flexibleSegments(needle)
	var matches []textMatch
	for _, start := range flexibleStartIndexes(content, segments[0]) {
		end, ok := flexibleMatchEnd(content, start, segments, 0)
		if ok {
			matches = append(matches, textMatch{start: start, end: end})
		}
	}
	return matches
}

func flexibleStartIndexes(content string, first flexibleSegment) []int {
	if !first.whitespace {
		var starts []int
		searchFrom := 0
		for {
			index := strings.Index(content[searchFrom:], first.text)
			if index < 0 {
				return starts
			}
			start := searchFrom + index
			starts = append(starts, start)
			searchFrom = start + 1
		}
	}

	var starts []int
	previous := rune(0)
	for index, char := range content {
		if !unicode.IsSpace(char) {
			previous = char
			continue
		}
		isNewline := char == '\n' || char == '\r'
		previousIsWhitespace := previous != 0 && unicode.IsSpace(previous)
		previousIsNewline := previous == '\n' || previous == '\r'
		if first.hasNewline {
			if !previousIsWhitespace {
				starts = append(starts, index)
			}
		} else if !isNewline && (!previousIsWhitespace || previousIsNewline) {
			starts = append(starts, index)
		}
		previous = char
	}
	return starts
}

func flexibleSegments(text string) []flexibleSegment {
	var segments []flexibleSegment
	for index := 0; index < len(text); {
		char, width := utf8.DecodeRuneInString(text[index:])
		whitespace := unicode.IsSpace(char)
		start := index
		hasNewline := char == '\n' || char == '\r'
		index += width
		for index < len(text) {
			next, nextWidth := utf8.DecodeRuneInString(text[index:])
			if unicode.IsSpace(next) != whitespace {
				break
			}
			hasNewline = hasNewline || next == '\n' || next == '\r'
			index += nextWidth
		}
		segments = append(segments, flexibleSegment{
			text:       text[start:index],
			whitespace: whitespace,
			hasNewline: hasNewline,
		})
	}
	return segments
}

func flexibleMatchEnd(content string, position int, segments []flexibleSegment, segmentIndex int) (int, bool) {
	if segmentIndex >= len(segments) {
		return position, true
	}
	if position > len(content) {
		return 0, false
	}
	segment := segments[segmentIndex]
	if !segment.whitespace {
		if !strings.HasPrefix(content[position:], segment.text) {
			return 0, false
		}
		return flexibleMatchEnd(content, position+len(segment.text), segments, segmentIndex+1)
	}
	for _, end := range whitespaceCandidateEnds(content, position, segment.hasNewline, segmentIndex == len(segments)-1) {
		if matchEnd, ok := flexibleMatchEnd(content, end, segments, segmentIndex+1); ok {
			return matchEnd, true
		}
	}
	return 0, false
}

func whitespaceCandidateEnds(content string, position int, needsNewline bool, terminal bool) []int {
	if position >= len(content) {
		return nil
	}
	var ends []int
	seenNewline := false
	index := position
	for index < len(content) {
		char, width := utf8.DecodeRuneInString(content[index:])
		if !unicode.IsSpace(char) {
			break
		}
		isNewline := char == '\n' || char == '\r'
		seenNewline = seenNewline || isNewline
		index += width
		if needsNewline != seenNewline && needsNewline {
			continue
		}
		if !needsNewline && isNewline {
			break
		}
		ends = append(ends, index)
		if terminal && terminalWhitespaceEnd(content, index, needsNewline) {
			break
		}
	}
	return ends
}

func terminalWhitespaceEnd(content string, position int, needsNewline bool) bool {
	if position >= len(content) {
		return true
	}
	char, _ := utf8.DecodeRuneInString(content[position:])
	if !unicode.IsSpace(char) {
		return true
	}
	return needsNewline && (char == '\n' || char == '\r')
}

func containsWhitespace(text string) bool {
	for _, char := range text {
		if unicode.IsSpace(char) {
			return true
		}
	}
	return false
}

func ambiguousMatchMessage(content string, matches []textMatch) string {
	candidates := uniqueExpandedMatches(content, matches)
	var message strings.Builder
	fmt.Fprintf(&message, "oldText matched %d locations; expand oldText until it is unique. Unique expanded candidates:", len(matches))
	if len(matches) > len(candidates) {
		fmt.Fprintf(&message, " showing first %d", len(candidates))
	}
	for i, candidate := range candidates {
		fmt.Fprintf(&message, "\n--- candidate %d ---\n%s", i+1, candidate)
	}
	return message.String()
}

func uniqueExpandedMatches(content string, matches []textMatch) []string {
	limit := minInt(len(matches), maxAmbiguousMatchCandidates)
	candidates := make([]string, 0, limit)
	for _, match := range matches[:limit] {
		candidates = append(candidates, uniqueExpandedMatch(content, match.start, match.end))
	}
	return candidates
}

func uniqueExpandedMatch(content string, start, end int) string {
	lineStarts := lineStartIndexes(content)
	startLine := lineIndexForOffset(lineStarts, start)
	endLine := lineIndexForOffset(lineStarts, maxInt(start, end-1))

	for padding := 0; ; padding++ {
		fromLine := maxInt(0, startLine-padding)
		toLine := minInt(len(lineStarts)-1, endLine+padding)
		from := lineStarts[fromLine]
		to := len(content)
		if toLine+1 < len(lineStarts) {
			to = lineStarts[toLine+1]
		}
		candidate := strings.TrimRight(content[from:to], "\r\n")
		if len(literalMatches(content, candidate)) == 1 {
			return candidate
		}
		if fromLine == 0 && toLine == len(lineStarts)-1 {
			return candidate
		}
	}
}

func lineStartIndexes(content string) []int {
	starts := []int{0}
	for index, char := range content {
		if char == '\n' && index+1 < len(content) {
			starts = append(starts, index+1)
		}
	}
	return starts
}

func lineIndexForOffset(lineStarts []int, offset int) int {
	line := 0
	for i := 1; i < len(lineStarts); i++ {
		if lineStarts[i] > offset {
			break
		}
		line = i
	}
	return line
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
