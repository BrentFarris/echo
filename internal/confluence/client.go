// Package confluence implements endpoint-confined, read-only Confluence access.
package confluence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const MaxResponseBytes = 16 << 20
const ChunkCharacters = 24000

type Config struct {
	Deployment string `json:"deployment"`
	URL        string `json:"url"`
	Email      string `json:"email"`
	TokenType  string `json:"tokenType"`
	CloudID    string `json:"cloudId"`
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
var contentID = regexp.MustCompile(`^[0-9]+$`)

func Normalize(c Config) (Config, error) {
	c.Deployment = strings.TrimSpace(c.Deployment)
	c.URL = strings.TrimRight(strings.TrimSpace(c.URL), "/")
	c.Email = strings.TrimSpace(c.Email)
	c.TokenType = strings.TrimSpace(c.TokenType)
	c.CloudID = strings.TrimSpace(c.CloudID)
	if c.Deployment == "" {
		c.Deployment = "cloud"
	}
	if c.TokenType == "" {
		c.TokenType = "classic"
	}
	if c.Deployment != "cloud" && c.Deployment != "datacenter" {
		return c, errors.New("Confluence deployment must be cloud or datacenter")
	}
	if c.TokenType != "classic" && c.TokenType != "scoped" {
		return c, errors.New("Confluence token type must be classic or scoped")
	}
	if strings.ContainsAny(c.Email, ":\r\n\x00") {
		return c, errors.New("invalid Confluence email")
	}
	if c.CloudID != "" && !identifier.MatchString(c.CloudID) {
		return c, errors.New("invalid Confluence Cloud ID")
	}
	if c.Deployment == "datacenter" && (c.Email != "" || c.CloudID != "" || c.TokenType != "classic") {
		return c, errors.New("email, Cloud ID and scoped tokens apply only to Confluence Cloud")
	}
	if c.URL == "" {
		return c, nil
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Scheme != "http" && u.Scheme != "https") || strings.ContainsAny(c.URL, "#\\\r\n\x00") || strings.Contains(u.EscapedPath(), "%") {
		return c, errors.New("Confluence URL must be an HTTP(S) base URL without credentials, query, fragment or encoded path")
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return c, errors.New("invalid Confluence base path")
		}
	}
	if c.Deployment == "cloud" {
		if u.Scheme != "https" {
			return c, errors.New("Confluence Cloud requires HTTPS")
		}
		if u.Path != "" && u.Path != "/wiki" {
			return c, errors.New("Confluence Cloud URL must be the site origin or its /wiki base")
		}
		c.URL = strings.TrimSuffix(c.URL, "/wiki")
	}
	return c, nil
}

func (c Config) Ready() error {
	if c.URL == "" || c.Deployment == "cloud" && (c.Email == "" || c.TokenType == "scoped" && c.CloudID == "") {
		return errors.New("save the Confluence URL, Cloud email and (for scoped tokens) Cloud ID in Settings → Confluence")
	}
	return nil
}

func ValidateToken(token string) error {
	if len(token) > 16384 || strings.ContainsAny(token, "\r\n\x00") || strings.TrimSpace(token) != token {
		return errors.New("Confluence token must be one line without surrounding whitespace, at most 16384 bytes")
	}
	return nil
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
	if err = config.Ready(); err != nil {
		return "", err
	}
	if c.Token == "" {
		return "", errors.New("save a token in Settings → Confluence")
	}
	if err = ValidateToken(c.Token); err != nil {
		return "", err
	}
	if config.Deployment == "datacenter" {
		return config.URL, nil
	}
	if config.TokenType == "scoped" {
		return "https://api.atlassian.com/ex/confluence/" + config.CloudID + "/wiki", nil
	}
	return config.URL + "/wiki", nil
}

func (c *Client) redact(value string) string {
	for _, secret := range []string{base64.StdEncoding.EncodeToString([]byte(c.Config.Email + ":" + c.Token)), c.Token} {
		if secret != "" {
			for _, encoded := range []string{url.QueryEscape(secret), url.PathEscape(secret), secret} {
				value = strings.ReplaceAll(value, encoded, "[redacted]")
			}
		}
	}
	return value
}

