package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeploymentSearchAndPagination(t *testing.T) {
	for _, deployment := range []string{"cloud", "datacenter"} {
		t.Run(deployment, func(t *testing.T) {
			var count int
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count++
				path := "/jira/rest/api/3/search/jql"
				if deployment == "datacenter" {
					path = "/jira/rest/api/2/search"
					if r.Header.Get("Authorization") != "Bearer secret-token" {
						t.Error("missing bearer")
					}
				} else if email, token, ok := r.BasicAuth(); !ok || email != "user@example.com" || token != "secret-token" {
					t.Error("missing basic auth")
				}
				if r.Method != "POST" || r.URL.Path != path {
					t.Error(r.Method, r.URL.Path)
				}
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["jql"] != "project = APP" || number(body["maxResults"]) != 50 || len(array(body["fields"])) != 6 {
					t.Error(body)
				}
				if count == 2 {
					if deployment == "cloud" && body["nextPageToken"] != "next+token" {
						t.Error(body)
					}
					if deployment == "datacenter" && number(body["startAt"]) != 1 {
						t.Error(body)
					}
				}
				result := map[string]any{"issues": []any{map[string]any{"key": "APP-1"}}}
				if deployment == "cloud" {
					if count == 1 {
						result["nextPageToken"] = "next+token"
					}
				} else {
					result["total"] = 2
				}
				_ = json.NewEncoder(w).Encode(result)
			}))
			defer server.Close()
			client := Client{Config: Config{Deployment: deployment, URL: server.URL + "/jira"}, Token: "secret-token", HTTPClient: server.Client()}
			if deployment == "cloud" {
				client.Config.Email = "user@example.com"
			}
			first, err := client.Search(context.Background(), "project = APP", nil, 0, "")
			if err != nil || first["nextCursor"] == "" {
				t.Fatal(first, err)
			}
			second, err := client.Search(context.Background(), "project = APP", nil, 0, first["nextCursor"].(string))
			if err != nil || second["nextCursor"] != "" || count != 2 {
				t.Fatal(second, err, count)
			}
		})
	}
}

func TestScopedCloudBase(t *testing.T) {
	c := Client{Config: Config{Deployment: "cloud", URL: "https://example.atlassian.net", Email: "user@example.com", CloudID: "cloud-123"}, Token: "secret"}
	base, err := c.base()
	if err != nil || base != "https://api.atlassian.com/ex/jira/cloud-123/rest/api/3" {
		t.Fatal(base, err)
	}
	c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != base+"/myself" {
			t.Error(r.URL)
		}
		if email, token, ok := r.BasicAuth(); !ok || email != c.Config.Email || token != c.Token {
			t.Error("scoped Cloud authentication missing")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"displayName":"Scoped reader"}`)), Header: http.Header{}}, nil
	})}
	if _, err := c.Myself(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestIssueAllFieldsCommentsAndRichText(t *testing.T) {
	var comments int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/2/issue/APP-1":
			if r.URL.Query().Get("fields") != "*all,-comment" || r.URL.Query().Get("expand") != "names,schema" {
				t.Error(r.URL)
			}
			fmt.Fprint(w, `{"id":"10","key":"APP-1","names":{"customfield_1":"Business context"},"fields":{"description":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Visible description"}]}]},"customfield_1":{"nested":"preserved"},"issuelinks":[{"id":"link"}],"attachment":[{"filename":"report.pdf"}]}}`)
		case "/rest/api/2/issue/APP-1/comment":
			if r.URL.Query().Get("startAt") != fmt.Sprint(comments) {
				t.Error(r.URL)
			}
			comments++
			fmt.Fprintf(w, `{"total":2,"comments":[{"id":"%d","body":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Comment %d"}]}]}}]}`, comments, comments)
		default:
			t.Error(r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := Client{Config: Config{Deployment: "datacenter", URL: server.URL}, Token: "secret"}
	v, err := c.Issue(context.Background(), "APP-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	fields := v["fields"].(map[string]any)
	if fields["descriptionText"] != "Visible description" || fields["customfield_1"].(map[string]any)["nested"] != "preserved" {
		t.Fatal(v)
	}
	discussion := v["comments"].(map[string]any)
	if len(array(discussion["comments"])) != 2 || discussion["nextCursor"] != "" || comments != 2 {
		t.Fatal(v)
	}
	if array(discussion["comments"])[0].(map[string]any)["bodyText"] != "Comment 1" {
		t.Fatal(v)
	}
}

func TestLimitsAndIncompleteComments(t *testing.T) {
	mode := "large-comments"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/comment") {
			if mode == "comments-error" {
				w.WriteHeader(403)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"total": 3, "comments": []any{map[string]any{"body": strings.Repeat("x", 400000)}, map[string]any{"body": strings.Repeat("x", 400000)}}})
		} else if mode == "large-field" {
			_ = json.NewEncoder(w).Encode(map[string]any{"fields": map[string]any{"description": strings.Repeat("x", MaxResultBytes)}})
		} else {
			fmt.Fprint(w, `{"key":"APP-1","fields":{}}`)
		}
	}))
	defer server.Close()
	c := Client{Config: Config{Deployment: "datacenter", URL: server.URL}, Token: "secret"}
	v, err := c.Issue(context.Background(), "APP-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	discussion := v["comments"].(map[string]any)
	if discussion["incomplete"] != true || len(array(discussion["comments"])) != 2 {
		t.Fatal(discussion)
	}
	page, err := decodeCursor(discussion["nextCursor"].(string), "comments:APP-1")
	if err != nil || page.Offset != 2 {
		t.Fatal(page, err)
	}
	mode = "large-field"
	if _, err = c.Issue(context.Background(), "APP-1", nil); err == nil || !strings.Contains(err.Error(), "fewer fields") {
		t.Fatal(err)
	}
	mode = "comments-error"
	v, err = c.Issue(context.Background(), "APP-1", nil)
	if err != nil || v["comments"].(map[string]any)["incomplete"] != true || v["comments"].(map[string]any)["nextCursor"] == "" {
		t.Fatal(v, err)
	}
}

