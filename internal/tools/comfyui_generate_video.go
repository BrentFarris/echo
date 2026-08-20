package tools

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/brent/echo/internal/comfyui"
	"github.com/google/uuid"
)

const maxComfyuiVideoInputImageBytes = 100 * 1024 * 1024 // 100MB limit for image upload in video generation

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "comfyui_generate_video",
			Description: "Generate a video using a ComfyUI server. If no workflow is specified, uses the default video workflow configured in settings. Supports custom workflows via workflowPath (workspace JSON file) or workflowJSON (inline), which override the configured default. Template variables such as {{PROMPT}}, {{FRAMES}}, {{FPS}}, {{FORMAT}}, {{DURATION}}, {{ASPECT_RATIO}}, {{MEGAPIXELS}}, etc. are substituted in the workflow before sending.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"prompt"},
				"properties": map[string]any{
					"prompt": map[string]any{
						"type":        "string",
						"description": "Positive text prompt.",
					},
					"negativePrompt": map[string]any{
						"type":        "string",
						"description": "Negative prompt. Defaults to empty string.",
					},
					"workflowPath": map[string]any{
						"type":        "string",
						"description": "Workspace-relative path to a .json ComfyUI workflow file. Overrides the configured default video workflow; omit it to use the default. Template variables in the file are substituted before sending.",
					},
					"workflowJSON": map[string]any{
						"type":        "string",
						"description": "Inline ComfyUI workflow JSON string. Takes precedence over workflowPath and overrides the configured default video workflow; omit it to use the default. Template variables are substituted before sending.",
					},
					"model": map[string]any{
						"type":        "string",
						"description": "Checkpoint/model name, substituted as {{MODEL}}.",
					},
					"width": map[string]any{
						"type":        "integer",
						"description": "Video width. Defaults to 512, substituted as {{WIDTH}}.",
						"minimum":     16,
					},
					"height": map[string]any{
						"type":        "integer",
						"description": "Video height. Defaults to 512, substituted as {{HEIGHT}}.",
						"minimum":     16,
					},
					"steps": map[string]any{
						"type":        "integer",
						"description": "Sampling steps. Defaults to 20, substituted as {{STEPS}}.",
						"minimum":     1,
					},
					"cfgScale": map[string]any{
						"type":        "number",
						"description": "CFG scale. Defaults to 7.5, substituted as {{CFG_SCALE}}.",
						"minimum":     0,
					},
					"seed": map[string]any{
						"type":        "integer",
						"description": "Random seed. Use -1 for random. Substituted as {{SEED}}.",
					},
					"frames": map[string]any{
						"type":        "integer",
						"description": "Number of frames. Defaults to 16, substituted as {{FRAMES}}.",
						"minimum":     1,
					},
					"fps": map[string]any{
						"type":        "number",
						"description": "Frames per second. Defaults to 8.0, substituted as {{FPS}}.",
						"minimum":     0.1,
					},
					"format": map[string]any{
						"type":        "string",
						"description": "Output format (\"mp4\" or \"gif\"). Defaults to \"mp4\", substituted as {{FORMAT}}.",
					},
					"duration": map[string]any{
						"type":        "number",
						"description": "Video duration in seconds for duration-driven workflows (e.g., MiniMax H3). Defaults to 5, substituted as {{DURATION}}. Prefer this over frames/fps when the workflow resolves frame count from duration.",
						"minimum":     0.1,
					},
					"aspectRatio": map[string]any{
						"type":        "string",
						"description": "ResolutionSelector aspect ratio label for resolution-driven workflows (e.g., \"3:4 (Portrait Standard)\"). Defaults to \"16:9 (Widescreen)\", substituted as {{ASPECT_RATIO}}.",
					},
					"megapixels": map[string]any{
						"type":        "number",
						"description": "ResolutionSelector target megapixels for resolution-driven workflows. Defaults to 0.4, substituted as {{MEGAPIXELS}}.",
						"minimum":     0.1,
						"maximum":     10,
					},
					"imagePath": map[string]any{
						"type":        "string",
						"description": "Workspace-relative path to an input image for image-to-video workflows. Uploaded to ComfyUI before generation.",
					},
					"attachedImageIndex": map[string]any{
						"type":        "integer",
						"description": "0-based index into chat-attached images for image-to-video workflows. Image data is uploaded directly from memory — no workspace disk I/O.",
						"minimum":     0,
					},
				},
			},
		},
		Run: comfyuiGenerateVideo,
	})
}

