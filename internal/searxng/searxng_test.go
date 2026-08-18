package searxng

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchSendsQueryAndFormat(t *testing.T) {
	var gotQuery string
	var gotFormat string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		gotFormat = r.URL.Query().Get("format")
		_ = json.NewEncoder(w).Encode(SearchResponse{
			Query: "echo",
			Results: []Result{
				{Title: "Echo", URL: "https://example.test", Content: "result"},
			},
		})
	}))
	defer server.Close()

	response, err := Search(context.Background(), server.URL+"/search", "echo")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if gotQuery != "echo" {
		t.Fatalf("expected query echo, got %q", gotQuery)
	}
	if gotFormat != "json" {
		t.Fatalf("expected format json, got %q", gotFormat)
	}
	if len(response.Results) != 1 || response.Results[0].Title != "Echo" {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestSearchRejectsInvalidURL(t *testing.T) {
	if _, err := Search(context.Background(), "notaurl", "echo"); err == nil {
		t.Fatal("expected invalid URL error")
	}
}

func TestSearchRejectsNonHTTPURL(t *testing.T) {
	if _, err := Search(context.Background(), "file:///tmp/search", "echo"); err == nil {
		t.Fatal("expected non-http URL error")
	}
}

func TestSearchReturnsNon2xxError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusBadGateway)
	}))
	defer server.Close()

	if _, err := Search(context.Background(), server.URL, "echo"); err == nil {
		t.Fatal("expected non-2xx error")
	}
}

func TestSearchReturnsMalformedJSONError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{"))
	}))
	defer server.Close()

	if _, err := Search(context.Background(), server.URL, "echo"); err == nil {
		t.Fatal("expected malformed JSON error")
	}
}

func TestSearchHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Search(ctx, DefaultURL, "echo"); err == nil {
		t.Fatal("expected canceled context error")
	}
}
