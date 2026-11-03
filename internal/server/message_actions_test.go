package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
)

func actionRequest(s *Server, method, path, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	s.messageActionRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	return w
}

func TestMessageActionsAPI(t *testing.T) {
	s := testServer(t)
	c := chat(t, s, domain.Generation{})
	for _, m := range []domain.Message{{ID: "user", Role: "user"}, {ID: "assistant", Role: "assistant", Content: "before"}, {ID: "tool", Role: "tool"}} {
		m.ChatID = c.ID
		m.Status = "completed"
		if err := s.Store.AddMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	path := "/api/v1/chats/" + c.ID + "/messages/"
	if err := s.Store.AppendChunk("assistant", " stale", ""); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, id, body string
		success          bool
	}{
		{"PATCH", "assistant", `{"content":"edited **markdown**"}`, true},
		{"PATCH", "user", `{"content":"invalid"}`, false},
		{"PATCH", "assistant", `{"content":"  "}`, false},
		{"PATCH", "missing", `{"content":"invalid"}`, false},
		{"DELETE", "tool", "", false},
	} {
		w := actionRequest(s, tc.method, path+tc.id, tc.body)
		if (w.Code == 200) != tc.success {
			t.Fatalf("%s %s: %d %s", tc.method, tc.id, w.Code, w.Body)
		}
	}
	ms, _ := s.Store.Messages(c.ID)
	if ms[1].Content != "edited **markdown**" {
		t.Fatal(ms)
	}
	other := chat(t, s, domain.Generation{})
	if w := actionRequest(s, "DELETE", "/api/v1/chats/"+other.ID+"/messages/assistant", ""); w.Code == 200 {
		t.Fatal("cross-chat deletion succeeded")
	}
	for _, guard := range []string{"active", "maintenance", "closing"} {
		s.mu.Lock()
		if guard == "active" {
			s.dispatches["active"] = &dispatch{chatID: c.ID}
		}
		s.maintenance = guard == "maintenance"
		if guard == "closing" {
			s.closingChats[c.ID] = 1
		}
		s.mu.Unlock()
		if w := actionRequest(s, "DELETE", path+"assistant", ""); w.Code == 200 {
			t.Fatalf("%s guard failed", guard)
		}
		s.mu.Lock()
		delete(s.dispatches, "active")
		s.maintenance = false
		delete(s.closingChats, c.ID)
		s.mu.Unlock()
	}
	if w := actionRequest(s, "DELETE", path+"user", ""); w.Code != 200 {
		t.Fatal(w.Body)
	}
	ms, _ = s.Store.Messages(c.ID)
	if len(ms) != 2 || ms[0].ID != "assistant" {
		t.Fatal(ms)
	}
}

func TestResubmitTruncatesBeforeModelAndReplays(t *testing.T) {
	s := testServer(t)
	requests := make(chan []model.Message, 4)
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []model.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests <- body.Messages
		stream(w, "new answer", nil)
	})
	c := chat(t, s, g)
	for _, m := range []domain.Message{
		{ID: "earlier", Role: "user", Content: "keep"},
		{ID: "edit", Role: "user", Content: "replace"},
		{ID: "answer", Role: "assistant", Content: "discard answer", BotID: "echo"},
		{ID: "later", Role: "user", Content: "discard later"},
	} {
		m.ChatID = c.ID
		m.Recipients = []string{"echo"}
		m.Status = "completed"
		if err := s.Store.AddMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	// Validation fails before truncation.
	if _, err := s.send(c.ID, SendRequest{Content: "@missing invalid", RequestID: "invalid", replaceMessageID: "edit"}); err == nil {
		t.Fatal("missing recipient accepted")
	}
	ms, _ := s.Store.Messages(c.ID)
	if len(ms) != 4 {
		t.Fatal("validation removed history", ms)
	}
	req := SendRequest{Content: "replacement", RequestID: "edit-request", replaceMessageID: "edit"}
	d, err := s.send(c.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	if d.err != nil {
		t.Fatal(d.err)
	}
	got := <-requests
	text, _ := json.Marshal(got)
	if !strings.Contains(string(text), "replacement") || !strings.Contains(string(text), "keep") || strings.Contains(string(text), "discard") {
		t.Fatal(string(text))
	}
	ms, _ = s.Store.Messages(c.ID)
	if len(ms) != 3 || ms[0].ID != "earlier" || ms[1].Content != "replacement" || ms[2].Content != "new answer" {
		t.Fatal(ms)
	}
	w := actionRequest(s, "POST", "/api/v1/chats/"+c.ID+"/messages/edit/resubmit", `{"content":"replacement","requestId":"edit-request"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), d.id) {
		t.Fatal(w.Code, w.Body)
	}
	ms, _ = s.Store.Messages(c.ID)
	if len(ms) != 3 {
		t.Fatal("replay duplicated history", ms)
	}
}

func TestMessageActionsReachSwarmWorkerHistory(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	requests := make(chan string, 8)
	attachModel(t, b, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []model.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		data, _ := json.Marshal(body.Messages)
		requests <- string(data)
		stream(w, "worker answer", nil)
	})
	c := chatForSwarm(t, a)
	send := func(req SendRequest) string {
		t.Helper()
		d, err := a.send(c.ID, req)
		if err != nil {
			t.Fatal(err)
		}
		waitDispatch(t, d)
		if d.err != nil {
			t.Fatal(d.err)
		}
		select {
		case value := <-requests:
			return value
		default:
			t.Fatal("worker did not call model")
			return ""
		}
	}
	send(SendRequest{Content: "@linux.echo first"})
	ms, _ := a.Store.Messages(c.ID)
	firstUser, firstAssistant := ms[0], ms[len(ms)-1]
	edited := "edited remote answer"
	if err := a.mutateMessage(c.ID, firstAssistant.ID, &edited); err != nil {
		t.Fatal(err)
	}
	if history := send(SendRequest{Content: "@linux.echo second"}); !strings.Contains(history, edited) {
		t.Fatal("worker missed edit", history)
	}
	ms, _ = a.Store.Messages(c.ID)
	secondUser := ms[2]
	if err := a.mutateMessage(c.ID, firstUser.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.mutateMessage(c.ID, firstAssistant.ID, nil); err != nil {
		t.Fatal(err)
	}
	if history := send(SendRequest{Content: "@linux.echo third"}); strings.Contains(history, edited) || strings.Contains(history, "@linux.echo first") {
		t.Fatal("worker retained deleted history", history)
	}
	history := send(SendRequest{Content: "@linux.echo replacement", RequestID: "remote-edit", replaceMessageID: secondUser.ID})
	if strings.Contains(history, "@linux.echo second") || strings.Contains(history, "@linux.echo third") || !strings.Contains(history, "@linux.echo replacement") {
		t.Fatal("worker retained truncated history", history)
	}
}
