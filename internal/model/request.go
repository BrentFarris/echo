package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/brent/echo/internal/domain"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// ProviderRequest pins requests and credentials to the selected endpoint's base
// path. A WASI provider can adapt an HTTP protocol without obtaining its secret.
func (c *Client) ProviderRequest(ctx context.Context, endpoint domain.Endpoint, key string, args json.RawMessage) (any, error) {
	var a struct {
		Path       string            `json:"path"`
		Method     string            `json:"method"`
		Body       any               `json:"body"`
		Headers    map[string]string `json:"headers"`
		AuthHeader string            `json:"authHeader"`
	}
	if e := json.Unmarshal(args, &a); e != nil {
		return nil, e
	}
	base, e := url.Parse(strings.TrimRight(endpoint.URL, "/") + "/")
	if e != nil {
		return nil, e
	}
	relative, e := url.Parse(a.Path)
	if e != nil || relative.IsAbs() || relative.Host != "" || relative.User != nil || relative.Fragment != "" {
		return nil, errors.New("provider request needs an endpoint-relative path")
	}
	target := base.ResolveReference(relative)
	basePath := strings.TrimRight(path.Clean(base.Path), "/") + "/"
	targetPath := path.Clean(strings.ReplaceAll(target.Path, `\`, "/"))
	if target.Scheme != base.Scheme || target.Host != base.Host || strings.Contains(target.Path, "%") || (targetPath != strings.TrimRight(basePath, "/") && !strings.HasPrefix(targetPath, basePath)) {
		return nil, errors.New("provider cannot leave the selected endpoint base path")
	}
	if a.Method == "" {
		a.Method = "POST"
	}
	if a.Method != "GET" && a.Method != "POST" {
		return nil, errors.New("model providers may use GET or POST")
	}
	data, e := json.Marshal(a.Body)
	if e != nil {
		return nil, e
	}
	if len(data) > 4<<20 {
		return nil, errors.New("provider request exceeds 4 MiB")
	}
	ctx, cancel := context.WithTimeout(context.WithValue(ctx, localEndpointKey{}, !endpoint.Cloud), 120*time.Second)
	defer cancel()
	request, e := http.NewRequestWithContext(ctx, a.Method, target.String(), bytes.NewReader(data))
	if e != nil {
		return nil, e
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range a.Headers {
		lower := strings.ToLower(name)
		if lower == "authorization" || lower == "cookie" || lower == "host" || lower == "proxy-authorization" || lower == "x-api-key" || lower == "api-key" {
			return nil, errors.New("credential headers are host-owned")
		}
		request.Header.Set(name, value)
	}
	if key != "" {
		switch strings.ToLower(a.AuthHeader) {
		case "", "authorization":
			request.Header.Set("Authorization", "Bearer "+key)
		case "x-api-key", "api-key":
			request.Header.Set(a.AuthHeader, key)
		default:
			return nil, errors.New("unsupported credential header")
		}
	}
	response, e := c.HTTP.Do(request)
	if e != nil {
		return nil, e
	}
	defer response.Body.Close()
	body, e := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if e != nil {
		return nil, e
	}
	if len(body) > 2<<20 {
		return nil, errors.New("provider response exceeds 2 MiB")
	}
	text := string(body)
	contentType := response.Header.Get("Content-Type")
	if key != "" {
		text = strings.ReplaceAll(text, key, "[redacted]")
		contentType = strings.ReplaceAll(contentType, key, "[redacted]")
	}
	if response.StatusCode/100 != 2 {
		return nil, fmt.Errorf("provider HTTP %d: %s", response.StatusCode, text)
	}
	return map[string]any{"status": response.StatusCode, "contentType": contentType, "body": text}, nil
}
