package services

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/brent/echo/internal/llm"
)

const maxChatImageAttachments = 4
const maxChatImageBytes = 10 * 1024 * 1024
const maxChatVideoAttachments = 4
const maxChatVideoBytes = 50 * 1024 * 1024
const maxChatMediaAttachments = 8
const maxChatImageMessageBytes = 20 * 1024 * 1024

type ChatMessageRequest struct {
	Content     string           `json:"content"`
	PlanMode    bool             `json:"planMode"`
	AgentModeID string           `json:"agentModeId"`
	Images      []ChatImageInput `json:"images,omitempty"`
	Videos      []ChatVideoInput `json:"videos,omitempty"`
}

type ChatImageInput struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	DataURL   string `json:"dataUrl"`
	Bytes     int64  `json:"bytes,omitempty"`
}

type ChatVideoInput struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	DataURL   string `json:"dataUrl"`
	Bytes     int64  `json:"bytes,omitempty"`
}

type ChatImageAttachment struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	MediaType string `json:"mediaType"`
	Bytes     int64  `json:"bytes"`
	DataURL   string `json:"dataUrl,omitempty"`
}

type ChatVideoAttachment struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	MediaType string `json:"mediaType"`
	Bytes     int64  `json:"bytes"`
	DataURL   string `json:"dataUrl,omitempty"`
}

type workspaceImageReference struct {
	Path      string
	Supported bool
}

type workspaceVideoReference struct {
	Path      string
	Supported bool
}

func (s *SystemService) prepareChatImages(workspace Workspace, content string, pasted []ChatImageInput) ([]ChatImageAttachment, error) {
	images := make([]ChatImageAttachment, 0, len(pasted))
	for _, reference := range referencedWorkspaceImages(content) {
		if !reference.Supported {
			return nil, fmt.Errorf("unsupported image format for @%s", reference.Path)
		}
		image, err := readWorkspaceChatImage(workspace, reference.Path)
		if err != nil {
			return nil, err
		}
		image.ID = s.nextChatID("img")
		images = append(images, image)
	}

	for index, input := range pasted {
		image, err := normalizePastedChatImage(index, input)
		if err != nil {
			return nil, err
		}
		if image.ID == "" {
			image.ID = s.nextChatID("img")
		}
		images = append(images, image)
	}

	if err := validateChatImages(images); err != nil {
		return nil, err
	}
	return images, nil
}

func normalizePastedChatImage(index int, input ChatImageInput) (ChatImageAttachment, error) {
	mediaType, data, err := parseChatImageDataURL(input.DataURL)
	if err != nil {
		return ChatImageAttachment{}, err
	}
	if input.Bytes > 0 && input.Bytes != int64(len(data)) {
		return ChatImageAttachment{}, fmt.Errorf("pasted image size does not match its data")
	}
	name := safeChatImageName(input.Name, fmt.Sprintf("pasted-image-%d%s", index+1, chatImageExtension(mediaType)))
	return ChatImageAttachment{
		ID:        strings.TrimSpace(input.ID),
		Source:    "pasted",
		Name:      name,
		MediaType: mediaType,
		Bytes:     int64(len(data)),
		DataURL:   chatImageDataURL(mediaType, data),
	}, nil
}

func parseChatImageDataURL(dataURL string) (string, []byte, error) {
	dataURL = strings.TrimSpace(dataURL)
	if !strings.HasPrefix(strings.ToLower(dataURL), "data:") {
		return "", nil, fmt.Errorf("image data must be a data URL")
	}
	comma := strings.Index(dataURL, ",")
	if comma < 0 {
		return "", nil, fmt.Errorf("image data URL is missing base64 content")
	}
	meta := dataURL[len("data:"):comma]
	payload := dataURL[comma+1:]
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(meta, ";")[0]))
	if !strings.Contains(strings.ToLower(meta), ";base64") {
		return "", nil, fmt.Errorf("image data URL must be base64 encoded")
	}
	if !supportedChatImageMediaType(mediaType) {
		return "", nil, fmt.Errorf("unsupported image format %q", mediaType)
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", nil, fmt.Errorf("decode image data: %w", err)
	}
	detected, err := detectChatImageMediaType(data)
	if err != nil {
		return "", nil, err
	}
	return detected, data, nil
}

