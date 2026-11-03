package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/brent/echo/internal/domain"
)

const nginx413 = `<html> <head><title>413 Request Entity Too Large</title></head> <body> <center><h1>413 Request Entity Too Large</h1></center> <hr><center>nginx/1.31.4</center> </body> </html>`

func TestProviderErrorSeparatesContextAndRequestSize(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		body          string
		context, size bool
	}{
		{"nginx", 413, nginx413, false, true},
		{"empty 413", 413, "", false, true},
		{"json bytes", 413, `{"error":{"message":"input too long: request body exceeds 1048576 bytes"}}`, false, true},
		{"HTML mentions tokens", 413, `<html><body>Proxy error <script>const help = "maximum context length";</script></body></html>`, false, true},
		{"context 413", 413, `{"error":{"code":"context_length_exceeded","message":"request too large","context_length":4096}}`, true, false},
		{"plain context 413", 413, "prompt is too long: 8000 tokens exceeds 4096", true, false},
		{"alternate code", 400, `{"error":{"code":"CONTEXT_WINDOW_EXCEEDED","message":"size limit"}}`, true, false},
		{"alternate type", 400, `{"error":{"type":"prompt_too_long","message":"size limit"}}`, true, false},
		{"string error", 413, `{"error":"prompt too long: 8000 tokens"}`, true, false},
		{"stream rate limit", 0, `{"error":{"message":"Too many tokens per minute"}}`, false, false},
		{"rate limit", 429, `{"error":{"message":"too many tokens"}}`, false, false},
		{"HTML gateway", 502, `<html><body>Bad gateway</body></html>`, false, false},
		{"JSON HTML gateway", 502, `{"error":{"message":"<html><body>Bad gateway</body></html>"}}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ProviderError(tc.status, []byte(tc.body))
			var size *RequestSizeError
			if IsContextOverflow(err) != tc.context || errors.As(err, &size) != tc.size {
				t.Fatal(err)
			}
			if strings.Contains(err.Error(), "<html") || strings.Contains(err.Error(), "<script") || strings.Contains(err.Error(), "nginx/") {
				t.Fatal("raw proxy page leaked", err)
			}
			if tc.size {
				for _, detail := range []string{"HTTP 413", "byte size", "attachments", "context", "administrator", "request-body size limit"} {
					if !strings.Contains(err.Error(), detail) {
						t.Fatalf("missing %q: %v", detail, err)
					}
				}
			}
		})
	}
}

type errorTransport func(*http.Request) (*http.Response, error)

func (f errorTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRequestSizeErrorsThroughBothTransports(t *testing.T) {
	for _, method := range []string{"complete", "provider-request"} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			client := &Client{HTTP: &http.Client{Transport: errorTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 413, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader(nginx413))}, nil
			})}}
			ep := domain.Endpoint{URL: "http://localhost/v1"}
			var err error
			if method == "complete" {
				_, err = client.Complete(context.Background(), ep, domain.Generation{}, nil, nil, "credential", nil)
			} else {
				_, err = client.ProviderRequest(context.Background(), ep, "credential", json.RawMessage(`{"path":"chat/completions","body":{}}`))
			}
			var size *RequestSizeError
			if !errors.As(err, &size) || calls != 1 || strings.Contains(err.Error(), "<html") {
				t.Fatal(calls, err)
			}
		})
	}
}
