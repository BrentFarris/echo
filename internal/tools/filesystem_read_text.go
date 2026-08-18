package tools

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	defaultReadTextMaxBytes     = 64 * 1024
	defaultReadTextLineCount    = 200
	defaultReadTextContextLines = 80
	maxReadTextLineCount        = 1000
	maxReadTextContextLines     = 499
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "filesystem_read_text",
			Description: "Read a UTF-8 or plain text file inside the active workspace for investigation. For source files, prefer aroundLine with a line number from search results; omit line options only when the whole file is needed.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"path"},
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Labeled workspace file path to read. " + labeledPathSchemaHint,
					},
					"maxBytes": map[string]any{
						"type":        "integer",
						"description": "Maximum bytes to read. Defaults to 65536 and is capped at 262144.",
						"minimum":     1,
						"maximum":     maxTextFileBytes,
					},
					"aroundLine": map[string]any{
						"type":        "integer",
						"description": "Optional 1-based line to read around. Easiest follow-up after filesystem_search_text or filesystem_search_workspace: copy the match line number here.",
						"minimum":     1,
					},
					"contextLines": map[string]any{
						"type":        "integer",
						"description": "Optional lines to include before and after aroundLine. Defaults to 80 and is capped at 499.",
						"minimum":     0,
						"maximum":     maxReadTextContextLines,
					},
					"startLine": map[string]any{
						"type":        "integer",
						"description": "Optional 1-based first line to read. Use aroundLine instead unless you already know the exact range start.",
						"minimum":     1,
					},
					"lineCount": map[string]any{
						"type":        "integer",
						"description": "Optional number of lines to read from startLine, or total centered lines when aroundLine is set. Defaults to 200 with startLine and is capped at 1000.",
						"minimum":     1,
						"maximum":     maxReadTextLineCount,
					},
				},
			},
		},
		Run: readTextFile,
	})
}

type readTextFileArgs struct {
	Path         string `json:"path"`
	MaxBytes     int64  `json:"maxBytes"`
	AroundLine   int    `json:"aroundLine"`
	ContextLines *int   `json:"contextLines"`
	StartLine    int    `json:"startLine"`
	LineCount    int    `json:"lineCount"`
}

type readTextFileOutput struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	BytesRead int64  `json:"bytesRead"`
	Truncated bool   `json:"truncated"`
	StartLine int    `json:"startLine,omitempty"`
	EndLine   int    `json:"endLine,omitempty"`
}

func readTextFile(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args readTextFileArgs
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	if args.Path == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "path is required"}
	}
	limit := args.MaxBytes
	if limit <= 0 {
		limit = defaultReadTextMaxBytes
	}
	if limit > maxTextFileBytes {
		limit = maxTextFileBytes
	}
	if args.StartLine < 0 {
		return nil, SafeError{Code: "invalid_arguments", Message: "startLine must be 1 or greater"}
	}
	if args.LineCount < 0 {
		return nil, SafeError{Code: "invalid_arguments", Message: "lineCount must be 1 or greater"}
	}
	if args.AroundLine < 0 {
		return nil, SafeError{Code: "invalid_arguments", Message: "aroundLine must be 1 or greater"}
	}
	if args.ContextLines != nil && *args.ContextLines < 0 {
		return nil, SafeError{Code: "invalid_arguments", Message: "contextLines must be 0 or greater"}
	}

	path, err := resolveWorkspacePath(ctx, args.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, SafeError{Code: "path_not_found", Message: fmt.Sprintf("file %s was not found", relativeWorkspacePath(ctx, path))}
	}
	if !info.Mode().IsRegular() {
		return nil, SafeError{Code: "not_file", Message: "path is not a regular file"}
	}

	if args.AroundLine > 0 {
		return readTextFileAroundLine(ctx, path, args.AroundLine, args.ContextLines, args.LineCount, limit)
	}
	if args.StartLine > 0 || args.LineCount > 0 {
		return readTextFileBlock(ctx, path, args.StartLine, args.LineCount, limit)
	}
	return readTextFilePrefix(ctx, path, info, limit)
}

