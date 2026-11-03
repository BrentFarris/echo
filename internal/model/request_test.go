package model

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/brent/echo/internal/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderRequestPinsEndpointAndKeepsCredentialInHost(t *testing.T) {
	calls := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json; secret=private-credential")
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "private-credential" {
			t.Errorf("wrong authorized request %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"reply":"private-credential"}`)
	}))
	defer remote.Close()
	client := New()
	endpoint := domain.Endpoint{URL: remote.URL + "/v1"}
	result, e := client.ProviderRequest(context.Background(), endpoint, "private-credential", json.RawMessage(`{"path":"messages","authHeader":"x-api-key","body":{"text":"hello"}}`))
	data, _ := json.Marshal(result)
	if e != nil || strings.Contains(string(data), "private-credential") || !strings.Contains(string(data), "[redacted]") {
		t.Fatal(string(data), e)
	}
	for _, args := range []string{`{"path":"https://example.com/steal"}`, `{"path":"//example.com/steal"}`, `{"path":"../admin"}`, `{"path":"%2e%2e/admin"}`, `{"path":"%252e%252e/admin"}`, `{"path":"..%5cadmin"}`, `{"path":"messages","headers":{"Authorization":"injected"}}`, `{"path":"messages","authHeader":"attacker"}`, `{"path":"messages","method":"DELETE"}`} {
		if _, e = client.ProviderRequest(context.Background(), endpoint, "private-credential", json.RawMessage(args)); e == nil {
			t.Fatal("allowed escaped provider request", args)
		}
	}
	if calls != 1 {
		t.Fatal("rejected requests reached server", calls)
	}
}
