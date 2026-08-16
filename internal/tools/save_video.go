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
			Name:        "save_video",
			Description: "Save a generated video to the workspace. Only use this when the user explicitly asks to save or download a video; do not call it automatically after generation.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"videoId", "path"},
				"properties": map[string]any{
					"videoId": map[string]any{
						"type":        "string",
						"description": "VideoID from a previous tool result (e.g., comfyui_generate_video output).",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Labeled workspace file path to save the video to. " + labeledPathSchemaHint,
					},
					"overwrite": map[string]any{
						"type":        "boolean",
						"description": "Whether to replace an existing file. Defaults to false.",
					},
				},
			},
		},
		Run: saveVideo,
	})
}

type saveVideoArgs struct {
	VideoID   string `json:"videoId"`
	Path      string `json:"path"`
	Overwrite bool   `json:"overwrite"`
}

type saveVideoOutput struct {
	Path         string `json:"path"`
	BytesWritten int64  `json:"bytesWritten"`
	Overwritten  bool   `json:"overwritten"`
}

func saveVideo(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}

	var args saveVideoArgs
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}

	args.VideoID = strings.TrimSpace(args.VideoID)
	if args.VideoID == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "videoId is required"}
	}

	args.Path = strings.TrimSpace(args.Path)
	if args.Path == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "path is required"}
	}

	// Look up the video in GeneratedVideos
	attachedVideo, ok := ctx.GeneratedVideos[args.VideoID]
	if !ok || attachedVideo.DataURL == "" {
		keys := make([]string, 0, len(ctx.GeneratedVideos))
		for k := range ctx.GeneratedVideos {
			keys = append(keys, k)
		}
		return nil, SafeError{Code: "video_not_found", Message: fmt.Sprintf("video with ID %q was not found. Available video IDs: %v", args.VideoID, keys)}
	}

	// Decode base64 from DataURL to raw bytes
	dataURL := strings.TrimSpace(attachedVideo.DataURL)
	if !strings.HasPrefix(strings.ToLower(dataURL), "data:") {
		return nil, SafeError{Code: "invalid_video_data", Message: "video data URL is malformed"}
	}
	comma := strings.Index(dataURL, ",")
	if comma < 0 {
		return nil, SafeError{Code: "invalid_video_data", Message: "video data URL is missing base64 content"}
	}
	payload := dataURL[comma+1:]
	videoData, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, SafeError{Code: "decode_video_failed", Message: fmt.Sprintf("failed to decode video data: %v", err)}
	}
	if len(videoData) == 0 {
		return nil, SafeError{Code: "invalid_video_data", Message: "video data is empty"}
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
	written, err := file.Write(videoData)
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

	return saveVideoOutput{
		Path:         relativeWorkspacePath(ctx, path),
		BytesWritten: int64(written),
		Overwritten:  overwritten,
	}, nil
}
