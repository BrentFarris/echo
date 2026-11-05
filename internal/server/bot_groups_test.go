package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/swarm"
)

func TestBotGroupSettingsAndBindings(t *testing.T) {
	s := testServer(t)
	group := domain.BotGroup{Handle: "MODBOTS", Members: []domain.BotGroupMember{{BotID: "echo"}, {BotID: "echo"}}}
	saved, err := s.saveBotGroup(t.Context(), "", group)
	if err != nil || saved.Handle != "modbots" || len(saved.Members) != 1 || saved.Members[0].NodeID != s.Swarm.Node().ID {
		t.Fatal(saved, err)
	}
	for _, invalid := range []domain.BotGroup{group, {Handle: "empty"}, {Handle: "EcHo", Members: saved.Members}, {Handle: "bad.name", Members: saved.Members}, {Handle: "unknown", Members: []domain.BotGroupMember{{BotID: "not-a-bot"}}}} {
		if _, err := s.saveBotGroup(t.Context(), "", invalid); err == nil {
			t.Fatal("accepted invalid group", invalid)
		}
	}
	if _, err := s.saveBot(domain.Bot{Handle: "MODBOTS"}); err == nil {
		t.Fatal("bot shadowed group")
	}
	saved.Handle = "renamed"
	if _, err := s.saveBotGroup(t.Context(), saved.ID, saved); err != nil {
		t.Fatal(err)
	}
	req := SendRequest{Content: "@modbots", Segments: []domain.Segment{{Type: "group", GroupID: saved.ID, Text: "@modbots", Range: &domain.SegmentRange{Start: 0, End: 8}, Members: []domain.BotGroupMember{{BotID: "forged"}}}}}
	recipients, err := s.recipients(req)
	if err != nil || len(recipients) != 1 || recipients[0].ID != "echo" {
		t.Fatal(recipients, err)
	}
	if err := s.Store.Delete("bot-groups", saved.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.saveBotGroup(t.Context(), "", group); err != nil {
		t.Fatal(err)
	}
	if _, err := s.recipients(req); err == nil {
		t.Fatal("deleted chip retargeted to a replacement group")
	}
}

func TestBotGroupDispatchHistoryAndFileScope(t *testing.T) {
	s := testServer(t)
	root := t.TempDir()
	first, err := s.saveBot(domain.Bot{Handle: "one", Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.saveBot(domain.Bot{Handle: "two", Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	group, err := s.saveBotGroup(t.Context(), "", domain.BotGroup{Handle: "mods", Members: []domain.BotGroupMember{{BotID: first.ID}, {BotID: second.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.saveBotGroup(t.Context(), "", domain.BotGroup{Handle: "others", Members: group.Members[1:]}); err != nil {
		t.Fatal(err)
	}
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) { stream(w, "done", nil) })
	c := chat(t, s, g)
	file := "資料@mods.txt"
	path := filepath.Join(root, file)
	if err := os.WriteFile(path, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	prefix := "🙂 #" + file
	content := prefix + " @mods @two @others `@mods`"
	req := SendRequest{Content: content, RequestID: "send", Segments: []domain.Segment{{Type: "file", Text: file, Path: path, Range: &domain.SegmentRange{Start: 3, End: utf16Offset(prefix)}}}}
	send := func(req SendRequest) domain.Message {
		t.Helper()
		d, err := s.send(c.ID, req)
		if err != nil {
			t.Fatal(err)
		}
		waitDispatch(t, d)
		msgs, err := s.Store.Messages(c.ID)
		if err != nil || len(msgs) == 0 {
			t.Fatal(err)
		}
		if d.err != nil {
			t.Fatal(d.err)
		}
		return msgs[0]
	}
	m := send(req)
	explicit, err := s.recipients(SendRequest{Content: "@one @two @two @two"})
	if err != nil || len(explicit) != 2 || !slices.Equal(m.Recipients, []string{explicit[0].ID, explicit[1].ID}) {
		t.Fatal(m.Recipients, err)
	}
	runs, err := s.Store.ChatRuns(c.ID)
	if err != nil || len(runs) != 2 || m.Content != content || !hasGroupSegment(m.Segments) {
		t.Fatal(m, runs, err)
	}
	for _, run := range runs {
		if run.Status != "completed" {
			t.Fatal(run.Status, run.Error)
		}
	}
	want := prefix + " @one @two @two @two `@mods`"
	if got := groupModelContent(m); got != want {
		t.Fatalf("model prompt %q, want %q", got, want)
	}
	for _, bot := range []domain.Bot{first, second} {
		history, err := s.history(bot, c.ID)
		if err != nil || len(history) < 2 || !strings.HasPrefix(history[1].Content, want) || strings.Contains(history[1].Content, path) != (bot.ID == first.ID) {
			t.Fatalf("history for %s: %v %v", bot.Handle, history, err)
		}
	}
	group.Members = group.Members[1:]
	if _, err = s.saveBotGroup(t.Context(), group.ID, group); err != nil {
		t.Fatal(err)
	}
	rerun := send(SendRequest{rerunMessageID: m.ID, RequestID: "rerun"})
	if !slices.Equal(rerun.Recipients, m.Recipients) || groupModelContent(rerun) != want {
		t.Fatal(rerun)
	}
	edit := send(SendRequest{replaceMessageID: rerun.ID, RequestID: "edit", Content: "@mods updated"})
	if !slices.Equal(edit.Recipients, []string{second.ID}) || groupModelContent(edit) != "@two updated" {
		t.Fatal(edit)
	}
	before, _ := s.Store.ChatRuns(c.ID)
	if err = s.Store.Delete("bots", second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.send(c.ID, SendRequest{Content: "@one @mods"}); err == nil || !strings.Contains(err.Error(), "@two") {
		t.Fatal(err)
	}
	after, _ := s.Store.ChatRuns(c.ID)
	if len(after) != len(before) {
		t.Fatal("partial send", after)
	}
}

func TestBotGroupSwarmRemoteHome(t *testing.T) {
	entry, _ := swarmFixture(t, "desktop", remotePassword)
	home, host := swarmFixture(t, "linux", remotePassword)
	node := connectSwarm(t, entry, host, remotePassword)
	self := entry.Swarm.Node().ID
	group, err := entry.saveBotGroup(t.Context(), "", domain.BotGroup{Handle: "mods", Members: []domain.BotGroupMember{{BotID: "echo"}, {NodeID: node.ID, BotID: "echo"}}})
	if err != nil {
		t.Fatal(err)
	}
	reply := func(w http.ResponseWriter, r *http.Request) { stream(w, "done", nil) }
	attachModel(t, entry, reply)
	g := attachModel(t, home, reply)
	local := chat(t, home, g)
	d, err := home.send(local.ID, SendRequest{Content: "@desktop.mods"})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	localMsgs, _ := home.Store.Messages(local.ID)
	if len(localMsgs[0].Recipients) != 2 || groupModelContent(localMsgs[0]) != "@desktop.echo @echo" {
		t.Fatal(localMsgs[0])
	}

	c := chat(t, home, g)
	forward := func(path, body string) domain.Message {
		t.Helper()
		target := "chats/" + c.ID + "/messages" + path
		r := httptest.NewRequest("POST", "/api/v1/swarm/instances/"+node.ID+"/"+target, strings.NewReader(body))
		r.SetPathValue("nodeId", node.ID)
		r.SetPathValue("path", target)
		w := httptest.NewRecorder()
		entry.swarmProxy(w, r)
		if w.Code != 200 {
			t.Fatal(path, w.Body.String())
		}
		var accepted struct {
			ID string `json:"dispatchId"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil || accepted.ID == "" {
			t.Fatal(w.Body.String(), err)
		}
		eventuallySwarm(t, func() bool {
			home.mu.Lock()
			defer home.mu.Unlock()
			return home.dispatches[accepted.ID] == nil
		})
		msgs, err := home.Store.Messages(c.ID)
		if err != nil || len(msgs) < 2 {
			t.Fatal(msgs, err)
		}
		return msgs[0]
	}
	m := forward("", `{"content":"@mods review","requestId":"group"}`)
	want := []string{swarm.Key(self, "echo"), "echo"}
	if !slices.Equal(m.Recipients, want) || groupModelContent(m) != "@echo @linux.echo review" {
		t.Fatal(m)
	}
	group.Members = group.Members[1:]
	if _, err := entry.saveBotGroup(t.Context(), group.ID, group); err != nil {
		t.Fatal(err)
	}
	m = forward("/"+m.ID+"/rerun", `{"requestId":"again"}`)
	if !slices.Equal(m.Recipients, want) {
		t.Fatal(m)
	}
	m = forward("/"+m.ID+"/resubmit", `{"content":"@mods edited","requestId":"edited"}`)
	if !slices.Equal(m.Recipients, []string{"echo"}) || groupModelContent(m) != "@linux.echo edited" {
		t.Fatal(m)
	}
	if err := entry.Swarm.SetDisconnected(node.ID, true); err != nil {
		t.Fatal(err)
	}
	fresh := chat(t, entry, g)
	if _, err := entry.send(fresh.ID, SendRequest{Content: "@echo @mods"}); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatal(err)
	}
	runs, _ := entry.Store.ChatRuns(fresh.ID)
	if len(runs) != 0 {
		t.Fatal(runs)
	}
}

func TestBotGroupLegacyWorkerCapability(t *testing.T) {
	entry, _ := swarmFixture(t, "desktop", remotePassword)
	old, _ := swarmFixture(t, "legacy", remotePassword)
	node := old.Swarm.Node()
	node.Capabilities = []string{"owner-api/1", "dispatch/1", "stream/1"}
	_, err := entry.Swarm.Pair(t.Context(), swarm.PairRequest{Node: node, Version: swarm.Version, Password: remotePassword, Activate: true, ID: strings.Repeat("a", 64), ReturnToken: strings.Repeat("b", 64)}, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	workers := []swarmParticipant{{Ref: swarm.Ref{NodeID: entry.Swarm.Node().ID, ID: "echo"}}, {Ref: swarm.Ref{NodeID: node.ID, ID: "echo"}}}
	if err := entry.checkGroupWorkers([]domain.Segment{{Type: "group"}}, workers); err == nil || !strings.Contains(err.Error(), "update Echo on legacy") {
		t.Fatal(err)
	}
	if err := entry.checkGroupWorkers([]domain.Segment{{Type: "bot"}}, workers); err != nil {
		t.Fatal(err)
	}
	if _, err := entry.saveBotGroup(t.Context(), "", domain.BotGroup{Handle: "mods", Members: []domain.BotGroupMember{{BotID: "echo"}}}); err != nil {
		t.Fatal(err)
	}
	// Even with all members on a modern worker, the home must be able to
	// retain the group snapshot before the entry forwards the request.
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"content":"@mods hello"}`))
	forward, err := entry.prepareForwardedMessage(httptest.NewRecorder(), r, node.ID, "chat", "", "")
	if forward || err == nil || !strings.Contains(err.Error(), "update Echo on legacy") {
		t.Fatal("forwarded a group to an older chat home", forward, err)
	}
}
