package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/brent/echo/internal/comfyui"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/plugins"
)

func comfyUIConfig(m domain.PluginManifest, config map[string]any, owner bool) (map[string]any, error) {
	data, _ := json.Marshal(config)
	if err := plugins.Validate(m.Settings, data); err != nil {
		return nil, err
	}
	result := map[string]any{}
	for k, v := range config {
		result[k] = strings.TrimSpace(v.(string))
	}
	base := text(result, "url")
	if !owner && base != text(m.Config, "url") {
		return nil, errors.New("set the ComfyUI server URL in its owner settings")
	}
	if base != "" {
		u, err := url.Parse(base)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("ComfyUI URL must be an HTTP(S) base URL without credentials, query or fragment")
		}
		result["url"] = strings.TrimRight(base, "/")
	}
	return result, nil
}

type comfyUIArgs struct {
	Prompt             string  `json:"prompt"`
	NegativePrompt     string  `json:"negativePrompt"`
	WorkflowJSON       string  `json:"workflowJSON"`
	WorkflowPath       string  `json:"workflowPath"`
	Model              string  `json:"model"`
	Width              int     `json:"width"`
	Height             int     `json:"height"`
	Steps              int     `json:"steps"`
	CfgScale           float64 `json:"cfgScale"`
	Seed               int64   `json:"seed"`
	ImagePath          string  `json:"imagePath"`
	AttachedImageIndex *int    `json:"attachedImageIndex"`
	Frames             int     `json:"frames"`
	FPS                float64 `json:"fps"`
	Format             string  `json:"format"`
	Duration           float64 `json:"duration"`
	AspectRatio        string  `json:"aspectRatio"`
	Megapixels         float64 `json:"megapixels"`
}

func (s *Server) comfyUI(ctx context.Context, scope domain.ExecutionScope, m domain.PluginManifest, method string, raw json.RawMessage) (any, error) {
	if scope.PluginID != "comfyui" || !m.Builtin {
		return nil, errors.New("ComfyUI capabilities belong to the ComfyUI plugin")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.HasPrefix(method, "comfyui.save_") {
		return s.saveComfyUIMedia(ctx, scope, method, raw)
	}
	args := comfyUIArgs{Seed: -1}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Prompt) == "" {
		return nil, errors.New("prompt is required")
	}
	config, err := comfyUIConfig(m, m.Config, true)
	if err != nil {
		return nil, err
	}
	base := text(config, "url")
	if base == "" {
		return nil, errors.New("configure the server URL in Settings → ComfyUI")
	}
	if _, err := s.Store.Chat(scope.ChatID); err != nil || scope.ToolMessageID == "" {
		return nil, errors.New("generate media from a chat so its result can be saved")
	}
	// The owner-selected endpoint is the entire network grant. Redirects must
	// never expand it to another service or silently change the request method.
	client := &comfyui.Client{BaseURL: base, HTTPClient: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("ComfyUI redirects are disabled; configure the final server URL")
	}}}
	video := method == "comfyui.generate_video"
	params := comfyui.TemplateParams{Prompt: args.Prompt, NegativePrompt: args.NegativePrompt, Model: args.Model,
		Width: args.Width, Height: args.Height, Steps: args.Steps, CfgScale: args.CfgScale, Seed: args.Seed,
		Frames: args.Frames, FPS: args.FPS, Format: args.Format, Duration: args.Duration, AspectRatio: args.AspectRatio, Megapixels: args.Megapixels}
	if params.Model == "" {
		params.Model = text(config, "defaultCheckpoint")
	}
	hasImage := args.ImagePath != "" || args.AttachedImageIndex != nil
	workflowPath := args.WorkflowPath
	if workflowPath == "" {
		key := "txt2imgWorkflow"
		if hasImage {
			key = "img2imgWorkflow"
		}
		if video {
			key = "videoWorkflow"
		}
		workflowPath = text(config, key)
	}
	var workflow map[string]any
	if args.WorkflowJSON != "" {
		workflow, err = comfyui.ParseWorkflowJSON([]byte(args.WorkflowJSON))
	} else if workflowPath != "" {
		var data []byte
		data, err = s.Files.ReadBytes(scope.Bot, workflowPath, comfyui.MaxWorkflowJSONBytes)
		if err == nil {
			workflow, err = comfyui.ParseWorkflowJSON(data)
		}
	} else if video {
		return nil, errors.New("no video workflow configured; set videoWorkflow or provide workflowPath/workflowJSON")
	}
	if err != nil {
		return nil, fmt.Errorf("load ComfyUI workflow: %w", err)
	}
	if hasImage {
		var data []byte
		name := ""
		if args.ImagePath != "" {
			data, err = s.Files.ReadBytes(scope.Bot, args.ImagePath, 100<<20)
			name = filepath.Base(args.ImagePath)
		} else {
			var attachment domain.Attachment
			attachment, data, err = s.comfyUIInputImage(scope, *args.AttachedImageIndex)
			name = attachment.Name
		}
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(http.DetectContentType(data), "image/") {
			return nil, errors.New("ComfyUI input must be an image")
		}
		params.Image, err = client.UploadImage(ctx, "echo_input_"+domain.ID()+filepath.Ext(name), data)
		if err != nil {
			return nil, err
		}
	}
	result, err := client.Generate(ctx, params, workflow)
	if err != nil {
		if params.Model == "" {
			return nil, fmt.Errorf("%w; set defaultCheckpoint or provide a model if the workflow requires one", err)
		}
		return nil, err
	}
	paths := result.OutputImages
	if video {
		paths = result.OutputVideos
	}
	if len(paths) == 0 {
		return nil, errors.New("workflow produced no media of the requested kind; check its saving nodes")
	}
	// Match the original inline result: preserve every output path, download
	// the first artifact, and keep only metadata in the WASI/model tool result.
	outputPath := paths[0]
	filename, subfolder := path.Base(outputPath), path.Dir(outputPath)
	if subfolder == "." {
		subfolder = ""
	}
	storageType := result.MediaTypes[outputPath]
	if storageType == "" {
		storageType = "output"
		if strings.EqualFold(path.Ext(filename), ".gif") {
			storageType = "custom"
		}
	}
	data, err := client.FetchImageBytes(ctx, filename, subfolder, storageType)
	if err != nil {
		return nil, err
	}
	kind := generatedMediaType(filename, data)
	if !strings.HasPrefix(kind, "image/") && !strings.HasPrefix(kind, "video/") {
		return nil, errors.New("ComfyUI returned unsupported media")
	}
	a := domain.Attachment{ID: domain.ID(), ChatID: scope.ChatID, Name: filename, MediaType: kind, Size: int64(len(data))}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.Store.AddGeneratedAttachment(a, scope.ToolMessageID, data); err != nil {
		return nil, err
	}
	output := map[string]any{"promptId": result.PromptID, "statusMessage": result.StatusMessage, "name": a.Name, "mediaType": kind, "bytes": a.Size,
		"url": "/api/v1/chats/" + scope.ChatID + "/attachments/" + a.ID}
	if video {
		output["videoId"] = a.ID
		output["outputVideos"] = paths
	} else {
		output["imageId"] = a.ID
		output["outputImages"] = paths
	}
	return output, nil
}

