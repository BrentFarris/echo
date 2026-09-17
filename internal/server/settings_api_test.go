package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/brent/echo/internal/llm"
)

func TestSettingsColumnGuides(t *testing.T) {
	s, _ := newTestServer(t)
	cfg := llm.DefaultSettings()
	cfg.EditorColumnGuidesEnabled = true
	cfg.EditorColumnGuides = []int{120, 90, 80, 90}
	body, err := json.Marshal(map[string]any{"settings": cfg})
	if err != nil {
		t.Fatal(err)
	}
	put := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, req)
		return rr
	}
	if rr := put(string(body)); rr.Code != http.StatusOK {
		t.Fatalf("save guides: %d %s", rr.Code, rr.Body.String())
	}
	for _, columns := range []string{`[0]`, `[-1]`, `[10001]`, `[80.5]`, `["80"]`} {
		rr := put(`{"settings":{"editorColumnGuidesEnabled":true,"editorColumnGuides":` + columns + `}}`)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("invalid guides %s: got %d %s", columns, rr.Code, rr.Body.String())
		}
	}
	rr := doRequest(t, s, http.MethodGet, "/api/settings")
	var envelope struct {
		Data struct {
			Settings llm.Settings `json:"settings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	got := envelope.Data.Settings
	if !got.EditorColumnGuidesEnabled || !slices.Equal(got.EditorColumnGuides, []int{80, 90, 120}) {
		t.Fatalf("invalid requests changed saved guides: %v / %v", got.EditorColumnGuidesEnabled, got.EditorColumnGuides)
	}
}
