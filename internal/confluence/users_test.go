package confluence

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfluenceUserLookupCloudRoutingPrivacyAndAuthoredPages(t *testing.T) {
	for _, mode := range []string{"classic", "scoped"} {
		t.Run(mode, func(t *testing.T) {
			c := &Client{Config: Config{Deployment: "cloud", TokenType: mode, URL: "https://site.atlassian.net", Email: "service@example.com", CloudID: "cloud-1"}, Token: "private-token"}
			calls := 0
			c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				prefix := "/wiki"
				host := "site.atlassian.net"
				if mode == "scoped" {
					prefix = "/ex/confluence/cloud-1/wiki"
					host = "api.atlassian.com"
				}
				if r.URL.Host != host || !strings.HasPrefix(r.URL.Path, prefix+"/rest/api/") {
					t.Error("wrong destination", r.URL)
				}
				if email, token, ok := r.BasicAuth(); !ok || email != "service@example.com" || token != "private-token" || r.Method != "GET" {
					t.Error("wrong authentication/method")
				}
				switch strings.TrimPrefix(r.URL.Path, prefix) {
				case "/rest/api/user/current":
					return jsonResponse(`{"type":"known","accountId":"service-id","displayName":"Service Account","email":"service@example.com"}`), nil
				case "/rest/api/search/user":
					if r.URL.Query().Get("cql") != `user.fullname ~ "Brent Farris"` || r.URL.Query().Get("expand") != "" {
						t.Error(r.URL)
					}
					return jsonResponse(`{"results":[{"user":{"accountId":"author-id","displayName":"Brent Farris","email":null}},{"user":{"accountId":"different-id","displayName":null,"publicName":"Brent Farris","email":null}}]}`), nil
				case "/rest/api/search":
					if r.URL.Query().Get("cql") != `type = page AND creator = "author-id" ORDER BY created DESC` || r.URL.Query().Get("limit") != "3" {
						t.Error(r.URL)
					}
					return jsonResponse(`{"results":[{"content":{"id":"3","type":"page","title":"Latest"}},{"content":{"id":"2","type":"page","title":"Second"}},{"content":{"id":"1","type":"page","title":"Third"}}]}`), nil
				default:
					t.Error("unexpected path", r.URL)
					return jsonResponse(`{}`), nil
				}
			})}
			current, err := c.CurrentUser(context.Background())
			if err != nil || current.AccountID != "service-id" {
				t.Fatal(current, err)
			}
			users, err := c.SearchUsers(context.Background(), "Brent Farris", 0, "")
			if err != nil || len(users.Results) != 2 || users.Results[0].AccountID != "author-id" || users.Results[1].PublicName != "Brent Farris" {
				t.Fatal(users, err)
			}
			raw, _ := json.Marshal(users)
			if strings.Contains(string(raw), "email") || strings.Contains(string(raw), "service-id") {
				t.Fatal("fabricated private fields or identity", string(raw))
			}
			pages, err := c.Search(context.Background(), fmt.Sprintf(`type = page AND creator = %q ORDER BY created DESC`, users.Results[0].AccountID), 3, "")
			if err != nil || len(pages.Results) != 3 {
				t.Fatal(pages, err)
			}
			if calls != 3 {
				t.Fatal(calls)
			}
		})
	}
}

func TestConfluenceUserSearchDataCenterAndEscaping(t *testing.T) {
	name := `Brent "Farris" \ Team`
	want := `user.fullname ~ "Brent \"Farris\" \\ Team"`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer private-token" {
			t.Error("missing PAT")
		}
		if r.URL.Path == "/confluence/rest/api/user/current" {
			fmt.Fprint(w, `{"type":"known","username":"service","userKey":"service-key"}`)
			return
		}
		if r.URL.Path != "/confluence/rest/api/search" || r.URL.Query().Get("cql") != want {
			t.Errorf("unexpected request %s, wanted CQL %s", r.URL, want)
		}
		fmt.Fprint(w, `{"results":[{"user":{"username":"brent.farris","userKey":"user-hash","displayName":"Brent Farris","email":"brent@example.com"}}]}`)
	}))
	defer server.Close()
	c := testClient(server)
	users, err := c.SearchUsers(context.Background(), name, 25, "")
	if err != nil || len(users.Results) != 1 || users.Results[0].Username != "brent.farris" || users.Results[0].UserKey != "user-hash" {
		t.Fatal(users, err)
	}
	current, err := c.CurrentUser(context.Background())
	if err != nil || current.Username != "service" || current.UserKey != "service-key" {
		t.Fatal(current, err)
	}
}

