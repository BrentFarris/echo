package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
)

func TestRerunPreservesInputAndReplacesSuffix(t *testing.T) {
	for _, role := range []string{"user", "assistant"} {
		t.Run(role, func(t *testing.T) {
			s := testServer(t)
			requests := make(chan string, 8)
			g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
				var body any
				_ = json.NewDecoder(r.Body).Decode(&body)
				data, _ := json.Marshal(body)
				requests <- string(data)
				stream(w, "new answer", nil)
			})
			root := t.TempDir()
			path := filepath.Join(root, "reference.txt")
			if err := os.WriteFile(path, []byte("reference bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			bot, err := s.saveBot(domain.Bot{Handle: "builder", Name: "Builder", Roots: []string{root}})
			if err != nil {
				t.Fatal(err)
			}
			c := chat(t, s, g)
			a := domain.Attachment{ID: domain.ID(), ChatID: c.ID, Name: "upload.txt", MediaType: "text/plain", Size: 6}
			if err = s.Store.AddAttachment(a, []byte("upload")); err != nil {
				t.Fatal(err)
			}
			send := func(req SendRequest) *dispatch {
				t.Helper()
				d, err := s.send(c.ID, req)
				if err != nil {
					t.Fatal(err)
				}
				waitDispatch(t, d)
				if d.err != nil {
					t.Fatal(d.err)
				}
				return d
			}
			send(SendRequest{Content: "@builder original", Attachments: []string{a.ID}, Segments: []domain.Segment{{Type: "file", Path: path}}})
			<-requests
			ms, _ := s.Store.Messages(c.ID)
			user, answer := ms[0], ms[len(ms)-1]
			target := user.ID
			if role == "assistant" {
				target = answer.ID
			}
			send(SendRequest{Content: "@builder later prompt"})
			<-requests
			// A rerun uses stable bot identity and the original recorded model.
			bot.Handle = "renamed"
			if _, err = s.saveBot(bot); err != nil {
				t.Fatal(err)
			}
			changed := g
			changed.Model = "different-model"
			if err = s.Store.SetChatGeneration(c.ID, changed); err != nil {
				t.Fatal(err)
			}
			d := send(SendRequest{rerunMessageID: target, RequestID: "rerun"})
			body := <-requests
			if !strings.Contains(body, "@builder original") || strings.Contains(body, "later prompt") || strings.Contains(body, "new answer") || strings.Contains(body, "different-model") {
				t.Fatal(body)
			}
			ms, _ = s.Store.Messages(c.ID)
			if len(ms) != 2 || ms[0].ID == user.ID || ms[1].ID == answer.ID || ms[0].Content != user.Content || ms[0].Recipients[0] != bot.ID {
				t.Fatal(ms)
			}
			if len(ms[0].Attachments) != 1 || ms[0].Attachments[0].ID != a.ID {
				t.Fatal(ms[0])
			}
			if _, bytes, err := s.Store.Attachment(c.ID, a.ID, false); err != nil || string(bytes) != "upload" {
				t.Fatal(err, string(bytes))
			}
			found := false
			for _, seg := range ms[0].Segments {
				if seg.Type == "file" && seg.Path == path && len(seg.AllowedBots) == 1 && seg.AllowedBots[0] == bot.ID {
					found = true
				}
			}
			if !found {
				t.Fatal("file reference was not retained and revalidated", ms[0])
			}
			w := actionRequest(s, "POST", "/api/v1/chats/"+c.ID+"/messages/"+target+"/rerun", `{"requestId":"rerun"}`)
			if w.Code != 200 || !strings.Contains(w.Body.String(), d.id) {
				t.Fatal(w.Code, w.Body)
			}
			select {
			case <-requests:
				t.Fatal("retry generated a second response")
			default:
			}
			// Forks preserve the relationship using fresh message identities.
			fork, err := s.forkChat(context.Background(), c.ID, ms[1].ID, "fork")
			if err != nil {
				t.Fatal(err)
			}
			fm, _ := s.Store.Messages(fork)
			fd, err := s.send(fork, SendRequest{rerunMessageID: fm[1].ID, RequestID: "fork-rerun"})
			if err != nil {
				t.Fatal(err)
			}
			waitDispatch(t, fd)
			if fd.err != nil {
				t.Fatal(fd.err)
			}
			<-requests
		})
	}
}