func (c *Client) request(ctx context.Context, method, path string, query url.Values, body any) (map[string]any, error) {
	base, err := c.base()
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, errors.New("invalid Confluence request")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	target := base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var input io.Reader
	if body != nil {
		input = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, input)
	if err != nil {
		return nil, errors.New("invalid Confluence request")
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
		return errors.New("Confluence redirects are disabled; configure the final base URL")
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New(c.redact("Confluence request failed: " + err.Error()))
	}
	defer response.Body.Close()
	data, err = io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, errors.New("could not read Confluence response")
	}
	if len(data) > MaxResponseBytes {
		return nil, errors.New("Confluence response exceeds 16 MiB; use a smaller search limit or narrower content")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := "Confluence returned HTTP " + strconv.Itoa(response.StatusCode)
		switch response.StatusCode {
		case 400:
			message += ": invalid CQL or request"
		case 401:
			message += ": check the saved token, Cloud email, token type and Cloud ID"
		case 403:
			message += ": account or token lacks permission; check read scopes"
		case 404:
			message += ": content is missing or inaccessible, or the base URL is incorrect"
		case 429:
			message += ": rate limited"
		default:
			if response.StatusCode >= 500 {
				message += ": Confluence is unavailable"
			}
		}
		if retry := response.Header.Get("Retry-After"); retry != "" {
			message += "; Retry-After: " + retry
		}
		var diagnostic struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &diagnostic) == nil && diagnostic.Message != "" {
			diagnostic.Message = c.redact(diagnostic.Message)
			chars := []rune(diagnostic.Message)
			if len(chars) > 1000 {
				chars = chars[:1000]
			}
			message += ": " + string(chars)
		}
		return nil, errors.New(c.redact(message))
	}
	var result map[string]any
	if json.Unmarshal(data, &result) != nil || result == nil {
		return nil, errors.New("Confluence returned invalid JSON")
	}
	redactValue(result, c.redact)
	return result, nil
}

func redactValue(value any, redact func(string) string) {
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		for _, key := range keys {
			child := v[key]
			cleanKey := redact(key)
			if s, ok := child.(string); ok {
				child = redact(s)
			} else {
				redactValue(child, redact)
			}
			if cleanKey != key {
				delete(v, key)
			}
			v[cleanKey] = child
		}
	case []any:
		for i, child := range v {
			if s, ok := child.(string); ok {
				v[i] = redact(s)
			} else {
				redactValue(child, redact)
			}
		}
	}
}

func object(value any) map[string]any { v, _ := value.(map[string]any); return v }
func str(value any) string            { v, _ := value.(string); return v }

// Continuations contain no credentials or arbitrary destinations. The binding
// invalidates them when the connection, credential, or operation arguments change.
type continuation struct {
	Binding string `json:"binding"`
	Start   string `json:"start,omitempty"`
	Cursor  string `json:"cursor,omitempty"`
	Offset  int    `json:"offset,omitempty"`
	Version int    `json:"version,omitempty"`
}

