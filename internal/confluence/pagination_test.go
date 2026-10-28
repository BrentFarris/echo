package confluence

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfluenceCloudCursorWithoutWikiPrefix(t *testing.T) {
	calls := 0
	c := &Client{Config: Config{Deployment: "cloud", URL: "https://site.atlassian.net", Email: "reader@example.com", TokenType: "scoped", CloudID: "cloud-1"}, Token: "private-token"}
	c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.atlassian.com" || r.URL.Path != "/ex/confluence/cloud-1/wiki/rest/api/search" || r.URL.Query().Get("cql") != "type = page" {
			t.Error(r.URL)
		}
		if calls == 1 {
			return jsonResponse(`{"results":[],"_links":{"next":"/rest/api/search?cursor=opaque%2Bcursor%3D&limit=10"}}`), nil
		}
		if r.URL.Query().Get("cursor") != "opaque+cursor=" || r.URL.Query().Get("limit") != "25" {
			t.Error(r.URL)
		}
		return jsonResponse(`{"results":[]}`), nil
	})}
	first, err := c.Search(context.Background(), "type = page", 0, "")
	if err != nil || first.NextContinuation == "" {
		t.Fatal(first, err)
	}
	if _, err = c.Search(context.Background(), "type = page", 0, first.NextContinuation); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestConfluenceDataCenterContentTypes(t *testing.T) {
	for _, kind := range []string{"blogpost", "comment", "attachment", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/confluence/rest/api/content/42" {
					t.Error(r.Method, r.URL)
				}
				json.NewEncoder(w).Encode(map[string]any{"id": "42", "type": kind, "version": map[string]any{"number": 1}, "body": map[string]any{"storage": map[string]any{"value": "<p>Body</p>"}}, "_links": map[string]any{"download": "/download/attachments/42/file.pdf"}})
			}))
			defer server.Close()
			result, err := testClient(server).Read(context.Background(), "42", "", "")
			if kind == "unsupported" {
				if err == nil || !strings.Contains(err.Error(), "unsupported") {
					t.Fatal(err)
				}
				return
			}
			if err != nil || !result.Complete || result.Type != kind {
				t.Fatal(result, err)
			}
			if kind == "attachment" {
				if result.Body != "" || result.Metadata["download"] != server.URL+"/confluence/download/attachments/42/file.pdf" {
					t.Fatal(result)
				}
			} else if result.Body != "Body" {
				t.Fatal(result)
			}
		})
	}
}
