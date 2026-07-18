package tools

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "save_image",
			Description: "Save an image to the workspace. Use this after generating images with comfyui_generate to persist them to disk.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"imageId", "path"},
				"properties": map[string]any{
					"imageId": map[string]any{
						"type":        "string",
						"description": "ImageID from a previous tool result (e.g., comfyui_generate output).",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Labeled workspace file path to save the image to. " + labeledPathSchemaHint,
					},
					"overwrite": map[string]any{
						"type":        "boolean",
						"description": "Whether to replace an existing file. Defaults to false.",
					},
				},
			},
		},
		Run: saveImage,
	})
}

type saveImageArgs struct {
	ImageID   string `json:"imageId"`
	Path      string `json:"path"`
	Overwrite bool   `json:"overwrite"`
}

type saveImageOutput struct {
	Path         string `json:"path"`
	BytesWritten int64  `json:"bytesWritten"`
	Overwritten  bool   `json:"overwritten"`
}

func saveImage(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}

	var args saveImageArgs
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}

	args.ImageID = strings.TrimSpace(args.ImageID)
	if args.ImageID == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "imageId is required"}
	}

	args.Path = strings.TrimSpace(args.Path)
	if args.Path == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "path is required"}
	}

	// Look up the image in GeneratedImages
	attachedImage, ok := ctx.GeneratedImages[args.ImageID]
	if !ok || attachedImage.DataURL == "" {
		keys := make([]string, 0, len(ctx.GeneratedImages))
		for k := range ctx.GeneratedImages {
			keys = append(keys, k)
		}
		return nil, SafeError{Code: "image_not_found", Message: fmt.Sprintf("image with ID %q was not found. Available image IDs: %v", args.ImageID, keys)}
	}

	// Decode base64 from DataURL to raw bytes
	dataURL := strings.TrimSpace(attachedImage.DataURL)
	if !strings.HasPrefix(strings.ToLower(dataURL), "data:") {
		return nil, SafeError{Code: "invalid_image_data", Message: "image data URL is malformed"}
	}
	comma := strings.Index(dataURL, ",")
	if comma < 0 {
		return nil, SafeError{Code: "invalid_image_data", Message: "image data URL is missing base64 content"}
	}
	payload := dataURL[comma+1:]
	imageData, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, SafeError{Code: "decode_image_failed", Message: fmt.Sprintf("failed to decode image data: %v", err)}
	}
	if len(imageData) == 0 {
		return nil, SafeError{Code: "invalid_image_data", Message: "image data is empty"}
	}

	// Resolve workspace path
	path, err := resolveWorkspaceChildPath(ctx, args.Path)
	if err != nil {
		return nil, err
	}

	before, err := snapshotExistingFile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("snapshot file before save: %w", err)
	}

	overwritten := false
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, SafeError{Code: "not_file", Message: "path exists and is not a regular file"}
		}
		if !args.Overwrite {
			return nil, SafeError{Code: "file_exists", Message: "file already exists"}
		}
		overwritten = true
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat file: %w", err)
	}

	if err := ctx.context().Err(); err != nil {
		return nil, err
	}

	flag := os.O_WRONLY | os.O_CREATE
	if args.Overwrite {
		flag |= os.O_TRUNC
	} else {
		flag |= os.O_EXCL
	}
	file, err := os.OpenFile(path, flag, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, SafeError{Code: "file_exists", Message: "file already exists"}
		}
		return nil, fmt.Errorf("create file: %w", err)
	}
	written, err := file.Write(imageData)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write file: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close file: %w", err)
	}

	after, err := snapshotExistingFile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("snapshot file after save: %w", err)
	}
	ctx.recordFileChanges(fileChangeForPath(ctx, path, before, after))

	return saveImageOutput{
		Path:         relativeWorkspacePath(ctx, path),
		BytesWritten: int64(written),
		Overwritten:  overwritten,
	}, nil
}
