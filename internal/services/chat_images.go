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
const maxChatImageMessageBytes = 20 * 1024 * 1024

type ChatMessageRequest struct {
	Content  string           `json:"content"`
	PlanMode bool             `json:"planMode"`
	Images   []ChatImageInput `json:"images,omitempty"`
}

type ChatImageInput struct {
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

type workspaceImageReference struct {
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
	resolved, err := resolveWorkspaceServicePath(workspace.FolderPath, path)
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
	relative := workspaceRelativePath(workspace.FolderPath, resolved)
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
	content = strings.TrimSpace(content)
	if content == "" && len(images) > 0 {
		content = "Please review the attached image(s)."
	}
	if len(images) == 0 {
		return content
	}
	var builder strings.Builder
	builder.WriteString(content)
	builder.WriteString("\n\nAttached images:\n")
	for index, image := range images {
		builder.WriteString(fmt.Sprintf("- Image %d: %s", index+1, image.Name))
		if image.Path != "" {
			builder.WriteString(fmt.Sprintf(" (%s)", image.Path))
		}
		builder.WriteString(fmt.Sprintf(", %s, %s\n", image.MediaType, formatChatImageBytes(image.Bytes)))
	}
	return strings.TrimSpace(builder.String())
}

func chatImageContentParts(content string, images []ChatImageAttachment) []llm.MessageContentPart {
	if len(images) == 0 {
		return nil
	}
	parts := []llm.MessageContentPart{llm.TextContentPart(chatImageTextContent(content, images))}
	for _, image := range images {
		parts = append(parts, llm.ImageURLContentPart(image.DataURL))
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