type comfyuiVideoArgs struct {
	Prompt             string  `json:"prompt"`
	NegativePrompt     string  `json:"negativePrompt"`
	WorkflowPath       string  `json:"workflowPath"`
	WorkflowJSON       string  `json:"workflowJSON"`
	Model              string  `json:"model"`
	Width              int     `json:"width"`
	Height             int     `json:"height"`
	Steps              int     `json:"steps"`
	CfgScale           float64 `json:"cfgScale"`
	Seed               int64   `json:"seed"`
	Frames             int     `json:"frames"`
	FPS                float64 `json:"fps"`
	Format             string  `json:"format"`
	Duration           float64 `json:"duration"`
	AspectRatio        string  `json:"aspectRatio"`
	Megapixels         float64 `json:"megapixels"`
	ImagePath          string  `json:"imagePath"`
	AttachedImageIndex *int    `json:"attachedImageIndex,omitempty"`
}

type comfyuiVideoOutput struct {
	PromptID      string   `json:"promptId"`
	OutputVideos  []string `json:"outputVideos,omitempty"`
	StatusMessage string   `json:"statusMessage"`
	// Unique identifier for the generated video, usable by save_video tool.
	VideoId string `json:"videoId,omitempty"`
	// Image/video metadata for inline rendering (not serialized to JSON except
	// the exported fields above).
	Name      string
	MediaType string
	Bytes     int64
	dataURL   string
}

func (o comfyuiVideoOutput) LLMVideoContent() (LLMVideoContent, bool) {
	if o.dataURL == "" {
		return LLMVideoContent{}, false
	}
	return LLMVideoContent{
		Path:      "comfyui_generated_video",
		Name:      o.Name,
		MediaType: o.MediaType,
		Bytes:     o.Bytes,
		DataURL:   o.dataURL,
	}, true
}

func (o comfyuiVideoOutput) VideoID() string {
	return o.VideoId
}

