package plugins

import (
	"context"
	"encoding/json"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDisableCancelsActivePlugin(t *testing.T) {
	guest, e := os.ReadFile("guest/builtin.wasm")
	if e != nil {
		t.Fatal(e)
	}
	db, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	started := make(chan struct{})
	h, e := New(context.Background(), db, t.TempDir(), guest, func(ctx context.Context, _ domain.ExecutionScope, _ string, _ json.RawMessage) (any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close(context.Background())
	finished := make(chan error, 1)
	go func() {
		_, e := h.Invoke(context.Background(), domain.ExecutionScope{Bot: domain.Bot{ID: "echo"}}, "web_get", json.RawMessage(`{"url":"https://example.com"}`))
		finished <- e
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("plugin did not enter broker")
	}
	m, _ := h.Get("web-get")
	m.Enabled = false
	if e = h.Save(m); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-finished:
		if e == nil {
			t.Fatal("revoked plugin succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("plugin survived revocation")
	}
}

func TestGuestHasNoAmbientFilesystemOrEnvironment(t *testing.T) {
	source := `package main
import("os";"encoding/json")
func main(){_,err:=os.ReadFile("/private.txt");json.NewEncoder(os.Stdout).Encode(map[string]any{"result":map[string]any{"readDenied":err!=nil,"environment":os.Getenv("ECHO2_TEST_SECRET")}})}`
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0600)
	command := exec.Command("go", "build", "-o", filepath.Join(dir, "guest.wasm"), filepath.Join(dir, "main.go"))
	command.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if output, e := command.CombinedOutput(); e != nil {
		t.Fatalf("compile isolation fixture: %v %s", e, output)
	}
	t.Setenv("ECHO2_TEST_SECRET", "must-stay-outside")
	guest, _ := os.ReadFile("guest/builtin.wasm")
	db, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	h, e := New(context.Background(), db, t.TempDir(), guest, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close(context.Background())
	wasm, _ := os.ReadFile(filepath.Join(dir, "guest.wasm"))
	m := domain.PluginManifest{API: 1, ID: "probe", Name: "Probe", Tools: []domain.Tool{{Name: "probe_check", Parameters: Schema(map[string]any{})}}}
	if _, e = h.Install(m, wasm); e != nil {
		t.Fatal(e)
	}
	value, e := h.Invoke(context.Background(), domain.ExecutionScope{Bot: domain.Bot{ID: "echo"}}, "probe_check", json.RawMessage(`{}`))
	data, _ := json.Marshal(value)
	if e != nil || !strings.Contains(string(data), `"readDenied":true`) || strings.Contains(string(data), "must-stay-outside") {
		t.Fatal(string(data), e)
	}
}
