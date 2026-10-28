package plugins

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/brent/echo/internal/domain"
)

func TestBotPluginSelectionsGateToolsProvidersAndScopes(t *testing.T) {
	h := appTestHost(t)
	calls := 0
	h.broker = func(_ context.Context, _ domain.ExecutionScope, _ string, _ json.RawMessage) (any, error) {
		calls++
		return "allowed", nil
	}
	guest, err := os.ReadFile("guest/builtin.wasm")
	if err != nil {
		t.Fatal(err)
	}
	m, err := h.Install(domain.PluginManifest{
		API: 1, ID: "opt-in", Name: "Opt in", Capabilities: []string{"web.get"},
		Tools: []domain.Tool{
			{Name: "opt_in_fetch", Capability: "web.get", Parameters: Schema(map[string]any{})},
			{Name: "opt_in_provider", Capability: "web.get", Parameters: Schema(map[string]any{})},
		}, Providers: []string{"opt_in_provider"},
	}, guest)
	if err != nil || !m.Pending {
		t.Fatal(m, err)
	}
	worker := domain.Bot{ID: "worker"}
	for _, bot := range []domain.Bot{worker, {ID: "echo"}, {ID: "worker", Plugins: map[string]bool{m.ID: true}}} {
		if h.Enabled(bot, m.ID) {
			t.Fatal("pending plugin available", bot)
		}
	}
	if _, err = h.ApproveDigest(m.ID, m.Digest); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		bot     domain.Bot
		enabled bool
	}{
		{worker, false},
		{domain.Bot{ID: "worker", Plugins: map[string]bool{}}, false},
		{domain.Bot{ID: "worker", Plugins: map[string]bool{m.ID: false}}, false},
		{domain.Bot{ID: "worker", Plugins: map[string]bool{m.ID: true}}, true},
		{domain.Bot{ID: "echo"}, true},
		{domain.Bot{ID: "echo", Plugins: map[string]bool{m.ID: false}}, false},
	} {
		if h.Enabled(tc.bot, m.ID) != tc.enabled {
			t.Fatal("wrong selection", tc)
		}
		tools := h.Tools(tc.bot)
		if slices.ContainsFunc(tools, func(tool domain.Tool) bool { return tool.Name == "opt_in_fetch" }) != tc.enabled {
			t.Fatal("tool list ignored selection", tc)
		}
		if slices.ContainsFunc(tools, func(tool domain.Tool) bool { return tool.Name == "opt_in_provider" }) {
			t.Fatal("provider leaked into tool list")
		}
		if _, err = h.Provider(tc.bot, "opt_in_provider"); (err == nil) != tc.enabled {
			t.Fatal("provider ignored selection", tc, err)
		}
		before := calls
		if _, err = h.Invoke(context.Background(), domain.ExecutionScope{Bot: tc.bot}, "opt_in_fetch", json.RawMessage(`{}`)); (err == nil) != tc.enabled {
			t.Fatal("invocation ignored selection", tc, err)
		}
		if (calls > before) != tc.enabled {
			t.Fatal("disabled invocation entered broker", tc)
		}
	}
	for _, id := range []string{"workspaces", "monaco-editor", m.ID} {
		plugin, err := h.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if id == "workspaces" && h.Enabled(domain.Bot{ID: "worker", Plugins: map[string]bool{id: true}}, id) {
			t.Fatal("Echo-only scope ignored")
		}
		if id == "monaco-editor" && !h.Enabled(domain.Bot{ID: "worker", Plugins: map[string]bool{id: false}}, id) {
			t.Fatal("app scope used per-bot selections")
		}
		plugin.Enabled = false
		if err = h.Save(plugin); err != nil {
			t.Fatal(err)
		}
		if h.Enabled(domain.Bot{ID: "echo", Plugins: map[string]bool{id: true}}, id) {
			t.Fatal("global disable ignored", id)
		}
	}
}