func (c *Client) binding(kind string, args ...any) string {
	data, _ := json.Marshal([]any{c.Config, c.Token, kind, args})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func encodeContinuation(next continuation) string {
	data, _ := json.Marshal(next)
	return base64.RawURLEncoding.EncodeToString(data)
}
func decodeContinuation(token, binding string) (continuation, error) {
	var next continuation
	data, err := base64.RawURLEncoding.DecodeString(token)
	if len(token) > 16384 || err != nil || json.Unmarshal(data, &next) != nil || next.Binding != binding || next.Offset < 0 {
		return next, errors.New("invalid Confluence continuation; restart the search or read with the same arguments")
	}
	return next, nil
}

type Content struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Title    string         `json:"title"`
	URL      string         `json:"url"`
	Space    any            `json:"space,omitempty"`
	Version  int            `json:"version,omitempty"`
	Excerpt  string         `json:"excerpt,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}
type SearchResult struct {
	Results          []Content `json:"results"`
	NextContinuation string    `json:"nextContinuation,omitempty"`
}

func (c *Client) webURL(raw string) string {
	base := c.Config.URL
	if c.Config.Deployment == "cloud" {
		base = strings.TrimSuffix(base, "/wiki") + "/wiki"
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || strings.ContainsAny(raw, "\\\r\n\x00") {
		return ""
	}
	if !u.IsAbs() {
		root, _ := url.Parse(base + "/")
		if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
			contextPath := strings.TrimRight(root.Path, "/")
			if contextPath == "" || u.Path == contextPath || strings.HasPrefix(u.Path, contextPath+"/") {
				u = root.ResolveReference(u)
			} else {
				u, _ = url.Parse(base + raw)
			}
		} else {
			u = root.ResolveReference(u)
		}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}
func (c *Client) content(raw map[string]any) Content {
	v := Content{ID: str(raw["id"]), Type: str(raw["type"]), Title: str(raw["title"]), Space: raw["space"]}
	if v.Space == nil {
		v.Space = raw["spaceId"]
	}
	if version, ok := object(raw["version"])["number"].(float64); ok {
		v.Version = int(version)
	}
	v.URL = c.webURL(str(object(raw["_links"])["webui"]))
	if link := str(raw["webuiLink"]); link != "" {
		v.URL = c.webURL(link)
	}
	if v.URL == "" || str(object(raw["_links"])["webui"]) == "" {
		if str(raw["webuiLink"]) == "" {
			parent := str(raw["pageId"])
			if parent == "" {
				parent = str(raw["blogPostId"])
			}
			if parent != "" {
				v.URL = c.webURL("/pages/viewpage.action?pageId=" + url.QueryEscape(parent) + "&focusedCommentId=" + url.QueryEscape(v.ID) + "#comment-" + v.ID)
			} else {
				v.URL = c.webURL("/pages/viewpage.action?pageId=" + url.QueryEscape(v.ID))
			}
		}
	}
	return v
}

func (c *Client) Search(ctx context.Context, cql string, limit int, token string) (SearchResult, error) {
	out := SearchResult{Results: []Content{}}
	if strings.TrimSpace(cql) == "" || len(cql) > 16384 {
		return out, errors.New("CQL is required and must be at most 16384 bytes")
	}
	if limit == 0 {
		limit = 25
	}
	if limit < 1 || limit > 100 {
		return out, errors.New("Confluence search limit must be 1–100")
	}
	binding := c.binding("search", cql, limit)
	query := url.Values{"cql": {cql}, "limit": {strconv.Itoa(limit)}, "expand": {"content.space,content.version"}}
	if token != "" {
		next, err := decodeContinuation(token, binding)
		if err != nil {
			return out, err
		}
		if next.Cursor != "" {
			query.Set("cursor", next.Cursor)
		}
		if next.Start != "" {
			query.Set("start", next.Start)
		}
	}
	raw, err := c.request(ctx, http.MethodGet, "/rest/api/search", query, nil)
	if err != nil {
		return out, err
	}
	rows, valid := raw["results"].([]any)
	if !valid {
		return out, errors.New("Confluence returned an invalid search result")
	}
	for _, row := range rows {
		r := object(row)
		content := object(r["content"])
		if content == nil {
			content = r
		}
		if str(content["id"]) == "" {
			continue
		}
		v := c.content(content)
		if v.Title == "" {
			v.Title = str(r["title"])
		}
		if link := str(r["url"]); link != "" {
			v.URL = c.webURL(link)
		}
		v.Excerpt = Markdown(str(r["excerpt"]))
		if v.Type == "attachment" {
			v.Metadata = c.attachmentMetadata(content)
		}
		out.Results = append(out.Results, v)
	}
	out.NextContinuation, err = c.searchContinuation(raw, "/rest/api/search", cql, binding)
	if err != nil {
		return out, err
	}
	return out, resultSize(out)
}

// Follow only the pagination position from an endpoint-confined link. All
// operation parameters and the destination are constructed again by the caller.
func (c *Client) searchContinuation(raw map[string]any, path, cql, binding string) (string, error) {
	if next := str(object(raw["_links"])["next"]); next != "" {
		base, err := c.base()
		if err != nil {
			return "", err
		}
		endpoint, _ := url.Parse(base + path)
		u, parseErr := url.Parse(next)
		if parseErr != nil || u.User != nil || u.Fragment != "" || strings.ContainsAny(next, "\\\r\n\x00") {
			return "", errors.New("unsafe Confluence pagination link")
		}
		// Cloud sometimes omits /wiki from API-relative pagination links.
		if !u.IsAbs() && u.Host == "" && (u.Path == path || c.Config.Deployment == "cloud" && u.Path == "/wiki"+path) {
			root, _ := url.Parse(base)
			u.Path = root.Path + path
		}
		u = endpoint.ResolveReference(u)
		if u.Scheme != endpoint.Scheme || !strings.EqualFold(u.Host, endpoint.Host) || u.Path != endpoint.Path || u.RawPath != "" {
			return "", errors.New("Confluence pagination cannot leave the configured search endpoint")
		}
		q := u.Query()
		if value := q.Get("cql"); value != "" && value != cql {
			return "", errors.New("Confluence pagination changed the CQL query")
		}
		nextToken := continuation{Binding: binding, Start: q.Get("start"), Cursor: q.Get("cursor")}
		if nextToken.Start != "" {
			n, e := strconv.Atoi(nextToken.Start)
			if e != nil || n < 0 {
				return "", errors.New("invalid Confluence pagination offset")
			}
		}
		if nextToken.Start == "" && nextToken.Cursor == "" {
			return "", errors.New("Confluence pagination has no cursor or offset")
		}
		return encodeContinuation(nextToken), nil
	}
	return "", nil
}

type ReadResult struct {
	Content
	Format           string `json:"format"`
	Body             string `json:"body"`
	Complete         bool   `json:"complete"`
	NextContinuation string `json:"nextContinuation,omitempty"`
}

// Keep metadata/search results below the broker's 2 MiB JSON boundary too.
func resultSize(value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > 1<<20 {
		return errors.New("Confluence result metadata exceeds 1 MiB; narrow the query or reduce the search limit")
	}
	return nil
}

func (c *Client) attachmentMetadata(raw map[string]any) map[string]any {
	metadata := map[string]any{}
	for _, key := range []string{"mediaType", "fileSize", "fileId", "downloadLink", "webuiLink", "extensions", "metadata"} {
		if value, ok := raw[key]; ok {
			metadata[key] = value
		}
	}
	if link := object(raw["_links"])["download"]; link != nil {
		metadata["download"] = link
	}
	for _, key := range []string{"download", "downloadLink", "webuiLink"} {
		if link, ok := metadata[key].(string); ok {
			metadata[key] = c.webURL(link)
		}
	}
	return metadata
}
func (c *Client) Read(ctx context.Context, id, format, token string) (ReadResult, error) {
	out := ReadResult{}
	if !contentID.MatchString(id) {
		return out, errors.New("Confluence content ID must be numeric")
	}
	if format == "" {
		format = "markdown"
	}
	if format != "markdown" && format != "storage" {
		return out, errors.New("Confluence format must be markdown or storage")
	}
	binding := c.binding("read", id, format)
	next := continuation{Binding: binding}
	if token != "" {
		var err error
		next, err = decodeContinuation(token, binding)
		if err != nil {
			return out, err
		}
	}
	var raw map[string]any
	var err error
	kind := ""
	if c.Config.Deployment == "datacenter" {
		raw, err = c.request(ctx, http.MethodGet, "/rest/api/content/"+id, url.Values{"expand": {"body.storage,space,version,metadata"}}, nil)
	} else {
		var types map[string]any
		types, err = c.request(ctx, http.MethodPost, "/api/v2/content/convert-ids-to-types", nil, map[string]any{"contentIds": []string{id}})
		if err != nil {
			return out, err
		}
		kind = str(object(types["results"])[id])
		if kind == "" {
			return out, errors.New("Confluence content is missing or inaccessible")
		}
		paths := map[string]string{"page": "pages", "blogpost": "blogposts", "inline-comment": "inline-comments", "footer-comment": "footer-comments", "attachment": "attachments"}
		path, ok := paths[kind]
		if !ok {
			return out, fmt.Errorf("Confluence content type %q is unsupported; read pages, blog posts, comments or attachment metadata", kind)
		}
		query := url.Values{}
		if kind != "attachment" {
			query.Set("body-format", "storage")
		}
		raw, err = c.request(ctx, http.MethodGet, "/api/v2/"+path+"/"+id, query, nil)
	}
	if err != nil {
		return out, err
	}
	out.Content = c.content(raw)
	if out.ID != "" && out.ID != id {
		return out, errors.New("Confluence returned a different content ID")
	}
	if out.ID == "" {
		out.ID = id
	}
	if out.Type == "" {
		out.Type = kind
	}
	out.Format = format
	if token != "" && out.Version != next.Version {
		return out, errors.New("Confluence content version changed; restart the read to avoid mixing revisions")
	}
	if out.Type == "attachment" {
		out.Metadata = c.attachmentMetadata(raw)
		out.Complete = true
		return out, resultSize(out)
	}
	switch out.Type {
	case "page", "blogpost", "comment", "inline-comment", "footer-comment":
	default:
		return out, fmt.Errorf("Confluence content type %q is unsupported", out.Type)
	}
	body, ok := object(object(raw["body"])["storage"])["value"].(string)
	if !ok {
		return out, errors.New("Confluence did not return a storage body; check token read scopes")
	}
	if format == "markdown" {
		body = Markdown(body)
	}
	chars := []rune(body)
	if next.Offset > len(chars) {
		return out, errors.New("invalid Confluence content offset; restart the read")
	}
	end := min(next.Offset+ChunkCharacters, len(chars))
	out.Body = string(chars[next.Offset:end])
	out.Complete = end == len(chars)
	if !out.Complete {
		if out.Version == 0 {
			return out, errors.New("Confluence omitted the content version; cannot safely continue a long read")
		}
		out.NextContinuation = encodeContinuation(continuation{Binding: binding, Offset: end, Version: out.Version})
	}
	return out, resultSize(out)
}