func generatedMediaType(filename string, data []byte) string {
	kind := http.DetectContentType(data)
	if strings.HasPrefix(kind, "image/") || strings.HasPrefix(kind, "video/") {
		return kind
	}
	switch strings.ToLower(path.Ext(filename)) {
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".avi":
		return "video/x-msvideo"
	case ".mkv":
		return "video/x-matroska"
	}
	return kind
}

func (s *Server) comfyUIInputImage(scope domain.ExecutionScope, index int) (domain.Attachment, []byte, error) {
	ms, err := s.Store.Messages(scope.ChatID)
	if err != nil {
		return domain.Attachment{}, nil, err
	}
	var run domain.Run
	_ = s.Store.Get("runs", scope.RunID, &run)
	for i := len(ms) - 1; i >= 0; i-- {
		m := ms[i]
		if !run.CreatedAt.IsZero() && m.CreatedAt.After(run.CreatedAt) {
			continue
		}
		if m.Role != "user" || !slices.Contains(m.Recipients, scope.Bot.ID) {
			continue
		}
		var images []domain.Attachment
		for _, a := range m.Attachments {
			if strings.HasPrefix(a.MediaType, "image/") {
				images = append(images, a)
			}
		}
		if len(images) == 0 {
			continue
		}
		if index < 0 || index >= len(images) {
			return domain.Attachment{}, nil, fmt.Errorf("attachedImageIndex %d is out of range; available images: %d", index, len(images))
		}
		return s.Store.Attachment(scope.ChatID, images[index].ID, false)
	}
	return domain.Attachment{}, nil, errors.New("no attached images in this bot's user message")
}

func (s *Server) saveComfyUIMedia(ctx context.Context, scope domain.ExecutionScope, method string, raw json.RawMessage) (any, error) {
	args, err := decodeArgs(raw)
	if err != nil {
		return nil, err
	}
	idKey := "imageId"
	if method == "comfyui.save_video" {
		idKey = "videoId"
	}
	id, destination := text(args, idKey), strings.TrimSpace(text(args, "path"))
	if id == "" || destination == "" {
		return nil, errors.New("media ID and destination path are required")
	}
	ms, err := s.Store.Messages(scope.ChatID)
	if err != nil {
		return nil, err
	}
	allowed := false
	for _, m := range ms {
		if m.Role != "tool" || m.BotID != scope.Bot.ID {
			continue
		}
		for _, a := range m.Attachments {
			if a.ID == id {
				allowed = true
			}
		}
	}
	if !allowed {
		return nil, errors.New("generated media not found in this bot's chat")
	}
	a, data, err := s.Store.Attachment(scope.ChatID, id, false)
	if err != nil {
		return nil, err
	}
	if idKey == "imageId" && !strings.HasPrefix(a.MediaType, "image/") || idKey == "videoId" && !strings.HasPrefix(a.MediaType, "video/") && a.MediaType != "image/gif" {
		return nil, errors.New("generated media is not the requested kind")
	}
	return s.trackFileChanges(ctx, scope.Bot, []string{destination}, func() (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var value map[string]any
		registration, err := s.Files.CoordinateFile(ctx, scope.Bot, "edit", destination, "", func() error {
			var err error
			value, err = s.Files.WriteBytes(scope.Bot, destination, data, boolean(args, "overwrite"))
			return err
		})
		if value != nil && registration.Pending {
			value["registration"] = registration
		}
		return value, err
	})
}
