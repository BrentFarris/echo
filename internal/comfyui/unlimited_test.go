package comfyui

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type pollTransport func(*http.Request) (*http.Response, error)

func (f pollTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGenerationWaitWithoutTimeLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		client := &Client{BaseURL: "http://comfy.test", HTTPClient: &http.Client{Transport: pollTransport(func(r *http.Request) (*http.Response, error) {
			body := `{}`
			if time.Since(start) >= 6*time.Minute {
				body = `{"job":{"outputs":{"1":{"images":[{"filename":"finished.png","type":"output"}]}}}}`
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})}}
		result, err := client.WaitForCompletionPoll(context.Background(), "job")
		if err != nil || len(result.OutputImages) != 1 || result.OutputImages[0] != "finished.png" {
			t.Fatalf("long generation: %+v, %v", result, err)
		}
	})
}