func TestRequestConfinementAndErrors(t *testing.T) {
	var escaped atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { escaped.Add(1) }))
	defer target.Close()
	mode := "redirect"
	token := "private-token"
	encoded := base64.StdEncoding.EncodeToString([]byte(":" + token))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "redirect":
			http.Redirect(w, r, target.URL, 302)
		case "rate":
			w.Header().Set("Retry-After", "9")
			w.WriteHeader(429)
			fmt.Fprintf(w, `{"errorMessages":["%s %s"]}`, token, encoded)
		case "echo":
			fmt.Fprintf(w, `{"displayName":"%s %s","%s":"secret key"}`, token, encoded, token)
		case "invalid":
			fmt.Fprint(w, "<html>login</html>")
		case "big":
			fmt.Fprint(w, strings.Repeat("x", maxResponseBytes+1))
		}
	}))
	defer server.Close()
	c := Client{Config: Config{Deployment: "datacenter", URL: server.URL}, Token: token}
	if _, err := c.Myself(context.Background()); err == nil || escaped.Load() != 0 {
		t.Fatal(err, escaped.Load())
	}
	for _, id := range []string{"../APP-1", "APP-1/comment", "APP%2f1", "a?b", "a\\b", ""} {
		if _, err := c.Issue(context.Background(), id, nil); err == nil {
			t.Fatal(id)
		}
	}
	mode = "rate"
	_, err := c.Myself(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Retry-After: 9") || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), encoded) {
		t.Fatal(err)
	}
	mode = "echo"
	v, err := c.request(context.Background(), "GET", "/myself", nil, nil)
	b, _ := json.Marshal(v)
	if err != nil || strings.Contains(string(b), token) || strings.Contains(string(b), encoded) {
		t.Fatal(string(b), err)
	}
	for _, m := range []string{"invalid", "big"} {
		mode = m
		if _, err := c.Myself(context.Background()); err == nil {
			t.Fatal(m)
		}
	}
	for _, size := range []int{-1, 101} {
		if _, err := c.Search(context.Background(), "project=APP", nil, size, ""); err == nil {
			t.Fatal(size)
		}
	}
	if _, err := c.Comments(context.Background(), "APP-1", 50, "invalid"); err == nil {
		t.Fatal("bad cursor accepted")
	}
}

func TestCancellationAndConfigValidation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	c := Client{Config: Config{Deployment: "datacenter", URL: server.URL}, Token: "secret"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.Myself(ctx); done <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("mock Jira request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("request did not stop")
	}
	for _, raw := range []string{"file:///etc/passwd", "https://user:pass@host", "https://host?x", "https://host#x", "https://host/../other", "https://host/%2fother"} {
		if _, err := Normalize(Config{URL: raw}); err == nil {
			t.Fatal(raw)
		}
	}
	if _, err := Normalize(Config{URL: "http://cloud.atlassian.net"}); err == nil {
		t.Fatal("cloud accepted HTTP")
	}
	if _, err := Normalize(Config{URL: "https://cloud.atlassian.net", CloudID: "../escape"}); err == nil {
		t.Fatal("Cloud ID escaped")
	}
	for _, token := range []string{"x\r\ny", strings.Repeat("x", 16385)} {
		if ValidateToken(token) == nil {
			t.Fatal("bad token")
		}
	}
}

func TestDiagnosticRedactionBeforeEscapingAndTruncation(t *testing.T) {
	token := `private-"quoted"-token`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]any{"errorMessages": []string{strings.Repeat("x", 2020) + token}})
	}))
	defer server.Close()
	c := Client{Config: Config{Deployment: "datacenter", URL: server.URL}, Token: token}
	_, err := c.Myself(context.Background())
	if err == nil || strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), "quoted") {
		t.Fatal(err)
	}
}
