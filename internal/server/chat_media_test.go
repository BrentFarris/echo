package server

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/tools"
	"github.com/brent/echo/internal/workspaces"
)

func testPNGBytes(t *testing.T) []byte {
	t.Helper()
	canvas := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	canvas.Set(0, 0, color.NRGBA{R: 255, A: 255})
	canvas.Set(1, 0, color.NRGBA{G: 255, A: 255})
	canvas.Set(0, 1, color.NRGBA{B: 255, A: 255})
	canvas.Set(1, 1, color.NRGBA{R: 255, G: 255, A: 255})
	var output bytes.Buffer
	if err := png.Encode(&output, canvas); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func testEncodedImage(t *testing.T, format string, width, height int) []byte {
	t.Helper()
	canvas := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			canvas.Set(x, y, color.NRGBA{R: byte(x), G: byte(y), B: 96, A: 255})
		}
	}
	var output bytes.Buffer
	var err error
	switch format {
	case "jpeg":
		err = jpeg.Encode(&output, canvas, &jpeg.Options{Quality: 95})
	case "gif":
		err = gif.Encode(&output, canvas, nil)
	default:
		err = png.Encode(&output, canvas)
	}
	if err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func testMediaInput(name, mediaType string, data []byte) chatMediaInput {
	return chatMediaInput{
		ID: "draft", Name: name, MediaType: mediaType, Bytes: int64(len(data)),
		DataURL: chatDataURL(mediaType, data),
	}
}

func TestPrepareChatMediaNormalizesAndValidatesImages(t *testing.T) {
	pngData := testPNGBytes(t)
	images, videos, err := prepareChatMedia([]chatMediaInput{testMediaInput(`C:\screens\shot.png`, "image/png", pngData)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || len(videos) != 0 {
		t.Fatalf("unexpected attachments: images=%#v videos=%#v", images, videos)
	}
	if images[0].Name != "shot.png" || images[0].MediaType != "image/jpeg" || !strings.HasPrefix(images[0].DataURL, "data:image/jpeg;base64,") {
		t.Fatalf("image was not safely named and normalized: %#v", images[0])
	}
	if images[0].Bytes <= 0 || images[0].ID == "" || images[0].ID == "draft" {
		t.Fatalf("server metadata was not assigned: %#v", images[0])
	}

	spoofed := testMediaInput("fake.jpg", "image/jpeg", pngData)
	spoofed.DataURL = chatDataURL("image/jpeg", pngData)
	if _, _, err := prepareChatMedia([]chatMediaInput{spoofed}, nil); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected spoofed image rejection, got %v", err)
	}

	broken := testMediaInput("broken.png", "image/png", pngData)
	broken.DataURL = "data:image/png;base64,not-base64!"
	if _, _, err := prepareChatMedia([]chatMediaInput{broken}, nil); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("expected corrupt data URL rejection, got %v", err)
	}

	mismatchedSize := testMediaInput("wrong.png", "image/png", pngData)
	mismatchedSize.Bytes++
	if _, _, err := prepareChatMedia([]chatMediaInput{mismatchedSize}, nil); err == nil || !strings.Contains(err.Error(), "size does not match") {
		t.Fatalf("expected byte-count rejection, got %v", err)
	}
}