func readTextFilePrefix(ctx ExecutionContext, path string, info os.FileInfo, limit int64) (readTextFileOutput, error) {
	file, err := os.Open(path)
	if err != nil {
		return readTextFileOutput{}, fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	buffer := make([]byte, limit+1)
	read, err := file.Read(buffer)
	if err != nil && read == 0 {
		return readTextFileOutput{}, fmt.Errorf("read file: %w", err)
	}
	data := buffer[:read]
	truncated := int64(read) > limit
	if truncated {
		data = data[:limit]
	}
	if !isTextLike(data) {
		return readTextFileOutput{}, SafeError{Code: "binary_file", Message: "file appears to be binary"}
	}
	if err := ctx.context().Err(); err != nil {
		return readTextFileOutput{}, err
	}
	return readTextFileOutput{
		Path:      relativeWorkspacePath(ctx, path),
		Content:   string(data),
		BytesRead: int64(len(data)),
		Truncated: truncated || info.Size() > int64(len(data)),
	}, nil
}

func readTextFileAroundLine(ctx ExecutionContext, path string, aroundLine int, contextLines *int, lineCount int, limit int64) (readTextFileOutput, error) {
	if lineCount > 0 && contextLines == nil {
		if lineCount > maxReadTextLineCount {
			lineCount = maxReadTextLineCount
		}
		startLine := maxInt(1, aroundLine-lineCount/2)
		return readTextFileBlock(ctx, path, startLine, lineCount, limit)
	}

	resolvedContextLines := defaultReadTextContextLines
	if contextLines != nil {
		resolvedContextLines = *contextLines
	}
	if resolvedContextLines > maxReadTextContextLines {
		resolvedContextLines = maxReadTextContextLines
	}
	startLine := maxInt(1, aroundLine-resolvedContextLines)
	lineCount = resolvedContextLines*2 + 1
	return readTextFileBlock(ctx, path, startLine, lineCount, limit)
}

func readTextFileBlock(ctx ExecutionContext, path string, startLine int, lineCount int, limit int64) (readTextFileOutput, error) {
	if startLine <= 0 {
		startLine = 1
	}
	lineCountCapped := false
	if lineCount <= 0 {
		lineCount = defaultReadTextLineCount
	}
	if lineCount > maxReadTextLineCount {
		lineCount = maxReadTextLineCount
		lineCountCapped = true
	}

	file, err := os.Open(path)
	if err != nil {
		return readTextFileOutput{}, fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	var content strings.Builder
	currentLine := 1
	endLine := startLine + lineCount - 1
	lastReturnedLine := 0
	truncated := lineCountCapped || startLine > 1

	for {
		if err := ctx.context().Err(); err != nil {
			return readTextFileOutput{}, err
		}

		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			if !isTextLike([]byte(line)) {
				return readTextFileOutput{}, SafeError{Code: "binary_file", Message: "file appears to be binary"}
			}
			if currentLine >= startLine && currentLine <= endLine {
				remaining := limit - int64(content.Len())
				if remaining <= 0 {
					truncated = true
					break
				}
				if int64(len(line)) > remaining {
					content.WriteString(line[:remaining])
					lastReturnedLine = currentLine
					truncated = true
					break
				}
				content.WriteString(line)
				lastReturnedLine = currentLine
			}
			currentLine++
			if currentLine > endLine {
				hasMore, peekErr := readTextReaderHasMore(reader)
				if peekErr != nil {
					return readTextFileOutput{}, peekErr
				}
				truncated = truncated || hasMore
				break
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return readTextFileOutput{}, fmt.Errorf("read file: %w", err)
		}
	}

	if err := ctx.context().Err(); err != nil {
		return readTextFileOutput{}, err
	}
	return readTextFileOutput{
		Path:      relativeWorkspacePath(ctx, path),
		Content:   content.String(),
		BytesRead: int64(content.Len()),
		Truncated: truncated,
		StartLine: startLine,
		EndLine:   lastReturnedLine,
	}, nil
}

func readTextReaderHasMore(reader *bufio.Reader) (bool, error) {
	if _, err := reader.Peek(1); err != nil {
		if err == io.EOF {
			return false, nil
		}
		return false, fmt.Errorf("read file: %w", err)
	}
	return true, nil
}
