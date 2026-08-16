package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewValidatesToken(t *testing.T) {
	// Make sure ATLASSIAN_AUTH_TOKEN is not set for these tests.
	t.Setenv("ATLASSIAN_AUTH_TOKEN", "")

	c, err := New("https://company.atlassian.net", "user@company.com", "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.baseURL != "https://company.atlassian.net" {
		t.Fatalf("baseURL = %q", c.baseURL)
	}

	c2, err := New("https://company.atlassian.net/", "", "t")
	if err != nil {
		t.Fatalf("trailing slash should be stripped: %v", err)
	}
	if c2.baseURL != "https://company.atlassian.net" {
		t.Fatalf("baseURL not trimmed: %q", c2.baseURL)
	}

	_, err = New("https://example.com", "", "")
	if err == nil {
		t.Fatal("expected error for empty token with no env var fallback")
	}
}

func TestNewEnvVarFallback(t *testing.T) {
	t.Setenv("ATLASSIAN_AUTH_TOKEN", "env-token-123")

	c, err := New("https://company.atlassian.net", "", "")
	if err != nil {
		t.Fatalf("unexpected error with env var set: %v", err)
	}
	if c.apiToken != "env-token-123" {
		t.Fatalf("apiToken = %q, want env var fallback", c.apiToken)
	}

	// Settings token takes precedence over env var.
	c2, err := New("https://company.atlassian.net", "", "settings-token")
	if err != nil {
		t.Fatal(err)
	}
	if c2.apiToken != "settings-token" {
		t.Fatalf("apiToken = %q, want settings token to take precedence", c2.apiToken)
	}
}

func TestSearchSingleIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		jql := r.URL.Query().Get("jql")
		if jql != "key = PROJ-123" {
			t.Errorf("jql = %q, want key = PROJ-123", jql)
		}

		w.Header().Set("Content-Type", "application/json")
		resp := SearchResponse{
			Total:      1,
			StartAt:    0,
			MaxResults: 50,
			Issues: []Ticket{{
				Key: "PROJ-123",
				Fields: TicketFields{
					Summary:  "Fix login bug",
					Status:   TicketStatus{Name: "In Progress"},
					Priority: &TicketPriority{Name: "High"},
					Description: ADFNode{
						Type: "doc",
						Content: []ADFNode{{
							Type: "paragraph",
							Content: []ADFNode{{
								Type: "text",
								Text: "Users cannot log in after password reset.",
							}},
						}},
					},
				},
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c, err := New(server.URL, "user@example.com", "token")
	if err != nil {
		t.Fatal(err)
	}

	result, err := c.Search(context.Background(), "key = PROJ-123", []string{"summary", "status"}, 10)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Issues) != 1 {
		t.Fatalf("got %d issues, want 1", len(result.Issues))
	}
	if result.Issues[0].Key != "PROJ-123" {
		t.Errorf("key = %q, want %q", result.Issues[0].Key, "PROJ-123")
	}
	if result.Issues[0].Fields.DescriptionText == "" {
		t.Error("DescriptionText should be populated from ADF")
	}
}

func TestSearchPagination(t *testing.T) {
	page := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startAtStr := r.URL.Query().Get("startAt")
		var startAt int
		fmt.Sscanf(startAtStr, "%d", &startAt)

		w.Header().Set("Content-Type", "application/json")
		resp := SearchResponse{
			Total:      5,
			StartAt:    startAt,
			MaxResults: 2,
			Issues:     make([]Ticket, 2-startAt%3),
		}
		for i := range resp.Issues {
			idx := startAt + i
			resp.Issues[i].Key = fmt.Sprintf("PROJ-%d", idx+1)
		}
		if len(resp.Issues) == 0 {
			resp.Issues = []Ticket{{Key: fmt.Sprintf("PROJ-%d", startAt+1)}}
		}
		json.NewEncoder(w).Encode(resp)
		page++
	}))
	defer server.Close()

	c, err := New(server.URL, "", "t")
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Search(context.Background(), "project = PROJ", nil, 2)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Issues) == 0 {
		t.Error("expected at least one issue")
	}
	if page > 10 {
		t.Errorf("pagination made %d requests, expected <=10", page)
	}
}

func TestSearchAuthFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"authentication failed"}`))
	}))
	defer server.Close()

	c, err := New(server.URL, "", "wrong-token")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Search(context.Background(), "project = X", nil, 10)
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
	if !containsStr(err.Error(), "unauthorized") && !containsStr(err.Error(), "401") {
		t.Errorf("error should mention unauthorized/401: %v", err)
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	c, _ := New("https://example.atlassian.net", "", "t")
	_, err := c.Search(context.Background(), "", nil, 10)
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestADFToText(t *testing.T) {
	tests := []struct {
		node   ADFNode
		wanted string
	}{
		{
			node: ADFNode{Type: "doc", Content: []ADFNode{{
				Type:    "paragraph",
				Content: []ADFNode{{Type: "text", Text: "Hello world"}},
			}}},
			wanted: "Hello world\n",
		},
		{
			node: ADFNode{Type: "doc", Content: []ADFNode{{
				Type: "paragraph",
				Content: []ADFNode{{
					Type:  "text", Text: "bold",
					Marks: []ADFMark{{Type: "strong"}},
				}},
			}}},
			wanted: "*bold*\n",
		},
		{
			node: ADFNode{Type: "doc", Content: []ADFNode{{
				Type: "bulletList",
				Content: []ADFNode{{
					Type: "listItem",
					Content: []ADFNode{{
						Type:    "paragraph",
						Content: []ADFNode{{Type: "text", Text: "item1"}},
					}},
				}},
			}}},
			wanted: "  • item1\n",
		},
	}
	for i, tt := range tests {
		got := adfToText(tt.node)
		if got != tt.wanted {
			t.Errorf("case %d: got %q, want %q", i, got, tt.wanted)
		}
	}
}

func TestMinimalJSON(t *testing.T) {
	resp := SearchResponse{
		Total: 1, StartAt: 0, MaxResults: 50,
		Issues: []Ticket{{
			Key: "X-1", Self: "http://self-url", Expand: "renderedFields",
			Fields: TicketFields{
				Summary: "Test",
				Assignee: &TicketAccount{
					DisplayName:  "Alice",
					Self:         "https://self/user",
					AvatarUrls:   AvatarUrls{Size48x48: "https://avatar.png"},
					AccountId:    "12345",
					AccountType:  "atlassian",
					EmailAddress: "alice@example.com",
				},
				Priority: &TicketPriority{Name: "High", Self: "http://self/pri", IconUrl: "http://icon.png"},
			},
		}},
	}
	out, err := resp.JSONRoundTrip()
	if err != nil {
		t.Fatal(err)
	}
	if out.Issues[0].Self != "" {
		t.Error("Self should be stripped")
	}
	if out.Issues[0].Expand != "" {
		t.Error("Expand should be stripped")
	}
	acc := out.Issues[0].Fields.Assignee
	if acc.Self != "" || acc.AvatarUrls.Size48x48 != "" || acc.AccountId != "" || acc.AccountType != "" || acc.EmailAddress != "" {
		t.Error("Account metadata should be stripped")
	}
	if acc.DisplayName == "" {
		t.Error("DisplayName should be preserved")
	}
	pri := out.Issues[0].Fields.Priority
	if pri.Self != "" || pri.IconUrl != "" {
		t.Error("Priority self/iconUrl should be stripped")
	}
}

func TestSearchCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond) // Slow response to allow cancellation
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(SearchResponse{})
	}))
	defer server.Close()

	c, _ := New(server.URL, "", "t")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately
	_, err := c.Search(ctx, "project = X", nil, 10)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func containsStr(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
