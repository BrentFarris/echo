package server

// On-demand chat content endpoints. Session snapshots are metadata-only (see
// chat_snapshot_redaction.go); clients fetch full tool results, reasoning
// text, and media bytes from here when the user expands a disclosure or opens
// the lightbox. All routes resolve the chat through the same confined session
// manager as the WebSocket surface and are covered by the global auth wrapper.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/brent/echo/internal/sessions"
	"github.com/disintegration/imaging"
)

// chatImagePreviewQuality matches the upload normalization quality.
const chatImagePreviewQuality = 85

// maxChatAttachmentWidth bounds the ?width= preview parameter.
const maxChatAttachmentWidth = 4096

// resolveChatTranscript locates a chat tab by ID across both surfaces.
func (s *Server) resolveChatTranscript(workspaceID, chatID string) (*chatSession, error) {
	parent, err := s.sessions.get(workspaceID)
	if err != nil {
		return nil, err
	}
	session, _, err := parent.resolveSurfaceTab(chatID, chatSurfaceMain)
	if err == nil {
		return session, nil
	}
	session, _, err = parent.resolveSurfaceTab(chatID, chatSurfaceCode)
	if err != nil {
		return nil, err
	}
	return session, nil
}

// handleGetChatAttachment serves GET /api/workspaces/{id}/chats/{chatId}/attachments/{attachmentId}?width=N
// as raw bytes. width>0 requests a downscaled preview variant (PNG/JPEG only;
// GIF keeps its animation, WebP/SVG and other types pass through untouched).
func (s *Server) handleGetChatAttachment(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")
	attachmentID := r.PathValue("attachmentId")
	width := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("width")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > maxChatAttachmentWidth {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("width must be a positive integer up to %d", maxChatAttachmentWidth))
			return
		}
		width = parsed
	}
	session, err := s.resolveChatTranscript(r.PathValue("id"), chatID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	session.mu.Lock()
	attachment := findChatAttachmentLocked(session, attachmentID)
	session.mu.Unlock()
	if attachment == nil {
		writeError(w, http.StatusNotFound, "attachment was not found")
		return
	}
	data, err := decodeChatMediaDataURL(attachment.DataURL)
	if err != nil {
		writeError(w, http.StatusNotFound, "attachment payload is unavailable")
		return
	}
	contentType := attachment.MediaType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if width > 0 {
		if resized, ok := resizeChatAttachment(data, contentType, width); ok {
			data = resized
			contentType = "image/png"
		}
	}
	// Attachments are immutable transcript history, so unlike the workspace
	// file browser (no-store) derived variants are safely cacheable.
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleGetChatToolResult serves GET .../chats/{chatId}/tool-results/{callId}
// with the full, untruncated tool result string.
func (s *Server) handleGetChatToolResult(w http.ResponseWriter, r *http.Request) {
	callID := r.PathValue("callId")
	session, err := s.resolveChatTranscript(r.PathValue("id"), r.PathValue("chatId"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	session.mu.Lock()
	content, ok := findChatToolResultLocked(session, callID)
	session.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "tool result was not found")
		return
	}
	writeData(w, http.StatusOK, map[string]any{"callId": callID, "content": content})
}

// handleGetChatReasoning serves GET .../chats/{chatId}/reasoning/{turnId}
// with ?assistant=N (assistant-turn reasoning) or ?agentId=X (research-agent
// reasoning). Exactly one selector must be provided.
func (s *Server) handleGetChatReasoning(w http.ResponseWriter, r *http.Request) {
	turnID := r.PathValue("turnId")
	assistantRaw := strings.TrimSpace(r.URL.Query().Get("assistant"))
	agentID := strings.TrimSpace(r.URL.Query().Get("agentId"))
	if (assistantRaw == "") == (agentID == "") {
		writeError(w, http.StatusBadRequest, "provide either the assistant query parameter or agentId")
		return
	}
	session, err := s.resolveChatTranscript(r.PathValue("id"), r.PathValue("chatId"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	turn := findChatTurnLocked(session, turnID)
	if turn == nil {
		writeError(w, http.StatusNotFound, "turn was not found")
		return
	}
	if agentID != "" {
		for _, item := range turn.ResearchReasoning {
			if item.AgentID == agentID {
				writeData(w, http.StatusOK, map[string]any{"turnId": turnID, "agentId": agentID, "reasoning": item.Reasoning})
				return
			}
		}
		writeError(w, http.StatusNotFound, "research reasoning was not found")
		return
	}
	assistantNumber, err := strconv.Atoi(assistantRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "assistant must be an integer")
		return
	}
	for _, assistant := range turn.AssistantTurns {
		if assistant.Number == assistantNumber {
			writeData(w, http.StatusOK, map[string]any{"turnId": turnID, "assistant": assistantNumber, "reasoning": assistant.Reasoning})
			return
		}
	}
	writeError(w, http.StatusNotFound, "assistant reasoning was not found")
}

// --- lookup helpers (callers must hold session.mu) -------------------------

func findChatTurnLocked(session *chatSession, turnID string) *sessions.Turn {
	for index := range session.transcript.Turns {
		if session.transcript.Turns[index].ID == turnID {
			return &session.transcript.Turns[index]
		}
	}
	if session.active != nil && session.active.ID == turnID {
		return session.active
	}
	return nil
}

func findChatToolResultLocked(session *chatSession, callID string) (string, bool) {
	scan := func(turn *sessions.Turn) (string, bool) {
		for _, assistant := range turn.AssistantTurns {
			for _, tool := range assistant.Tools {
				if tool.CallID == callID {
					return tool.Result, true
				}
			}
		}
		for _, tool := range turn.ResearchTools {
			if tool.CallID == callID {
				return tool.Result, true
			}
		}
		return "", false
	}
	for index := range session.transcript.Turns {
		if content, ok := scan(&session.transcript.Turns[index]); ok {
			return content, true
		}
	}
	if session.active != nil {
		if content, ok := scan(session.active); ok {
			return content, true
		}
	}
	return "", false
}

func findChatAttachmentLocked(session *chatSession, attachmentID string) *sessions.MediaAttachment {
	var found *sessions.MediaAttachment
	scanTurn := func(turn *sessions.Turn) bool {
		if attachment := findAttachmentInLists(attachmentID, turn.Images, turn.Videos); attachment != nil {
			found = attachment
			return true
		}
		for _, steering := range turn.GoalSteering {
			if attachment := findAttachmentInLists(attachmentID, steering.Images, steering.Videos); attachment != nil {
				found = attachment
				return true
			}
		}
		for _, assistant := range turn.AssistantTurns {
			if attachment := findAttachmentInLists(attachmentID, assistant.Images, assistant.Videos); attachment != nil {
				found = attachment
				return true
			}
		}
		return false
	}
	for index := range session.transcript.Turns {
		if scanTurn(&session.transcript.Turns[index]) {
			return found
		}
	}
	if session.active != nil && scanTurn(session.active) {
		return found
	}
	for goalIndex := range session.transcript.Goals {
		for _, steering := range session.transcript.Goals[goalIndex].PendingSteering {
			if attachment := findAttachmentInLists(attachmentID, steering.Images, steering.Videos); attachment != nil {
				found = attachment
				return found
			}
		}
	}
	return nil
}

func findAttachmentInLists(attachmentID string, lists ...[]sessions.MediaAttachment) *sessions.MediaAttachment {
	for _, list := range lists {
		for index := range list {
			if list[index].ID == attachmentID {
				return &list[index]
			}
		}
	}
	return nil
}

// --- payload decoding and preview resizing ---------------------------------

func decodeChatMediaDataURL(dataURL string) ([]byte, error) {
	dataURL = strings.TrimSpace(dataURL)
	if dataURL == "" {
		return nil, fmt.Errorf("empty data url")
	}
	_, payload, ok := strings.Cut(dataURL, ",")
	if !ok {
		return nil, fmt.Errorf("malformed data url")
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(payload)
		if err != nil {
			return nil, fmt.Errorf("decode attachment payload: %w", err)
		}
	}
	return data, nil
}

// resizeChatAttachment downscales an image so its longest edge fits width.
// Only PNG and JPEG are resized (WebP has no stdlib encoder, GIF must keep
// its animation); anything else passes through unchanged. Images already
// within bounds also pass through so previews never upscale.
func resizeChatAttachment(data []byte, mediaType string, width int) ([]byte, bool) {
	if mediaType != "image/png" && mediaType != "image/jpeg" {
		return nil, false
	}
	if cached, ok := chatAttachmentPreviewCache.get(mediaType, data, width); ok {
		return cached, true
	}
	decoded, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, false
	}
	if decoded.Bounds().Dx() <= width && decoded.Bounds().Dy() <= width {
		return nil, false
	}
	fitted := imaging.Fit(decoded, width, width, imaging.Lanczos)
	var output bytes.Buffer
	if mediaType == "image/jpeg" {
		err = imaging.Encode(&output, fitted, imaging.JPEG, imaging.JPEGQuality(chatImagePreviewQuality))
	} else {
		err = imaging.Encode(&output, fitted, imaging.PNG)
	}
	if err != nil {
		return nil, false
	}
	resized := output.Bytes()
	chatAttachmentPreviewCache.put(mediaType, data, width, resized)
	return resized, true
}

// chatPreviewCache is a tiny bounded cache for derived preview variants so
// repeated snapshot re-renders do not re-run Lanczos on large photos.
var chatAttachmentPreviewCache = &chatPreviewCache{entries: map[string][]byte{}}

const chatPreviewCacheMaxEntries = 256

type chatPreviewCache struct {
	mu      sync.Mutex
	entries map[string][]byte
}

func (c *chatPreviewCache) key(mediaType string, source []byte, width int) string {
	return fmt.Sprintf("%s|%d|%d|%x", mediaType, width, len(source), source[:min(len(source), 512)])
}

func (c *chatPreviewCache) get(mediaType string, source []byte, width int) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[c.key(mediaType, source, width)]
	return entry, ok
}

func (c *chatPreviewCache) put(mediaType string, source []byte, width int, resized []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= chatPreviewCacheMaxEntries {
		c.entries = map[string][]byte{}
	}
	c.entries[c.key(mediaType, source, width)] = resized
}
