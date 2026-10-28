package confluence

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// Only return profile fields supplied by Confluence. In Cloud, private email and
// display-name fields can be null even when the account ID is visible.
type User struct {
	AccountID   string `json:"accountId,omitempty"`
	UserKey     string `json:"userKey,omitempty"`
	Username    string `json:"username,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	PublicName  string `json:"publicName,omitempty"`
	Email       string `json:"email,omitempty"`
	AccountType string `json:"accountType,omitempty"`
}

func userProfile(raw map[string]any) User {
	return User{AccountID: str(raw["accountId"]), UserKey: str(raw["userKey"]), Username: str(raw["username"]), DisplayName: str(raw["displayName"]), PublicName: str(raw["publicName"]), Email: str(raw["email"]), AccountType: str(raw["accountType"])}
}

func (u User) identified() bool { return u.AccountID != "" || u.UserKey != "" || u.Username != "" }

type UserSearchResult struct {
	Results          []User `json:"results"`
	NextContinuation string `json:"nextContinuation,omitempty"`
}

func (c *Client) SearchUsers(ctx context.Context, name string, limit int, token string) (UserSearchResult, error) {
	out := UserSearchResult{Results: []User{}}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 1024 || strings.ContainsFunc(name, unicode.IsControl) {
		return out, errors.New("Confluence user query must be a visible name of 1–1024 bytes without control characters")
	}
	if limit == 0 {
		limit = 25
	}
	if limit < 1 || limit > 100 {
		return out, errors.New("Confluence user search limit must be 1–100")
	}
	// Escape the CQL string literal; the caller supplies a name, not query syntax.
	cql := `user.fullname ~ "` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(name) + `"`
	path := "/rest/api/search/user"
	if c.Config.Deployment == "datacenter" {
		path = "/rest/api/search"
	}
	binding := c.binding("users.search", name, limit)
	query := url.Values{"cql": {cql}, "limit": {strconv.Itoa(limit)}}
	if token != "" {
		next, err := decodeContinuation(token, binding)
		if err != nil {
			return out, err
		}
		if next.Start != "" {
			n, err := strconv.Atoi(next.Start)
			if err != nil || n < 0 {
				return out, errors.New("invalid Confluence user search offset")
			}
			query.Set("start", next.Start)
		}
		if next.Cursor != "" {
			query.Set("cursor", next.Cursor)
		}
	}
	raw, err := c.request(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return out, err
	}
	rows, ok := raw["results"].([]any)
	if !ok {
		return out, errors.New("Confluence returned an invalid user search result")
	}
	for _, row := range rows {
		r := object(row)
		profile := object(r["user"])
		if profile == nil {
			profile = r
		}
		user := userProfile(profile)
		if user.identified() {
			out.Results = append(out.Results, user)
		}
	}
	out.NextContinuation, err = c.searchContinuation(raw, path, cql, binding)
	if err != nil {
		return out, err
	}
	return out, resultSize(out)
}

func (c *Client) CurrentUser(ctx context.Context) (User, error) {
	raw, err := c.request(ctx, http.MethodGet, "/rest/api/user/current", nil, nil)
	if err != nil {
		return User{}, err
	}
	user := userProfile(raw)
	if !user.identified() || str(raw["type"]) == "anonymous" {
		return User{}, errors.New("Confluence did not return an authenticated token account")
	}
	return user, resultSize(user)
}
