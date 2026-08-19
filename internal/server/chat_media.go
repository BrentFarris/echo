package server

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/tools"
	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"
)

const (
	maxChatImageAttachments = 4
	maxChatVideoAttachments = 4
	maxChatMediaAttachments = 8
	maxChatImageBytes       = 10 << 20
	maxChatMediaBytes       = 20 << 20
	maxChatImageDimension   = 2048
	chatJPEGQuality         = 85
)

var supportedChatImageTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/webp": true, "image/gif": true,
}

var supportedChatVideoTypes = map[string]bool{
	"video/mp4": true, "video/webm": true, "video/quicktime": true,
}

type chatMediaInput struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	MediaType string `json:"mediaType"`
	Bytes     int64  `json:"bytes"`
	DataURL   string `json:"dataUrl"`
}

func prepareChatMedia(imageInputs, videoInputs []chatMediaInput) ([]sessions.MediaAttachment, []sessions.MediaAttachment, error) {
	if len(imageInputs) > maxChatImageAttachments {
		return nil, nil, fmt.Errorf("a message can include at most %d images", maxChatImageAttachments)
	}
	if len(videoInputs) > maxChatVideoAttachments {
		return nil, nil, fmt.Errorf("a message can include at most %d videos", maxChatVideoAttachments)
	}
	if len(imageInputs)+len(videoInputs) > maxChatMediaAttachments {
		return nil, nil, fmt.Errorf("a message can include at most %d media attachments", maxChatMediaAttachments)
	}

	var rawTotal int64
	images := make([]sessions.MediaAttachment, 0, len(imageInputs))
	for index, input := range imageInputs {
		mediaType, raw, err := parseChatDataURL(input, supportedChatImageTypes, maxChatImageBytes, "image")
		if err != nil {
			return nil, nil, fmt.Errorf("image %d: %w", index+1, err)
		}
		rawTotal += int64(len(raw))
		if rawTotal > maxChatMediaBytes {
			return nil, nil, fmt.Errorf("attached media is larger than the %d byte message limit", maxChatMediaBytes)
		}
		normalized, normalizedType, err := normalizeChatImage(raw, mediaType)
		if err != nil {
			return nil, nil, fmt.Errorf("image %d: %w", index+1, err)
		}
		images = append(images, sessions.MediaAttachment{
			ID: newSessionID("img"), Name: safeChatMediaName(input.Name, fmt.Sprintf("image-%d%s", index+1, chatMediaExtension(normalizedType))),
			MediaType: normalizedType, Bytes: int64(len(normalized)), DataURL: chatDataURL(normalizedType, normalized),
		})
	}

	videos := make([]sessions.MediaAttachment, 0, len(videoInputs))
	for index, input := range videoInputs {
		mediaType, raw, err := parseChatDataURL(input, supportedChatVideoTypes, maxChatMediaBytes, "video")
		if err != nil {
			return nil, nil, fmt.Errorf("video %d: %w", index+1, err)
		}
		rawTotal += int64(len(raw))
		if rawTotal > maxChatMediaBytes {
			return nil, nil, fmt.Errorf("attached media is larger than the %d byte message limit", maxChatMediaBytes)
		}
		videos = append(videos, sessions.MediaAttachment{
			ID: newSessionID("vid"), Name: safeChatMediaName(input.Name, fmt.Sprintf("video-%d%s", index+1, chatMediaExtension(mediaType))),
			MediaType: mediaType, Bytes: int64(len(raw)), DataURL: chatDataURL(mediaType, raw),
		})
	}
	return images, videos, nil
}

func parseChatDataURL(input chatMediaInput, supported map[string]bool, maximum int64, kind string) (string, []byte, error) {
	dataURL := strings.TrimSpace(input.DataURL)
	if !strings.HasPrefix(strings.ToLower(dataURL), "data:") {
		return "", nil, fmt.Errorf("%s data must be a data URL", kind)
	}
	comma := strings.IndexByte(dataURL, ',')
	if comma < 0 {
		return "", nil, fmt.Errorf("%s data URL is missing base64 content", kind)
	}
	meta := dataURL[len("data:"):comma]
	if !strings.Contains(strings.ToLower(meta), ";base64") {
		return "", nil, fmt.Errorf("%s data URL must be base64 encoded", kind)
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(meta, ";")[0]))
	declaredType := strings.ToLower(strings.TrimSpace(input.MediaType))
	if !supported[mediaType] || declaredType != mediaType {
		return "", nil, fmt.Errorf("unsupported or mismatched %s format %q", kind, declaredType)
	}
	payload := dataURL[comma+1:]
	if len(payload) > base64.StdEncoding.EncodedLen(int(maximum)) {
		return "", nil, fmt.Errorf("%s is larger than the %d byte limit", safeChatMediaName(input.Name, kind), maximum)
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", nil, fmt.Errorf("decode %s data: %w", kind, err)
	}
	if int64(len(data)) > maximum {
		return "", nil, fmt.Errorf("%s is larger than the %d byte limit", safeChatMediaName(input.Name, kind), maximum)
	}
	if input.Bytes < 0 || input.Bytes != int64(len(data)) {
		return "", nil, fmt.Errorf("%s size does not match its data", kind)
	}
	detected, err := detectChatMediaType(data, kind)
	if err != nil {
		return "", nil, err
	}
	if detected != mediaType {
		return "", nil, fmt.Errorf("declared %s format %q does not match the file data", kind, mediaType)
	}
	return mediaType, data, nil
}

