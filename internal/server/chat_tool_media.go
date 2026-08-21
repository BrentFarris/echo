package server

import (
	"strings"

	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/tools"
)

// Cap on media attachments recorded for a single assistant sub-turn. Each
// provider currently yields at most one artifact per tool call, so the cap
// guards against turns that invoke media-producing tools repeatedly (e.g.,
// many generations in one batch): it bounds both the WS payload burst and the
// size of the persisted transcript.
const maxAssistantTurnMedia = 8

// extractToolMedia pulls image/video media out of a successful tool result via
// the LLM content-provider interfaces. It is purely read-only: the result is
// not mutated and no data is copied beyond building the attachment structs.
// existingImages/existingVideos are the counts already recorded on the active
// assistant sub-turn; extraction trims output so the combined totals stay
// within maxAssistantTurnMedia.
func extractToolMedia(result tools.ExecutionResult, existingImages, existingVideos int) (images, videos []sessions.MediaAttachment) {
	if !result.Success || result.Output == nil {
		return nil, nil
	}
	budget := maxAssistantTurnMedia - existingImages - existingVideos
	if budget < 0 {
		budget = 0
	}
	dropped := 0

	if provider, ok := result.Output.(tools.LLMImageContentProvider); ok {
		image, ok := provider.LLMImageContent()
		if ok && strings.TrimSpace(image.DataURL) != "" {
			if budget > 0 {
				images = append(images, sessions.MediaAttachment{
					ID:        newSessionID("gen-img"),
					Name:      mediaName(firstNonBlank(image.Name, image.Path), "generated-image"),
					MediaType: image.MediaType,
					Bytes:     image.Bytes,
					DataURL:   image.DataURL,
				})
				budget--
			} else {
				dropped++
			}
		}
	}
	if provider, ok := result.Output.(tools.LLMVideoContentProvider); ok {
		video, ok := provider.LLMVideoContent()
		if ok && strings.TrimSpace(video.DataURL) != "" {
			if budget > 0 {
				videos = append(videos, sessions.MediaAttachment{
					ID:        newSessionID("gen-vid"),
					Name:      mediaName(firstNonBlank(video.Name, video.Path), "generated-video"),
					MediaType: video.MediaType,
					Bytes:     video.Bytes,
					DataURL:   video.DataURL,
				})
				budget--
			} else {
				dropped++
			}
		}
	}
	if dropped > 0 {
		logf("dropped %d tool media attachment(s): assistant turn exceeds the %d media cap", dropped, maxAssistantTurnMedia)
	}
	return images, videos
}

// toolMediaSummary builds a lightweight trajectory entry for media produced by
// a tool call. Counts and names only — never payload data — because the
// trajectory is diagnostic, not an artifact archive. Returns nil when the call
// produced no media so the step map stays clean.
func toolMediaSummary(images, videos []sessions.MediaAttachment) map[string]any {
	if len(images) == 0 && len(videos) == 0 {
		return nil
	}
	names := func(attachments []sessions.MediaAttachment) []string {
		out := make([]string, 0, len(attachments))
		for _, attachment := range attachments {
			out = append(out, attachment.Name)
		}
		return out
	}
	return map[string]any{
		"imageCount": len(images), "videoCount": len(videos),
		"images": names(images), "videos": names(videos),
	}
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// mediaName sanitizes a tool-supplied display name with the same rules used for
// user-uploaded attachments, falling back to kind when nothing usable remains.
func mediaName(candidate, fallback string) string {
	name := safeChatMediaName(candidate, "")
	if name == "" || name == "." {
		return fallback
	}
	return name
}
