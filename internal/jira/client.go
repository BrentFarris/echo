// Package jira implements the fixed, read-only Jira operations exposed to bots.
package jira

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const MaxResultBytes = 1 << 20
const maxResponseBytes = 8 << 20

type Config struct {
	Deployment string `json:"deployment"`
	URL        string `json:"url"`
	Email      string `json:"email"`
	CloudID    string `json:"cloudId"`
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func Normalize(c Config) (Config, error) {
	c.Deployment = strings.TrimSpace(c.Deployment)
	if c.Deployment == "" {
		c.Deployment = "cloud"
	}
	c.URL = strings.TrimRight(strings.TrimSpace(c.URL), "/")
	c.Email = strings.TrimSpace(c.Email)
	c.CloudID = strings.TrimSpace(c.CloudID)
	if c.Deployment != "cloud" && c.Deployment != "datacenter" {
		return c, errors.New("Jira deployment must be cloud or datacenter")
	}
	if strings.ContainsAny(c.Email, "\r\n\x00:") {
		return c, errors.New("invalid Jira email")
	}
	if c.CloudID != "" && !identifier.MatchString(c.CloudID) {
		return c, errors.New("invalid Jira Cloud ID")
	}
	if c.Deployment == "datacenter" && (c.CloudID != "" || c.Email != "") {
		return c, errors.New("email and Cloud ID apply only to Jira Cloud")
	}
	if c.URL == "" {
		return c, nil
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Scheme != "http" && u.Scheme != "https") || strings.ContainsAny(c.URL, "#\\\r\n\x00") {
		return c, errors.New("Jira URL must be an HTTP(S) site base URL without credentials, query or fragment")
	}
	if c.Deployment == "cloud" && u.Scheme != "https" {
		return c, errors.New("Jira Cloud requires HTTPS")
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." || strings.ContainsAny(part, "\\\r\n\x00") {
			return c, errors.New("invalid Jira base path")
		}
	}
	if strings.Contains(u.EscapedPath(), "%") {
		return c, errors.New("Jira base path must not contain encoded characters")
	}
	return c, nil
}

type Client struct {
	Config     Config
	Token      string
	HTTPClient *http.Client
}

func (c *Client) base() (string, error) {
	config, err := Normalize(c.Config)
	if err != nil {
		return "", err
	}
	if config.URL == "" || c.Token == "" || config.Deployment == "cloud" && config.Email == "" {
		return "", errors.New("configure Jira URL, account and token in Settings → Jira")
	}
	base, version := config.URL, "2"
	if config.Deployment == "cloud" {
		version = "3"
		if config.CloudID != "" {
			base = "https://api.atlassian.com/ex/jira/" + config.CloudID
		}
	}
	return base + "/rest/api/" + version, nil
}

func (c *Client) redact(s string) string {
	replacements := []string{}
	seen := map[string]bool{}
	for _, secret := range []string{base64.StdEncoding.EncodeToString([]byte(c.Config.Email + ":" + c.Token)), c.Token} {
		if secret != "" {
			for _, encoded := range []string{url.QueryEscape(secret), url.PathEscape(secret), secret} {
				if !seen[encoded] {
					replacements = append(replacements, encoded, "[redacted]")
					seen[encoded] = true
				}
			}
		}
	}
	return strings.NewReplacer(replacements...).Replace(s)
}

func (c *Client) request(ctx context.Context, method, path string, query url.Values, body any) (map[string]any, error) {
	base, err := c.base()
	if err != nil {
		return nil, err
	}
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	u := base + path
	if len(query) != 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("invalid Jira request")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Config.Deployment == "datacenter" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	} else {
		req.SetBasicAuth(c.Config.Email, c.Token)
	}
	client := http.Client{Timeout: 30 * time.Second}
	if c.HTTPClient != nil {
		client = *c.HTTPClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("Jira redirects are disabled; configure the final site URL")
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New(c.redact("Jira request failed: " + err.Error()))
	}
	defer response.Body.Close()
	data, err = io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, errors.New(c.redact("could not read Jira response: " + err.Error()))
	}
	if len(data) > maxResponseBytes {
		return nil, errors.New("Jira response exceeds 8 MiB; request fewer fields or a smaller page")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := "Jira returned HTTP " + strconv.Itoa(response.StatusCode)
		switch response.StatusCode {
		case 400:
			message += ": invalid JQL or request"
		case 401:
			message += ": check the saved token, account, and Cloud ID"
		case 403:
			message += ": account or token lacks permission"
		case 404:
			message += ": ticket is missing or inaccessible, or the site URL is incorrect"
		case 429:
			message += ": rate limited"
		}
		if retry := response.Header.Get("Retry-After"); retry != "" {
			message += "; Retry-After: " + retry
		}
		// Only structured Jira diagnostics are useful; never echo HTML login pages.
		var diagnostic struct {
			ErrorMessages []string          `json:"errorMessages"`
			Errors        map[string]string `json:"errors"`
		}
		if json.Unmarshal(data, &diagnostic) == nil {
			for i, message := range diagnostic.ErrorMessages {
				diagnostic.ErrorMessages[i] = c.redact(message)
			}
			clean := map[string]string{}
			for key, message := range diagnostic.Errors {
				clean[c.redact(key)] = c.redact(message)
			}
			diagnostic.Errors = clean
			b, _ := json.Marshal(diagnostic)
			if len(b) > 2048 {
				b = b[:2048]
			}
			message += ": " + string(b)
		}
		return nil, errors.New(c.redact(message))
	}
	// Redact string values after decoding to avoid corrupting JSON syntax.
	var result map[string]any
	if json.Unmarshal(data, &result) != nil || result == nil {
		return nil, errors.New("Jira returned invalid JSON")
	}
	redactValue(result, c.redact)
	return result, nil
}

