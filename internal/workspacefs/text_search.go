package workspacefs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

const maximumTextSearchMatches = 10000

type TextSearchOverlay struct {
	Ref      FileRef `json:"ref"`
	Revision string  `json:"revision"`
	Content  string  `json:"content"`
	HasBOM   bool    `json:"hasBom"`
}

type TextSearchRequest struct {
	Query         string              `json:"query"`
	Replacement   string              `json:"replacement"`
	Regex         bool                `json:"regex"`
	CaseSensitive bool                `json:"caseSensitive"`
	WholeWord     bool                `json:"wholeWord"`
	Include       []string            `json:"include"`
	Exclude       []string            `json:"exclude"`
	Overlays      []TextSearchOverlay `json:"overlays"`
}

type TextSearchMatch struct {
	ID                 string `json:"id"`
	Line               int    `json:"line"`
	Column             int    `json:"column"`
	EndLine            int    `json:"endLine"`
	EndColumn          int    `json:"endColumn"`
	Preview            string `json:"preview"`
	PreviewMatchStart  int    `json:"previewMatchStart"`
	PreviewMatchEnd    int    `json:"previewMatchEnd"`
	Match              string `json:"match"`
	ReplacementPreview string `json:"replacementPreview"`
}

type TextSearchFileResult struct {
	Ref             FileRef           `json:"ref"`
	Name            string            `json:"name"`
	ReferencePath   string            `json:"referencePath"`
	Revision        string            `json:"revision"`
	ContentRevision string            `json:"contentRevision"`
	Overlay         bool              `json:"overlay"`
	Matches         []TextSearchMatch `json:"matches"`
}

type TextSearchResponse struct {
	Files         []TextSearchFileResult `json:"files"`
	MatchCount    int                    `json:"matchCount"`
	FilesSearched int                    `json:"filesSearched"`
	FilesSkipped  int                    `json:"filesSkipped"`
	Indexing      bool                   `json:"indexing"`
	Indexed       int                    `json:"indexed"`
	Truncated     bool                   `json:"truncated"`
}

type TextReplaceTarget struct {
	Ref             FileRef  `json:"ref"`
	Revision        string   `json:"revision"`
	ContentRevision string   `json:"contentRevision"`
	MatchIDs        []string `json:"matchIds,omitempty"`
}

type TextReplaceRequest struct {
	Search  TextSearchRequest   `json:"search"`
	Scope   string              `json:"scope"`
	Targets []TextReplaceTarget `json:"targets"`
}

type TextReplaceUpdate struct {
	Ref        FileRef `json:"ref"`
	Revision   string  `json:"revision"`
	Size       int64   `json:"size"`
	ModifiedAt string  `json:"modifiedAt"`
	EOL        string  `json:"eol"`
	HasBOM     bool    `json:"hasBom"`
	Content    string  `json:"content,omitempty"`
}

type TextReplaceResponse struct {
	Updated []TextReplaceUpdate `json:"updated"`
}

type PartialReplaceError struct {
	Cause    error
	Response TextReplaceResponse
}

func (e *PartialReplaceError) Error() string { return e.Cause.Error() }
func (e *PartialReplaceError) Unwrap() error { return e.Cause }

type compiledTextSearch struct {
	matcher *regexp.Regexp
	include []*regexp.Regexp
	exclude []*regexp.Regexp
}

type locatedTextMatch struct {
	public      TextSearchMatch
	start       int
	end         int
	submatches  []int
	replacement string
}

