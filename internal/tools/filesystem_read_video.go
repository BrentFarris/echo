package tools

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxVideoFileBytes = 50 * 1024 * 1024

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "filesystem_read_video",
			Description: "Read a video file (MP4, WebM, MOV) inside the active workspace and make it available to the model as an OpenAI-compatible video_url input.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"path"},
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Labeled workspace video file path to read. " + labeledPathSchemaHint,
					},
					"detail": map[string]any{
						"type":        "string",
						"description": "Optional OpenAI video detail hint.",
						"enum":        []any{"auto", "low", "high"},
					},
				},
			},
		},
		Run: readVideoFile,
	})
}

type LLMVideoContent struct {
	Path      string
	Name      string
	MediaType string
	Bytes     int64
	DataURL   string
	Detail    string
}

type LLMVideoContentProvider interface {
	LLMVideoContent() (LLMVideoContent, bool)
}

type readVideoFileArgs struct {
	Path   string `json:"path"`
	Detail string `json:"detail"`
}

type readVideoFileOutput struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	MediaType   string `json:"mediaType"`
	Bytes       int64  `json:"bytes"`
	ContentType string `json:"contentType"`
	Detail      string `json:"detail,omitempty"`
	dataURL     string
}

func (o readVideoFileOutput) LLMVideoContent() (LLMVideoContent, bool) {
	if o.dataURL == "" {
		return LLMVideoContent{}, false
	}
	return LLMVideoContent{
		Path:      o.Path,
		Name:      o.Name,
		MediaType: o.MediaType,
		Bytes:     o.Bytes,
		DataURL:   o.dataURL,
		Detail:    o.Detail,
	}, true
}

func readVideoFile(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args readVideoFileArgs
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	args.Path = strings.TrimSpace(args.Path)
	if args.Path == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "path is required"}
	}
	args.Detail = strings.ToLower(strings.TrimSpace(args.Detail))
	if args.Detail != "" && args.Detail != "auto" && args.Detail != "low" && args.Detail != "high" {
		return nil, SafeError{Code: "invalid_arguments", Message: "detail must be auto, low, or high"}
	}

	path, err := resolveWorkspacePath(ctx, args.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, SafeError{Code: "path_not_found", Message: "video file was not found"}
	}
	if !info.Mode().IsRegular() {
		return nil, SafeError{Code: "not_file", Message: "path is not a regular file"}
	}
	if info.Size() > maxVideoFileBytes {
		return nil, SafeError{Code: "file_too_large", Message: fmt.Sprintf("video is larger than the %d byte limit", maxVideoFileBytes)}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read video: %w", err)
	}
	if len(data) > maxVideoFileBytes {
		return nil, SafeError{Code: "file_too_large", Message: fmt.Sprintf("video is larger than the %d byte limit", maxVideoFileBytes)}
	}
	mediaType, err := detectVideoMediaType(data)
	if err != nil {
		return nil, err
	}
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}

	relative := relativeWorkspacePath(ctx, path)
	return readVideoFileOutput{
		Path:        relative,
		Name:        filepath.Base(relative),
		MediaType:   mediaType,
		Bytes:       int64(len(data)),
		ContentType: "video_url",
		Detail:      args.Detail,
		dataURL:     videoDataURL(mediaType, data),
	}, nil
}

func detectVideoMediaType(data []byte) (string, error) {
	if len(data) == 0 {
		return "", SafeError{Code: "empty_file", Message: "video data is empty"}
	}
	switch {
	// MP4 / M4V (ISO Base Media file format with ftyp brand)
	case len(data) >= 8 && string(data[4:8]) == "ftyp":
		return "video/mp4", nil
	// WebM (EBML header: 0x1A 0x45 0xDF 0xA3)
	case len(data) >= 4 && data[0] == 0x1a && data[1] == 0x45 && data[2] == 0xdf && data[3] == 0xa3:
		return "video/webm", nil
	// MOV (QuickTime: .moov or moov at offset 4)
	case len(data) >= 8 && string(data[4:8]) == "moov":
		return "video/quicktime", nil
	default:
		return "", SafeError{Code: "unsupported_video", Message: "unsupported video format"}
	}
}

func videoDataURL(mediaType string, data []byte) string {
	return fmt.Sprintf("data:%s;base64,%s", mediaType, base64.StdEncoding.EncodeToString(data))
}