func redactValue(v any, redact func(string) string) {
	switch value := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		for _, k := range keys {
			child := value[k]
			key := redact(k)
			if s, ok := child.(string); ok {
				child = redact(s)
			} else {
				redactValue(child, redact)
			}
			if key != k {
				delete(value, k)
			}
			value[key] = child
		}
	case []any:
		for i, child := range value {
			if s, ok := child.(string); ok {
				value[i] = redact(s)
			} else {
				redactValue(child, redact)
			}
		}
	}
}

type cursor struct {
	Kind   string `json:"kind"`
	Offset int    `json:"offset,omitempty"`
	Token  string `json:"token,omitempty"`
}

func encodeCursor(v cursor) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeCursor(raw, kind string) (cursor, error) {
	v := cursor{Kind: kind}
	if raw == "" {
		return v, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if len(raw) > 16384 || err != nil || json.Unmarshal(b, &v) != nil || v.Kind != kind || v.Offset < 0 {
		return v, errors.New("invalid Jira continuation cursor")
	}
	return v, nil
}
func pageSize(n int) (int, error) {
	if n == 0 {
		return 50, nil
	}
	if n < 1 || n > 100 {
		return 0, errors.New("pageSize must be between 1 and 100")
	}
	return n, nil
}
func number(v any) int { n, _ := v.(float64); return int(n) }
func array(v any) []any {
	a, _ := v.([]any)
	if a == nil {
		return []any{}
	}
	return a
}

func bounded(result map[string]any) (map[string]any, error) {
	b, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxResultBytes {
		return nil, errors.New("Jira result exceeds 1 MiB; select fewer fields or use a smaller pageSize")
	}
	return result, nil
}

func (c *Client) Myself(ctx context.Context) (map[string]any, error) {
	v, err := c.request(ctx, "GET", "/myself", nil, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"displayName": v["displayName"], "accountId": v["accountId"], "name": v["name"]}, nil
}

func (c *Client) Search(ctx context.Context, jql string, fields []string, size int, continuation string) (map[string]any, error) {
	if strings.TrimSpace(jql) == "" {
		return nil, errors.New("jql is required")
	}
	n, err := pageSize(size)
	if err != nil {
		return nil, err
	}
	kind, path := "cloud-search", "/search/jql"
	if c.Config.Deployment == "datacenter" {
		kind, path = "dc-search", "/search"
	}
	page, err := decodeCursor(continuation, kind)
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		fields = []string{"summary", "status", "issuetype", "priority", "assignee", "updated"}
	}
	body := map[string]any{"jql": jql, "fields": fields, "maxResults": n}
	if kind == "dc-search" {
		body["startAt"] = page.Offset
	} else if page.Token != "" {
		body["nextPageToken"] = page.Token
	}
	v, err := c.request(ctx, "POST", path, nil, body)
	if err != nil {
		return nil, err
	}
	issues := array(v["issues"])
	result := map[string]any{"source": "jira", "issues": issues, "nextCursor": ""}
	if kind == "dc-search" {
		start := page.Offset + len(issues)
		if v["total"] != nil {
			result["total"] = v["total"]
		}
		if len(issues) > 0 && (v["total"] == nil || start < number(v["total"])) {
			result["nextCursor"] = encodeCursor(cursor{Kind: kind, Offset: start})
		}
	} else if token, _ := v["nextPageToken"].(string); token != "" {
		result["nextCursor"] = encodeCursor(cursor{Kind: kind, Token: token})
	}
	if v["warningMessages"] != nil {
		result["warnings"] = v["warningMessages"]
	}
	addText(result)
	return bounded(result)
}

func validIssue(id string) error {
	if len(id) > 255 || !identifier.MatchString(id) {
		return errors.New("issue must be a Jira key or numeric ID, without path separators")
	}
	return nil
}