func TestPrepareChatMediaSupportsFormatsResizesAndPreservesGIF(t *testing.T) {
	webp, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, mediaType, outputType string
		data                        []byte
	}{
		{"photo.jpg", "image/jpeg", "image/jpeg", testEncodedImage(t, "jpeg", 3, 2)},
		{"graphic.webp", "image/webp", "image/jpeg", webp},
		{"animation.gif", "image/gif", "image/gif", testEncodedImage(t, "gif", 3, 2)},
	}
	for _, test := range tests {
		images, _, err := prepareChatMedia([]chatMediaInput{testMediaInput(test.name, test.mediaType, test.data)}, nil)
		if err != nil {
			t.Fatalf("%s was not accepted: %v", test.mediaType, err)
		}
		if len(images) != 1 || images[0].MediaType != test.outputType {
			t.Fatalf("unexpected normalized %s attachment: %#v", test.mediaType, images)
		}
		if test.mediaType == "image/gif" {
			_, payload, err := parseChatDataURL(chatMediaInput{MediaType: images[0].MediaType, Bytes: images[0].Bytes, DataURL: images[0].DataURL}, supportedChatImageTypes, maxChatImageBytes, "image")
			if err != nil || !bytes.Equal(payload, test.data) {
				t.Fatalf("GIF bytes were not preserved: err=%v", err)
			}
		}
	}

	wide := testEncodedImage(t, "png", maxChatImageDimension+200, 20)
	images, _, err := prepareChatMedia([]chatMediaInput{testMediaInput("wide.png", "image/png", wide)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, normalized, err := parseChatDataURL(chatMediaInput{MediaType: images[0].MediaType, Bytes: images[0].Bytes, DataURL: images[0].DataURL}, supportedChatImageTypes, maxChatImageBytes, "image")
	if err != nil {
		t.Fatal(err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(normalized))
	if err != nil || config.Width > maxChatImageDimension || config.Height > maxChatImageDimension {
		t.Fatalf("large image was not resized within %dpx: config=%#v err=%v", maxChatImageDimension, config, err)
	}
}

func TestPrepareChatMediaLimitsAndVideoTypes(t *testing.T) {
	pngData := testPNGBytes(t)
	tooMany := make([]chatMediaInput, maxChatImageAttachments+1)
	for index := range tooMany {
		tooMany[index] = testMediaInput("image.png", "image/png", pngData)
	}
	if _, _, err := prepareChatMedia(tooMany, nil); err == nil || !strings.Contains(err.Error(), "at most 4 images") {
		t.Fatalf("expected image count rejection, got %v", err)
	}

	large := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, make([]byte, maxChatImageBytes)...)
	if _, _, err := prepareChatMedia([]chatMediaInput{testMediaInput("large.png", "image/png", large)}, nil); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("expected image size rejection, got %v", err)
	}

	mp4 := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	mov := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'q', 't', ' ', ' '}
	webm := []byte{0x1a, 0x45, 0xdf, 0xa3, 0, 0, 0, 0}
	for _, test := range []struct {
		name, mediaType string
		data            []byte
	}{{"clip.mp4", "video/mp4", mp4}, {"clip.mov", "video/quicktime", mov}, {"clip.webm", "video/webm", webm}} {
		_, videos, err := prepareChatMedia(nil, []chatMediaInput{testMediaInput(test.name, test.mediaType, test.data)})
		if err != nil || len(videos) != 1 || videos[0].MediaType != test.mediaType {
			t.Fatalf("%s was not accepted: videos=%#v err=%v", test.name, videos, err)
		}
	}
	firstLargeVideo := append(append([]byte(nil), mp4...), make([]byte, 10<<20-len(mp4))...)
	secondLargeVideo := append(append([]byte(nil), mp4...), make([]byte, (10<<20)+1-len(mp4))...)
	if _, _, err := prepareChatMedia(nil, []chatMediaInput{
		testMediaInput("first.mp4", "video/mp4", firstLargeVideo),
		testMediaInput("second.mp4", "video/mp4", secondLargeVideo),
	}); err == nil || !strings.Contains(err.Error(), "message limit") {
		t.Fatalf("expected total media size rejection, got %v", err)
	}
}

