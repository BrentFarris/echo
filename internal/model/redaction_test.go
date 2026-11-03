package model

import (
	"context"
	"fmt"
	"github.com/brent/echo/internal/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCredentialRedactionAcrossChunks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, part := range []string{"safe sk-", "secret-value end"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", part)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	var streamed string
	out, e := New().Complete(context.Background(), domain.Endpoint{URL: server.URL}, domain.Generation{}, nil, nil, "sk-secret-value", func(d Delta) { streamed += d.Content })
	if e != nil || streamed != "safe [redacted] end" || out.Content != streamed || strings.Contains(streamed, "sk-secret") {
		t.Fatal(out, streamed, e)
	}
}