func readWorkspaceChatImage(workspace Workspace, path string) (ChatImageAttachment, error) {
	path = cleanWorkspacePathCandidate(path)
	if path == "" {
		return ChatImageAttachment{}, fmt.Errorf("image path is required")
	}
	resolved, err := resolveWorkspaceServicePath(workspace, path)
	if err != nil {
		return ChatImageAttachment{}, fmt.Errorf("image path %q: %w", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return ChatImageAttachment{}, fmt.Errorf("image file %q was not found", path)
	}
	if !info.Mode().IsRegular() {
		return ChatImageAttachment{}, fmt.Errorf("image path %q is not a regular file", path)
	}
	if info.Size() > maxChatImageBytes {
		return ChatImageAttachment{}, fmt.Errorf("image %q is larger than the %d byte limit", path, maxChatImageBytes)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return ChatImageAttachment{}, fmt.Errorf("read image %q: %w", path, err)
	}
	mediaType, err := detectChatImageMediaType(data)
	if err != nil {
		return ChatImageAttachment{}, fmt.Errorf("image %q: %w", path, err)
	}
	relative := workspaceRelativePath(workspace, resolved)
	return ChatImageAttachment{
		Source:    "workspace",
		Name:      fileName(relative),
		Path:      relative,
		MediaType: mediaType,
		Bytes:     int64(len(data)),
		DataURL:   chatImageDataURL(mediaType, data),
	}, nil
}

func detectChatImageMediaType(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("image data is empty")
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
		return "", fmt.Errorf("unsupported image format")
	}
}

func validateChatImages(images []ChatImageAttachment) error {
	if len(images) > maxChatImageAttachments {
		return fmt.Errorf("a message can include at most %d images", maxChatImageAttachments)
	}
	var total int64
	for _, image := range images {
		if !supportedChatImageMediaType(image.MediaType) {
			return fmt.Errorf("unsupported image format %q", image.MediaType)
		}
		if image.Bytes > maxChatImageBytes {
			return fmt.Errorf("image %q is larger than the %d byte limit", image.Name, maxChatImageBytes)
		}
		total += image.Bytes
	}
	if total > maxChatImageMessageBytes {
		return fmt.Errorf("attached images are larger than the %d byte message limit", maxChatImageMessageBytes)
	}
	return nil
}

func chatImageDataURL(mediaType string, data []byte) string {
	return fmt.Sprintf("data:%s;base64,%s", mediaType, base64.StdEncoding.EncodeToString(data))
}

func chatImageTextContent(content string, images []ChatImageAttachment) string {
	return chatMediaTextContent(content, images, nil)
}

func chatMediaTextContent(content string, images []ChatImageAttachment, videos []ChatVideoAttachment) string {
	content = strings.TrimSpace(content)
	hasMedia := len(images) > 0 || len(videos) > 0
	if content == "" && hasMedia {
		var defaultPrompt string
		if len(videos) > 0 {
			defaultPrompt = "Please review the attached video(s)."
		} else {
			defaultPrompt = "Please review the attached image(s)."
		}
		content = defaultPrompt
	}
	if !hasMedia {
		return content
	}
	var builder strings.Builder
	builder.WriteString(content)
	builder.WriteString("\n\nAttached media:\n")
	for index, image := range images {
		builder.WriteString(fmt.Sprintf("- Image %d: %s", index+1, image.Name))
		if image.Path != "" {
			builder.WriteString(fmt.Sprintf(" (%s)", image.Path))
		}
		builder.WriteString(fmt.Sprintf(", %s, %s\n", image.MediaType, formatChatImageBytes(image.Bytes)))
	}
	for index, video := range videos {
		builder.WriteString(fmt.Sprintf("- Video %d: %s", index+1, video.Name))
		if video.Path != "" {
			builder.WriteString(fmt.Sprintf(" (%s)", video.Path))
		}
		builder.WriteString(fmt.Sprintf(", %s, %s\n", video.MediaType, formatChatImageBytes(video.Bytes)))
	}
	return strings.TrimSpace(builder.String())
}

func chatImageContentParts(content string, images []ChatImageAttachment) []llm.MessageContentPart {
	return chatMediaContentParts(content, images, nil)
}

func chatMediaContentParts(content string, images []ChatImageAttachment, videos []ChatVideoAttachment) []llm.MessageContentPart {
	if len(images) == 0 && len(videos) == 0 {
		return nil
	}
	parts := []llm.MessageContentPart{llm.TextContentPart(chatMediaTextContent(content, images, videos))}
	for _, image := range images {
		parts = append(parts, llm.ImageURLContentPart(image.DataURL))
	}
	for _, video := range videos {
		parts = append(parts, llm.VideoURLContentPart(video.DataURL))
	}
	return parts
}

