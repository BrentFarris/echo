package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/brent/echo/internal/domain"
)

func TestUsageAfterFinishAndUnsupportedNegotiation(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		t.Run(fmt.Sprint(unsupported), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if unsupported && requests == 1 {
					if body["stream_options"] == nil {
						t.Error("usage not requested")
					}
					w.WriteHeader(400)
					fmt.Fprint(w, `{"error":{"message":"stream_options.include_usage is not supported"}}`)
					return
				}
				if (body["stream_options"] == nil) != unsupported {
					t.Error("capability not cached", body)
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":123,\"completion_tokens\":45,\"total_tokens\":168}}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			client := New()
			for i := 0; i < 2; i++ {
				result, err := client.Complete(context.Background(), domain.Endpoint{URL: server.URL}, domain.Generation{}, nil, nil, "", nil)
				if err != nil || result.Usage == nil || *result.Usage != (domain.TokenUsage{InputTokens: 123, OutputTokens: 45, TotalTokens: 168}) {
					t.Fatal(result, err)
				}
			}
			want := 2
			if unsupported {
				want = 3
			}
			if requests != want {
				t.Fatal(requests)
			}
		})
	}
}

func TestTypedOverflowHTTPAndStreamRedaction(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw := `{"error":{"code":"context_length_exceeded","message":"too large secret-token","context_length":4096,"prompt_tokens":5000,"completion_tokens":100}}`
				if streaming {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\ndata: "+raw+"\n\n")
				} else {
					w.WriteHeader(400)
					fmt.Fprint(w, raw)
				}
			}))
			defer server.Close()
			_, err := New().Complete(context.Background(), domain.Endpoint{URL: server.URL}, domain.Generation{}, nil, nil, "secret-token", nil)
			var overflow *ContextOverflowError
			if !errors.As(err, &overflow) || overflow.LimitTokens != 4096 || overflow.InputTokens != 5000 || strings.Contains(err.Error(), "secret-token") {
				t.Fatal(err)
			}
		})
	}
	for _, raw := range []string{`{"error":{"message":"request entity too large"}}`, `{"error":{"message":"invalid API key"}}`, `{"error":{"message":"stream_options invalid value"}}`} {
		if IsContextOverflow(ProviderError(413, []byte(raw))) {
			t.Fatal("unrelated error classified", raw)
		}
	}
	if unsupportedStreamUsage(400, "stream_options invalid value") {
		t.Fatal("non-capability error retried")
	}
	if IsContextOverflow(ProviderError(429, []byte(`{"error":{"message":"too many tokens"}}`))) {
		t.Fatal("rate limit classified as context")
	}
	var overflow *ContextOverflowError
	err := ProviderError(400, []byte(`{"error":{"message":"maximum context length is 4096 tokens; prompt contains 5000 tokens"}}`))
	if !errors.As(err, &overflow) || overflow.LimitTokens != 4096 || overflow.InputTokens != 5000 {
		t.Fatal(err)
	}
	if !IsContextOverflow(ProviderError(500, []byte(`{"error":{"code":"context_length_exceeded","message":"too large"}}`))) {
		t.Fatal("explicit context code lost")
	}
}

func TestUsageNormalizationMissingAndAliases(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want *domain.TokenUsage
	}{
		{`{}`, nil}, {`{"total_tokens":100}`, nil}, {`{"prompt_tokens":-1}`, nil},
		{`{"input_tokens":100,"output_tokens":50}`, &domain.TokenUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}},
	} {
		var wire wireUsage
		if err := json.Unmarshal([]byte(test.raw), &wire); err != nil {
			t.Fatal(err)
		}
		got := wire.normalized()
		if (got == nil) != (test.want == nil) || got != nil && *got != *test.want {
			t.Fatal(test.raw, got)
		}
	}
}
