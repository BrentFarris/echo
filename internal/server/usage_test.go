package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/brent/echo/internal/contextmanager"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
	"github.com/brent/echo/internal/plugins"
)

func usageStream(w http.ResponseWriter, input, output int, tool bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	delta := map[string]any{"content": "Done"}
	if tool {
		delta["tool_calls"] = []any{map[string]any{"index": 0, "id": "tool-one", "function": map[string]string{"name": "unknown_tool", "arguments": "{}"}}}
	}
	data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": "stop"}}})
	// Duplicate snapshots must never be added together.
	fmt.Fprintf(w, "data: %s\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d}}\n\ndata: [DONE]\n\n", data, input, output)
}

func TestTurnUsageAcrossModelAndFailedToolRounds(t *testing.T) {
	s := testServer(t)
	requests := 0
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		requests++
		usageStream(w, requests*10, 2, requests == 1)
	})
	c := chat(t, s, g)
	d, err := s.send(c.ID, SendRequest{Content: "test usage", Generation: g})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	if d.err != nil {
		t.Fatal(d.err)
	}
	messages, _ := s.Store.Messages(c.ID)
	var run domain.Run
	if err := s.Store.Get("runs", messages[len(messages)-1].RunID, &run); err != nil {
		t.Fatal(err)
	}
	stats := run.Statistics
	if stats == nil || stats.ModelCalls != 2 || stats.ToolCalls != 1 || stats.InputTokens != 30 || stats.OutputTokens != 4 || stats.CompletedAt == nil || stats.StartedAt.After(*stats.CompletedAt) {
		t.Fatal(stats)
	}
	totals, _ := s.Store.Usage()
	if totals.UsageCounts != stats.UsageCounts {
		t.Fatal(totals, stats)
	}
	started := stats.StartedAt
	startRunStatistics(&run)
	if run.Statistics.StartedAt != started || run.Statistics.CompletedAt != nil || run.Statistics.InputTokens != 30 {
		t.Fatal(run.Statistics)
	}
	if _, err := s.Store.ResetUsage("reset"); err != nil {
		t.Fatal(err)
	}
	var saved domain.Run
	s.Store.Get("runs", run.ID, &saved)
	if saved.Statistics.InputTokens != 30 {
		t.Fatal(saved)
	}
}

func TestUsageIncludesSummaryAndInlineRequests(t *testing.T) {
	s := testServer(t)
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) { usageStream(w, 12, 3, false) })
	var endpoint domain.Endpoint
	s.Store.Get("endpoints", g.EndpointID, &endpoint)
	// The same completion callback is supplied to compressors and normal requests.
	summary := contextmanager.CompressionRequest{Endpoint: endpoint, Generation: g, Complete: s.contextCompletion("")}
	if _, err := summary.Complete(context.Background(), endpoint, g, []model.Message{{Role: "user", Content: "summarize"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	bot, input, _ := inlineFixture(t, s)
	input.Generation = g
	setRemotePassword(t, s)
	events := inlineEvents(t, inlineHTTP(s, bot, input, remoteLogin(t, s), context.Background()))
	if events[len(events)-1].Type != "done" {
		t.Fatal(events)
	}
	totals, _ := s.Store.Usage()
	if totals.ModelCalls != 2 || totals.InputTokens != 24 || totals.OutputTokens != 6 {
		t.Fatal(totals)
	}
}

func TestUsageAPIResetValidationAndAuthentication(t *testing.T) {
	s := testServer(t)
	s.Store.RecordModelUsage("one", &domain.TokenUsage{InputTokens: 10, OutputTokens: 4}, "", nil)
	if response := scmAPI(t, s, "GET", "/usage", nil); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if response := scmAPI(t, s, "POST", "/usage/reset", map[string]any{}); response.Code == 200 {
		t.Fatal("accepted missing reset ID")
	}
	if response := scmAPI(t, s, "POST", "/usage/reset", map[string]any{"requestId": "reset"}); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	u, _ := s.Store.Usage()
	if u.InputTokens != 0 {
		t.Fatal(u)
	}
	for _, method := range []string{"GET", "POST"} {
		path := "/api/v1/usage"
		if method == "POST" {
			path += "/reset"
		}
		r := httptest.NewRequest(method, "https://echo.example"+path, nil)
		r.RemoteAddr = "192.0.2.10:12345"
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code == 200 {
			t.Fatal("unauthenticated usage access")
		}
	}
}

func TestUsageReportedBeforeStreamFailureIsRetained(t *testing.T) {
	s := testServer(t)
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":2}}\n\ndata: {\"error\":{\"message\":\"failed\"}}\n\n")
	})
	var endpoint domain.Endpoint
	s.Store.Get("endpoints", g.EndpointID, &endpoint)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.complete(ctx, endpoint, g, nil, nil, "", nil); err == nil {
		t.Fatal("expected failure")
	}
	u, _ := s.Store.Usage()
	if u.InputTokens != 9 || u.OutputTokens != 2 {
		t.Fatal(u)
	}
}

func TestUsageCustomProviderDelegationIsCountedOnce(t *testing.T) {
	s := testServer(t)
	guest, err := os.ReadFile("../plugins/guest/builtin.wasm")
	if err != nil {
		t.Fatal(err)
	}
	manifest := domain.PluginManifest{API: 1, ID: "usage-provider", Name: "Usage provider", Version: "1", Scope: "bot", Capabilities: []string{"model.complete"}, Providers: []string{"usage_provider_complete"}, Settings: plugins.Schema(map[string]any{}), Config: map[string]any{}, Tools: []domain.Tool{{Name: "usage_provider_complete", Description: "Delegate", Capability: "model.complete", Parameters: map[string]any{"type": "object"}}}}
	installed, err := s.Plugins.Install(manifest, guest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Plugins.ApproveDigest(installed.ID, installed.Digest); err != nil {
		t.Fatal(err)
	}
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) { usageStream(w, 40, 7, false) })
	var endpoint domain.Endpoint
	s.Store.Get("endpoints", g.EndpointID, &endpoint)
	endpoint.Provider = "usage_provider_complete"
	bot, _ := s.Bot("echo")
	ctx := context.WithValue(context.Background(), providerKey{}, &providerInvocation{Scope: domain.ExecutionScope{Bot: bot}})
	result, err := s.complete(ctx, endpoint, g, []model.Message{{Role: "user", Content: "test"}}, nil, "", nil)
	if err != nil || result.Usage == nil || result.Usage.InputTokens != 40 {
		t.Fatal(result, err)
	}
	u, err := s.Store.Usage()
	if err != nil || u.ModelCalls != 1 || u.InputTokens != 40 || u.OutputTokens != 7 {
		t.Fatal(u, err)
	}
}
