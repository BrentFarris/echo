package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
)

func TestReferenceRangesValidateUTF16AndRouting(t *testing.T) {
	s := testServer(t)
	bot, err := s.saveBot(domain.Bot{Handle: "kaiju", Name: "Kaiju", Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	content := "🙂 @kaiju #notes@missing.txt #notes@missing.txt"
	start := utf16Offset("🙂 @kaiju ")
	ref := domain.Segment{Type: "file", Text: "notes@missing.txt", Path: filepath.Join(bot.Roots[0], "notes@missing.txt"), Range: &domain.SegmentRange{Start: start, End: start + 18}}
	// Use string lengths rather than hard-coded byte offsets for non-ASCII input.
	ref.Range.End = start + utf16Offset("#"+ref.Text)
	other := ref
	other.Range = &domain.SegmentRange{Start: ref.Range.End + 1, End: utf16Offset(content)}
	req := SendRequest{Content: content, Segments: []domain.Segment{ref, other}}
	bots, err := s.recipients(req)
	if err != nil || len(bots) != 1 || bots[0].ID != bot.ID {
		t.Fatalf("routing: %v %v", bots, err)
	}
	segments := structuredSegments(req, bots)
	if segments[1].Type != "bot" || segments[1].Range.Start != 3 || segments[1].Range.End != 9 {
		t.Fatalf("bot offsets: %+v", segments)
	}
	for _, invalid := range []*domain.SegmentRange{{Start: 1, End: 3}, {Start: -1, End: 3}, {Start: start, End: 999}, {Start: start + 1, End: ref.Range.End}} {
		bad := ref
		bad.Range = invalid
		if _, err := referenceContent(SendRequest{Content: content, Segments: []domain.Segment{bad}}); err == nil {
			t.Fatalf("accepted invalid range %+v", invalid)
		}
	}
	if _, err := referenceContent(SendRequest{Content: content, Segments: []domain.Segment{ref, ref}}); err == nil {
		t.Fatal("accepted overlapping ranges")
	}
}

func TestReferencePersistenceAndReveal(t *testing.T) {
	s := testServer(t)
	root := t.TempDir()
	path := filepath.Join(root, "My File.txt")
	if err := os.WriteFile(path, []byte("reference"), 0600); err != nil {
		t.Fatal(err)
	}
	bot, err := s.saveBot(domain.Bot{Handle: "kaiju", Name: "Kaiju", Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) { stream(w, "Done", nil) })
	c := chat(t, s, g)
	content := "🙂 @kaiju inspect #My File.txt"
	ref := domain.Segment{Type: "file", Text: "My File.txt", Path: path, Kind: "directory", AllowedBots: []string{"spoof"}, Range: &domain.SegmentRange{Start: utf16Offset("🙂 @kaiju inspect "), End: utf16Offset(content)}}
	req := SendRequest{Content: content, Segments: []domain.Segment{ref}, Generation: g}
	validation := scmAPI(t, s, "POST", "/references/validate", req)
	var validated []domain.Segment
	if validation.Code != 200 || json.Unmarshal(validation.Body.Bytes(), &validated) != nil || len(validated) != 1 || validated[0].Kind != "file" || validated[0].AllowedBots[0] != bot.ID {
		t.Fatal(validation.Body.String())
	}
	d, err := s.send(c.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	ms, err := s.Store.Messages(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	user := ms[0]
	saved := user.Segments[len(user.Segments)-1]
	if saved.Kind != "file" || saved.Range == nil || *saved.Range != *ref.Range || saved.Path != path {
		t.Fatalf("lost reference: %+v", saved)
	}
	var revealed []broker.RevealTarget
	s.revealFile = func(target broker.RevealTarget) error { revealed = append(revealed, target); return nil }
	reveal := func(chatID, target string) *httptest.ResponseRecorder {
		return scmAPI(t, s, "POST", "/chats/"+chatID+"/references/reveal", map[string]any{"messageId": user.ID, "path": target})
	}
	if response := reveal(c.ID, path); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	draft := map[string]any{"content": content, "segments": req.Segments, "path": path}
	if response := scmAPI(t, s, "POST", "/references/reveal", draft); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if len(revealed) != 2 || !revealed[0].Select {
		t.Fatal(revealed)
	}
	if response := reveal(chat(t, s, g).ID, path); response.Code == 200 {
		t.Fatal("cross-chat reference accepted")
	}
	if response := reveal(c.ID, filepath.Join(root, "other.txt")); response.Code == 200 {
		t.Fatal("unrecorded path accepted")
	}
	remote := httptest.NewRequest("POST", "http://localhost/", strings.NewReader(`{}`))
	remote.RemoteAddr = "192.0.2.1:1234"
	for _, handler := range []http.HandlerFunc{s.revealDraftReference, s.revealMessageReference} {
		response := httptest.NewRecorder()
		handler(response, remote)
		if response.Code != 403 {
			t.Fatal("remote native reveal accepted")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if response := reveal(c.ID, path); response.Code != 200 || !strings.Contains(response.Body.String(), `"fallback":true`) || revealed[len(revealed)-1].Select {
		t.Fatal(response.Body.String())
	}
	bot.Roots = []string{t.TempDir()}
	if _, err := s.saveBot(bot); err != nil {
		t.Fatal(err)
	}
	if response := reveal(c.ID, path); response.Code == 200 {
		t.Fatal("scope revocation ignored")
	}
	if response := scmAPI(t, s, "POST", "/references/reveal", draft); response.Code == 200 {
		t.Fatal("draft scope revocation ignored")
	}
	if err := s.Store.Delete("bots", bot.ID); err != nil {
		t.Fatal(err)
	}
	if response := reveal(c.ID, path); response.Code == 200 {
		t.Fatal("deleted bot accepted")
	}
}
