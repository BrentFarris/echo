package model

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/brent/echo/internal/domain"
)

func TestCompletionsWithoutImplicitTimeouts(t *testing.T) {
	for _, provider := range []bool{false, true} {
		t.Run(fmt.Sprintf("provider=%t", provider), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client := New()
				transport := client.HTTP.Transport.(*http.Transport)
				defer transport.CloseIdleConnections()
				transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
					conn, remote := net.Pipe()
					go func() {
						defer remote.Close()
						request, err := http.ReadRequest(bufio.NewReader(remote))
						if err != nil {
							t.Error(err)
							return
						}
						io.Copy(io.Discard, request.Body)
						request.Body.Close()
						// Exceed the former response-header limit before any headers,
						// then exceed both the idle and form's overall defaults.
						time.Sleep(70 * time.Second)
						fmt.Fprint(remote, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nConnection: close\r\n\r\n")
						time.Sleep(11 * time.Minute)
						if provider {
							fmt.Fprint(remote, `{"reply":"finished"}`)
						} else {
							fmt.Fprint(remote, "data: {\"choices\":[{\"delta\":{\"content\":\"finished\"}}]}\n\ndata: [DONE]\n\n")
						}
					}()
					return conn, nil
				}
				endpoint := domain.Endpoint{URL: "http://model.test"}
				if provider {
					result, err := client.ProviderRequest(context.Background(), endpoint, "", json.RawMessage(`{"path":"messages"}`))
					if err != nil || !strings.Contains(fmt.Sprint(result), "finished") {
						t.Fatalf("slow provider request: %v, %v", result, err)
					}
				} else {
					result, err := client.Complete(context.Background(), endpoint, domain.Generation{}, nil, nil, "", nil)
					if err != nil || result.Content != "finished" {
						t.Fatalf("slow completion: %+v, %v", result, err)
					}
				}
			})
		})
	}
}