func TestChatMediaPromptHydrationRoutingAndToolContext(t *testing.T) {
	imageAttachment := sessions.MediaAttachment{ID: "img-1", Name: "screen.png", MediaType: "image/png", Bytes: 3, DataURL: "data:image/png;base64,abc"}
	videoAttachment := sessions.MediaAttachment{ID: "vid-1", Name: "clip.mp4", MediaType: "video/mp4", Bytes: 4, DataURL: "data:video/mp4;base64,abcd"}
	if got := chatMediaTextContent("", []sessions.MediaAttachment{imageAttachment}, []sessions.MediaAttachment{videoAttachment}); !strings.HasPrefix(got, "Please review the attached media.") {
		t.Fatalf("unexpected attachment-only prompt: %q", got)
	}

	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "Review these files."},
		{Role: llm.RoleUser, Content: "Use this steering image too."},
	}
	turns := []sessions.Turn{{
		UserMessageIndex: 0, Images: []sessions.MediaAttachment{imageAttachment}, Videos: []sessions.MediaAttachment{videoAttachment},
		GoalSteering: []sessions.GoalSteering{{UserMessageIndex: 1, Images: []sessions.MediaAttachment{imageAttachment}}},
	}}
	hydrated := hydrateChatMediaHistory(messages, turns)
	if len(hydrated[0].ContentParts) != 3 || hydrated[0].ContentParts[1].ImageURL == nil || hydrated[0].ContentParts[2].VideoURL == nil {
		t.Fatalf("media history was not rehydrated: %#v", hydrated[0].ContentParts)
	}
	if len(hydrated[1].ContentParts) != 2 || hydrated[1].ContentParts[1].ImageURL == nil {
		t.Fatalf("goal steering media history was not rehydrated: %#v", hydrated[1].ContentParts)
	}
	if len(messages[0].ContentParts) != 0 {
		t.Fatal("hydration mutated persisted messages")
	}

	chat := &historyStreamer{}
	vision := &historyStreamer{}
	server := &Server{llm: chat, visionLLM: vision, visionSeparate: true, llmSettings: llm.DefaultSettings(), visionSettings: llm.DefaultSettings()}
	server.visionSettings.Endpoint = "http://vision.invalid/v1"
	server.visionSettings.Endpoints = nil
	_, routed := server.routeMediaChat(server.llmSettings, hydrated, false)
	if routed != vision {
		t.Fatal("media-bearing history did not route to the vision streamer")
	}
	server.visionSeparate = false
	selected := server.llmSettings
	selected.Model = "selected-chat-model"
	routedSettings, routed := server.routeMediaChat(selected, hydrated, false)
	if routed != chat || routedSettings.Model != selected.Model {
		t.Fatal("media did not fall back to the selected Chat route when Vision was not separate")
	}

	session := &chatSession{active: &sessions.Turn{Images: []sessions.MediaAttachment{imageAttachment}}}
	attached := session.latestAttachedImages()
	if len(attached) != 1 || attached[0] != (tools.AttachedImage{Name: "screen.png", MediaType: "image/png", DataURL: imageAttachment.DataURL}) {
		t.Fatalf("attached image tool context was not populated: %#v", attached)
	}
}

