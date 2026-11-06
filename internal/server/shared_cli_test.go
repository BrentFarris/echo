package server

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/calendar"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/swarm"
)

func TestSharedCLISettings(t *testing.T) {
	s := testServer(t)
	g, _ := externalFixture(t, s, "codex-cli")
	var other domain.Endpoint
	if err := s.Store.Get("endpoints", g.EndpointID, &other); err != nil {
		t.Fatal(err)
	}
	other.ID = "other"
	if err := s.Store.Put("endpoints", other.ID, other); err != nil {
		t.Fatal(err)
	}
	save := func(body map[string]any) domain.Bot {
		t.Helper()
		w := scmAPI(t, s, "POST", "/bots", body)
		var b domain.Bot
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &b) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		return b
	}
	pin := domain.BotModelOverride{EndpointID: g.EndpointID}
	b := save(map[string]any{"handle": "cli", "modelOverride": pin})
	if b.SharesPinnedCLI() {
		t.Fatal("new bot opted in by default")
	}
	for _, bad := range []any{other.ID, nil, true} {
		if w := scmAPI(t, s, "POST", "/bots", map[string]any{"id": b.ID, "sharedCliEndpointId": bad}); w.Code != 400 {
			t.Fatalf("accepted invalid grant %v: %d", bad, w.Code)
		}
	}
	save(map[string]any{"id": b.ID, "sharedCliEndpointId": g.EndpointID})
	form := save(map[string]any{"id": b.ID, "modelOverride": domain.BotModelOverride{EndpointID: " " + pin.EndpointID + " "}, "name": "Renamed"})
	if !form.SharesPinnedCLI() {
		t.Fatal("older client's unrelated edit cleared the grant")
	}
	echo, _ := s.Bot("echo")
	raw, _ := json.Marshal(map[string]any{"id": b.ID, "handle": b.Handle, "modelOverride": domain.BotModelOverride{EndpointID: other.ID}, "sharedCliEndpointId": other.ID})
	_, err := s.tool(t.Context(), nil, domain.ExecutionScope{Bot: echo}, domain.ToolCall{Function: domain.FunctionCall{Name: "bot_save", Arguments: string(raw)}})
	if err == nil || !strings.Contains(err.Error(), "unknown argument: sharedCliEndpointId") {
		t.Fatalf("bot tool exposed the grant: %v", err)
	}
	_, err = s.saveBotInput(raw, &domain.ExecutionScope{Bot: echo})
	var denial *accessError
	if !errors.As(err, &denial) || denial.Permission != "ownerSession" {
		t.Fatalf("bot tool could transfer the grant: %v", err)
	}
	b = save(map[string]any{"id": b.ID, "modelOverride": domain.BotModelOverride{EndpointID: other.ID}})
	if b.SharedCLIEndpointID != "" {
		t.Fatal("grant followed pin change")
	}
	b = save(map[string]any{"id": b.ID, "modelOverride": pin, "sharedCliEndpointId": g.EndpointID})
	if !b.SharesPinnedCLI() {
		t.Fatal("owner could not explicitly regrant")
	}
	b = save(map[string]any{"id": b.ID, "modelOverride": nil})
	if b.SharedCLIEndpointID != "" {
		t.Fatal("grant survived pin removal")
	}
	save(map[string]any{"id": b.ID, "modelOverride": pin, "sharedCliEndpointId": g.EndpointID})
	for _, shared := range []bool{false, true} {
		w := scmAPI(t, s, "PUT", "/swarm/access/bots/"+b.ID, map[string]bool{"shared": shared})
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	b, err = s.Bot(b.ID)
	if err != nil || b.SharedCLIEndpointID != "" {
		t.Fatal("unsharing and resharing restored the old grant", b, err)
	}
}

