package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
	"github.com/brent/echo/internal/store"
)

func mediaTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			im.SetNRGBA(x, y, color.NRGBA{uint8(x * 17), uint8(y * 31), uint8(x * y), 255})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, im); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func mediaToolScope(t *testing.T, s *Server, root string) domain.ExecutionScope {
	t.Helper()
	c := chat(t, s, domain.Generation{})
	m := domain.Message{ID: domain.ID(), ChatID: c.ID, Role: "tool", BotID: "echo", CallID: "read", Status: "running"}
	if err := s.Store.AddMessage(m); err != nil {
		t.Fatal(err)
	}
	return domain.ExecutionScope{Bot: domain.Bot{ID: "echo", Roots: []string{root}}, ChatID: c.ID, ToolMessageID: m.ID}
}

func TestReadImageArchivesSnapshotsAndRehydratesAfterFork(t *testing.T) {
	s := testServer(t)
	root := t.TempDir()
	scope := mediaToolScope(t, s, root)
	filename := filepath.Join(root, "screen.png")
	first := mediaTestPNG(t, 40, 20)
	if err := os.WriteFile(filename, first, 0600); err != nil {
		t.Fatal(err)
	}
	call := domain.ToolCall{Function: domain.FunctionCall{Name: "file_read_image", Arguments: `{"path":"screen.png"}`}}
	value, err := s.tool(context.Background(), nil, scope, call)
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(value)
	if bytes.Contains(meta, []byte("base64")) || bytes.Contains(meta, []byte("data:")) {
		t.Fatal("binary escaped into tool result")
	}
	second := mediaTestPNG(t, 20, 40)
	if err := os.WriteFile(filename, second, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.tool(context.Background(), nil, scope, call); err != nil {
		t.Fatal(err)
	}
	attachments, err := s.Store.MessageAttachments(scope.ToolMessageID)
	if err != nil || len(attachments) != 2 || attachments[0].ID == attachments[1].ID || !attachments[0].ModelInput {
		t.Fatal(attachments, err)
	}
	for i, expected := range [][]byte{first, second} {
		_, actual, err := s.Store.Attachment(scope.ChatID, attachments[i].ID, false)
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatal("snapshot changed", i, err)
		}
	}
	ms, _, err := s.snapshot(scope.ChatID)
	if err != nil || len(ms[0].Attachments) != 2 {
		t.Fatal("reconnect lost media", ms, err)
	}
	tool := ms[0]
	tool.Status = "completed"
	tool.Content = string(meta)
	if err := s.Store.FinishMessage(tool); err != nil {
		t.Fatal(err)
	}
	putForkMessage(t, s, scope.ChatID, domain.Message{ID: "answer", Role: "assistant", BotID: "echo", Content: "Reviewed"})
	fork, err := s.forkChat(context.Background(), scope.ChatID, "answer", "media-fork")
	if err != nil {
		t.Fatal(err)
	}
	history, err := s.history(scope.Bot, fork)
	if err != nil {
		t.Fatal(err)
	}
	images := 0
	for _, m := range history {
		for _, part := range m.Parts {
			if part.ImageURL != nil {
				images++
			}
		}
	}
	if images != 2 {
		t.Fatal("fork lost model images", images)
	}
	notice := "Image omitted from model context"
	if _, err := s.Store.SetModelContent(scope.ChatID, tool.ID, notice); err != nil {
		t.Fatal(err)
	}
	history, err = s.history(scope.Bot, scope.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range history {
		if len(m.Parts) != 0 {
			t.Fatal("compacted image was reinserted")
		}
	}
	ms, _, err = s.snapshot(scope.ChatID)
	if err != nil || len(ms[0].Attachments) != 2 {
		t.Fatal("compaction removed gallery media", err)
	}
}

func TestReadImageRejectsInvalidOversizedAndOutOfScopeFiles(t *testing.T) {
	s := testServer(t)
	root := t.TempDir()
	scope := mediaToolScope(t, s, root)
	outside := filepath.Join(t.TempDir(), "outside.png")
	os.WriteFile(outside, mediaTestPNG(t, 1, 1), 0600)
	os.WriteFile(filepath.Join(root, "invalid.png"), []byte("not an image"), 0600)
	os.WriteFile(filepath.Join(root, "truncated.png"), mediaTestPNG(t, 10, 10)[:35], 0600)
	large, err := os.Create(filepath.Join(root, "large.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate(store.MaxAttachmentBytes + 1); err != nil {
		t.Fatal(err)
	}
	large.Close()
	for _, path := range []string{outside, "invalid.png", "truncated.png", "large.png", root} {
		if _, err := s.readImage(context.Background(), scope, path); err == nil {
			t.Fatal("accepted", path)
		}
	}
	attachments, err := s.Store.MessageAttachments(scope.ToolMessageID)
	if err != nil || len(attachments) != 0 {
		t.Fatal(attachments, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.recordToolMedia(ctx, scope, "image.png", "image/png", bytes.NewReader([]byte{1}), true); err == nil {
		t.Fatal("cancelled capture succeeded")
	}
}

func TestReadImageSurvivesServerRestartAndLegacyImageMetadata(t *testing.T) {
	guest, err := os.ReadFile("../plugins/guest/builtin.wasm")
	if err != nil {
		t.Fatal(err)
	}
	dir, root := t.TempDir(), t.TempDir()
	s, err := New(dir, nil, guest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s != nil {
			s.Close()
		}
	})
	scope := mediaToolScope(t, s, root)
	data := mediaTestPNG(t, 30, 10)
	os.WriteFile(filepath.Join(root, "image.png"), data, 0600)
	result, err := s.readImage(context.Background(), scope, "image.png")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	ms, _ := s.Store.Messages(scope.ChatID)
	m := ms[0]
	m.Status = "completed"
	m.Content = string(encoded)
	if err := s.Store.FinishMessage(m); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = nil
	s, err = New(dir, nil, guest)
	if err != nil {
		t.Fatal(err)
	}
	ms, _, err = s.snapshot(scope.ChatID)
	if err != nil || len(ms[0].Attachments) != 1 || !ms[0].Attachments[0].ModelInput {
		t.Fatal("restart lost image metadata", ms, err)
	}
	history, err := s.history(scope.Bot, scope.ChatID)
	if err != nil || len(history[len(history)-1].Parts) != 1 {
		t.Fatal("restart lost model image", history, err)
	}
	a := ms[0].Attachments[0]
	_, original, err := s.Store.Attachment(scope.ChatID, a.ID, false)
	if err != nil || !bytes.Equal(original, data) {
		t.Fatal("restart changed image", err)
	}
	// Existing records with imageId, but no new flag, still hydrate. Legacy GIF
	// video outputs with videoId do not gain model-visible bytes.
	for _, idField := range []string{"imageId", "videoId"} {
		legacy := ms[0]
		legacy.Attachments = []domain.Attachment{a}
		legacy.Attachments[0].ModelInput = false
		b, _ := json.Marshal(map[string]string{idField: a.ID})
		legacy.Content = string(b)
		got, err := s.withGeneratedImages(scope.ChatID, "echo", []domain.Message{legacy}, []model.Message{{ID: legacy.ID, Role: "tool", CallID: legacy.CallID, Content: legacy.Content}})
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if idField == "imageId" {
			want = 2
		}
		if len(got) != want {
			t.Fatal("legacy visibility changed", idField, got)
		}
	}
}

func TestAttachmentThumbnailRequiresAuthAndNeverFallsBackToInvalidOriginal(t *testing.T) {
	s := testServer(t)
	scope := mediaToolScope(t, s, t.TempDir())
	a, err := s.recordToolMedia(context.Background(), scope, "invalid.png", "image/png", strings.NewReader("bad image"), true)
	if err != nil {
		t.Fatal(err)
	}
	url := "/api/v1/chats/" + scope.ChatID + "/attachments/" + a.ID + "?width=384"
	r := httptest.NewRecorder()
	s.Handler().ServeHTTP(r, httptest.NewRequest("GET", url, nil))
	if r.Code != 401 {
		t.Fatal("thumbnail bypassed owner authentication", r.Code)
	}
	mux := http.NewServeMux()
	s.attachmentRoutes(mux)
	r = httptest.NewRecorder()
	mux.ServeHTTP(r, httptest.NewRequest("GET", url, nil))
	if r.Code != 422 || r.Body.String() == "bad image" {
		t.Fatal("invalid preview returned original", r.Code)
	}
}

func TestReadImageToolLoopKeepsToolGroupBeforeModelImages(t *testing.T) {
	s := testServer(t)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "screen.png"), mediaTestPNG(t, 30, 20), 0600)
	bot, _ := s.Bot("echo")
	bot.Roots = []string{root}
	if _, err := s.saveBot(bot); err != nil {
		t.Fatal(err)
	}
	var received []model.Message
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []model.Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&request)
		for _, m := range request.Messages {
			if m.Role == "tool" {
				received = request.Messages
				stream(w, "Reviewed the screenshot.", nil)
				return
			}
		}
		stream(w, "", []domain.ToolCall{
			{ID: "first", Type: "function", Function: domain.FunctionCall{Name: "file_read_image", Arguments: `{"path":"screen.png"}`}},
			{ID: "second", Type: "function", Function: domain.FunctionCall{Name: "file_list", Arguments: `{"path":"."}`}},
		})
	})
	c := chat(t, s, g)
	d, err := s.send(c.ID, SendRequest{Content: "Review the image", Generation: g})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	if len(received) < 5 {
		t.Fatal("model did not receive tool history", d.err)
	}
	last := received[len(received)-1]
	if last.Role != "user" || len(last.Parts) != 1 || last.Parts[0].ImageURL == nil || !strings.HasPrefix(last.Parts[0].ImageURL.URL, "data:image/png;base64,") {
		t.Fatal(last)
	}
	if received[len(received)-2].Role != "tool" || received[len(received)-3].Role != "tool" {
		t.Fatal("images split tool group", received)
	}
	ms, _, err := s.snapshot(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(ms)
	if bytes.Contains(b, []byte("base64")) {
		t.Fatal("snapshot contains image bytes")
	}
}