func referencedWorkspaceImages(content string) []workspaceImageReference {
	seen := map[string]bool{}
	var references []workspaceImageReference
	for _, candidate := range chatMentionPathCandidates(content) {
		candidate = cleanWorkspacePathCandidate(candidate)
		if candidate == "" {
			continue
		}
		kind := chatImagePathKind(candidate)
		if kind == "" {
			continue
		}
		key := strings.ToLower(filepath.ToSlash(candidate))
		if seen[key] {
			continue
		}
		seen[key] = true
		references = append(references, workspaceImageReference{
			Path:      candidate,
			Supported: kind == "supported",
		})
	}
	return references
}

func chatMentionPathCandidates(content string) []string {
	var candidates []string
	for index := 0; index < len(content); index++ {
		if content[index] != '@' || index+1 >= len(content) {
			continue
		}
		if index > 0 {
			previous, _ := utf8.DecodeLastRuneInString(content[:index])
			if !unicode.IsSpace(previous) {
				continue
			}
		}
		start := index + 1
		if content[start] == '"' {
			var builder strings.Builder
			escaped := false
			for cursor := start + 1; cursor < len(content); cursor++ {
				char := content[cursor]
				if escaped {
					builder.WriteByte(char)
					escaped = false
					continue
				}
				if char == '\\' {
					escaped = true
					continue
				}
				if char == '"' {
					candidates = append(candidates, builder.String())
					index = cursor
					break
				}
				builder.WriteByte(char)
			}
			continue
		}
		cursor := start
		for cursor < len(content) {
			runeValue, width := utf8.DecodeRuneInString(content[cursor:])
			if unicode.IsSpace(runeValue) {
				break
			}
			cursor += width
		}
		if cursor > start {
			candidates = append(candidates, content[start:cursor])
			index = cursor - 1
		}
	}
	return candidates
}

func chatImagePathKind(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return "supported"
	case ".svg", ".bmp", ".tif", ".tiff", ".ico", ".avif":
		return "unsupported"
	default:
		return ""
	}
}

func supportedChatImageMediaType(mediaType string) bool {
	switch strings.ToLower(mediaType) {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

func chatImageExtension(mediaType string) string {
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
		return ""
	}
}

func safeChatImageName(name string, fallback string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.Trim(name, "\"'`")
	if name == "" || name == "." || name == string(filepath.Separator) {
		return fallback
	}
	return name
}

func fileName(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	if slash := strings.LastIndex(path, "/"); slash >= 0 {
		return path[slash+1:]
	}
	return path
}

func formatChatImageBytes(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
}

// --- Video attachment support ---

func (s *SystemService) prepareChatVideos(workspace Workspace, content string, pasted []ChatVideoInput) ([]ChatVideoAttachment, error) {
	videos := make([]ChatVideoAttachment, 0, len(pasted))
	for _, reference := range referencedWorkspaceVideos(content) {
		if !reference.Supported {
			return nil, fmt.Errorf("unsupported video format for @%s", reference.Path)
		}
		video, err := readWorkspaceChatVideo(workspace, reference.Path)
		if err != nil {
			return nil, err
		}
		video.ID = s.nextChatID("vid")
		videos = append(videos, video)
	}

	for index, input := range pasted {
		video, err := normalizePastedChatVideo(index, input)
		if err != nil {
			return nil, err
		}
		if video.ID == "" {
			video.ID = s.nextChatID("vid")
		}
		videos = append(videos, video)
	}

	if err := validateChatVideos(videos); err != nil {
		return nil, err
	}
	return videos, nil
}

func normalizePastedChatVideo(index int, input ChatVideoInput) (ChatVideoAttachment, error) {
	mediaType, data, err := parseChatVideoDataURL(input.DataURL)
	if err != nil {
		return ChatVideoAttachment{}, err
	}
	if input.Bytes > 0 && input.Bytes != int64(len(data)) {
		return ChatVideoAttachment{}, fmt.Errorf("pasted video size does not match its data")
	}
	name := safeChatImageName(input.Name, fmt.Sprintf("pasted-video-%d%s", index+1, chatVideoExtension(mediaType)))
	return ChatVideoAttachment{
		ID:        strings.TrimSpace(input.ID),
		Source:    "pasted",
		Name:      name,
		MediaType: mediaType,
		Bytes:     int64(len(data)),
		DataURL:   chatMediaDataURL(mediaType, data),
	}, nil
}

func parseChatVideoDataURL(dataURL string) (string, []byte, error) {
	dataURL = strings.TrimSpace(dataURL)
	if !strings.HasPrefix(strings.ToLower(dataURL), "data:") {
		return "", nil, fmt.Errorf("video data must be a data URL")
	}
	comma := strings.Index(dataURL, ",")
	if comma < 0 {
		return "", nil, fmt.Errorf("video data URL is missing base64 content")
	}
	meta := dataURL[len("data:"):comma]
	payload := dataURL[comma+1:]
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(meta, ";")[0]))
	if !strings.Contains(strings.ToLower(meta), ";base64") {
		return "", nil, fmt.Errorf("video data URL must be base64 encoded")
	}
	if !supportedChatVideoMediaType(mediaType) {
		return "", nil, fmt.Errorf("unsupported video format %q", mediaType)
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", nil, fmt.Errorf("decode video data: %w", err)
	}
	detected, err := detectChatVideoMediaType(data)
	if err != nil {
		return "", nil, err
	}
	return detected, data, nil
}

