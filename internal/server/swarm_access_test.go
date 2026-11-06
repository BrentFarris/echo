package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/calendar"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/swarm"
)

func TestSwarmAccessDestinationAndSharedTools(t *testing.T) {
	a, _ := swarmFixture(t, "coworker", remotePassword)
	b, bh := swarmFixture(t, "owner", remotePassword)
	node := connectSwarm(t, a, bh, remotePassword)
	root := t.TempDir()
	bot, err := b.saveBot(domain.Bot{Handle: "coding", Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	private, err := b.saveBot(domain.Bot{Handle: "private"})
	if err != nil {
		t.Fatal(err)
	}
	peer := a.Swarm.Node().ID
	permissions := map[string]bool{}
	for _, key := range domain.SwarmPermissions {
		permissions[key] = false
	}
	if err := b.saveAccessPolicy("", domain.SwarmPolicy{Permissions: permissions}); err != nil {
		t.Fatal(err)
	}
	if err := b.saveAccessPolicy(peer, domain.SwarmPolicy{Permissions: map[string]bool{domain.AccessBots: true}, Bots: map[string]bool{private.ID: false}, Plugins: map[string]bool{"shell": false}}); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	var state struct {
		Bots        []domain.Bot
		Plugins     []domain.PluginManifest
		SwarmAccess domain.SwarmAccess
	}
	if err := a.Swarm.JSON(ctx, node.ID, "GET", "/api/v1/state", nil, &state); err != nil {
		t.Fatal(err)
	}
	if state.SwarmAccess.Full || !state.SwarmAccess.Permissions[domain.AccessBots] || state.SwarmAccess.Permissions[domain.AccessRead] || slices.ContainsFunc(state.Bots, func(b domain.Bot) bool { return b.ID == private.ID }) || slices.ContainsFunc(state.Plugins, func(p domain.PluginManifest) bool { return p.ID == "shell" }) {
		t.Fatal("incorrect peer inventory", state.SwarmAccess)
	}
	chat := chatForSwarm(t, b)
	for _, check := range []struct{ method, path, permission string }{
		{"GET", "plugins/terminal/bots/echo/sessions", "fullAccess"},
		{"GET", "plugins/monaco-editor/bots/echo/fs/roots", domain.AccessRead},
		{"PUT", "plugins/monaco-editor/bots/echo/fs/file", domain.AccessWrite},
		{"POST", "bots", domain.AccessManageBots}, {"POST", "bot-groups", domain.AccessManageBots},
		{"PATCH", "plugins/calendar", domain.AccessManagePlugins}, {"POST", "endpoints", domain.AccessModels},
		{"POST", "schedules", domain.AccessSchedules}, {"DELETE", "chats/" + chat.ID, domain.AccessChats},
		{"GET", "hosting/sessions", domain.AccessMachine}, {"PUT", "swarm/access", "ownerSession"},
		{"GET", "swarm/invitations", "ownerSession"}, {"POST", "plugins/shell/bridge", "plugin:shell"},
		{"GET", "plugins/monaco-editor/bots/" + private.ID + "/fs/roots", "bot:" + private.ID},
	} {
		t.Run(check.path+check.method, func(t *testing.T) {
			err := a.Swarm.JSON(ctx, node.ID, check.method, "/api/v1/"+check.path, map[string]any{}, nil)
			var deny *swarm.AccessDenied
			if !errors.As(err, &deny) || deny.Permission != check.permission {
				t.Fatalf("want 403 %s, got %v", check.permission, err)
			}
		})
	}
	if err := a.Swarm.JSON(ctx, node.ID, "GET", "/api/v1/chats/"+chat.ID+"/messages", nil, nil); err != nil {
		t.Fatal("existing history", err)
	}
	access := &domain.ExecutionAccess{Origin: peer}
	scope := domain.ExecutionScope{Bot: bot, Access: access}
	path := filepath.Join(root, "shared.txt")
	args, _ := json.Marshal(map[string]string{"path": path, "content": "scoped work"})
	if _, err = b.tool(ctx, nil, scope, domain.ToolCall{Function: domain.FunctionCall{Name: "file_write", Arguments: string(args)}}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "scoped work" {
		t.Fatal("shared bot could not edit", err)
	}
	if err := b.authorizeTool(scope, "process.run", nil); err != nil {
		t.Fatal("shared bot cannot build in its folders", err)
	}
	if runtime.GOOS == "windows" && os.Getenv("ECHO2_NATIVE_TESTS") == "1" {
		scope.PluginID = "shell"
		args, _ := json.Marshal(map[string]string{"directory": root, "command": "Get-Content shared.txt | Set-Content build.txt"})
		if _, err := b.pluginCall(ctx, scope, "process.run", args); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(root, "build.txt"))
		if err != nil || strings.TrimSpace(string(data)) != "scoped work" {
			t.Fatal("restricted bot's scoped shell failed", err)
		}
	}
	if err := b.authorizeTool(scope, "future.admin", nil); err == nil {
		t.Fatal("unclassified host capability admitted")
	}
	result, err := broker.NetworkFetch(b.accessContext(ctx, access), broker.NetworkRequest{URL: bh.URL + "/api/v1/state"}, []string{bh.URL})
	if err != nil || result["status"] != http.StatusForbidden || !strings.Contains(result["content"].(string), "swarm_access_denied") {
		t.Fatal("plugin network bypassed destination policy", result, err)
	}
	if err := b.authorizeTool(domain.ExecutionScope{Access: access, Bot: bot, PluginID: "shell", Standalone: true}, "process.run", nil); err == nil {
		t.Fatal("standalone used private plugin")
	}
	for _, method := range []string{"bot_save", "workspace.bind", "model_save", "plugin.install", "plugin.configure", "calendar.save"} {
		if err := b.authorizeTool(scope, method, nil); err == nil {
			t.Fatal("administration through shared bot", method)
		}
	}
	if err := b.requireSharedBot(nil, private.ID); err != nil {
		t.Fatal("owner lost private bot", err)
	}
	if err := b.setResourceSharing("bots", bot.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := b.authorizeTool(scope, "file_read", nil); err == nil {
		t.Fatal("stale bot scope still actionable")
	}
}

func TestSwarmAccessDelegationAndRevocation(t *testing.T) {
	a, _ := swarmFixture(t, "alice", remotePassword)
	b, bh := swarmFixture(t, "bob", remotePassword)
	c, ch := swarmFixture(t, "carol", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	connectSwarm(t, b, ch, remotePassword)
	connectSwarm(t, a, ch, remotePassword)
	origin := a.Swarm.Node().ID
	access := &domain.ExecutionAccess{Origin: origin}
	if err := b.saveAccessPolicy(origin, domain.SwarmPolicy{Permissions: map[string]bool{domain.AccessManageBots: false}}); err != nil {
		t.Fatal(err)
	}
	echo, _ := b.Bot("echo")
	g := attachModel(t, b, func(w http.ResponseWriter, r *http.Request) { stream(w, "ok", nil) })
	attachModel(t, c, func(w http.ResponseWriter, r *http.Request) { stream(w, "done", nil) })
	scope := delegationScope(t, b, echo, g)
	scope.Access = access
	receipt, err := b.startDelegatedChat(t.Context(), scope, "carol.echo", "Work on this", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	eventuallySwarm(t, func() bool {
		runs, _ := b.Store.ChatRuns(receipt.ChatID)
		return len(runs) == 1 && runs[0].Status == "completed"
	})
	second, err := b.startDelegatedChat(t.Context(), scope, "carol.echo", "Retry", "job-1")
	if err != nil || second != receipt {
		t.Fatal("duplicate delegated work", second, err)
	}
	var worker workerRequest
	if err := c.Store.Get("swarm-workers", workerKey(b.Swarm.Node().ID, receipt.DispatchID), &worker); err != nil {
		t.Fatal(err)
	}
	if worker.Access == nil || worker.Access.Origin != origin || c.requireAccess(worker.Access, domain.AccessManageBots) == nil || !slices.Contains(worker.Access.Via, b.Swarm.Node().ID) || len(worker.Access.Parents) != 1 || worker.Access.Parents[0].Task != scope.RunID {
		t.Fatal("delegated work gained authority", worker.Access)
	}
	if err := c.validateAccessParents(t.Context(), worker.Access); err != nil {
		t.Fatal(err)
	}
	forged := worker
	forged.Access = &domain.ExecutionAccess{Origin: b.Swarm.Node().ID}
	if err := c.authorizeWorker(forged, requestFingerprint(forged)); err == nil {
		t.Fatal("forged provenance accepted")
	}
	ids, err := c.saveScheduleAccess(worker.Access)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.saveAccessPolicy(origin, domain.SwarmPolicy{Permissions: map[string]bool{domain.AccessBots: false}}); err != nil {
		t.Fatal(err)
	}
	if err := c.validateAccessParents(t.Context(), worker.Access); err == nil {
		t.Fatal("revoked descendant remained authorized")
	}
	err = c.scheduled(t.Context(), calendar.Occurrence{BotID: "echo", Prompt: "replay", AccessIDs: ids})
	var denied *accessError
	if !errors.As(err, &denied) {
		t.Fatal("scheduled work lost provenance", err)
	}
	ctx, release := c.trackAccess(context.Background(), access)
	defer release()
	other, localRelease := c.trackAccess(context.Background(), nil)
	defer localRelease()
	if err := c.saveAccessPolicy(origin, domain.SwarmPolicy{Permissions: map[string]bool{domain.AccessRead: false}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("remote stream not cancelled")
	}
	if other.Err() != nil {
		t.Fatal("owner stream cancelled")
	}
	recovered := restartSwarmFixture(t, c)
	if recovered.effectiveAccess(origin).Permissions[domain.AccessRead] {
		t.Fatal("peer restriction lost on restart")
	}
	restored, err := recovered.scheduleAccess(ids)
	if err != nil || restored.Origin != origin || !slices.Equal(restored.Parents, worker.Access.Parents) {
		t.Fatal("scheduled delegation lost its provenance on restart", err)
	}
}

func TestSwarmAccessInvitations(t *testing.T) {
	a, _ := swarmFixture(t, "alice", remotePassword)
	b, bh := swarmFixture(t, "issuer", remotePassword)
	c, _ := swarmFixture(t, "other", remotePassword)
	issue := func(expiry time.Time, revoked bool) string {
		token := domain.ID() + domain.ID()
		hash := invitationHash(token)
		if err := b.Store.Put("swarm-invitations", hash, swarmInvitation{ID: domain.ID(), Hash: hash, Expires: expiry, Revoked: revoked, Policy: domain.SwarmPolicy{Permissions: map[string]bool{domain.AccessManageBots: false}}}); err != nil {
			t.Fatal(err)
		}
		return token + "." + hex.EncodeToString(b.Swarm.Node().PublicKey)
	}
	redeem := func(s *Server, code string) error {
		_, err := s.Swarm.ConnectInvitation(t.Context(), bh.URL, "", code, func(n swarm.Node) error {
			return s.saveAccessPolicy(n.ID, domain.SwarmPolicy{Permissions: map[string]bool{domain.AccessWrite: false}})
		})
		return err
	}
	for _, code := range []string{issue(time.Now().Add(-time.Second), false), issue(time.Now().Add(time.Minute), true)} {
		if redeem(a, code) == nil {
			t.Fatal("expired/revoked invitation used")
		}
	}
	code := issue(time.Now().Add(time.Minute), false)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, m := range []*Server{a, c} {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- redeem(m, code) }()
	}
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatal("single-use invitation had", successes, "successful redeemers")
	}
	winner := a
	if !b.Swarm.Authorized(winner.Swarm.Node().ID) {
		winner = c
	}
	if err := redeem(winner, code); err != nil {
		t.Fatal("retry failed", err)
	}
	if b.effectiveAccess(winner.Swarm.Node().ID).Permissions[domain.AccessManageBots] || winner.effectiveAccess(b.Swarm.Node().ID).Permissions[domain.AccessWrite] {
		t.Fatal("directional policy lost")
	}
	for _, host := range []*Server{b, winner} {
		secrets := host.Secrets.(*swarmTestSecrets)
		secrets.Lock()
		for key, value := range secrets.values {
			if strings.Contains(key, "/discovery") || value == code || value == remotePassword {
				t.Error("invitation stored a discovery secret")
			}
		}
		secrets.Unlock()
	}
	secret, _, _ := strings.Cut(code, ".")
	raw, _ := b.Store.List("swarm-invitations")
	for _, row := range raw {
		if strings.Contains(string(row), secret) {
			t.Fatal("invitation token stored in plaintext")
		}
	}
	res, err := winner.Swarm.RoundTrip(t.Context(), b.Swarm.Node().ID, "PUT", "/api/v1/swarm/access", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 403 {
		t.Fatal(res.StatusCode, string(body))
	}
	if err := b.Swarm.Revoke(winner.Swarm.Node().ID); err != nil {
		t.Fatal(err)
	}
	if err := redeem(winner, code); err == nil {
		t.Fatal("replayed invitation restored a revoked pairing")
	}
}
