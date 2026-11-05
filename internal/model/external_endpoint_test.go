package model

import (
	"path/filepath"
	"testing"

	"github.com/brent/echo/internal/domain"
)

func TestExternalEndpointValidationPreservesHTTP(t *testing.T) {
	for _, kind := range []string{"", "http"} {
		if err := ValidateEndpoint(domain.Endpoint{Kind: kind, URL: "http://127.0.0.1:1234/v1"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"codex-cli", "claude-code-cli"} {
		base := domain.Endpoint{Kind: kind}
		if err := ValidateEndpoint(base); err != nil {
			t.Fatal(err)
		}
		base.Executable, base.WorkingDirectory = filepath.Join(t.TempDir(), "agent.exe"), t.TempDir()
		if err := ValidateEndpoint(base); err != nil {
			t.Fatal(err)
		}
		for _, mutate := range []func(*domain.Endpoint){
			func(e *domain.Endpoint) { e.URL = "http://127.0.0.1:1234" },
			func(e *domain.Endpoint) { e.Credential = "api-key" },
			func(e *domain.Endpoint) { e.Provider = "plugin" },
			func(e *domain.Endpoint) { e.Executable = "relative.exe" },
		} {
			e := base
			mutate(&e)
			if ValidateEndpoint(e) == nil {
				t.Fatal("invalid CLI connection accepted", e)
			}
		}
	}
	if ValidateEndpoint(domain.Endpoint{Kind: "unknown"}) == nil {
		t.Fatal("unknown connection accepted")
	}
}