func readWorkspaceChatVideo(workspace Workspace, path string) (ChatVideoAttachment, error) {
	path = cleanWorkspacePathCandidate(path)
	if path == "" {
		return ChatVideoAttachment{}, fmt.Errorf("video path is required")
	}
	resolved, err := resolveWorkspaceServicePath(workspace, path)
	if err != nil {
		return ChatVideoAttachment{}, fmt.Errorf("video path %q: %w", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return ChatVideoAttachment{}, fmt.Errorf("video file %q was not found", path)
	}
	if !info.Mode().IsRegular() {
		return ChatVideoAttachment{}, fmt.Errorf("video path %q is not a regular file", path)
	}
	if info.Size() > maxChatVideoBytes {
		return ChatVideoAttachment{}, fmt.Errorf("video %q is larger than the %d byte limit", path, maxChatVideoBytes)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return ChatVideoAttachment{}, fmt.Errorf("read video %q: %w", path, err)
	}
	mediaType, err := detectChatVideoMediaType(data)
	if err != nil {
		return ChatVideoAttachment{}, fmt.Errorf("video %q: %w", path, err)
	}
	relative := workspaceRelativePath(workspace, resolved)
	return ChatVideoAttachment{
		Source:    "workspace",
		Name:      fileName(relative),
		Path:      relative,
		MediaType: mediaType,
		Bytes:     int64(len(data)),
		DataURL:   chatMediaDataURL(mediaType, data),
	}, nil
}

func detectChatVideoMediaType(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("video data is empty")
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
		return "", fmt.Errorf("unsupported video format")
	}
}

func validateChatVideos(videos []ChatVideoAttachment) error {
	if len(videos) > maxChatVideoAttachments {
		return fmt.Errorf("a message can include at most %d videos", maxChatVideoAttachments)
	}
	for _, video := range videos {
		if !supportedChatVideoMediaType(video.MediaType) {
			return fmt.Errorf("unsupported video format %q", video.MediaType)
		}
		if video.Bytes > maxChatVideoBytes {
			return fmt.Errorf("video %q is larger than the %d byte limit", video.Name, maxChatVideoBytes)
		}
	}
	return nil
}

func chatMediaDataURL(mediaType string, data []byte) string {
	return fmt.Sprintf("data:%s;base64,%s", mediaType, base64.StdEncoding.EncodeToString(data))
}

func referencedWorkspaceVideos(content string) []workspaceVideoReference {
	seen := map[string]bool{}
	var references []workspaceVideoReference
	for _, candidate := range chatMentionPathCandidates(content) {
		candidate = cleanWorkspacePathCandidate(candidate)
		if candidate == "" {
			continue
		}
		kind := chatVideoPathKind(candidate)
		if kind == "" {
			continue
		}
		key := strings.ToLower(filepath.ToSlash(candidate))
		if seen[key] {
			continue
		}
		seen[key] = true
		references = append(references, workspaceVideoReference{
			Path:      candidate,
			Supported: kind == "supported",
		})
	}
	return references
}

func chatVideoPathKind(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".webm", ".mov":
		return "supported"
	default:
		return ""
	}
}

func supportedChatVideoMediaType(mediaType string) bool {
	switch strings.ToLower(mediaType) {
	case "video/mp4", "video/webm", "video/quicktime":
		return true
	default:
		return false
	}
}

func chatVideoExtension(mediaType string) string {
	switch strings.ToLower(mediaType) {
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "video/quicktime":
		return ".mov"
	default:
		return ""
	}
}
