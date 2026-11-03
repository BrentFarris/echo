package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/brent/echo/internal/contextmanager"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
)

func TestCompactionUsesEndpointThresholdAndPreservesMedia(t *testing.T) {
	s := testServer(t)
	calls := 0
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) { calls++; stream(w, "Earlier context summary", nil) })
	var e domain.Endpoint
	s.Store.Get("endpoints", g.EndpointID, &e)
	e.ContextTokens = 6000
	e.Compact = true
	e.CompressionThreshold = 90
	history := []model.Message{{Role: "system", Content: "Help the user"}}
	for i := 0; i < 9; i++ {
		history = append(history, model.Message{Role: "user", Content: strings.Repeat("a", 250)})
	}
	history[len(history)-1].Parts = []model.ContentPart{{Type: "image_url", ImageURL: &model.MediaURL{URL: "data:image/png;base64," + strings.Repeat("A", 30000)}}}
	manager := contextmanager.Manager{}
	_, before, err := manager.Complete(context.Background(), contextmanager.Request{History: history, Endpoint: e, Generation: g, Complete: s.contextCompletion("")})
	if err != nil || len(before) != len(history) || calls != 1 {
		t.Fatal("binary data was counted as text tokens", err, calls)
	}
	e.CompressionThreshold = 30
	_, after, err := manager.Complete(context.Background(), contextmanager.Request{History: history, Endpoint: e, Generation: g, Complete: s.contextCompletion("")})
	if err != nil || len(after) >= len(history) || calls != 3 {
		t.Fatal("configured threshold not used", err, calls)
	}
	if len(after[len(after)-1].Parts) != 1 || len(history[len(history)-1].Parts) != 1 {
		t.Fatal("recent media was lost or original history mutated")
	}
}