func (s *Service) SearchText(ctx context.Context, workspaceID string, request TextSearchRequest) (TextSearchResponse, error) {
	response := TextSearchResponse{Files: []TextSearchFileResult{}}
	compiled, err := compileTextSearch(request)
	if err != nil {
		return response, err
	}
	entries, indexing, indexed, indexTruncated := s.index.ContentCandidates(workspaceID)
	response.Indexing, response.Indexed, response.Truncated = indexing, indexed, indexTruncated
	overlays := textSearchOverlayMap(request.Overlays)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return response, err
		}
		if !compiled.matchesPath(entry.Ref.Path, entry.ReferencePath) {
			continue
		}
		content, revision, overlay, readErr := s.textSearchContent(workspaceID, entry.Ref, overlays)
		if readErr != nil {
			response.FilesSkipped++
			continue
		}
		response.FilesSearched++
		contentToken := contentRevision([]byte(content))
		remaining := maximumTextSearchMatches - response.MatchCount
		matches := findTextMatches(content, contentToken, compiled.matcher, request.Replacement, remaining)
		if len(matches) == 0 {
			continue
		}
		publicMatches := make([]TextSearchMatch, len(matches))
		for index := range matches {
			publicMatches[index] = matches[index].public
		}
		response.Files = append(response.Files, TextSearchFileResult{
			Ref: entry.Ref, Name: entry.Name, ReferencePath: entry.ReferencePath,
			Revision: revision, ContentRevision: contentToken, Overlay: overlay, Matches: publicMatches,
		})
		response.MatchCount += len(matches)
		if response.MatchCount >= maximumTextSearchMatches {
			response.Truncated = true
			break
		}
	}
	return response, nil
}