func comfyuiGenerateVideo(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}

	var args comfyuiVideoArgs
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}

	args.Prompt = strings.TrimSpace(args.Prompt)
	if args.Prompt == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "prompt is required"}
	}

	baseURL := strings.TrimSpace(ctx.ComfyuiURL)
	if baseURL == "" {
		return nil, SafeError{Code: "missing_comfyui_url", Message: "ComfyUI URL is not configured. Set ComfyuiURL in settings."}
	}

	// Cascade model resolution: explicit arg > context default checkpoint > hardcoded fallback
	modelName := strings.TrimSpace(args.Model)
	if modelName == "" {
		modelName = strings.TrimSpace(ctx.ComfyuiDefaultCheckpoint)
	}

	params := comfyui.TemplateParams{
		Prompt:         args.Prompt,
		NegativePrompt: args.NegativePrompt,
		Model:          modelName,
		Width:          args.Width,
		Height:         args.Height,
		Steps:          args.Steps,
		CfgScale:       args.CfgScale,
		Seed:           args.Seed,
		Frames:         args.Frames,
		FPS:            args.FPS,
		Format:         args.Format,
		Duration:       args.Duration,
		AspectRatio:    args.AspectRatio,
		Megapixels:     args.Megapixels,
	}

	var workflow map[string]any
	var loadErr error

	if args.WorkflowJSON != "" {
		// Inline JSON takes precedence.
		workflow, loadErr = comfyui.ParseWorkflowJSON([]byte(args.WorkflowJSON))
		if loadErr != nil {
			return nil, SafeError{Code: "invalid_workflow_json", Message: fmt.Sprintf("workflowJSON is invalid: %v", loadErr)}
		}
	} else if args.WorkflowPath != "" {
		// Load from workspace file.
		resolvedPath, err := resolveWorkspaceChildPath(ctx, args.WorkflowPath)
		if err != nil {
			return nil, err
		}
		workflow, loadErr = comfyui.LoadWorkflowJSON(resolvedPath)
		if loadErr != nil {
			return nil, SafeError{Code: "load_workflow_failed", Message: fmt.Sprintf("failed to load workflow: %v", loadErr)}
		}
	} else {
		// Fall back to the dedicated video workflow setting.
		defaultWorkflowPath := strings.TrimSpace(ctx.ComfyuiVideoWorkflow)
		if defaultWorkflowPath != "" {
			workflow, loadErr = comfyui.LoadWorkflowJSON(defaultWorkflowPath)
			if loadErr != nil {
				return nil, SafeError{Code: "load_workflow_failed", Message: fmt.Sprintf("failed to load default video workflow %q: %v", defaultWorkflowPath, loadErr)}
			}
		} else {
			return nil, SafeError{Code: "missing_video_workflow", Message: "no video workflow configured. Provide a workflowPath or workflowJSON parameter."}
		}
	}

	client := &comfyui.Client{
		BaseURL: baseURL,
	}

	// Resolution priority: explicit imagePath > attachedImageIndex with attached images available.
	if args.ImagePath != "" {
		resolvedImagePath, err := resolveWorkspaceChildPath(ctx, args.ImagePath)
		if err != nil {
			return nil, err
		}
		info, statErr := os.Stat(resolvedImagePath)
		if statErr != nil {
			return nil, SafeError{Code: "path_not_found", Message: "input image file was not found"}
		}
		if !info.Mode().IsRegular() {
			return nil, SafeError{Code: "not_file", Message: "imagePath is not a regular file"}
		}
		if info.Size() > maxComfyuiVideoInputImageBytes {
			return nil, SafeError{Code: "file_too_large", Message: fmt.Sprintf("input image is larger than the %d byte limit", maxComfyuiVideoInputImageBytes)}
		}
		imageData, readErr := os.ReadFile(resolvedImagePath)
		if readErr != nil {
			return nil, SafeError{Code: "read_image_failed", Message: fmt.Sprintf("failed to read input image: %v", readErr)}
		}
		serverFilename := "echo_input_" + uuid.New().String() + filepath.Ext(resolvedImagePath)
		uploadedName, uploadErr := client.UploadImage(ctx.context(), serverFilename, imageData)
		if uploadErr != nil {
			return nil, SafeError{Code: "upload_image_failed", Message: fmt.Sprintf("failed to upload image to ComfyUI: %v", uploadErr)}
		}
		params.Image = uploadedName
	} else if args.AttachedImageIndex != nil && len(ctx.AttachedImages) > 0 {
		index := *args.AttachedImageIndex
		if index < 0 || index >= len(ctx.AttachedImages) {
			return nil, SafeError{Code: "invalid_index", Message: fmt.Sprintf("attachedImageIndex %d is out of range; available images: %d", index, len(ctx.AttachedImages))}
		}
		attached := ctx.AttachedImages[index]
		imageData, decodeErr := decodeAttachedImageData(attached.DataURL)
		if decodeErr != nil {
			return nil, SafeError{Code: "decode_image_failed", Message: fmt.Sprintf("failed to decode attached image: %v", decodeErr)}
		}
		ext := attachedImageExtension(attached.MediaType)
		serverFilename := "echo_input_" + uuid.New().String() + ext
		uploadedName, uploadErr := client.UploadImage(ctx.context(), serverFilename, imageData)
		if uploadErr != nil {
			return nil, SafeError{Code: "upload_image_failed", Message: fmt.Sprintf("failed to upload attached image to ComfyUI: %v", uploadErr)}
		}
		params.Image = uploadedName
	}

	result, err := client.Generate(ctx.context(), params, workflow)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "execution status is error") || strings.Contains(msg, "execution_error") {
			if modelName == "" {
				msg += "; no checkpoint model was specified — set ComfyuiDefaultCheckpoint in settings or provide a model argument"
			}
		}
		return nil, SafeError{Code: "comfyui_error", Message: msg}
	}

	output := comfyuiVideoOutput{
		PromptID:      result.PromptID,
		OutputVideos:  result.OutputVideos,
		StatusMessage: result.StatusMessage,
	}

	// Fetch the first generated video for inline display
	if len(result.OutputVideos) > 0 {
		vidPath := result.OutputVideos[0]
		parts := strings.SplitN(vidPath, "/", 2)
		subfolder := ""
		filename := parts[0]
		if len(parts) == 2 {
			subfolder = parts[0]
			filename = parts[1]
		}

		// Use the storage type ComfyUI reported ("output" or "custom"); /view
		// only finds files under the matching directory. Fall back by extension.
		videoType := result.MediaTypes[vidPath]
		if videoType == "" {
			if strings.EqualFold(filepath.Ext(filename), ".gif") {
				videoType = "custom"
			} else {
				videoType = "output"
			}
		}

		videoData, fetchErr := client.FetchVideoBytes(ctx.context(), filename, subfolder, videoType)
		if fetchErr != nil {
			return nil, SafeError{Code: "comfyui_error", Message: fmt.Sprintf("failed to fetch generated video: %v", fetchErr)}
		}

		mediaType := detectComfyuiVideoMediaType(filename, videoData)

		output.Name = filename
		output.MediaType = mediaType
		output.Bytes = int64(len(videoData))
		output.dataURL = fmt.Sprintf("data:%s;base64,%s", mediaType, base64.StdEncoding.EncodeToString(videoData))
		output.VideoId = uuid.New().String()
	}

	return output, nil
}

// detectComfyuiVideoMediaType detects the video media type from filename extension and bytes.
// Named differently to avoid conflict with filesystem_read_video.go's detectVideoMediaType.
func detectComfyuiVideoMediaType(filename string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".mp4":
		return "video/mp4"
	case ".gif":
		return "image/gif"
	case ".webm":
		return "video/webm"
	}

	// Fall back to byte detection
	if len(data) >= 8 {
		switch {
		case data[0] == 'G' && data[1] == 'I' && data[2] == 'F':
			return "image/gif"
		case bytesEqual(data[4:8], []byte("ftyp")) || bytesEqual(data[0:4], []byte{0x1a, 0x45, 0xdf, 0xa3}):
			return "video/mp4"
		}
	}

	return "video/mp4" // default fallback
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