func TestRerunGuardsAndMissingInput(t *testing.T) {
	s := testServer(t)
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) { stream(w, "answer", nil) })
	c := chat(t, s, g)
	d, err := s.send(c.ID, SendRequest{Content: "original"})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	ms, _ := s.Store.Messages(c.ID)
	user, answer := ms[0], ms[len(ms)-1]
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
		if _, err := s.send(c.ID, SendRequest{rerunMessageID: answer.ID, RequestID: "guard"}); err == nil {
			t.Fatal("guard failed", guard)
		}
		s.mu.Lock()
		delete(s.dispatches, "test")
		delete(s.closingChats, c.ID)
		s.maintenance = false
		c.Projection = false
		_ = s.Store.SaveChat(c)
		s.mu.Unlock()
	}
	other := chat(t, s, g)
	if _, err := s.send(other.ID, SendRequest{rerunMessageID: answer.ID, RequestID: "wrong"}); err == nil {
		t.Fatal("cross-chat rerun accepted")
	}
	prepared, err := s.prepareResubmit(c.ID, SendRequest{rerunMessageID: answer.ID, RequestID: "race"})
	if err != nil {
		t.Fatal(err)
	}
	text := "edited answer"
	if err = s.mutateMessage(c.ID, answer.ID, &text); err != nil {
		t.Fatal(err)
	}
	if err = s.checkRerunSnapshot(c.ID, prepared); err == nil {
		t.Fatal("stale rerun accepted")
	}
	if err = s.mutateMessage(c.ID, user.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.send(c.ID, SendRequest{rerunMessageID: answer.ID, RequestID: "deleted"}); err == nil {
		t.Fatal("deleted input accepted")
	}
	visible, _ := s.messages(c.ID)
	if len(visible) != 1 || !visible[0].RerunUnavailable {
		t.Fatal(visible)
	}
}

func TestRerunLegacyInputAssociation(t *testing.T) {
	at := time.Now()
	users := []domain.Message{{ID: "first", Role: "user", CreatedAt: at}, {ID: "second", Role: "user", CreatedAt: at.Add(2 * time.Second)}}
	runs := []domain.Run{{ID: "r1", DispatchID: "d1", CreatedAt: at.Add(time.Second)}, {ID: "r2", DispatchID: "d2", CreatedAt: at.Add(3 * time.Second)}}
	if got := rerunInputs(users, runs); got["r1"].ID != "first" || got["r2"].ID != "second" {
		t.Fatal(got)
	}
	if got := rerunInputs(users[:1], runs); got["r2"].ID != "" {
		t.Fatal("deleted legacy input resolved to older input", got)
	}
}

func TestRerunSwarmReconcilesOriginalTurn(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	requests := make(chan string, 4)
	attachModel(t, b, func(w http.ResponseWriter, r *http.Request) {
		var body any
		_ = json.NewDecoder(r.Body).Decode(&body)
		data, _ := json.Marshal(body)
		requests <- string(data)
		stream(w, "remote answer", nil)
	})
	c := chatForSwarm(t, a)
	send := func(req SendRequest) {
		t.Helper()
		d, err := a.send(c.ID, req)
		if err != nil {
			t.Fatal(err)
		}
		waitDispatch(t, d)
		if d.err != nil {
			t.Fatal(d.err)
		}
	}
	send(SendRequest{Content: "@linux.echo original"})
	<-requests
	ms, _ := a.Store.Messages(c.ID)
	answer := ms[len(ms)-1]
	send(SendRequest{Content: "@linux.echo later"})
	<-requests
	send(SendRequest{rerunMessageID: answer.ID, RequestID: "rerun"})
	body := <-requests
	if strings.Count(body, "@linux.echo original") != 1 || strings.Contains(body, "@linux.echo later") || strings.Contains(body, "remote answer") {
		t.Fatal(body)
	}
	ms, _ = a.Store.Messages(c.ID)
	if len(ms) != 2 {
		t.Fatal(ms)
	}
	w := actionRequest(a, "POST", fmt.Sprintf("/api/v1/chats/%s/messages/%s/rerun", c.ID, answer.ID), `{"requestId":"rerun"}`)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
}