func (s *Service) ReplaceText(ctx context.Context, workspaceID string, request TextReplaceRequest) (TextReplaceResponse, error) {
	response := TextReplaceResponse{Updated: []TextReplaceUpdate{}}
	if request.Scope != "match" && request.Scope != "file" && request.Scope != "all" {
		return response, &Error{Code: "invalid_search", Message: "replace scope must be match, file, or all", Cause: ErrInvalidPath}
	}
	if len(request.Targets) == 0 {
		return response, &Error{Code: "invalid_search", Message: "replace targets are required", Cause: ErrInvalidPath}
	}
	compiled, err := compileTextSearch(request.Search)
	if err != nil {
		return response, err
	}
	overlays := textSearchOverlayMap(request.Search.Overlays)
	type pendingWrite struct {
		target  TextReplaceTarget
		path    string
		mode    os.FileMode
		content string
		data    []byte
		hasBOM  bool
		overlay bool
	}
	pending := make([]pendingWrite, 0, len(request.Targets))
	paths := make([]string, 0, len(request.Targets))
	seen := make(map[string]bool, len(request.Targets))
	for _, target := range request.Targets {
		key := textSearchRefKey(target.Ref)
		if seen[key] {
			return response, &Error{Code: "invalid_search", Message: "replace targets contain a duplicate file", Cause: ErrInvalidPath}
		}
		seen[key] = true
		_, resolved, _, resolveErr := s.resolve(workspaceID, target.Ref, false, false)
		if resolveErr != nil {
			return response, resolveErr
		}
		paths = append(paths, resolved)
		pending = append(pending, pendingWrite{target: target, path: resolved})
	}
	unlock := s.lockPaths(paths...)
	defer unlock()

	for index := range pending {
		if err := ctx.Err(); err != nil {
			return response, err
		}
		item := &pending[index]
		snapshot, readErr := s.Read(workspaceID, item.target.Ref)
		if readErr != nil {
			return response, readErr
		}
		if snapshot.Revision != item.target.Revision {
			return response, searchConflict("a target file changed after the search completed")
		}
		item.content = snapshot.Content
		item.hasBOM = snapshot.HasBOM
		if overlay, ok := overlays[textSearchRefKey(item.target.Ref)]; ok {
			if overlay.Revision != snapshot.Revision {
				return response, searchConflict("an unsaved buffer is based on an older disk revision")
			}
			item.content = overlay.Content
			item.hasBOM = overlay.HasBOM
			item.overlay = true
		}
		contentToken := contentRevision([]byte(item.content))
		if contentToken != item.target.ContentRevision {
			return response, searchConflict("search results are stale")
		}
		matches := findTextMatches(item.content, contentToken, compiled.matcher, request.Search.Replacement, maximumTextSearchMatches)
		selected := matches
		if request.Scope == "match" {
			wanted := make(map[string]bool, len(item.target.MatchIDs))
			for _, id := range item.target.MatchIDs {
				wanted[id] = true
			}
			selected = selected[:0]
			for _, match := range matches {
				if wanted[match.public.ID] {
					selected = append(selected, match)
					delete(wanted, match.public.ID)
				}
			}
			if len(wanted) > 0 || len(selected) == 0 {
				return response, searchConflict("the selected match is no longer available")
			}
		}
		if len(selected) == 0 {
			return response, searchConflict("a target file no longer contains a matching result")
		}
		item.content = applyTextReplacements(item.content, selected)
		item.data = []byte(item.content)
		if item.hasBOM {
			item.data = append(append([]byte(nil), utf8BOM...), item.data...)
		}
		if int64(len(item.data)) > MaxEditableBytes {
			return response, &Error{Code: "file_too_large", Message: "replacement would exceed the 10 MiB editor limit", Cause: ErrTooLarge}
		}
		info, statErr := os.Stat(item.path)
		if statErr != nil || !info.Mode().IsRegular() {
			return response, searchConflict("a target file is no longer available")
		}
		item.mode = info.Mode().Perm()
	}

	changes := make([]Change, 0, len(pending))
	for _, item := range pending {
		if err := ctx.Err(); err != nil {
			if len(changes) > 0 {
				s.index.ApplyChanges(workspaceID, changes)
				return response, &PartialReplaceError{Cause: err, Response: response}
			}
			return response, err
		}
		if writeErr := atomicWrite(item.path, item.data, item.mode); writeErr != nil {
			if len(changes) > 0 {
				s.index.ApplyChanges(workspaceID, changes)
			}
			return response, &PartialReplaceError{Cause: fmt.Errorf("replace file: %w", writeErr), Response: response}
		}
		info, _ := os.Stat(item.path)
		update := TextReplaceUpdate{
			Ref: item.target.Ref, Revision: contentRevision(item.data), Size: int64(len(item.data)),
			ModifiedAt: time.Now().UTC().Format(time.RFC3339Nano), EOL: detectEOL([]byte(item.content)), HasBOM: item.hasBOM,
		}
		if info != nil {
			update.ModifiedAt = info.ModTime().UTC().Format(time.RFC3339Nano)
		}
		if item.overlay {
			update.Content = item.content
		}
		response.Updated = append(response.Updated, update)
		changes = append(changes, Change{Op: "write", Ref: item.target.Ref})
	}
	s.index.ApplyChanges(workspaceID, changes)
	return response, nil
}

func compileTextSearch(request TextSearchRequest) (compiledTextSearch, error) {
	if request.Query == "" || strings.ContainsAny(request.Query, "\r\n") {
		return compiledTextSearch{}, &Error{Code: "invalid_search", Message: "search query must be a non-empty single line", Cause: ErrInvalidPath}
	}
	if strings.ContainsAny(request.Replacement, "\r\n") {
		return compiledTextSearch{}, &Error{Code: "invalid_search", Message: "replacement must be a single line", Cause: ErrInvalidPath}
	}
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
		return compiledTextSearch{}, &Error{Code: "invalid_search", Message: fmt.Sprintf("query is not a valid regular expression: %v", err), Cause: ErrInvalidPath}
	}
	include, err := compileTextGlobs(request.Include)
	if err != nil {
		return compiledTextSearch{}, err
	}
	exclude, err := compileTextGlobs(request.Exclude)
	if err != nil {
		return compiledTextSearch{}, err
	}
	return compiledTextSearch{matcher: matcher, include: include, exclude: exclude}, nil
}

