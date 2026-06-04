package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/brent/echo/internal/searxng"
)

func TestWebSearchRejectsMissingQuery(t *testing.T) {
	result := Execute(ExecutionContext{Context: context.Background()}, "web_search", mustJSON(t, map[string]any{}))
	if result.Success {
		t.Fatalf("expected missing query to fail, got %#v", result)
	}
	if result.Error == nil || result.Error.Code != "invalid_arguments" {
		t.Fatalf("unexpected error: %#v", result.Error)
	}
}

func TestWebSearchUsesConfiguredSearxngURL(t *testing.T) {
	hit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		if r.URL.Query().Get("q") != "echo agent" {
			t.Fatalf("unexpected query: %q", r.URL.Query().Get("q"))
		}
		_ = json.NewEncoder(w).Encode(searxng.SearchResponse{
			Query: "echo agent",
			Results: []searxng.Result{
				{Title: "Echo Agent", URL: "https://example.test/echo", Content: "Agent search result", Engine: "test", Score: 1.25},
			},
		})
	}))
	defer server.Close()

	result := Execute(ExecutionContext{Context: context.Background(), SearxngURL: server.URL}, "web_search", mustJSON(t, map[string]any{
		"query":      "echo agent",
		"maxResults": 1,
	}))
	if !result.Success {
		t.Fatalf("expected search success, got %#v", result)
	}
	if !hit {
		t.Fatal("expected configured SearXNG server to be called")
	}
	output, ok := result.Output.(webSearchOutput)
	if !ok {
		t.Fatalf("unexpected output type: %T", result.Output)
	}
	if output.Query != "echo agent" || output.ResultCount != 1 {
		t.Fatalf("unexpected output summary: %#v", output)
	}
	if output.Results[0].Title != "Echo Agent" || output.Results[0].URL != "https://example.test/echo" {
		t.Fatalf("unexpected result: %#v", output.Results[0])
	}
}

func TestWebSearchCapsMaxResults(t *testing.T) {
	results := make([]searxng.Result, 12)
	for i := range results {
		results[i] = searxng.Result{Title: fmt.Sprintf("Result %d", i+1)}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(searxng.SearchResponse{
			Query:   "echo",
			Results: results,
		})
	}))
	defer server.Close()

	result := Execute(ExecutionContext{Context: context.Background(), SearxngURL: server.URL}, "web_search", mustJSON(t, map[string]any{
		"query":      "echo",
		"maxResults": 50,
	}))
	if !result.Success {
		t.Fatalf("expected search success, got %#v", result)
	}
	output := result.Output.(webSearchOutput)
	if output.ResultCount != maxWebSearchResults || len(output.Results) != maxWebSearchResults {
		t.Fatalf("expected capped results, got %#v", output)
	}
}

func TestWebSearchDefaultsMaxResults(t *testing.T) {
	results := make([]searxng.Result, 8)
	for i := range results {
		results[i] = searxng.Result{Title: fmt.Sprintf("Result %d", i+1)}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(searxng.SearchResponse{
			Query:   "echo",
			Results: results,
		})
	}))
	defer server.Close()

	result := Execute(ExecutionContext{Context: context.Background(), SearxngURL: server.URL}, "web_search", mustJSON(t, map[string]any{
		"query": "echo",
	}))
	if !result.Success {
		t.Fatalf("expected search success, got %#v", result)
	}
	output := result.Output.(webSearchOutput)
	if output.ResultCount != defaultWebSearchResults || len(output.Results) != defaultWebSearchResults {
		t.Fatalf("expected default result count, got %#v", output)
	}
}
