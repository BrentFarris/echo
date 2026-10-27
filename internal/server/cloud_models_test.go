package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/brent/echo/internal/domain"
)

func TestCloudConnectionsIgnoreLegacyDisabledSetting(t *testing.T) {
	s := testServer(t)
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
	})
	if err := s.Store.Put("plugins", "cloud-models", domain.PluginManifest{ID: "cloud-models", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	var endpoint domain.Endpoint
	if err := s.Store.Get("endpoints", g.EndpointID, &endpoint); err != nil {
		t.Fatal(err)
	}
	endpoint.Cloud = true
	endpoint.Provider = "openai-compatible-cloud"
	response := scmAPI(t, s, "POST", "/endpoints", map[string]any{"endpoint": endpoint})
	if response.Code != http.StatusOK {
		t.Fatal("cloud connection requires enabling", response.Body.String())
	}
	response = scmAPI(t, s, "GET", "/endpoints/"+endpoint.ID+"/models", nil)
	if response.Code != http.StatusOK {
		t.Fatal("cloud model discovery requires enabling", response.Body.String())
	}
	echo, err := s.Bot("echo")
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"name": "Cloud via tool", "url": endpoint.URL, "model": endpoint.Model, "cloud": true, "provider": endpoint.Provider})
	value, err := s.tool(context.Background(), nil, domain.ExecutionScope{Bot: echo}, domain.ToolCall{Function: domain.FunctionCall{Name: "model_save", Arguments: string(args)}})
	if err != nil {
		t.Fatal("model_save requires enabling cloud models", err)
	}
	if saved := value.(domain.Endpoint); !saved.Cloud || saved.Model != endpoint.Model {
		t.Fatal("model_save lost cloud connection settings", saved)
	}
}
