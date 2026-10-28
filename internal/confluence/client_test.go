package confluence

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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func jsonResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func testClient(server *httptest.Server) *Client {
	return &Client{Config: Config{Deployment: "datacenter", URL: server.URL + "/confluence", TokenType: "classic"}, Token: "private-token"}
}

func TestConfluenceAuthenticationAndCloudRouting(t *testing.T) {
	for _, mode := range []string{"classic", "scoped"} {
		t.Run(mode, func(t *testing.T) {
			client := &Client{Config: Config{Deployment: "cloud", URL: "https://site.atlassian.net", Email: "reader@example.com", TokenType: mode, CloudID: "cloud-1"}, Token: "private-token"}
			client.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if email, token, ok := r.BasicAuth(); !ok || email != "reader@example.com" || token != "private-token" {
					t.Error("missing basic authentication")
				}
				want := "https://site.atlassian.net/wiki/rest/api/search"
				if mode == "scoped" {
					want = "https://api.atlassian.com/ex/confluence/cloud-1/wiki/rest/api/search"
				}
				if strings.Split(r.URL.String(), "?")[0] != want || r.Method != "GET" {
					t.Errorf("wrong request %s %s", r.Method, r.URL)
				}
				return jsonResponse(`{"results":[]}`), nil
			})}
			if _, err := client.Search(context.Background(), `type = page AND title ~ "résumé & plans"`, 0, ""); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConfluenceSearchPaginationAndBinding(t *testing.T) {
	cql := `type IN (page, blogpost, comment) AND text ~ "研发 & docs"`
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/confluence/rest/api/search" || r.Header.Get("Authorization") != "Bearer private-token" || r.URL.Query().Get("cql") != cql {
			t.Errorf("wrong request %s", r.URL)
		}
		if r.URL.Query().Get("start") == "2" {
			fmt.Fprint(w, `{"results":[{"content":{"id":"3","type":"comment","title":"Comment"}}]}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"content": map[string]any{"id": "1", "type": "page", "title": "Read me", "space": map[string]any{"key": "ENG"}, "version": map[string]any{"number": 3}, "_links": map[string]any{"webui": "/display/ENG/Read+me"}}, "excerpt": "<p>Hello <b>world</b></p>"}, map[string]any{"content": map[string]any{"id": "2", "type": "attachment", "title": "file.pdf", "extensions": map[string]any{"mediaType": "application/pdf"}}}}, "_links": map[string]any{"next": "/confluence/rest/api/search?start=2&limit=2"}})
	}))
	defer server.Close()
	c := testClient(server)
	first, err := c.Search(context.Background(), cql, 25, "")
	if err != nil || len(first.Results) != 2 || first.NextContinuation == "" {
		t.Fatal(first, err)
	}
	if first.Results[0].Version != 3 || !strings.Contains(first.Results[0].Excerpt, "**world**") || first.Results[1].Metadata == nil || !strings.HasPrefix(first.Results[0].URL, server.URL+"/confluence/display/") {
		t.Fatal(first)
	}
	second, err := c.Search(context.Background(), cql, 25, first.NextContinuation)
	if err != nil || len(second.Results) != 1 || second.NextContinuation != "" {
		t.Fatal(second, err)
	}
	for _, change := range []func(){func() { c.Token = "rotated" }, func() { c.Config.URL += "/other" }} {
		change()
		if _, err = c.Search(context.Background(), cql, 25, first.NextContinuation); err == nil {
			t.Fatal("accepted stale continuation")
		}
	}
	if calls.Load() != 2 {
		t.Fatal("invalid continuation contacted upstream")
	}
}

func TestConfluenceRejectsPaginationEscapes(t *testing.T) {
	for _, link := range []string{"https://evil.example/rest/api/search?start=1", "//evil.example/rest/api/search?start=1", "/confluence/rest/api/other?start=1", "/confluence/rest/api/search/../content?start=1", "/confluence/rest/api/search?cql=different&start=1", "/confluence/rest/api/search?start=-1", "/confluence/rest/api/search"} {
		t.Run(link, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"results": []any{}, "_links": map[string]any{"next": link}})
			}))
			defer server.Close()
			if _, err := testClient(server).Search(context.Background(), "type=page", 0, ""); err == nil {
				t.Fatal("accepted unsafe next link")
			}
		})
	}
}

func TestConfluenceFullUnicodeReadsAndVersionChecks(t *testing.T) {
	body := "<h1>Full document</h1><p>" + strings.Repeat("界🙂é ", 12000) + "</p>"
	var version atomic.Int32
	version.Store(4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/confluence/rest/api/content/42" {
			t.Error(r.URL)
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "42", "type": "page", "title": "Document", "version": map[string]any{"number": version.Load()}, "body": map[string]any{"storage": map[string]any{"value": body}}})
	}))
	defer server.Close()
	c := testClient(server)
	for _, format := range []string{"markdown", "storage"} {
		token := ""
		var combined strings.Builder
		count := 0
		for {
			result, err := c.Read(context.Background(), "42", format, token)
			if err != nil {
				t.Fatal(err)
			}
			if len([]rune(result.Body)) > ChunkCharacters {
				t.Fatal("oversized chunk")
			}
			combined.WriteString(result.Body)
			count++
			if result.Complete {
				if result.NextContinuation != "" {
					t.Fatal("complete result has continuation")
				}
				break
			}
			if result.NextContinuation == token || result.NextContinuation == "" {
				t.Fatal("no progress")
			}
			token = result.NextContinuation
		}
		want := body
		if format == "markdown" {
			want = Markdown(body)
		}
		if combined.String() != want || count < 2 {
			t.Fatal("full body was not reconstructed")
		}
	}
	first, err := c.Read(context.Background(), "42", "markdown", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Read(context.Background(), "43", "markdown", first.NextContinuation); err == nil {
		t.Fatal("accepted different ID")
	}
	if _, err = c.Read(context.Background(), "42", "storage", first.NextContinuation); err == nil {
		t.Fatal("accepted different format")
	}
	version.Add(1)
	if _, err = c.Read(context.Background(), "42", "markdown", first.NextContinuation); err == nil || !strings.Contains(err.Error(), "version changed") {
		t.Fatal(err)
	}
}

func TestConfluenceCloudContentRouting(t *testing.T) {
	for kind, path := range map[string]string{"page": "pages", "blogpost": "blogposts", "inline-comment": "inline-comments", "footer-comment": "footer-comments", "attachment": "attachments"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			c := &Client{Config: Config{Deployment: "cloud", URL: "https://site.atlassian.net", Email: "reader@example.com", TokenType: "classic"}, Token: "private-token"}
			c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					if r.Method != "POST" || r.URL.Path != "/wiki/api/v2/content/convert-ids-to-types" {
						t.Error(r.Method, r.URL)
					}
					data, _ := io.ReadAll(r.Body)
					if string(data) != `{"contentIds":["42"]}` {
						t.Error(string(data))
					}
					return jsonResponse(`{"results":{"42":"` + kind + `"}}`), nil
				}
				if r.Method != "GET" || r.URL.Path != "/wiki/api/v2/"+path+"/42" {
					t.Error(r.Method, r.URL)
				}
				return jsonResponse(`{"id":"42","title":"Content","pageId":"12","version":{"number":2},"mediaType":"application/pdf","fileSize":123,"body":{"storage":{"value":"<p>Complete text</p>"}}}`), nil
			})}
			result, err := c.Read(context.Background(), "42", "", "")
			if err != nil || !result.Complete || result.Type != kind {
				t.Fatal(result, err)
			}
			if kind == "attachment" {
				if result.Body != "" || result.Metadata["mediaType"] != "application/pdf" {
					t.Fatal(result)
				}
			} else if result.Body != "Complete text" {
				t.Fatal(result)
			}
		})
	}
}

func TestConfluenceErrorsLimitsRedactionAndCancellation(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "9")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"message":"private-token"}`)
			}))
			defer server.Close()
			_, err := testClient(server).Search(context.Background(), "type=page", 0, "")
			if err == nil || strings.Contains(err.Error(), "private-token") || !strings.Contains(err.Error(), "Retry-After: 9") {
				t.Fatal(err)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("cql") {
		case "oversize":
			io.WriteString(w, strings.Repeat("x", MaxResponseBytes+1))
		case "redirect":
			http.Redirect(w, r, "https://evil.example", http.StatusFound)
		case "invalid":
			fmt.Fprint(w, "<html>login</html>")
		case "wait":
			<-r.Context().Done()
		default:
			json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"content": map[string]any{"id": "1", "type": "page", "title": "private-token " + base64.StdEncoding.EncodeToString([]byte(":private-token"))}}}})
		}
	}))
	defer server.Close()
	c := testClient(server)
	for _, query := range []string{"oversize", "redirect", "invalid"} {
		if _, err := c.Search(context.Background(), query, 0, ""); err == nil {
			t.Fatal("accepted", query)
		}
	}
	result, err := c.Search(context.Background(), "redact", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "private-token") || strings.Contains(string(raw), base64.StdEncoding.EncodeToString([]byte(":private-token"))) {
		t.Fatal("secret leaked")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = c.Search(ctx, "wait", 0, ""); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestConfluenceConfigValidation(t *testing.T) {
	for _, raw := range []Config{{URL: "https://user:secret@example.com"}, {URL: "https://example.com?query=x"}, {URL: "https://example.com/#fragment"}, {URL: "https://example.com/%2e%2e"}, {URL: "https://example.com/../other"}, {URL: "http://example.com"}, {Deployment: "unknown"}, {TokenType: "unknown"}, {CloudID: "../bad"}, {Email: "reader:password"}} {
		if _, err := Normalize(raw); err == nil {
			t.Fatal("accepted invalid config", raw)
		}
	}
	config, err := Normalize(Config{URL: "https://site.atlassian.net/wiki/"})
	if err != nil || config.URL != "https://site.atlassian.net" {
		t.Fatal(config, err)
	}
	config, err = Normalize(Config{Deployment: "datacenter", URL: "http://internal/confluence"})
	if err != nil || config.Ready() != nil {
		t.Fatal(config, err)
	}
	for _, token := range []string{"bad\ntoken", " token", strings.Repeat("x", 16385)} {
		if ValidateToken(token) == nil {
			t.Fatal("accepted invalid token")
		}
	}
}