func compileTextGlobs(values []string) ([]*regexp.Regexp, error) {
	patterns := make([]string, 0, len(values))
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			if item = strings.TrimSpace(strings.ReplaceAll(item, "\\", "/")); item != "" {
				patterns = append(patterns, strings.TrimPrefix(item, "./"))
			}
		}
	}
	result := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		expression := strings.Builder{}
		expression.WriteString(`^`)
		if !strings.Contains(pattern, "/") {
			expression.WriteString(`(?:.*/)?`)
		}
		for index := 0; index < len(pattern); index++ {
			character := pattern[index]
			switch character {
			case '*':
				if index+1 < len(pattern) && pattern[index+1] == '*' {
					index++
					if index+1 < len(pattern) && pattern[index+1] == '/' {
						index++
						expression.WriteString(`(?:.*/)?`)
					} else {
						expression.WriteString(`.*`)
					}
				} else {
					expression.WriteString(`[^/]*`)
				}
			case '?':
				expression.WriteString(`[^/]`)
			default:
				expression.WriteString(regexp.QuoteMeta(string(character)))
			}
		}
		expression.WriteString(`$`)
		compiled, err := regexp.Compile(expression.String())
		if err != nil {
			return nil, &Error{Code: "invalid_search", Message: fmt.Sprintf("invalid file glob %q", pattern), Cause: err}
		}
		result = append(result, compiled)
	}
	return result, nil
}

func (search compiledTextSearch) matchesPath(relative, reference string) bool {
	relative = filepath.ToSlash(relative)
	reference = filepath.ToSlash(reference)
	matchAny := func(patterns []*regexp.Regexp) bool {
		for _, pattern := range patterns {
			if pattern.MatchString(relative) || pattern.MatchString(reference) {
				return true
			}
		}
		return false
	}
	if len(search.include) > 0 && !matchAny(search.include) {
		return false
	}
	return !matchAny(search.exclude)
}

func textSearchOverlayMap(overlays []TextSearchOverlay) map[string]TextSearchOverlay {
	result := make(map[string]TextSearchOverlay, len(overlays))
	for _, overlay := range overlays {
		result[textSearchRefKey(overlay.Ref)] = overlay
	}
	return result
}

func textSearchRefKey(ref FileRef) string { return ref.RootID + "\x00" + filepath.ToSlash(ref.Path) }

func (s *Service) textSearchContent(workspaceID string, ref FileRef, overlays map[string]TextSearchOverlay) (string, string, bool, error) {
	if overlay, ok := overlays[textSearchRefKey(ref)]; ok {
		if !utf8.ValidString(overlay.Content) || int64(len(overlay.Content)) > MaxEditableBytes {
			return "", "", false, ErrUnsupportedFile
		}
		return overlay.Content, overlay.Revision, true, nil
	}
	snapshot, err := s.Read(workspaceID, ref)
	if err != nil {
		return "", "", false, err
	}
	return snapshot.Content, snapshot.Revision, false, nil
}

