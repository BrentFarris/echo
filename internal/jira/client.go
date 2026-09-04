package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Client is an HTTP client for the Jira REST API v3 using Bearer token auth.
type Client struct {
	baseURL  string
	username string // email for Basic auth (email:token)
	apiToken string // API token (ATATT...) or Bearer token fallback
	http     *http.Client
}

// New creates a new Jira client. It validates the base URL and strips trailing slashes.
// If apiToken is empty, falls back to the ATLASSIAN_AUTH_TOKEN environment variable (same as Jane).
func New(baseURL, username, apiToken string) (*Client, error) {
	token := strings.TrimSpace(apiToken)
	if token == "" {
		token = os.Getenv(jiraEnvAuth)
	}
	if token == "" {
		return nil, fmt.Errorf("jira API token not set (configure in settings or set ATLASSIAN_AUTH_TOKEN environment variable)")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return &Client{
		baseURL:  baseURL,
		username: strings.TrimSpace(username),
		apiToken: token,
		http:     &http.Client{},
	}, nil
}

// setAuth sets the Authorization header. Uses Basic auth (email:token) if username is set,
// otherwise falls back to Bearer token (for env var / Jane compat).
func (c *Client) setAuth(req *http.Request) {
	if c.username != "" && c.apiToken != "" {
		req.SetBasicAuth(c.username, c.apiToken)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.apiToken)
	}
}

// Search calls the Jira search endpoint with auto-pagination.
// Uses GET with query params (same pattern as Jane) instead of POST JSON body.
func (c *Client) Search(ctx context.Context, jql string, fields []string, maxResults int) (*SearchResponse, error) {
	jql = strings.TrimSpace(jql)
	if jql == "" {
		return nil, fmt.Errorf("jql query is required")
	}
	if len(fields) == 0 {
		fields = defaultFields()
	}
	if maxResults <= 0 {
		maxResults = 50
	}
	if maxResults > 100 {
		maxResults = 100
	}

	const maxPages = 10
	startAt := 0
	var final SearchResponse

	for page := 0; page < maxPages; page++ {
		res, err := c.searchPage(ctx, jql, fields, startAt, maxResults)
		if err != nil {
			return nil, fmt.Errorf("jira search page %d failed: %w", page+1, err)
		}

		// Convert ADF to text for descriptions and comments.
		for i := range res.Issues {
			res.Issues[i].Fields.DescriptionText = adfToText(res.Issues[i].Fields.Description)
			for j := range res.Issues[i].Fields.Comment.Comments {
				res.Issues[i].Fields.Comment.Comments[j].BodyText = adfToText(res.Issues[i].Fields.Comment.Comments[j].Body)
			}
		}

		final.Issues = append(final.Issues, res.Issues...)
		final.Total = res.Total
		if res.Total-startAt <= res.MaxResults {
			break
		}
		startAt += res.MaxResults
	}

	return &final, nil
}

func (c *Client) searchPage(ctx context.Context, jql string, fields []string, startAt, maxResults int) (*SearchResponse, error) {
	q := url.Values{}
	q.Set("jql", strings.ReplaceAll(jql, "\n", " "))
	q.Set("maxResults", fmt.Sprintf("%d", maxResults))
	q.Set("startAt", fmt.Sprintf("%d", startAt))
	if len(fields) > 0 {
		q.Set("fields", strings.Join(fields, ","))
	}

	reqURL := c.baseURL + "/rest/api/3/search/jql?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("unauthorized (status %d): check Jira host URL, email, and API token", resp.StatusCode)
		}
		return nil, fmt.Errorf("jira returned status %d: %s", resp.StatusCode, trimBody(string(bodyBytes), 200))
	}

	var result SearchResponse
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

func defaultFields() []string {
	return []string{"key", "summary", "description", "status", "assignee", "priority", "comment"}
}

func trimBody(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// JSONRoundTrip serializes then deserializes via the minimal representation.
// Used in tests to validate MinimalJSON roundtrip safety.
func (r *SearchResponse) JSONRoundTrip() (*SearchResponse, error) {
	jsonStr := r.MinimalJSON()
	var out SearchResponse
	if err := json.Unmarshal([]byte(jsonStr), &out); err != nil {
		return nil, err
	}
	return &out, nil
}