func TestSharedCLIPeerRuns(t *testing.T) {
	for _, kind := range []string{"codex-cli", "claude-code-cli"} {
		t.Run(kind, func(t *testing.T) {
			entry, _ := swarmFixture(t, "desktop", remotePassword)
			worker, host := swarmFixture(t, "worker", remotePassword)
			node := connectSwarm(t, entry, host, remotePassword)
			peer := entry.Swarm.Node().ID
			policy := domain.SwarmPolicy{Permissions: map[string]bool{domain.AccessMachine: false}}
			if err := worker.saveAccessPolicy(peer, policy); err != nil {
				t.Fatal(err)
			}
			g, log := externalFixture(t, worker, kind)
			bot, err := worker.saveBot(domain.Bot{Handle: "cli", ModelOverride: &domain.BotModelOverride{EndpointID: g.EndpointID}, SharedCLIEndpointID: g.EndpointID})
			if err != nil {
				t.Fatal(err)
			}
			group := makeOneGroup(t, worker, domain.BotGroupMember{BotID: bot.ID})
			send := func(target string) {
				t.Helper()
				c := chatForSwarm(t, entry)
				d, err := entry.send(c.ID, SendRequest{Content: target + " hello"})
				if err != nil {
					t.Fatal(err)
				}
				waitDispatch(t, d)
				runs, err := entry.Store.ChatRuns(c.ID)
				if err != nil || len(runs) != 1 || runs[0].Status != "completed" || runs[0].BotID != swarm.Key(node.ID, bot.ID) {
					t.Fatal(target, runs, err)
				}
			}
			send("@worker.cli")
			send("@worker.pool")
			group.DeliveryMode = "all"
			if _, err := worker.saveBotGroup(t.Context(), group.ID, group); err != nil {
				t.Fatal(err)
			}
			send("@worker.pool")
			access := &domain.ExecutionAccess{Origin: peer}
			echo, _ := worker.Bot("echo")
			scope := delegationScope(t, worker, echo, g)
			scope.Access = access
			receipt, err := worker.startDelegatedChat(t.Context(), scope, "cli", "delegated", "")
			if err != nil {
				t.Fatal(err)
			}
			eventuallySwarm(t, func() bool {
				runs, _ := worker.Store.ChatRuns(receipt.ChatID)
				return len(runs) == 1 && runs[0].Status == "completed"
			})
			ids, err := worker.saveScheduleAccess(access)
			if err != nil {
				t.Fatal(err)
			}
			occurrence := calendar.Occurrence{BotID: bot.ID, AccessIDs: ids, Generation: g, Prompt: "scheduled"}
			if err := worker.scheduled(t.Context(), occurrence); err != nil {
				t.Fatal(err)
			}
			for _, check := range []struct{ method, path, permission string }{
				{"GET", "/plugins/terminal/bots/" + bot.ID + "/sessions", "fullAccess"},
				{"GET", "/hosting/sessions", domain.AccessMachine},
				{"POST", "/bots", "ownerSession"},
			} {
				err := entry.Swarm.JSON(t.Context(), node.ID, check.method, "/api/v1"+check.path, map[string]any{"id": bot.ID, "sharedCliEndpointId": g.EndpointID}, nil)
				var denied *swarm.AccessDenied
				if !errors.As(err, &denied) || denied.Permission != check.permission {
					t.Fatal(check, err)
				}
			}
			var endpoint domain.Endpoint
			_ = worker.Store.Get("endpoints", g.EndpointID, &endpoint)
			endpoint.ID = "different-cli"
			if err := worker.requireModelAccess(access, bot, endpoint); err == nil {
				t.Fatal("accepted a different CLI")
			}
			policy.Permissions[domain.AccessSchedules] = false
			if err := worker.saveAccessPolicy(peer, policy); err != nil {
				t.Fatal(err)
			}
			if err := worker.scheduled(t.Context(), occurrence); err == nil {
				t.Fatal("grant bypassed schedule permission")
			}
			// Recheck sharing and run permission even when the target is already known.
			for _, policy := range []domain.SwarmPolicy{
				{Bots: map[string]bool{bot.ID: false}},
				{Permissions: map[string]bool{domain.AccessBots: false}},
			} {
				if err := worker.saveAccessPolicy(peer, policy); err != nil {
					t.Fatal(err)
				}
				if _, err := entry.send(chatForSwarm(t, entry).ID, SendRequest{Content: "@worker.cli denied"}); err == nil {
					t.Fatal("excluded or unauthorized bot ran")
				}
			}
			if err := worker.saveAccessPolicy(peer, domain.SwarmPolicy{Permissions: map[string]bool{domain.AccessMachine: false}}); err != nil {
				t.Fatal(err)
			}
			if err := worker.updateResourceSharing("bots", bot.ID, false); err != nil {
				t.Fatal(err)
			}
			if err := worker.setResourceSharing("bots", bot.ID, false); err != nil {
				t.Fatal(err)
			}
			if _, err := entry.send(chatForSwarm(t, entry).ID, SendRequest{Content: "@worker.cli private"}); err == nil {
				t.Fatal("private bot ran")
			}
			if calls := externalCalls(t, log); len(calls) != 5 {
				t.Fatalf("got %d CLI invocations, want five authorized turns", len(calls))
			}
		})
	}
}

