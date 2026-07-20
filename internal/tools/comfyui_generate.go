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

const defaultComfyUIBaseURL = "http://localhost:8188"

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "comfyui_generate",
			Description: "Generate an image using a ComfyUI server. Supports custom workflows via workflowPath (workspace JSON file) or workflowJSON (inline). Template variables such as {{PROMPT}}, {{SEED}}, etc. are substituted in the workflow before sending.",
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
						"description": "Workspace-relative path to a .json ComfyUI workflow file. Template variables in the file are substituted before sending. Overrides default workflow.",
					},
					"workflowJSON": map[string]any{
						"type":        "string",
						"description": "Inline ComfyUI workflow JSON string. Takes precedence over workflowPath. Template variables are substituted before sending.",
					},
					"model": map[string]any{
						"type":        "string",
						"description": "Checkpoint/model name, substituted as {{MODEL}}.",
					},
					"width": map[string]any{
						"type":        "integer",
						"description": "Image width. Defaults to 512, substituted as {{WIDTH}}.",
						"minimum":     16,
					},
					"height": map[string]any{
						"type":        "integer",
						"description": "Image height. Defaults to 512, substituted as {{HEIGHT}}.",
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
					"imagePath": map[string]any{
						"type":        "string",
						"description": "Workspace-relative path to an input image for img2img workflows. Uploaded to ComfyUI before generation.",
					},
					"attachedImageIndex": map[string]any{
						"type":        "integer",
						"description": "0-based index into chat-attached images for img2img workflows. Routes to img2img workflow. Image data is uploaded directly from memory — no workspace disk I/O.",
						"minimum":     0,
					},
				},
			},
		},
		Run: comfyuiGenerate,
	})
}

type comfyuiArgs struct {
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
	ImagePath          string  `json:"imagePath"`
	AttachedImageIndex *int    `json:"attachedImageIndex,omitempty"`
}

type comfyuiOutput struct {
	PromptID      string   `json:"promptId"`
	OutputImages  []string `json:"outputImages,omitempty"`
	StatusMessage string   `json:"statusMessage"`
	// Unique identifier for the generated image, usable by save_image tool.
	ImageID       string   `json:"imageId,omitempty"`
	// Image data for inline rendering (not serialized to JSON except imageID)
	Name        string
	MediaType   string
	Bytes       int64
	ContentType string
	dataURL     string
}

func (o comfyuiOutput) LLMImageContent() (LLMImageContent, bool) {
	if o.dataURL == "" {
		return LLMImageContent{}, false
	}
	return LLMImageContent{
		Path:      "comfyui_generated",
		Name:      o.Name,
		MediaType: o.MediaType,
		Bytes:     o.Bytes,
		DataURL:   o.dataURL,
	}, true
}

func (o comfyuiOutput) GetImageID() string {
	return o.ImageID
}

// attachedImageExtension maps a media type to a file extension for ComfyUI upload.
func attachedImageExtension(mediaType string) string {
	switch strings.ToLower(mediaType) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".png"
	}
}

// decodeAttachedImageData extracts raw bytes from a data URL.
func decodeAttachedImageData(dataURL string) ([]byte, error) {
	dataURL = strings.TrimSpace(dataURL)
	if !strings.HasPrefix(strings.ToLower(dataURL), "data:") {
		return nil, fmt.Errorf("image data must be a data URL")
	}
	comma := strings.Index(dataURL, ",")
	if comma < 0 {
		return nil, fmt.Errorf("image data URL is missing base64 content")
	}
	payload := dataURL[comma+1:]
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("decode image data: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("attached image data is empty")
	}
	return data, nil
}

func comfyuiGenerate(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}

	var args comfyuiArgs
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
		baseURL = defaultComfyUIBaseURL
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
	}

	hasInputImage := args.ImagePath != "" || (args.AttachedImageIndex != nil && len(ctx.AttachedImages) > 0)

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
		// Select default workflow based on whether an input image was supplied.
		var defaultWorkflowPath string
		if hasInputImage {
			defaultWorkflowPath = strings.TrimSpace(ctx.ComfyuiImg2imgWorkflow)
		} else {
			defaultWorkflowPath = strings.TrimSpace(ctx.ComfyuiTxt2imgWorkflow)
		}
		if defaultWorkflowPath != "" {
			workflow, loadErr = comfyui.LoadWorkflowJSON(defaultWorkflowPath)
			if loadErr != nil {
				return nil, SafeError{Code: "load_workflow_failed", Message: fmt.Sprintf("failed to load default workflow %q: %v", defaultWorkflowPath, loadErr)}
			}
		} else {
			workflow = comfyui.BuildDefaultWorkflow(params)
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
		if info.Size() > maxImageFileBytes {
			return nil, SafeError{Code: "file_too_large", Message: fmt.Sprintf("input image is larger than the %d byte limit", maxImageFileBytes)}
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
		// Enhanced error message when model might be missing.
		msg := err.Error()
		if strings.Contains(msg, "execution status is error") || strings.Contains(msg, "execution_error") {
			if modelName == "" {
				msg += "; no checkpoint model was specified — set ComfyuiDefaultCheckpoint in settings or provide a model argument"
			}
		}
		return nil, SafeError{Code: "comfyui_error", Message: msg}
	}

	output := comfyuiOutput{
		PromptID:      result.PromptID,
		OutputImages:  result.OutputImages,
		StatusMessage: result.StatusMessage,
	}

	// Fetch the first generated image for inline display
	if len(result.OutputImages) > 0 {
		imgPath := result.OutputImages[0]
		parts := strings.SplitN(imgPath, "/", 2)
		subfolder := ""
		filename := parts[0]
		if len(parts) == 2 {
			subfolder = parts[0]
			filename = parts[1]
		}

		imgData, fetchErr := client.FetchImageBytes(ctx.context(), filename, subfolder, "output")
		if fetchErr != nil {
			return nil, SafeError{Code: "comfyui_error", Message: fmt.Sprintf("failed to fetch generated image: %v", fetchErr)}
		}

		mediaType, detectErr := detectImageMediaType(imgData)
		if detectErr != nil {
			// Default to PNG if detection fails
			mediaType = "image/png"
		}

		output.Name = filename
		output.MediaType = mediaType
		output.Bytes = int64(len(imgData))
		output.ContentType = "image_url"
		output.dataURL = fmt.Sprintf("data:%s;base64,%s", mediaType, base64.StdEncoding.EncodeToString(imgData))
		output.ImageID = uuid.New().String()
	}

	return output, nil
}
