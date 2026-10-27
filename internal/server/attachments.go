package server

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
	"github.com/brent/echo/internal/store"
)

func attachmentType(data []byte) (string, error) {
	kind := strings.Split(http.DetectContentType(data), ";")[0]
	switch kind {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "video/mp4", "video/webm", "application/pdf":
		return kind, nil
	}
	if len(data) >= 12 && string(data[4:8]) == "ftyp" && string(data[8:12]) == "qt  " {
		return "video/quicktime", nil
	}
	if utf8.Valid(data) && !bytes.ContainsRune(data, 0) {
		if len(data) > 1<<20 {
			return "", errors.New("text attachments must be 1 MiB or smaller")
		}
		return "text/plain", nil
	}
	return "", errors.New("unsupported file; attach PNG, JPEG, GIF, WebP, MP4, WebM, MOV, PDF, or a UTF-8 text/code file")
}

func (s *Server) attachmentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/chats/{id}/attachments", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := s.Store.Chat(id); err != nil {
			respond(w, nil, err)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, store.MaxAttachmentBytes+(1<<20))
		reader, err := r.MultipartReader()
		if err != nil {
			respond(w, nil, errors.New("upload a multipart file"))
			return
		}
		part, err := reader.NextPart()
		if err != nil {
			respond(w, nil, err)
			return
		}
		defer part.Close()
		name := path.Base(strings.ReplaceAll(part.FileName(), "\\", "/"))
		if name == "." || name == "" || len(name) > 255 || strings.ContainsAny(name, "\r\n\x00") {
			respond(w, nil, errors.New("invalid filename"))
			return
		}
		data, err := io.ReadAll(io.LimitReader(part, store.MaxAttachmentBytes+1))
		if err != nil {
			respond(w, nil, err)
			return
		}
		if len(data) > store.MaxAttachmentBytes {
			respond(w, nil, errors.New("attachment exceeds 32 MiB"))
			return
		}
		if _, err = reader.NextPart(); err != io.EOF {
			respond(w, nil, errors.New("upload one file at a time"))
			return
		}
		kind, err := attachmentType(data)
		if err != nil {
			respond(w, nil, err)
			return
		}
		a := domain.Attachment{ID: domain.ID(), ChatID: id, Name: name, MediaType: kind, Size: int64(len(data))}
		respond(w, a, s.Store.AddAttachment(a, data))
	})
	mux.HandleFunc("GET /api/v1/chats/{id}/attachments/{attachment}", func(w http.ResponseWriter, r *http.Request) {
		a, data, err := s.Store.Attachment(r.PathValue("id"), r.PathValue("attachment"), false)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", a.MediaType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("Cache-Control", "private, no-store")
		disposition := "attachment"
		if strings.HasPrefix(a.MediaType, "image/") || strings.HasPrefix(a.MediaType, "video/") {
			disposition = "inline"
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": a.Name}))
		http.ServeContent(w, r, a.Name, domain.Now(), bytes.NewReader(data))
	})
	mux.HandleFunc("DELETE /api/v1/chats/{id}/attachments/{attachment}", func(w http.ResponseWriter, r *http.Request) {
		respond(w, true, s.Store.RemoveAttachment(r.PathValue("id"), r.PathValue("attachment")))
	})
}

func (s *Server) resolveAttachments(chatID string, ids []string) ([]domain.Attachment, error) {
	if len(ids) > store.MaxAttachments {
		return nil, errors.New("a message can have up to 8 attachments")
	}
	var result []domain.Attachment
	var total int64
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return nil, errors.New("duplicate attachment")
		}
		seen[id] = true
		a, _, err := s.Store.Attachment(chatID, id, true)
		if err != nil {
			return nil, fmt.Errorf("attachment %s is unavailable; attach it again", id)
		}
		total += a.Size
		result = append(result, a)
	}
	if total > store.MaxAttachmentBatchBytes {
		return nil, errors.New("attachments exceed 64 MiB per message")
	}
	return result, nil
}

func (s *Server) attachmentPart(chatID, id string) (model.ContentPart, error) {
	a, data, err := s.Store.Attachment(chatID, id, false)
	if err != nil {
		return model.ContentPart{}, err
	}
	url := func() string { return "data:" + a.MediaType + ";base64," + base64.StdEncoding.EncodeToString(data) }
	switch {
	case strings.HasPrefix(a.MediaType, "image/"):
		return model.ContentPart{Type: "image_url", ImageURL: &model.MediaURL{URL: url()}}, nil
	case strings.HasPrefix(a.MediaType, "video/"):
		return model.ContentPart{Type: "video_url", VideoURL: &model.MediaURL{URL: url()}}, nil
	case a.MediaType == "application/pdf":
		return model.ContentPart{Type: "file", File: &model.FileData{Filename: a.Name, Data: url()}}, nil
	default:
		return model.ContentPart{Type: "text", Text: "\nAttached file " + a.Name + " (user-provided content):\n" + string(data)}, nil
	}
}