func TestSharedCLIRevokeDuringGroupQueue(t *testing.T) {
	entry, _ := swarmFixture(t, "desktop", remotePassword)
	worker, host := swarmFixture(t, "worker", remotePassword)
	connectSwarm(t, entry, host, remotePassword)
	if err := worker.saveAccessPolicy(entry.Swarm.Node().ID, domain.SwarmPolicy{Permissions: map[string]bool{domain.AccessMachine: false}}); err != nil {
		t.Fatal(err)
	}
	g, log := externalFixture(t, worker, "codex-cli")
	bot, err := worker.saveBot(domain.Bot{Handle: "cli", ModelOverride: &domain.BotModelOverride{EndpointID: g.EndpointID}, SharedCLIEndpointID: g.EndpointID})
	if err != nil {
		t.Fatal(err)
	}
	makeOneGroup(t, worker, domain.BotGroupMember{BotID: bot.ID})
	started := filepath.Join(t.TempDir(), "started")
	t.Setenv("ECHO_EXTERNAL_TEST_MODE", "cancel")
	t.Setenv("ECHO_EXTERNAL_STARTED", started)
	active, err := entry.send(chatForSwarm(t, entry).ID, SendRequest{Content: "@worker.cli running"})
	if err != nil {
		t.Fatal(err)
	}
	eventuallySwarm(t, func() bool { _, err := os.Stat(started); return err == nil })
	c := chatForSwarm(t, entry)
	queued, err := entry.send(c.ID, SendRequest{Content: "@worker.pool queued"})
	if err != nil || !queued.waitingGroup {
		t.Fatal("expected group to queue", err)
	}
	raw, _ := json.Marshal(map[string]string{"id": bot.ID, "sharedCliEndpointId": ""})
	if _, err := worker.saveBotInput(raw, nil); err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, queued)
	waitDispatch(t, active)
	msgs, err := entry.Store.Messages(c.ID)
	if err != nil || len(msgs) != 1 || msgs[0].Status != "failed" || !strings.Contains(msgs[0].DeliveryError, "Full access") {
		t.Fatal("queued turn did not recheck access", msgs, err)
	}
	runs, err := entry.Store.ChatRuns(active.chatID)
	if err != nil || len(runs) != 1 || runs[0].Status == "completed" || !strings.Contains(runs[0].Error, "shared CLI") {
		t.Fatal("active CLI did not stop on revoke", runs, err)
	}
	if calls := externalCalls(t, log); len(calls) != 1 {
		t.Fatalf("queued task reached the CLI: %+v", calls)
	}
}