func detectChatMediaType(data []byte, kind string) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("%s data is empty", kind)
	}
	if kind == "image" {
		switch {
		case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
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
	switch {
	case len(data) >= 12 && string(data[4:8]) == "ftyp" && string(data[8:12]) == "qt  ":
		return "video/quicktime", nil
	case len(data) >= 8 && string(data[4:8]) == "ftyp":
		return "video/mp4", nil
	case len(data) >= 4 && bytes.Equal(data[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}):
		return "video/webm", nil
	case len(data) >= 8 && string(data[4:8]) == "moov":
		return "video/quicktime", nil
	default:
		return "", fmt.Errorf("unsupported video format")
	}
}

func normalizeChatImage(data []byte, mediaType string) ([]byte, string, error) {
	if mediaType == "image/gif" {
		if _, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil {
			return nil, "", fmt.Errorf("decode GIF: %w", err)
		}
		return data, mediaType, nil
	}
	decoded, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, "", fmt.Errorf("decode image: %w", err)
	}
	bounds := decoded.Bounds()
	if bounds.Dx() > maxChatImageDimension || bounds.Dy() > maxChatImageDimension {
		decoded = imaging.Fit(decoded, maxChatImageDimension, maxChatImageDimension, imaging.Lanczos)
	}
	var output bytes.Buffer
	if err := jpeg.Encode(&output, decoded, &jpeg.Options{Quality: chatJPEGQuality}); err != nil {
		return nil, "", fmt.Errorf("encode image: %w", err)
	}
	return output.Bytes(), "image/jpeg", nil
}

func chatMediaTextContent(content string, images, videos []sessions.MediaAttachment) string {
	content = strings.TrimSpace(content)
	if content == "" {
		content = chatMediaDefaultPrompt(images, videos)
	}
	if len(images) == 0 && len(videos) == 0 {
		return content
	}
	var builder strings.Builder
	builder.WriteString(content)
	builder.WriteString("\n\nAttached media:\n")
	for index, attachment := range images {
		fmt.Fprintf(&builder, "- Image %d: %s, %s, %s\n", index+1, attachment.Name, attachment.MediaType, formatChatMediaBytes(attachment.Bytes))
	}
	for index, attachment := range videos {
		fmt.Fprintf(&builder, "- Video %d: %s, %s, %s\n", index+1, attachment.Name, attachment.MediaType, formatChatMediaBytes(attachment.Bytes))
	}
	return strings.TrimSpace(builder.String())
}

func chatMediaDefaultPrompt(images, videos []sessions.MediaAttachment) string {
	switch {
	case len(images) > 0 && len(videos) == 0:
		return "Please review the attached image(s)."
	case len(videos) > 0 && len(images) == 0:
		return "Please review the attached video(s)."
	default:
		return "Please review the attached media."
	}
}

func chatMediaContentParts(text string, images, videos []sessions.MediaAttachment) []llm.MessageContentPart {
	if len(images) == 0 && len(videos) == 0 {
		return nil
	}
	parts := []llm.MessageContentPart{llm.TextContentPart(text)}
	for _, attachment := range images {
		parts = append(parts, llm.ImageURLContentPart(attachment.DataURL))
	}
	for _, attachment := range videos {
		parts = append(parts, llm.VideoURLContentPart(attachment.DataURL))
	}
	return parts
}

func hydrateChatMediaHistory(messages []llm.Message, turns []sessions.Turn) []llm.Message {
	hydrated := append([]llm.Message(nil), messages...)
	for _, turn := range turns {
		if len(turn.Images) == 0 && len(turn.Videos) == 0 {
			continue
		}
		index := turn.UserMessageIndex
		if index < 0 || index >= len(hydrated) || hydrated[index].Role != llm.RoleUser {
			continue
		}
		hydrated[index].ContentParts = chatMediaContentParts(hydrated[index].Content, turn.Images, turn.Videos)
	}
	return hydrated
}

func messagesRequireMedia(messages []llm.Message) bool {
	for _, message := range messages {
		for _, part := range message.ContentParts {
			if part.ImageURL != nil || part.VideoURL != nil {
				return true
			}
		}
	}
	return false
}

func (s *Server) routeMediaChat(defaultSettings llm.Settings, messages []llm.Message) (llm.Settings, chatStreamer) {
	if !messagesRequireMedia(messages) || !s.visionSeparate || s.visionLLM == nil {
		return defaultSettings, s.llm
	}
	return s.visionSettings, s.visionLLM
}

func (s *chatSession) latestAttachedImages() []tools.AttachedImage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil && len(s.active.Images) > 0 {
		return toolAttachedImages(s.active.Images)
	}
	for index := len(s.transcript.Turns) - 1; index >= 0; index-- {
		if len(s.transcript.Turns[index].Images) > 0 {
			return toolAttachedImages(s.transcript.Turns[index].Images)
		}
	}
	return nil
}

func toolAttachedImages(images []sessions.MediaAttachment) []tools.AttachedImage {
	result := make([]tools.AttachedImage, 0, len(images))
	for _, attachment := range images {
		if attachment.DataURL != "" {
			result = append(result, tools.AttachedImage{Name: attachment.Name, MediaType: attachment.MediaType, DataURL: attachment.DataURL})
		}
	}
	return result
}

func safeChatMediaName(name, fallback string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = strings.Trim(name, "\"'`")
	if name == "" || name == "." || len(name) > 255 {
		return fallback
	}
	return name
}

func chatMediaExtension(mediaType string) string {
	switch mediaType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
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

func chatDataURL(mediaType string, data []byte) string {
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func formatChatMediaBytes(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	if size < 1<<20 {
		return fmt.Sprintf("%.1f KB", float64(size)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(size)/(1<<20))
}