func TestChatMediaPersistsAndRehydratesAfterReload(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "media-history")
	fake := &historyStreamer{}
	server.llm = fake
	url := startWebSocketTestServer(t, server)
	connection := dialSharedClient(t, url)
	subscribeChat(t, connection, workspace.ID)
	chatID := testActiveChatID(t, server, workspace.ID)

	pngData := testPNGBytes(t)
	mp4Data := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	payload := map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "media-first", "message": "",
		"images": []chatMediaInput{testMediaInput("screen.png", "image/png", pngData)},
		"videos": []chatMediaInput{testMediaInput("clip.mp4", "video/mp4", mp4Data)},
	}
	if err := connection.WriteJSON(payload); err != nil {
		t.Fatal(err)
	}
	started := readSessionEventForChat(t, connection, chatID, "turn_started")
	if started["message"] != "Please review the attached media." || len(started["images"].([]any)) != 1 || len(started["videos"].([]any)) != 1 {
		t.Fatalf("turn start omitted normalized media: %#v", started)
	}
	readSessionEventForChat(t, connection, chatID, "turn_finished")
	connection.Close()

	stored, err := sessions.NewWorkspaceStore(workspace.MainPath).Load(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Tabs) != 1 || !stored.Tabs[0].Vision || len(stored.Tabs[0].Turns) != 1 || len(stored.Tabs[0].Turns[0].Images) != 1 || len(stored.Tabs[0].Turns[0].Videos) != 1 {
		t.Fatalf("media turn was not persisted: %#v", stored)
	}
	for _, message := range stored.Tabs[0].Messages {
		if len(message.ContentParts) != 0 {
			t.Fatal("persisted LLM messages duplicated media content parts")
		}
	}

	server.sessions.invalidate(workspace.ID)
	reloaded := dialSharedClient(t, url)
	if err := reloaded.WriteJSON(map[string]any{"type": "session_subscribe", "workspaceId": workspace.ID}); err != nil {
		t.Fatal(err)
	}
	snapshot := readChatSnapshot(t, reloaded)
	snapshotTurns, ok := snapshot["turns"].([]any)
	if !ok || len(snapshotTurns) != 1 {
		t.Fatalf("reloaded snapshot omitted the media turn: %#v", snapshot)
	}
	snapshotTurn := snapshotTurns[0].(map[string]any)
	if len(snapshotTurn["images"].([]any)) != 1 || len(snapshotTurn["videos"].([]any)) != 1 {
		t.Fatalf("reloaded snapshot omitted attachments: %#v", snapshotTurn)
	}
	if err := reloaded.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "media-second", "message": "What was in it?",
	}); err != nil {
		t.Fatal(err)
	}
	readSessionEventForChat(t, reloaded, chatID, "turn_finished")
	fake.mu.Lock()
	requests := append([]llm.ChatRequest(nil), fake.requests...)
	fake.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("expected two model requests, got %d", len(requests))
	}
	foundImage := false
	foundVideo := false
	for _, message := range requests[1].Messages {
		for _, part := range message.ContentParts {
			if part.ImageURL != nil && strings.HasPrefix(part.ImageURL.URL, "data:image/jpeg;base64,") {
				foundImage = true
			}
			if part.VideoURL != nil && strings.HasPrefix(part.VideoURL.URL, "data:video/mp4;base64,") {
				foundVideo = true
			}
		}
	}
	if !foundImage || !foundVideo {
		t.Fatal("reloaded history did not feed the earlier media back to the model")
	}
	reloaded.Close()
}

func TestVisualToolResultSwitchesToVisionAndPersistsAcrossReload(t *testing.T) {
	server, _ := newTestServer(t)
	workspacePath := t.TempDir()
	imageBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(workspacePath, "screen.png"), imageBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := server.workspaces.Create(workspaces.CreateRequest{Name: "vision-tool", MainPath: workspacePath})
	if err != nil {
		t.Fatal(err)
	}
	chat := &imageToolStreamer{path: normalizeWorkspaceFolderLabel(filepath.Base(workspacePath)) + "/screen.png"}
	vision := &historyStreamer{}
	server.llm = chat
	server.visionLLM = vision
	server.visionSeparate = true
	server.visionSettings = server.llmSettings
	server.visionSettings.Model = "vision-test-model"
	server.visionSettings.Endpoints = nil
	server.visionSettings.EndpointSelection = llm.EndpointSelection{}

	url := startWebSocketTestServer(t, server)
	connection := dialSharedClient(t, url)
	subscribeChat(t, connection, workspace.ID)
	chatID := testActiveChatID(t, server, workspace.ID)
	if err := connection.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "chatId": chatID, "requestId": "read-image", "message": "Read the image.",
	}); err != nil {
		t.Fatal(err)
	}
	readSessionEventForChat(t, connection, chatID, "turn_finished")
	connection.Close()

	chat.mu.Lock()
	chatRequests := append([]llm.ChatRequest(nil), chat.requests...)
	chat.mu.Unlock()
	vision.mu.Lock()
	visionRequests := append([]llm.ChatRequest(nil), vision.requests...)
	vision.mu.Unlock()
	if len(chatRequests) != 1 || len(visionRequests) != 1 || visionRequests[0].Model != "vision-test-model" {
		t.Fatalf("visual tool result did not switch endpoints: chat=%d vision=%#v", len(chatRequests), visionRequests)
	}
	if !messagesRequireMedia(visionRequests[0].Messages) {
		t.Fatal("Vision request did not contain the image returned by the tool")
	}
	stored, err := sessions.NewWorkspaceStore(workspace.MainPath).Load(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Tabs) != 1 || !stored.Tabs[0].Vision {
		t.Fatalf("Vision routing state was not persisted: %#v", stored)
	}

	server.sessions.invalidate(workspace.ID)
	reloaded := dialSharedClient(t, url)
	defer reloaded.Close()
	subscribeChat(t, reloaded, workspace.ID)
	if err := reloaded.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "chatId": chatID, "requestId": "later-text", "message": "Continue without another image.",
	}); err != nil {
		t.Fatal(err)
	}
	readSessionEventForChat(t, reloaded, chatID, "turn_finished")
	chat.mu.Lock()
	chatCount := len(chat.requests)
	chat.mu.Unlock()
	vision.mu.Lock()
	visionRequests = append([]llm.ChatRequest(nil), vision.requests...)
	vision.mu.Unlock()
	if chatCount != 1 || len(visionRequests) != 2 || visionRequests[1].Model != "vision-test-model" {
		t.Fatalf("later text turn did not stay on Vision: chat=%d vision=%#v", chatCount, visionRequests)
	}
}

