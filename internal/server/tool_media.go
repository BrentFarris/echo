package server

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
)

// recordToolMedia archives an immutable snapshot from a file, in-memory result,
// or image stream. Only metadata crosses the tool/event transport; model input
// is rehydrated from the same archived bytes after the complete tool group.
func (s *Server) recordToolMedia(ctx context.Context, scope domain.ExecutionScope, name, kind string, source io.Reader, modelInput bool) (domain.Attachment, error) {
	var a domain.Attachment
	if scope.ToolMessageID == "" || scope.ChatID == "" {
		return a, errors.New("media inspection requires a durable chat tool message")
	}
	if err := ctx.Err(); err != nil {
		return a, err
	}
	data, err := io.ReadAll(io.LimitReader(source, store.MaxGeneratedAttachmentBytes+1))
	if err != nil {
		return a, err
	}
	if len(data) == 0 || len(data) > store.MaxGeneratedAttachmentBytes {
		return a, errors.New("tool media must be between 1 byte and 256 MiB")
	}
	if !strings.HasPrefix(kind, "image/") && !strings.HasPrefix(kind, "video/") {
		return a, errors.New("unsupported tool media")
	}
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." {
		name = "tool-media"
	}
	a = domain.Attachment{ID: domain.ID(), ChatID: scope.ChatID, Name: name, MediaType: kind, Size: int64(len(data)), ModelInput: modelInput && strings.HasPrefix(kind, "image/")}
	if err := ctx.Err(); err != nil {
		return domain.Attachment{}, err
	}
	err = s.Store.AddGeneratedAttachment(a, scope.ToolMessageID, data)
	return a, err
}

func (s *Server) readImage(ctx context.Context, scope domain.ExecutionScope, filename string) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if scope.ToolMessageID == "" {
		return nil, errors.New("image reading requires a durable chat")
	}
	data, err := s.Files.ReadBytes(scope.Bot, filename, store.MaxAttachmentBytes)
	if err != nil {
		return nil, err
	}
	kind := http.DetectContentType(data)
	switch kind {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return nil, errors.New("read a PNG, JPEG, GIF, or WebP image")
	}
	if err := checkPreviewDimensions(data); err != nil {
		return nil, err
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return nil, errors.New("invalid image contents")
	}
	a, err := s.recordToolMedia(ctx, scope, filename, kind, bytes.NewReader(data), true)
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": filename, "name": a.Name, "mediaType": a.MediaType, "bytes": a.Size, "attachmentId": a.ID}, nil
}
