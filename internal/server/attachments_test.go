package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
)

func TestAttachmentsReachOnlyRecipientsAndDeleteWithChat(t *testing.T) {
	s := testServer(t)
	var received []model.Message
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []model.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		received = request.Messages
		stream(w, "Saw the attachment", nil)
	})
	c := chat(t, s, g)
	image, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScLttAAAAABJRU5ErkJggg==")
	a := domain.Attachment{ID: domain.ID(), ChatID: c.ID, Name: "paste.png", MediaType: "image/png", Size: int64(len(image))}
	if err := s.Store.AddAttachment(a, image); err != nil {
		t.Fatal(err)
	}
	other := chat(t, s, g)
	if _, err := s.resolveAttachments(other.ID, []string{a.ID}); err == nil {
		t.Fatal("cross-chat attachment accepted")
	}
	if _, err := s.resolveAttachments(c.ID, []string{a.ID, a.ID}); err == nil {
		t.Fatal("duplicate accepted")
	}
	d, err := s.send(c.ID, SendRequest{Attachments: []string{a.ID}, Generation: g})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	last := received[len(received)-1]
	if len(last.Parts) != 1 || last.Parts[0].ImageURL == nil || !strings.HasPrefix(last.Parts[0].ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("lost image: %+v", last)
	}
	if _, err := s.resolveAttachments(c.ID, []string{a.ID}); err == nil {
		t.Fatal("sent upload reused")
	}
	if err := s.Store.RemoveAttachment(c.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Store.Attachment(c.ID, a.ID, false); err != nil {
		t.Fatal("accepted attachment removed by draft cleanup", err)
	}
	history, err := s.history(domain.Bot{ID: "outsider"}, c.ID)
	if err != nil || len(history) != 1 {
		t.Fatalf("attachment leaked to other bot: %+v %v", history, err)
	}
	ms, err := s.Store.Messages(c.ID)
	if err != nil || len(ms[0].Attachments) != 1 || ms[0].Attachments[0].Name != "paste.png" {
		t.Fatal(ms, err)
	}
	if err := s.Store.DeleteChat(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Store.Attachment(c.ID, a.ID, false); err == nil {
		t.Fatal("deleted chat retained attachment")
	}
	if err := s.Store.AddAttachment(a, image); err == nil {
		t.Fatal("deleted chat resurrected by upload")
	}
}

func TestAttachmentUploadSniffsContentAndDownloadIsInert(t *testing.T) {
	s := testServer(t)
	c := chat(t, s, domain.Generation{})
	mux := http.NewServeMux()
	s.attachmentRoutes(mux)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "unsafe.html")
	part.Write([]byte("<script>alert('unsafe')</script>"))
	writer.Close()
	req := httptest.NewRequest("POST", "/api/v1/chats/"+c.ID+"/attachments", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var a domain.Attachment
	if err := json.Unmarshal(response.Body.Bytes(), &a); err != nil || a.MediaType != "text/plain" {
		t.Fatal(a, err)
	}
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/chats/"+c.ID+"/attachments/"+a.ID, nil))
	if response.Header().Get("Content-Type") != "text/plain" || response.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal(response.Header())
	}
	partValue, err := s.attachmentPart(c.ID, a.ID)
	if err != nil || partValue.Type != "text" || !strings.Contains(partValue.Text, "unsafe.html") {
		t.Fatal(partValue, err)
	}
	if _, err := attachmentType([]byte{'M', 'Z', 0, 0}); err == nil {
		t.Fatal("accepted binary executable")
	}
	if _, err := attachmentType(bytes.Repeat([]byte("a"), (1<<20)+1)); err == nil {
		t.Fatal("accepted oversized text")
	}
}
