package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
	"github.com/brent/echo/internal/swarm"
)

func TestForkProjectionFiltersAndRevalidatesHistory(t *testing.T) {
	s := testServer(t)
	root, outside := t.TempDir(), t.TempDir()
	allowed, denied := filepath.Join(root, "allowed.txt"), filepath.Join(outside, "denied.txt")
	for _, path := range []string{allowed, denied} {
		if err := os.WriteFile(path, []byte("current workspace"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bot, err := s.saveBot(domain.Bot{Handle: "builder", Name: "Builder", Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	self := s.Swarm.Node().ID
	qualified := swarm.Key(self, bot.ID)
	c := domain.Chat{ID: projectionID("home", "fork"), Projection: true, ForkedFrom: &domain.ForkOrigin{ChatID: "deleted-source"}}
	if err = s.Store.CreateChat(c); err != nil {
		t.Fatal(err)
	}
	transcript := []domain.Message{
		{ID: "user", Role: "user", Status: "completed", Recipients: []string{qualified, "home::echo"}, Segments: []domain.Segment{
			{Type: "bot", BotID: qualified},
			{Type: "file", NodeID: self, Path: allowed, AllowedBots: []string{"foreign-grant"}, Kind: "directory"},
			{Type: "file", NodeID: self, Path: denied, AllowedBots: []string{bot.ID}, Kind: "file"},
			{Type: "file", NodeID: "home", Path: allowed, AllowedBots: []string{bot.ID}, Kind: "file"},
		}},
		{ID: swarm.Key(self, "call"), Role: "assistant", RunID: "historical", BotID: qualified, Status: "completed", Calls: []domain.ToolCall{{ID: "question-call", Function: domain.FunctionCall{Name: AskUserQuestionsToolName}}}},
		{ID: swarm.Key(self, "answer"), Role: "tool", RunID: "historical", BotID: qualified, Status: "completed", CallID: "question-call", Content: `{"questions":[{"id":"q","question":"Which?"}],"answers":[{"questionId":"q","text":"Chosen"}]}`},
		{ID: "unaddressed", Role: "user", Recipients: []string{"another::echo"}},
		{ID: "another::output", Role: "assistant", RunID: "other", BotID: "another::echo"},
		{ID: "current", Role: "user", Recipients: []string{qualified}},
	}
	req := workerRequest{Home: "home", Chat: domain.Chat{ID: "fork"}, Message: domain.Message{ID: "current"}}
	if err = s.seedForkHistory(context.Background(), req, c, transcript); err != nil {
		t.Fatal(err)
	}
	ms, err := s.Store.Messages(c.ID)
	if err != nil || len(ms) != 3 || ms[0].ID != "user" || ms[1].ID != "call" || ms[2].ID != "answer" {
		t.Fatal(ms, err)
	}
	if len(ms[0].Recipients) != 1 || ms[0].Recipients[0] != bot.ID || ms[0].Segments[0].BotID != bot.ID || ms[1].BotID != bot.ID {
		t.Fatal("worker identities were not translated", ms)
	}
	segments := ms[0].Segments
	if len(segments[1].AllowedBots) != 1 || segments[1].AllowedBots[0] != bot.ID || segments[1].Kind != "file" {
		t.Fatal("local reference was not revalidated", segments)
	}
	for _, seg := range segments[2:] {
		if len(seg.AllowedBots) != 0 || seg.Kind != "" {
			t.Fatal("imported a historical or foreign permission", seg)
		}
	}
	if transcript[0].Segments[1].AllowedBots[0] != "foreign-grant" || transcript[0].Segments[0].BotID != qualified {
		t.Fatal("initialization mutated its authoritative input")
	}
	if ms[2].Content != transcript[2].Content || ms[2].CallID != ms[1].Calls[0].ID || len(s.dispatches) != 0 {
		t.Fatal("answered question changed or history started execution", ms)
	}
	if err = s.seedForkHistory(context.Background(), req, c, transcript); err != nil {
		t.Fatal(err)
	}
	ms, _ = s.Store.Messages(c.ID)
	if len(ms) != 3 {
		t.Fatal("retry duplicated history", ms)
	}
}

func TestForkSwarmSeedsHistoryAndIndependentMedia(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	requests := make(chan string, 8)
	attachModel(t, b, func(w http.ResponseWriter, r *http.Request) {
		var body any
		_ = json.NewDecoder(r.Body).Decode(&body)
		data, _ := json.Marshal(body)
		requests <- string(data)
		stream(w, "worker reply", nil)
	})
	c := chatForSwarm(t, a)
	send := func(chat, text string) string {
		t.Helper()
		d, err := a.send(chat, SendRequest{Content: text})
		if err != nil {
			t.Fatal(err)
		}
		waitDispatch(t, d)
		if d.err != nil {
			t.Fatal(d.err)
		}
		select {
		case request := <-requests:
			return request
		default:
			t.Fatal("worker did not call model")
			return ""
		}
	}
	send(c.ID, "@linux.echo included prompt")
	ms, _ := a.Store.Messages(c.ID)
	runID := ms[len(ms)-1].RunID
	projection := projectionID(a.Swarm.Node().ID, c.ID)
	image := domain.Attachment{ID: "generated-image", ChatID: projection, Name: "image.png", MediaType: "image/png", Size: 5}
	for _, m := range []domain.Message{
		{ID: "call", Role: "assistant", Content: "included work", Calls: []domain.ToolCall{{ID: "image-call", Function: domain.FunctionCall{Name: "comfyui_generate", Arguments: `{}`}}}},
		{ID: "tool", Role: "tool", CallID: "image-call", Content: fmt.Sprintf(`{"imageId":"generated-image","url":"/api/v1/chats/%s/attachments/generated-image"}`, projection)},
		{ID: "final", Role: "assistant", Content: "final with image"},
	} {
		m.RunID, m.BotID = runID, "echo"
		putForkMessage(t, b, projection, m)
		if m.Role == "tool" {
			if err := b.Store.AddGeneratedAttachment(image, m.ID, []byte("image")); err != nil {
				t.Fatal(err)
			}
			remote := image
			remote.NodeID, remote.OriginChatID = b.Swarm.Node().ID, projection
			m.Attachments = []domain.Attachment{remote}
		}
		m.ID, m.NodeID, m.BotID = swarm.Key(b.Swarm.Node().ID, m.ID), b.Swarm.Node().ID, swarm.Key(b.Swarm.Node().ID, "echo")
		putForkMessage(t, a, c.ID, m)
	}
	putForkMessage(t, a, c.ID, domain.Message{ID: "later", Role: "user", Content: "excluded prompt", Recipients: []string{swarm.Key(b.Swarm.Node().ID, "echo")}})
	id, err := a.forkChat(context.Background(), c.ID, swarm.Key(b.Swarm.Node().ID, "final"), "fork")
	if err != nil {
		t.Fatal(err)
	}
	forkMessages, _ := a.Store.Messages(id)
	var copiedImage domain.Attachment
	for _, m := range forkMessages {
		if m.Role == "tool" {
			copiedImage = m.Attachments[0]
		}
	}
	if copiedImage.ID == "" || copiedImage.ID == image.ID || copiedImage.NodeID != "" || copiedImage.OriginChatID != "" {
		t.Fatal(copiedImage)
	}
	if err = a.Store.DeleteChat(c.ID); err != nil {
		t.Fatal(err)
	}
	if err = b.Store.DeleteChat(projection); err != nil {
		t.Fatal(err)
	}
	history := send(id, "@linux.echo follow up")
	for _, included := range []string{"included prompt", "included work", "final with image", "data:image/png;base64,", copiedImage.ID} {
		if !strings.Contains(history, included) {
			t.Fatalf("missing %q in %s", included, history)
		}
	}
	if strings.Contains(history, "excluded prompt") {
		t.Fatal(history)
	}
	seeded, err := b.Store.ForkProjectionSeeded(projectionID(a.Swarm.Node().ID, id))
	if err != nil || !seeded {
		t.Fatal(seeded, err)
	}
	history = send(id, "@linux.echo another follow up")
	if strings.Count(history, "included prompt") != 1 {
		t.Fatal("history seeded twice", history)
	}
	if len(a.dispatches) != 0 || len(b.dispatches) != 0 {
		t.Fatal("historical work restarted")
	}
}

func TestForkWorkerCapabilityGate(t *testing.T) {
	participants := []swarmParticipant{{Ref: swarm.Ref{NodeID: "self", ID: "echo"}}, {Ref: swarm.Ref{NodeID: "remote", ID: "echo"}, Handle: "remote.echo"}}
	old := []swarm.Peer{{Node: swarm.Node{ID: "remote", Capabilities: []string{"dispatch/1"}}}}
	fork := domain.Chat{ForkedFrom: &domain.ForkOrigin{ChatID: "source"}}
	if err := checkForkWorkers(fork, participants, "self", old); err == nil || !strings.Contains(err.Error(), "update remote.echo") {
		t.Fatal(err)
	}
	if err := checkForkWorkers(domain.Chat{}, participants, "self", old); err != nil {
		t.Fatal("ordinary chat compatibility changed", err)
	}
	old[0].Node.Capabilities = append(old[0].Node.Capabilities, "fork-history/1")
	if err := checkForkWorkers(fork, participants, "self", old); err != nil {
		t.Fatal(err)
	}
}

func TestForkRemoteMediaFailureAndConcurrentEdit(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	blocked, release := make(chan struct{}, 1), make(chan struct{})
	var missing atomic.Bool
	mux := http.NewServeMux()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	peer, err := swarm.New(context.Background(), swarm.Config{Store: db, Secrets: &swarmTestSecrets{values: map[string]string{}}, SecretPrefix: "media", IdentityFile: filepath.Join(t.TempDir(), "identity"), Name: "media", Epoch: domain.ID(), Handler: mux, Enabled: func() bool { return true }, Enable: func() error { return nil }, CheckPassword: func(context.Context, string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(peer.Close)
	mux.HandleFunc("POST /api/v1/swarm/pair", func(w http.ResponseWriter, r *http.Request) {
		var input swarm.PairRequest
		_ = json.NewDecoder(r.Body).Decode(&input)
		result, err := peer.Pair(r.Context(), input, r.RemoteAddr)
		respond(w, result, err)
	})
	mux.HandleFunc("GET /api/v1/swarm/link", peer.LinkHTTP)
	mux.HandleFunc("GET /api/v1/chats/remote-chat/attachments/image", func(w http.ResponseWriter, r *http.Request) {
		blocked <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		if missing.Load() {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("image"))
	})
	host := httptest.NewServer(mux)
	t.Cleanup(host.Close)
	connectSwarm(t, a, host, remotePassword)
	image := domain.Attachment{ID: "image", ChatID: "remote-chat", Name: "image.png", Size: 5, MediaType: "image/png"}
	c := chatForSwarm(t, a)
	image.NodeID, image.OriginChatID = peer.Node().ID, "remote-chat"
	putForkMessage(t, a, c.ID, domain.Message{ID: "remote", Role: "tool", NodeID: peer.Node().ID, Attachments: []domain.Attachment{image}})
	putForkMessage(t, a, c.ID, domain.Message{ID: "final", Role: "assistant", Content: "before"})
	finished := make(chan error, 1)
	go func() { _, err := a.forkChat(context.Background(), c.ID, "final", "request"); finished <- err }()
	select {
	case <-blocked:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("media request did not arrive")
	}
	text := "after"
	if err := a.mutateMessage(c.ID, "final", &text); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-finished; err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatal(err)
	}
	chats, _ := a.Store.Chats()
	if len(chats) != 1 {
		t.Fatal("partial fork committed", chats)
	}
	missing.Store(true)
	if _, err := a.forkChat(context.Background(), c.ID, "final", "request"); err == nil {
		t.Fatal("missing media accepted")
	}
	chats, _ = a.Store.Chats()
	if len(chats) != 1 {
		t.Fatal(chats)
	}
}