func (c *Client) Comments(ctx context.Context, issue string, size int, continuation string) (map[string]any, error) {
	if err := validIssue(issue); err != nil {
		return nil, err
	}
	n, err := pageSize(size)
	if err != nil {
		return nil, err
	}
	page, err := decodeCursor(continuation, "comments:"+issue)
	if err != nil {
		return nil, err
	}
	v, err := c.request(ctx, "GET", "/issue/"+issue+"/comment", url.Values{"startAt": {strconv.Itoa(page.Offset)}, "maxResults": {strconv.Itoa(n)}, "orderBy": {"created"}}, nil)
	if err != nil {
		return nil, err
	}
	comments := array(v["comments"])
	result := map[string]any{"source": "jira", "comments": comments, "nextCursor": ""}
	start := page.Offset + len(comments)
	if v["total"] != nil {
		result["total"] = v["total"]
	}
	if len(comments) > 0 && (v["total"] == nil || start < number(v["total"])) {
		result["nextCursor"] = encodeCursor(cursor{Kind: "comments:" + issue, Offset: start})
	}
	addText(result)
	return bounded(result)
}

func (c *Client) Issue(ctx context.Context, issue string, fields []string) (map[string]any, error) {
	if err := validIssue(issue); err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		fields = []string{"*all"}
	}
	selected := append(append([]string{}, fields...), "-comment")
	v, err := c.request(ctx, "GET", "/issue/"+issue, url.Values{"fields": {strings.Join(selected, ",")}, "expand": {"names,schema"}}, nil)
	if err != nil {
		return nil, err
	}
	v["source"] = "jira"
	// The embedded Jira comment field is paginated independently. Fetch it
	// explicitly and leave an exact continuation point when the result is full.
	if f, ok := v["fields"].(map[string]any); ok {
		delete(f, "comment")
	}
	addText(v)
	if _, err = bounded(v); err != nil {
		return nil, err
	}
	all := []any{}
	next := ""
	for {
		page, pageErr := c.Comments(ctx, issue, 50, next)
		if pageErr != nil {
			v["comments"] = map[string]any{"comments": all, "nextCursor": encodeCursor(cursor{Kind: "comments:" + issue, Offset: len(all)}), "incomplete": true, "error": pageErr.Error()}
			return bounded(v)
		}
		items := array(page["comments"])
		for i, item := range items {
			candidate := append(all, item)
			v["comments"] = map[string]any{"comments": candidate, "nextCursor": encodeCursor(cursor{Kind: "comments:" + issue, Offset: len(candidate)}), "incomplete": true, "total": page["total"]}
			if _, err = bounded(v); err != nil {
				v["comments"] = map[string]any{"comments": all, "nextCursor": encodeCursor(cursor{Kind: "comments:" + issue, Offset: len(all)}), "incomplete": true, "total": page["total"]}
				if len(all) == 0 && i == 0 {
					v["comments"].(map[string]any)["warning"] = "Use jira_get_comments with a smaller pageSize"
				}
				return bounded(v)
			}
			all = candidate
		}
		next, _ = page["nextCursor"].(string)
		v["comments"] = map[string]any{"comments": all, "nextCursor": next, "incomplete": next != "", "total": page["total"]}
		if next == "" {
			return bounded(v)
		}
	}
}

// ReadableText preserves paragraph/list boundaries while retaining raw ADF in
// the original fields. Unknown nodes retain their children's visible text.
func ReadableText(v any) string {
	switch node := v.(type) {
	case string:
		return node
	case []any:
		var out strings.Builder
		for _, child := range node {
			out.WriteString(ReadableText(child))
		}
		return out.String()
	case map[string]any:
		t, _ := node["type"].(string)
		attrs, _ := node["attrs"].(map[string]any)
		switch t {
		case "text":
			text, _ := node["text"].(string)
			return text
		case "hardBreak":
			return "\n"
		case "mention":
			if s, ok := attrs["text"].(string); ok {
				return s
			}
		case "emoji":
			if s, ok := attrs["text"].(string); ok {
				return s
			}
			if s, ok := attrs["shortName"].(string); ok {
				return s
			}
		case "inlineCard", "blockCard":
			if s, ok := attrs["url"].(string); ok {
				return s
			}
		}
		text := ReadableText(node["content"])
		switch t {
		case "paragraph", "heading", "listItem", "codeBlock", "blockquote", "tableRow":
			text += "\n"
		case "tableCell", "tableHeader":
			text += "\t"
		}
		return text
	}
	return ""
}

func addText(v any) {
	switch value := v.(type) {
	case map[string]any:
		for k, child := range value {
			if doc, ok := child.(map[string]any); ok && doc["type"] == "doc" {
				value[k+"Text"] = strings.TrimSpace(ReadableText(doc))
			} else {
				addText(child)
			}
		}
	case []any:
		for _, child := range value {
			addText(child)
		}
	}
}

// Token validation is shared by the protected owner credential route.
func ValidateToken(token string) error {
	if len(token) > 16384 || strings.ContainsAny(token, "\r\n\x00") {
		return errors.New("Jira token must be one line, at most 16384 bytes")
	}
	return nil
}
