package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/brent/echo/internal/jira"
)

func TestJiraReadUnconfigured(t *testing.T) {
	result := Execute(ExecutionContext{Context: context.Background()}, "jira_read", mustJSON(t, map[string]any{
		"query": "key = PROJ-123",
	}))
	if result.Success {
		t.Fatal("expected failure when JiraHost is empty")
	}
	if result.Error == nil || result.Error.Code != "not_configured" {
		t.Fatalf("expected not_configured error, got %v", result.Error)
	}
}

func TestJiraReadEmptyQuery(t *testing.T) {
	result := Execute(ExecutionContext{Context: context.Background(), JiraHost: "https://example.atlassian.net"}, "jira_read", mustJSON(t, map[string]any{
		"query": "",
	}))
	if result.Success {
		t.Fatal("expected failure for empty query")
	}
	if result.Error == nil || result.Error.Code != "invalid_arguments" {
		t.Fatalf("expected invalid_arguments error, got %v", result.Error)
	}
}

func TestJiraReadSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := jira.SearchResponse{
			Total:      1,
			StartAt:    0,
			MaxResults: 50,
			Issues: []jira.Ticket{{
				Key: "PROJ-123",
				Fields: jira.TicketFields{
					Summary:  "Fix crash on startup",
					Status:   jira.TicketStatus{Name: "Open"},
					Priority: &jira.TicketPriority{Name: "Highest"},
				},
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	result := Execute(ExecutionContext{
		Context:      context.Background(),
		JiraHost:     server.URL,
		JiraAPIToken: "token",
	}, "jira_read", mustJSON(t, map[string]any{
		"query":      "key = PROJ-123",
		"maxResults": 5,
	}))
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}

	var out jiraReadOutput
	data, _ := json.Marshal(result.Output)
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}
	if out.ResultCount != 1 {
		t.Errorf("resultCount = %d, want 1", out.ResultCount)
	}
	if out.Total != 1 {
		t.Errorf("total = %d, want 1", out.Total)
	}
	if out.Query != "key = PROJ-123" {
		t.Errorf("query = %q, want %q", out.Query, "key = PROJ-123")
	}
}

func TestJiraReadCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := Execute(ExecutionContext{Context: ctx}, "jira_read", mustJSON(t, map[string]any{
		"query": "project = X",
	}))
	if result.Success {
		t.Fatal("expected failure for canceled context")
	}
}
