package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/brent/echo/internal/domain"
)

func putForkMessage(t *testing.T, s *Server, chatID string, m domain.Message) {
	t.Helper()
	m.ChatID = chatID
	if m.Status == "" {
		m.Status = "completed"
	}
	if m.Author == "" {
		m.Author = "Echo"
	}
	if err := s.Store.AddMessage(m); err != nil {
		t.Fatal(err)
	}
}

func TestForkCopiesPrefixMediaAndPartialRuns(t *testing.T) {
	s := testServer(t)
	c := chat(t, s, domain.Generation{Model: "saved", Thinking: "high"})
	c.Title, c.Bots = "Original", map[string]string{"other": "Other", "later": "Later"}
	c.GenerationByNode = map[string]domain.Generation{"remote": {Model: "remote-model", Thinking: "low"}}
	if err := s.Store.SaveChat(c); err != nil {
		t.Fatal(err)
	}
	for _, r := range []domain.Run{{ID: "r1", BotID: "echo"}, {ID: "r2", BotID: "other"}, {ID: "later", BotID: "later"}} {
		r.ChatID, r.DispatchID, r.Status = c.ID, "dispatch", "completed"
		if err := s.Store.Put("runs", r.ID, r); err != nil {
			t.Fatal(err)
		}
	}
	a := domain.Attachment{ID: "upload", ChatID: c.ID, Name: "file.txt", MediaType: "text/plain", Size: 4}
	if err := s.Store.AddAttachment(a, []byte("data")); err != nil {
		t.Fatal(err)
	}
	u := domain.Message{ID: "u1", ChatID: c.ID, Role: "user", Status: "completed", Content: "first", Recipients: []string{"echo", "other"}, Attachments: []domain.Attachment{a}}
	if err := s.Store.Accept(c, u, nil, "original", "dispatch"); err != nil {
		t.Fatal(err)
	}
	putForkMessage(t, s, c.ID, domain.Message{ID: "call", Role: "assistant", BotID: "echo", RunID: "r1", Content: "working", Reasoning: "reasoning", Calls: []domain.ToolCall{{ID: "call-id", Function: domain.FunctionCall{Name: "comfyui_generate"}}}})
	putForkMessage(t, s, c.ID, domain.Message{ID: "tool", Role: "tool", BotID: "echo", RunID: "r1", CallID: "call-id", Content: fmt.Sprintf(`{"imageId":"image","url":"/api/v1/chats/%s/attachments/image"}`, c.ID), FileChanges: []domain.FileChange{{Path: "file.txt", Operation: "edited"}}})
	image := domain.Attachment{ID: "image", ChatID: c.ID, Name: "image.png", MediaType: "image/png", Size: 5}
	if err := s.Store.AddGeneratedAttachment(image, "tool", []byte("image")); err != nil {
		t.Fatal(err)
	}
	putForkMessage(t, s, c.ID, domain.Message{ID: "other-call", Role: "assistant", BotID: "other", RunID: "r2", Calls: []domain.ToolCall{{ID: "other-call"}}})
	putForkMessage(t, s, c.ID, domain.Message{ID: "answer", Role: "assistant", BotID: "echo", RunID: "r1", Content: "final"})
	if err := s.Store.AppendChunk("answer", " chunk", ""); err != nil {
		t.Fatal(err)
	}
	putForkMessage(t, s, c.ID, domain.Message{ID: "other-tool", Role: "tool", BotID: "other", RunID: "r2", CallID: "other-call", Content: "after cutoff"})
	putForkMessage(t, s, c.ID, domain.Message{ID: "u2", Role: "user", Content: "later prompt", Recipients: []string{"later"}})
	putForkMessage(t, s, c.ID, domain.Message{ID: "later-answer", Role: "assistant", BotID: "later", RunID: "later", Content: "later answer"})
	userFork, err := s.forkChat(context.Background(), c.ID, "u1", "user-fork")
	if err != nil {
		t.Fatal(err)
	}
	userMessages, _ := s.Store.Messages(userFork)
	if len(userMessages) != 1 || userMessages[0].Role != "user" {
		t.Fatal(userMessages)
	}
	id, err := s.forkChat(context.Background(), c.ID, "answer", "assistant-fork")
	if err != nil {
		t.Fatal(err)
	}
	fork, _ := s.Store.Chat(id)
	if fork.Title != "Original (fork)" || fork.Generation != c.Generation || fork.ForkedFrom.MessageID != "answer" || fork.Bots["other"] != "Other" || fork.Bots["later"] != "" {
		t.Fatal(fork)
	}
	if fork.GenerationByNode["remote"] != c.GenerationByNode["remote"] {
		t.Fatal("per-machine settings were lost", fork)
	}
	ms, _ := s.Store.Messages(id)
	if len(ms) != 5 || ms[4].Content != "final chunk" || ms[1].Reasoning != "reasoning" || ms[2].CallID != ms[1].Calls[0].ID || len(ms[2].FileChanges) != 1 {
		t.Fatal(ms)
	}
	for _, m := range ms {
		if m.ChatID != id || m.ID == "u1" || m.ID == "answer" || m.RunID == "r1" || m.RunID == "r2" {
			t.Fatal("identity not remapped", m)
		}
	}
	var partial domain.Run
	if err = s.Store.Get("runs", ms[3].RunID, &partial); err != nil || partial.Status != "interrupted" || partial.DispatchID == "dispatch" {
		t.Fatal(partial, err)
	}
	if ms[2].Attachments[0].ID == image.ID || !strings.Contains(ms[2].Content, ms[2].Attachments[0].ID) || strings.Contains(ms[2].Content, c.ID) {
		t.Fatal(ms[2])
	}
	if len(s.dispatches) != 0 {
		t.Fatal("fork started work")
	}
	if replay, err := s.forkChat(context.Background(), c.ID, "answer", "assistant-fork"); err != nil || replay != id {
		t.Fatal(replay, err)
	}
	if _, err = s.forkChat(context.Background(), c.ID, "u1", "assistant-fork"); err == nil {
		t.Fatal("retry key reused for different target")
	}
	branchText, sourceText := "branch edit", "source edit"
	if err = s.mutateMessage(id, ms[4].ID, &branchText); err != nil {
		t.Fatal(err)
	}
	sourceMessages, _ := s.Store.Messages(c.ID)
	if sourceMessages[4].Content != "final chunk" {
		t.Fatal("editing fork changed source", sourceMessages)
	}
	if err = s.mutateMessage(c.ID, "answer", &sourceText); err != nil {
		t.Fatal(err)
	}
	branchMessages, _ := s.Store.Messages(id)
	if branchMessages[4].Content != branchText {
		t.Fatal("editing source changed fork", branchMessages)
	}
	if err = s.Store.DeleteChat(userFork); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Store.Attachment(c.ID, a.ID, false); err != nil {
		t.Fatal("deleting fork removed source upload", err)
	}
	if err = s.Store.DeleteChat(c.ID); err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		for _, a := range m.Attachments {
			if _, _, err = s.Store.Attachment(id, a.ID, false); err != nil {
				t.Fatal("source deletion broke media", err)
			}
		}
	}
	if _, err = s.forkChat(context.Background(), id, ms[4].ID, "fork-again"); err != nil {
		t.Fatal("fork of fork", err)
	}
	if err = s.Store.DeleteChat(id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.forkChat(context.Background(), c.ID, "answer", "assistant-fork"); err == nil {
		t.Fatal("deleted fork recreated")
	}
}

