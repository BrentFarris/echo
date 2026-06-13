package tools

import (
	"encoding/json"
	"fmt"
	"os"
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "filesystem_read_text",
			Description: "Read a UTF-8 or plain text file inside the active workspace for investigation.",
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
				},
			},
		},
		Run: readTextFile,
	})
}

type readTextFileArgs struct {
	Path     string `json:"path"`
	MaxBytes int64  `json:"maxBytes"`
}

type readTextFileOutput struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	BytesRead int64  `json:"bytesRead"`
	Truncated bool   `json:"truncated"`
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
		limit = 64 * 1024
	}
	if limit > maxTextFileBytes {
		limit = maxTextFileBytes
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

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	buffer := make([]byte, limit+1)
	read, err := file.Read(buffer)
	if err != nil && read == 0 {
		return nil, fmt.Errorf("read file: %w", err)
	}
	data := buffer[:read]
	truncated := int64(read) > limit
	if truncated {
		data = data[:limit]
	}
	if !isTextLike(data) {
		return nil, SafeError{Code: "binary_file", Message: "file appears to be binary"}
	}
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	return readTextFileOutput{
		Path:      relativeWorkspacePath(ctx, path),
		Content:   string(data),
		BytesRead: int64(len(data)),
		Truncated: truncated || info.Size() > int64(len(data)),
	}, nil
}
