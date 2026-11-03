package model

import (
	"context"
	"fmt"
	"github.com/brent/echo/internal/domain"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStreamingToolFragments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\",\"tool_calls\":[{\"index\":0,\"id\":\"call1\",\"function\":{\"name\":\"file_read\",\"arguments\":\"{\\\"path\\\":\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"test\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	out, e := New().Complete(context.Background(), domain.Endpoint{URL: server.URL}, domain.Generation{Model: "test"}, nil, nil, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Calls) != 1 || out.Calls[0].Function.Arguments != `{"path":"test"}` || out.Reasoning != "thinking" {
		t.Fatal(out)
	}
}
func TestIncompleteStreamFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	}))
	defer server.Close()
	_, e := New().Complete(context.Background(), domain.Endpoint{URL: server.URL}, domain.Generation{}, nil, nil, "", nil)
	if e == nil {
		t.Fatal("truncated stream accepted")
	}
}
