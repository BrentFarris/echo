package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebFetchRejectsMissingURL(t *testing.T) {
	result := Execute(ExecutionContext{Context: context.Background()}, "web_fetch", mustJSON(t, map[string]any{}))
	if result.Success {
		t.Fatalf("expected missing url to fail, got %#v", result)
	}
	if result.Error == nil || result.Error.Code != "invalid_arguments" {
		t.Fatalf("unexpected error: %#v", result.Error)
	}
}

func TestWebFetchGetsDirectEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.Header.Get("X-Test") != "echo" {
			t.Fatalf("expected custom header, got %q", r.Header.Get("X-Test"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	result := Execute(ExecutionContext{Context: context.Background()}, "web_fetch", mustJSON(t, map[string]any{
		"url":     server.URL,
		"headers": map[string]any{"X-Test": "echo"},
	}))
	if !result.Success {
		t.Fatalf("expected fetch success, got %#v", result)
	}
	output, ok := result.Output.(webFetchOutput)
	if !ok {
		t.Fatalf("unexpected output type: %T", result.Output)
	}
	if output.StatusCode != http.StatusOK || output.Body != `{"ok":true}` || output.BodyEncoding != "utf-8" {
		t.Fatalf("unexpected output: %#v", output)
	}
	if output.ContentType != "application/json" {
		t.Fatalf("unexpected content type: %q", output.ContentType)
	}
}

func TestWebFetchPostsBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if payload["message"] != "hello" {
			t.Fatalf("unexpected payload: %#v", payload)
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("accepted"))
	}))
	defer server.Close()

	result := Execute(ExecutionContext{Context: context.Background()}, "web_fetch", mustJSON(t, map[string]any{
		"url":     server.URL,
		"method":  "POST",
		"headers": map[string]any{"Content-Type": "application/json"},
		"body":    `{"message":"hello"}`,
	}))
	if !result.Success {
		t.Fatalf("expected fetch success, got %#v", result)
	}
	output := result.Output.(webFetchOutput)
	if output.StatusCode != http.StatusOK || output.Body != "accepted" {
		t.Fatalf("unexpected output: %#v", output)
	}
}

func TestWebFetchTruncatesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("abcdef"))
	}))
	defer server.Close()

	result := Execute(ExecutionContext{Context: context.Background()}, "web_fetch", mustJSON(t, map[string]any{
		"url":      server.URL,
		"maxBytes": 3,
	}))
	if !result.Success {
		t.Fatalf("expected fetch success, got %#v", result)
	}
	output := result.Output.(webFetchOutput)
	if output.Body != "abc" || !output.Truncated || output.BytesRead != 3 {
		t.Fatalf("expected truncated response, got %#v", output)
	}
}

func TestWebFetchReturnsBinaryAsBase64(t *testing.T) {
	binary := []byte{0, 1, 2, 3}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(binary)
	}))
	defer server.Close()

	result := Execute(ExecutionContext{Context: context.Background()}, "web_fetch", mustJSON(t, map[string]any{
		"url": server.URL,
	}))
	if !result.Success {
		t.Fatalf("expected fetch success, got %#v", result)
	}
	output := result.Output.(webFetchOutput)
	if output.BodyEncoding != "base64" || output.BodyBase64 != base64.StdEncoding.EncodeToString(binary) || output.Body != "" {
		t.Fatalf("unexpected binary output: %#v", output)
	}
}

func TestWebFetchRejectsUnsupportedInputs(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "unsupported scheme",
			args: map[string]any{"url": "file:///tmp/example"},
			want: "absolute HTTP or HTTPS URL",
		},
		{
			name: "unsupported method",
			args: map[string]any{"url": "https://example.test", "method": "CONNECT"},
			want: "method",
		},
		{
			name: "body with get",
			args: map[string]any{"url": "https://example.test", "body": "not allowed"},
			want: "body is only supported",
		},
		{
			name: "invalid header",
			args: map[string]any{"url": "https://example.test", "headers": map[string]any{"Bad Header": "x"}},
			want: "invalid header name",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := Execute(ExecutionContext{Context: context.Background()}, "web_fetch", mustJSON(t, tc.args))
			if result.Success {
				t.Fatalf("expected failure, got %#v", result)
			}
			if result.Error == nil || result.Error.Code != "invalid_arguments" || !strings.Contains(result.Error.Message, tc.want) {
				t.Fatalf("unexpected error: %#v", result.Error)
			}
		})
	}
}

func TestWebReadAllowsOnlyReadMethods(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Fatalf("expected HEAD, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	result := Execute(ExecutionContext{Context: context.Background()}, "web_read", mustJSON(t, map[string]any{"url": server.URL, "method": "HEAD"}))
	if !result.Success {
		t.Fatalf("expected read success, got %#v", result)
	}
	result = Execute(ExecutionContext{Context: context.Background()}, "web_read", mustJSON(t, map[string]any{"url": server.URL, "method": "POST"}))
	if result.Success || result.Error == nil || result.Error.Code != "invalid_arguments" {
		t.Fatalf("expected POST to be rejected, got %#v", result)
	}
}