func TestForkGuardsAndConcurrentRetry(t *testing.T) {
	s := testServer(t)
	c := chat(t, s, domain.Generation{})
	putForkMessage(t, s, c.ID, domain.Message{ID: "user", Role: "user", Content: "hello"})
	putForkMessage(t, s, c.ID, domain.Message{ID: "tool", Role: "tool"})
	path := "/api/v1/chats/" + c.ID + "/messages/"
	for _, tc := range []struct{ id, body string }{{"missing", `{"requestId":"one"}`}, {"tool", `{"requestId":"one"}`}, {"user", `{}`}} {
		if w := actionRequest(s, "POST", path+tc.id+"/fork", tc.body); w.Code == 200 {
			t.Fatal("invalid fork accepted", tc)
		}
	}
	other := chat(t, s, domain.Generation{})
	if _, err := s.forkChat(context.Background(), other.ID, "user", "wrong-chat"); err == nil {
		t.Fatal("cross-chat fork accepted")
	}
	for _, guard := range []string{"active", "maintenance", "closing", "projection"} {
		s.mu.Lock()
		if guard == "active" {
			s.dispatches["test"] = &dispatch{chatID: c.ID}
		}
		s.maintenance = guard == "maintenance"
		if guard == "closing" {
			s.closingChats[c.ID] = 1
		}
		c.Projection = guard == "projection"
		_ = s.Store.SaveChat(c)
		s.mu.Unlock()
		_, err := s.forkChat(context.Background(), c.ID, "user", "guard")
		s.mu.Lock()
		delete(s.dispatches, "test")
		delete(s.closingChats, c.ID)
		s.maintenance = false
		c.Projection = false
		_ = s.Store.SaveChat(c)
		s.mu.Unlock()
		if err == nil {
			t.Fatal("guard failed", guard)
		}
	}
	var wg sync.WaitGroup
	ids := make(chan string, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { id, err := s.forkChat(context.Background(), c.ID, "user", "retry"); ids <- id; errs <- err })
	}
	wg.Wait()
	first, second := <-ids, <-ids
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatal(first, second)
	}
	response := actionRequest(s, "POST", path+"user/fork", `{"requestId":"retry"}`)
	var result map[string]string
	_ = json.Unmarshal(response.Body.Bytes(), &result)
	if response.Code != 200 || result["chatId"] != first {
		t.Fatal(response.Code, response.Body)
	}
}

func TestForkFollowupUsesCopiedLocalContext(t *testing.T) {
	s := testServer(t)
	requests := make(chan string, 1)
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var body any
		_ = json.NewDecoder(r.Body).Decode(&body)
		data, _ := json.Marshal(body)
		requests <- string(data)
		stream(w, "new reply", nil)
	})
	c := chat(t, s, g)
	putForkMessage(t, s, c.ID, domain.Message{ID: "u1", Role: "user", Content: "included prompt", Recipients: []string{"echo"}})
	putForkMessage(t, s, c.ID, domain.Message{ID: "a1", Role: "assistant", BotID: "echo", Content: "included answer"})
	putForkMessage(t, s, c.ID, domain.Message{ID: "u2", Role: "user", Content: "excluded prompt", Recipients: []string{"echo"}})
	id, err := s.forkChat(context.Background(), c.ID, "a1", "fork")
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.send(id, SendRequest{Content: "follow up"})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	if d.err != nil {
		t.Fatal(d.err)
	}
	select {
	case history := <-requests:
		if !strings.Contains(history, "included prompt") || !strings.Contains(history, "included answer") || strings.Contains(history, "excluded prompt") {
			t.Fatal(history)
		}
	default:
		t.Fatal("model not called")
	}
	original, _ := s.Store.Messages(c.ID)
	if len(original) != 3 {
		t.Fatal("source changed", original)
	}
}