func TestConfluenceUserSearchPaginationAndContinuationIsolation(t *testing.T) {
	for _, deployment := range []string{"cloud", "datacenter"} {
		t.Run(deployment, func(t *testing.T) {
			c := &Client{Config: Config{Deployment: deployment, TokenType: "classic", URL: "https://site.example", Email: "reader@example.com"}, Token: "private-token"}
			path := "/rest/api/search/user"
			if deployment == "datacenter" {
				path = "/rest/api/search"
				c.Config.URL += "/confluence"
				c.Config.Email = ""
			}
			calls := 0
			c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Query().Get("start") == "2" {
					return jsonResponse(`{"results":[{"user":{"accountId":"third","userKey":"third-key"}}]}`), nil
				}
				data, _ := json.Marshal(map[string]any{"results": []any{map[string]any{"user": map[string]any{"accountId": "first", "userKey": "first-key"}}, map[string]any{"user": map[string]any{"accountId": "second", "userKey": "second-key"}}}, "_links": map[string]any{"next": path + "?start=2&limit=2"}})
				return jsonResponse(string(data)), nil
			})}
			first, err := c.SearchUsers(context.Background(), "Farris", 25, "")
			if err != nil || len(first.Results) != 2 || first.NextContinuation == "" {
				t.Fatal(first, err)
			}
			second, err := c.SearchUsers(context.Background(), "Farris", 25, first.NextContinuation)
			if err != nil || len(second.Results) != 1 || second.Results[0].AccountID != "third" || second.NextContinuation != "" {
				t.Fatal(second, err)
			}
			if _, err = c.SearchUsers(context.Background(), "Brent", 25, first.NextContinuation); err == nil {
				t.Fatal("accepted another name")
			}
			if _, err = c.SearchUsers(context.Background(), "Farris", 3, first.NextContinuation); err == nil {
				t.Fatal("accepted another limit")
			}
			if _, err = c.Search(context.Background(), `user.fullname ~ "Farris"`, 25, first.NextContinuation); err == nil {
				t.Fatal("accepted user continuation as content continuation")
			}
			c.Token = "rotated"
			if _, err = c.SearchUsers(context.Background(), "Farris", 25, first.NextContinuation); err == nil {
				t.Fatal("accepted rotated token")
			}
			if calls != 2 {
				t.Fatal("invalid continuation contacted server", calls)
			}
		})
	}
}

func TestConfluenceUserSearchRejectsUnsafePagination(t *testing.T) {
	for _, link := range []string{"https://evil.example/rest/api/search/user?start=2", "/wiki/rest/api/search?start=2", "/wiki/rest/api/user/current?start=2", "/wiki/rest/api/search/user?cql=changed&start=2", "/wiki/rest/api/search/user?start=-1"} {
		t.Run(link, func(t *testing.T) {
			c := &Client{Config: Config{Deployment: "cloud", URL: "https://site.example", Email: "reader@example.com"}, Token: "private-token"}
			c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				data, _ := json.Marshal(map[string]any{"results": []any{}, "_links": map[string]any{"next": link}})
				return jsonResponse(string(data)), nil
			})}
			if _, err := c.SearchUsers(context.Background(), "Brent", 0, ""); err == nil {
				t.Fatal("accepted unsafe user pagination")
			}
		})
	}
}

func TestConfluenceUserValidationErrorsRedactionAndCancellation(t *testing.T) {
	c := &Client{Config: Config{Deployment: "cloud", URL: "https://site.example", Email: "reader@example.com"}, Token: "private-token"}
	calls := 0
	c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(`{"accountId":"author-id","displayName":"private-token","email":null,"results":[]}`), nil
	})}
	for _, name := range []string{"", "  ", "Brent\nFarris", strings.Repeat("a", 1025)} {
		if _, err := c.SearchUsers(context.Background(), name, 0, ""); err == nil {
			t.Fatal("accepted invalid name")
		}
	}
	for _, limit := range []int{-1, 101} {
		if _, err := c.SearchUsers(context.Background(), "Brent", limit, ""); err == nil {
			t.Fatal("accepted invalid limit")
		}
	}
	if calls != 0 {
		t.Fatal("invalid input contacted server")
	}
	current, err := c.CurrentUser(context.Background())
	if err != nil || current.DisplayName != "[redacted]" {
		t.Fatal(current, err)
	}
	for _, body := range []string{`{}`, `{"type":"anonymous","username":"anonymous"}`} {
		c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { return jsonResponse(body), nil })}
		if _, err := c.CurrentUser(context.Background()); err == nil {
			t.Fatal("accepted unauthenticated/malformed identity")
		}
	}
	c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response := jsonResponse(`{"message":"private-token"}`)
		response.StatusCode = 403
		return response, nil
	})}
	if _, err := c.SearchUsers(context.Background(), "Brent", 0, ""); err == nil || !strings.Contains(err.Error(), "read scopes") || strings.Contains(err.Error(), "private-token") {
		t.Fatal(err)
	}
	c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.CurrentUser(ctx); err == nil {
		t.Fatal("ignored cancellation")
	}
}
