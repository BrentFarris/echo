package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
	"github.com/tetratelabs/wazero"
)

func TestWASIBrokerAndPermissionBoundary(t *testing.T) {
	guest, e := os.ReadFile("guest/builtin.wasm")
	if e != nil {
		t.Fatal("build the bundled WASI guest first", e)
	}
	s, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	calls := 0
	h, e := New(context.Background(), s, filepath.Join(t.TempDir(), "plugins"), guest, func(ctx context.Context, scope domain.ExecutionScope, method string, args json.RawMessage) (any, error) {
		calls++
		if scope.PluginID == "" {
			t.Fatal("missing invocation identity")
		}
		return map[string]any{"method": method}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close(context.Background())
	scope := domain.ExecutionScope{Bot: domain.Bot{ID: "echo", Plugins: map[string]bool{}}}
	value, e := h.Invoke(context.Background(), scope, "web_get", json.RawMessage(`{"url":"https://example.com"}`))
	if e != nil {
		t.Fatal(e)
	}
	if value.(map[string]any)["method"] != "web.get" || calls != 1 {
		t.Fatal(value, calls)
	}
	m := domain.PluginManifest{API: 1, ID: "untrusted", Name: "Untrusted", Capabilities: []string{"web.get"}, Tools: []domain.Tool{{Name: "untrusted_fetch", Capability: "web.get", Parameters: Schema(map[string]any{"url": S("URL")}, "url")}}}
	first, e := h.Install(m, guest)
	if e != nil || !first.Pending {
		t.Fatal(first, e)
	}
	second, e := h.Install(m, guest)
	if e != nil || !second.Pending {
		t.Fatal("pending update inherited unapproved authority", second, e)
	}
	if _, e = h.Invoke(context.Background(), scope, "untrusted_fetch", json.RawMessage(`{"url":"https://example.com"}`)); e == nil || calls != 1 {
		t.Fatal("pending code executed")
	}
	if _, e = h.Approve(m.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = h.Invoke(context.Background(), scope, "untrusted_fetch", json.RawMessage(`{"url":"https://example.com"}`)); e != nil {
		t.Fatal(e)
	}
}
func TestRunawayWASMIsCancelled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCloseOnContextDone(true).WithMemoryLimitPages(1024))
	defer r.Close(context.Background())
	module := []byte{0, 97, 115, 109, 1, 0, 0, 0, 1, 4, 1, 96, 0, 0, 3, 2, 1, 0, 7, 10, 1, 6, 95, 115, 116, 97, 114, 116, 0, 0, 10, 9, 1, 7, 0, 3, 64, 12, 0, 11, 11}
	_, e := r.Instantiate(ctx, module)
	if e == nil || ctx.Err() == nil {
		t.Fatal("infinite loop was not terminated", e)
	}
}
