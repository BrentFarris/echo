package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/brent/echo/internal/contextmanager"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
)

func TestHTTP413ErrorPresentationAndContextRecovery(t *testing.T) {
	for _, inline := range []bool{false, true} {
		for _, tokens := range []bool{false, true} {
			t.Run(fmt.Sprintf("inline=%t/tokens=%t", inline, tokens), func(t *testing.T) {
				s := testServer(t)
				calls, compressions := 0, 0
				s.ContextCompressor = testCompressor(func(_ context.Context, r contextmanager.CompressionRequest) ([]model.Message, error) {
					compressions++
					var next []model.Message
					for i, m := range r.History {
						if r.Protected[i] {
							next = append(next, m)
						}
					}
					return next, nil
				})
				g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
					calls++
					var body struct {
						Messages []model.Message `json:"messages"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					old := false
					for _, m := range body.Messages {
						old = old || strings.Contains(m.Content, "old-context-marker")
					}
					if tokens && !old {
						stream(w, "Recovered", nil)
						return
					}
					w.WriteHeader(413)
					if tokens {
						fmt.Fprint(w, `{"error":{"type":"prompt_too_long","message":"prompt is too long"}}`)
					} else {
						fmt.Fprint(w, `<html><head><title>413 Request Entity Too Large</title></head><body><h1>413 Request Entity Too Large</h1><hr><center>nginx/1.31.4</center></body></html>`)
					}
				})
				message := ""
				if inline {
					bot, input, _ := inlineFixture(t, s)
					input.Generation = g
					input.History = []inlineChatMessage{{Role: "user", Content: "old-context-marker"}}
					setRemotePassword(t, s)
					cookie := remoteLogin(t, s)
					events := inlineEvents(t, inlineHTTP(s, bot, input, cookie, context.Background()))
					last := events[len(events)-1]
					if tokens {
						if last.Type != "done" {
							t.Fatal(events)
						}
					} else {
						if last.Type != "error" {
							t.Fatal(events)
						}
						message = last.Text
					}
				} else {
					c := chat(t, s, g)
					if err := s.Store.AddMessage(domain.Message{ID: domain.ID(), ChatID: c.ID, Role: "user", Recipients: []string{"echo"}, Content: "old-context-marker", Status: "completed", CreatedAt: domain.Now()}); err != nil {
						t.Fatal(err)
					}
					d, err := s.send(c.ID, SendRequest{Content: "Continue", Generation: g})
					if err != nil {
						t.Fatal(err)
					}
					waitDispatch(t, d)
					runs := dispatchRuns(t, s, d)
					if len(runs) != 1 {
						t.Fatal(runs)
					}
					if tokens {
						if runs[0].Status != "completed" {
							t.Fatal(runs)
						}
					} else {
						if runs[0].Status != "failed" {
							t.Fatal(runs)
						}
						message = runs[0].Error
					}
				}
				if tokens {
					if calls != 2 || compressions != 1 {
						t.Fatal(calls, compressions)
					}
				} else {
					if calls != 1 || compressions != 0 {
						t.Fatal("byte rejection triggered context recovery", calls, compressions)
					}
					if !strings.Contains(message, "HTTP 413") || !strings.Contains(message, "byte size") || !strings.Contains(message, "smaller attachments") || strings.Contains(message, "<html") || strings.Contains(message, "nginx/") {
						t.Fatal(message)
					}
				}
			})
		}
	}
}

func TestProviderRequestContextErrorReachesHostRecovery(t *testing.T) {
	s := testServer(t)
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(413)
		fmt.Fprint(w, `{"error":{"code":"context_window_exceeded","message":"prompt too long secret"}}`)
	})
	var endpoint domain.Endpoint
	if err := s.Store.Get("endpoints", g.EndpointID, &endpoint); err != nil {
		t.Fatal(err)
	}
	endpoint.Provider = "openai-compatible-cloud"
	bot, err := s.Bot("echo")
	if err != nil {
		t.Fatal(err)
	}
	invocation := &providerInvocation{Endpoint: endpoint, Credential: "secret", Scope: domain.ExecutionScope{Bot: bot}}
	ctx := context.WithValue(context.Background(), providerKey{}, invocation)
	_, err = s.providerOperation(ctx, domain.ExecutionScope{Bot: bot, PluginID: "cloud-models"}, "model.request", json.RawMessage(`{"path":"chat/completions","body":{}}`))
	var overflow *model.ContextOverflowError
	if !errors.As(invocation.Err, &overflow) || !model.IsContextOverflow(err) || strings.Contains(err.Error(), "secret") {
		t.Fatal(invocation.Err, err)
	}
}
