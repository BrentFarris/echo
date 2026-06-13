package tools

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxImageFileBytes = 10 * 1024 * 1024

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "filesystem_read_image",
			Description: "Read a PNG, JPEG, WEBP, or GIF image inside the active workspace and make it available to the model as an OpenAI-compatible image_url input.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"path"},
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Labeled workspace image file path to read. " + labeledPathSchemaHint,
					},
					"detail": map[string]any{
						"type":        "string",
						"description": "Optional OpenAI image detail hint.",
						"enum":        []any{"auto", "low", "high"},
					},
				},
			},
		},
		Run: readImageFile,
	})
}

type LLMImageContent struct {
	Path      string
	Name      string
	MediaType string
	Bytes     int64
	DataURL   string
	Detail    string
}

type LLMImageContentProvider interface {
	LLMImageContent() (LLMImageContent, bool)
}

type readImageFileArgs struct {
	Path   string `json:"path"`
	Detail string `json:"detail"`
}

type readImageFileOutput struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	MediaType   string `json:"mediaType"`
	Bytes       int64  `json:"bytes"`
	ContentType string `json:"contentType"`
	Detail      string `json:"detail,omitempty"`
	dataURL     string
}

func (o readImageFileOutput) LLMImageContent() (LLMImageContent, bool) {
	if o.dataURL == "" {
		return LLMImageContent{}, false
	}
	return LLMImageContent{
		Path:      o.Path,
		Name:      o.Name,
		MediaType: o.MediaType,
		Bytes:     o.Bytes,
		DataURL:   o.dataURL,
		Detail:    o.Detail,
	}, true
}

func readImageFile(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args readImageFileArgs
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
		return nil, SafeError{Code: "path_not_found", Message: "image file was not found"}
	}
	if !info.Mode().IsRegular() {
		return nil, SafeError{Code: "not_file", Message: "path is not a regular file"}
	}
	if info.Size() > maxImageFileBytes {
		return nil, SafeError{Code: "file_too_large", Message: fmt.Sprintf("image is larger than the %d byte limit", maxImageFileBytes)}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	if len(data) > maxImageFileBytes {
		return nil, SafeError{Code: "file_too_large", Message: fmt.Sprintf("image is larger than the %d byte limit", maxImageFileBytes)}
	}
	mediaType, err := detectImageMediaType(data)
	if err != nil {
		return nil, err
	}
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}

	relative := relativeWorkspacePath(ctx, path)
	return readImageFileOutput{
		Path:        relative,
		Name:        filepath.Base(relative),
		MediaType:   mediaType,
		Bytes:       int64(len(data)),
		ContentType: "image_url",
		Detail:      args.Detail,
		dataURL:     imageDataURL(mediaType, data),
	}, nil
}

func detectImageMediaType(data []byte) (string, error) {
	if len(data) == 0 {
		return "", SafeError{Code: "empty_file", Message: "image data is empty"}
	}
	switch {
	case len(data) >= 8 &&
		data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' &&
		data[4] == '\r' && data[5] == '\n' && data[6] == 0x1a && data[7] == '\n':
		return "image/png", nil
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg", nil
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif", nil
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp", nil
	default:
		return "", SafeError{Code: "unsupported_image", Message: "unsupported image format"}
	}
}

func imageDataURL(mediaType string, data []byte) string {
	return fmt.Sprintf("data:%s;base64,%s", mediaType, base64.StdEncoding.EncodeToString(data))
}
