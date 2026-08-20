package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/workspaces"
)

// TestChatToolMediaTransportEmitsAndPersists verifies the Phase 0 contract end
// to end for a tool that produces image media:
//  1. the tool_result session event carries structured images[] attachments,
//  2. the finished transcript persists them on the AssistantTurn,
//  3. a fresh subscriber's snapshot restores the same attachments.
func TestChatToolMediaTransportEmitsAndPersists(t *testing.T) {
	s, _ := newTestServer(t)

	wsDir := t.TempDir()
	// A minimal valid PNG header so filesystem_read_image detects image/png.
	pngBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(wsDir, "pic.png"), pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := s.workspaces.Create(workspaces.CreateRequest{Name: "media-ws", MainPath: wsDir})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := s.workspaces.SetActive(ws.ID); err != nil {
		t.Fatalf("set active: %v", err)
	}

	fake := &imageToolStreamer{path: normalizeWorkspaceFolderLabel(filepath.Base(wsDir)) + "/pic.png"}
	s.llm = fake
	url := startWebSocketTestServer(t, s)

	conn := dialSharedClient(t, url)
	subscribeChat(t, conn, ws.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": ws.ID, "requestId": "request-media", "message": "read the image",
	}); err != nil {
		t.Fatal(err)
	}

	var toolResult = readUntilSessionEvent(t, conn, "tool_result")
	readUntilSessionEvent(t, conn, "turn_finished")

	// 1. Structured media rides the tool_result event.
	images, ok := toolResult["images"].([]any)
	if !ok || len(images) != 1 {
		t.Fatalf("expected exactly one image attachment on tool_result, got %#v", toolResult["images"])
	}
	image, ok := images[0].(map[string]any)
	if !ok {
		t.Fatalf("malformed image attachment: %#v", images[0])
	}
	if id, _ := image["id"].(string); !strings.HasPrefix(id, "gen-img-") {
		t.Fatalf("expected gen-img-* ID, got %q", id)
	}
	if name, _ := image["name"].(string); name != "pic.png" {
		t.Fatalf("expected name pic.png, got %q", name)
	}
	if mediaType, _ := image["mediaType"].(string); mediaType != "image/png" {
		t.Fatalf("expected mediaType image/png, got %q", mediaType)
	}
	if bytes, _ := image["bytes"].(float64); bytes != float64(len(pngBytes)) {
		t.Fatalf("expected bytes %d, got %v", len(pngBytes), image["bytes"])
	}
	dataURL, _ := image["dataUrl"].(string)
	if !strings.HasPrefix(dataURL, "data:image/png;base64,") {
		t.Fatalf("expected image data URL, got %q", dataURL)
	}
	if _, hasVideos := toolResult["videos"]; hasVideos {
		t.Fatalf("unexpected videos key on tool_result: %#v", toolResult)
	}

	// 2. The finished transcript persists media on the assistant turn.
	transcript := loadActiveTabTranscript(t, ws)
	if len(transcript.Turns) != 1 {
		t.Fatalf("expected one persisted turn, got %d", len(transcript.Turns))
	}
	assistantTurns := transcript.Turns[0].AssistantTurns
	if len(assistantTurns) == 0 {
		t.Fatalf("no assistant turns persisted: %#v", transcript.Turns[0])
	}
	foundImage := false
	for _, assistant := range assistantTurns {
		for _, attachment := range assistant.Images {
			if attachment.Name == "pic.png" && strings.HasPrefix(attachment.DataURL, "data:image/png;base64,") {
				foundImage = true
			}
		}
	}
	if !foundImage {
		t.Fatalf("assistant turn media not persisted: %#v", assistantTurns)
	}

	// 3. A fresh subscriber's snapshot restores the same attachments.
	late := dialSharedClient(t, url)
	defer late.Close()
	if err := late.WriteJSON(map[string]any{"type": "session_subscribe", "workspaceId": ws.ID}); err != nil {
		t.Fatal(err)
	}
	late.SetReadDeadline(time.Now().Add(3 * time.Second))
	var snapshot map[string]any
	for {
		var message map[string]any
		if err := late.ReadJSON(&message); err != nil {
			t.Fatalf("read late snapshot: %v", err)
		}
		if message["type"] == "session_snapshot" {
			snapshot = message
			break
		}
	}
	snapshotTurns, _ := snapshot["turns"].([]any)
	if len(snapshotTurns) != 1 {
		t.Fatalf("expected one snapshot turn, got %d", len(snapshotTurns))
	}
	turn, _ := snapshotTurns[0].(map[string]any)
	assistantArr, _ := turn["assistantTurns"].([]any)
	restored := false
	for _, rawAssistant := range assistantArr {
		assistant, _ := rawAssistant.(map[string]any)
		rawImages, _ := assistant["images"].([]any)
		for _, rawImage := range rawImages {
			img, _ := rawImage.(map[string]any)
			if img["name"] == "pic.png" && strings.HasPrefix(asString(img["dataUrl"]), "data:image/png;base64,") {
				restored = true
			}
		}
	}
	if !restored {
		t.Fatalf("fresh subscriber did not receive restored media: %#v", snapshotTurns[0])
	}
}

func asString(value any) string {
	out, _ := value.(string)
	return out
}