func TestChatMediaValidationFailureDoesNotCreateTurn(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "invalid-media")
	server.llm = &historyStreamer{}
	url := startWebSocketTestServer(t, server)
	connection := dialSharedClient(t, url)
	defer connection.Close()
	subscribeChat(t, connection, workspace.ID)
	chatID := testActiveChatID(t, server, workspace.ID)

	if err := connection.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "chatId": chatID, "requestId": "invalid-media", "message": "review",
		"images": []chatMediaInput{{Name: "broken.png", MediaType: "image/png", Bytes: 1, DataURL: "data:image/png;base64,not-base64!"}},
	}); err != nil {
		t.Fatal(err)
	}
	connection.SetReadDeadline(testReadDeadline())
	for {
		var response map[string]any
		if err := connection.ReadJSON(&response); err != nil {
			t.Fatal(err)
		}
		if response["type"] == "command_error" {
			if response["code"] != "invalid_attachments" {
				t.Fatalf("unexpected rejection: %#v", response)
			}
			break
		}
	}

	parent, err := server.sessions.get(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := parent.resolveTab(chatID)
	if err != nil {
		t.Fatal(err)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.active != nil || len(session.transcript.Turns) != 0 || len(session.transcript.Messages) != 0 {
		t.Fatalf("validation failure created chat state: active=%#v transcript=%#v", session.active, session.transcript)
	}
}

func TestCodeChatRejectsMediaAttachments(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "code-media")
	server.llm = &historyStreamer{}
	url := startWebSocketTestServer(t, server)
	connection := dialSharedClient(t, url)
	defer connection.Close()
	if err := connection.WriteJSON(map[string]any{
		"type": "chat_send", "surface": "code", "workspaceId": workspace.ID, "requestId": "code-media", "message": "review",
		"images": []chatMediaInput{testMediaInput("screen.png", "image/png", testPNGBytes(t))},
	}); err != nil {
		t.Fatal(err)
	}
	connection.SetReadDeadline(testReadDeadline())
	for {
		var response map[string]any
		if err := connection.ReadJSON(&response); err != nil {
			t.Fatal(err)
		}
		if response["type"] == "command_error" {
			if response["code"] != "invalid_attachments_surface" {
				t.Fatalf("unexpected rejection: %#v", response)
			}
			return
		}
	}
}

func testReadDeadline() (deadline time.Time) {
	return time.Now().Add(3 * time.Second)
}

func testActiveChatID(t *testing.T, server *Server, workspaceID string) string {
	t.Helper()
	parent, err := server.sessions.get(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	parent.mu.Lock()
	defer parent.mu.Unlock()
	return parent.activeChatID
}