func TestAttachmentThumbnailsFormatsOrientationAndDeletion(t *testing.T) {
	s := testServer(t)
	c := chat(t, s, domain.Generation{})
	data := mediaTestPNG(t, 800, 400)
	im, _, _ := image.Decode(bytes.NewReader(data))
	var jpegData, gifData bytes.Buffer
	jpeg.Encode(&jpegData, im, nil)
	gif.Encode(&gifData, im, nil)
	// EXIF orientation 6 rotates this 800x400 JPEG to 400x800.
	exif := []byte{'E', 'x', 'i', 'f', 0, 0, 'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 1, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0}
	oriented := append([]byte{0xff, 0xd8, 0xff, 0xe1, 0, byte(len(exif) + 2)}, exif...)
	oriented = append(oriented, jpegData.Bytes()[2:]...)
	webp, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.attachmentRoutes(mux)
	for _, tc := range []struct {
		name, kind string
		data       []byte
		w, h       int
	}{
		{"image.png", "image/png", data, 100, 50},
		{"image.jpg", "image/jpeg", jpegData.Bytes(), 100, 50},
		{"rotated.jpg", "image/jpeg", oriented, 50, 100},
		{"image.gif", "image/gif", gifData.Bytes(), 100, 50},
		{"image.webp", "image/webp", webp, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := domain.Attachment{ID: domain.ID(), ChatID: c.ID, Name: tc.name, MediaType: tc.kind, Size: int64(len(tc.data))}
			// Linked attachments avoid the upload-count budget in this format test.
			m := domain.Message{ID: domain.ID(), ChatID: c.ID, Role: "tool"}
			s.Store.AddMessage(m)
			if err := s.Store.AddGeneratedAttachment(a, m.ID, tc.data); err != nil {
				t.Fatal(err)
			}
			url := "/api/v1/chats/" + c.ID + "/attachments/" + a.ID
			for range 2 {
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, httptest.NewRequest("GET", url+"?width=100", nil))
				if response.Code != 200 {
					t.Fatal(response.Code, response.Body.String())
				}
				config, _, err := image.DecodeConfig(bytes.NewReader(response.Body.Bytes()))
				if err != nil || config.Width != tc.w || config.Height != tc.h {
					t.Fatal(config, err)
				}
				if tc.w > 1 && response.Body.Len() >= len(tc.data) {
					t.Fatal("thumbnail did not save bandwidth")
				}
			}
			original := httptest.NewRecorder()
			mux.ServeHTTP(original, httptest.NewRequest("GET", url, nil))
			if !bytes.Equal(original.Body.Bytes(), tc.data) || original.Header().Get("Content-Type") != tc.kind {
				t.Fatal("original changed")
			}
			for _, width := range []string{"", "0", "-1", "4097", "no", "1.5"} {
				r := httptest.NewRecorder()
				mux.ServeHTTP(r, httptest.NewRequest("GET", url+"?width="+width, nil))
				if r.Code != 400 {
					t.Fatal(width, r.Code)
				}
			}
			other := chat(t, s, domain.Generation{})
			r := httptest.NewRecorder()
			mux.ServeHTTP(r, httptest.NewRequest("GET", strings.Replace(url, c.ID, other.ID, 1)+"?width=100", nil))
			if r.Code != 404 {
				t.Fatal("cross-chat thumbnail", r.Code)
			}
		})
	}
	if err := s.Store.DeleteChat(c.ID); err != nil {
		t.Fatal(err)
	}
	for key := range s.mediaPreviews.entries {
		r := httptest.NewRecorder()
		mux.ServeHTTP(r, httptest.NewRequest("GET", "/api/v1/chats/"+c.ID+"/attachments/"+key.attachment+"?width=100", nil))
		if r.Code != 404 {
			t.Fatal("cached deleted attachment served")
		}
	}
}