func findTextMatches(content, contentToken string, matcher *regexp.Regexp, replacement string, limit int) []locatedTextMatch {
	if limit <= 0 {
		return nil
	}
	result := make([]locatedTextMatch, 0)
	lineNumber := 1
	for lineStart := 0; lineStart <= len(content) && len(result) < limit; lineNumber++ {
		lineEnd := strings.IndexByte(content[lineStart:], '\n')
		nextStart := len(content) + 1
		if lineEnd < 0 {
			lineEnd = len(content)
		} else {
			lineEnd += lineStart
			nextStart = lineEnd + 1
		}
		textEnd := lineEnd
		if textEnd > lineStart && content[textEnd-1] == '\r' {
			textEnd--
		}
		line := content[lineStart:textEnd]
		for _, indices := range matcher.FindAllStringSubmatchIndex(line, -1) {
			if indices[0] == indices[1] {
				continue
			}
			absolute := append([]int(nil), indices...)
			for index, value := range absolute {
				if value >= 0 {
					absolute[index] = value + lineStart
				}
			}
			start, end := absolute[0], absolute[1]
			preview, previewStart, previewEnd := textMatchPreview(line, indices[0], indices[1])
			matched := line[indices[0]:indices[1]]
			if len(matched) > 512 {
				matched = matched[:512] + "…"
			}
			expanded := string(matcher.ExpandString(nil, normalizeReplacement(replacement), line, indices))
			result = append(result, locatedTextMatch{
				public: TextSearchMatch{
					ID: textMatchID(contentToken, start, end), Line: lineNumber,
					Column: utf16Length(line[:indices[0]]) + 1, EndLine: lineNumber,
					EndColumn: utf16Length(line[:indices[1]]) + 1, Preview: preview,
					PreviewMatchStart: previewStart, PreviewMatchEnd: previewEnd,
					Match: matched, ReplacementPreview: expanded,
				},
				start: start, end: end, submatches: absolute, replacement: expanded,
			})
			if len(result) >= limit {
				break
			}
		}
		if nextStart > len(content) {
			break
		}
		lineStart = nextStart
	}
	return result
}

func textMatchID(contentToken string, start, end int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", contentToken, start, end)))
	return hex.EncodeToString(digest[:12])
}

func normalizeReplacement(value string) string {
	var result strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] == '$' && index+1 < len(value) {
			switch value[index+1] {
			case '$':
				result.WriteString("$$")
				index++
				continue
			case '&':
				result.WriteString("${0}")
				index++
				continue
			}
		}
		result.WriteByte(value[index])
	}
	return result.String()
}

func textMatchPreview(line string, matchStart, matchEnd int) (string, int, int) {
	const maximumPreviewBytes = 1200
	windowStart, windowEnd := 0, len(line)
	if len(line) > maximumPreviewBytes {
		windowStart = matchStart - 400
		if windowStart < 0 {
			windowStart = 0
		}
		for windowStart > 0 && !utf8.RuneStart(line[windowStart]) {
			windowStart--
		}
		windowEnd = windowStart + maximumPreviewBytes
		if windowEnd < matchEnd {
			windowEnd = min(len(line), matchEnd+400)
			windowStart = max(0, windowEnd-maximumPreviewBytes)
			if windowStart > matchStart {
				windowStart = matchStart
			}
			for windowStart > 0 && !utf8.RuneStart(line[windowStart]) {
				windowStart--
			}
		}
		if windowEnd > len(line) {
			windowEnd = len(line)
		}
		for windowEnd < len(line) && !utf8.RuneStart(line[windowEnd]) {
			windowEnd++
		}
	}
	prefix, suffix := "", ""
	if windowStart > 0 {
		prefix = "…"
	}
	if windowEnd < len(line) {
		suffix = "…"
	}
	displayMatchEnd := min(matchEnd, windowEnd)
	preview := prefix + line[windowStart:windowEnd] + suffix
	start := utf16Length(prefix + line[windowStart:matchStart])
	end := utf16Length(prefix + line[windowStart:displayMatchEnd])
	return preview, start, end
}

func utf16Length(value string) int {
	length := 0
	for _, character := range value {
		length += utf16.RuneLen(character)
	}
	return length
}

func applyTextReplacements(content string, matches []locatedTextMatch) string {
	sort.Slice(matches, func(left, right int) bool { return matches[left].start > matches[right].start })
	result := []byte(content)
	for _, match := range matches {
		updated := make([]byte, 0, len(result)-(match.end-match.start)+len(match.replacement))
		updated = append(updated, result[:match.start]...)
		updated = append(updated, match.replacement...)
		updated = append(updated, result[match.end:]...)
		result = updated
	}
	return string(result)
}

func searchConflict(message string) error {
	return &Error{Code: "search_conflict", Message: message, Cause: ErrConflict}
}
