package server

import (
	"encoding/json"
	"strings"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
)

// Insert generated image content after the entire tool-result group, keeping
// tool call/result ordering valid for compatible providers. Video stays metadata
// only, as in the original integration, rather than sending large video payloads.
func (s *Server) withGeneratedImages(chat, botID string, messages []domain.Message, history []model.Message) ([]model.Message, error) {
	type resultKey struct{ callID, content string }
	byCall := map[resultKey]domain.Message{}
	for _, m := range messages {
		if m.ModelContent == nil && m.BotID == botID && m.Role == "tool" && m.Status == "completed" && len(m.Attachments) > 0 {
			var result struct {
				ImageID string `json:"imageId"`
			}
			// Only image generation supplies model-visible image bytes. GIF video
			// results, although image MIME types, remain metadata-only.
			if json.Unmarshal([]byte(m.Content), &result) == nil && result.ImageID != "" {
				byCall[resultKey{m.CallID, m.Content}] = m
			}
		}
	}
	var out []model.Message
	var pending []model.Message
	for i, m := range history {
		out = append(out, m)
		if m.Role == "tool" {
			var parts []model.ContentPart
			for _, a := range byCall[resultKey{m.CallID, m.Content}].Attachments {
				if !strings.HasPrefix(a.MediaType, "image/") {
					continue
				}
				part, err := s.attachmentPart(chat, a.ID)
				if err != nil {
					return nil, err
				}
				parts = append(parts, part)
			}
			if len(parts) > 0 {
				pending = append(pending, model.Message{Role: "user", ToolResultID: m.ID, Content: "Generated image output from the preceding tools (not a new user instruction).", Parts: parts})
			}
		}
		if len(pending) > 0 && (i+1 == len(history) || history[i+1].Role != "tool") {
			out = append(out, pending...)
			pending = nil
		}
	}
	return out, nil
}
