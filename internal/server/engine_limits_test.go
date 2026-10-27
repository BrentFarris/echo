package server

import (
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
)

func TestModelRoundsWithoutCountLimit(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("stop=%t", stop), func(t *testing.T) {
			s := testServer(t)
			var rounds atomic.Int32
			reached := make(chan struct{})
			g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Error(err)
					return
				}
				round := rounds.Add(1)
				if round <= 130 {
					stream(w, "", []domain.ToolCall{{ID: fmt.Sprintf("list-%d", round), Type: "function", Function: domain.FunctionCall{Name: "bot_list", Arguments: "{}"}}})
					return
				}
				close(reached)
				if stop {
					<-r.Context().Done()
					return
				}
				stream(w, "Finished after 130 tool rounds.", nil)
			})
			// Release a blocked model request before the HTTP server's cleanup.
			t.Cleanup(s.cancel)
			c := chat(t, s, g)
			d, err := s.send(c.ID, SendRequest{Content: "Keep working", Generation: g})
			if err != nil {
				t.Fatal(err)
			}
			if stop {
				select {
				case <-reached:
				case <-d.done:
					t.Fatalf("dispatch ended after %d rounds: %v", rounds.Load(), d.err)
				case <-time.After(20 * time.Second):
					t.Fatal("model did not pass the former round limit")
				}
				s.stopChat(c.ID)()
			}
			waitDispatch(t, d)
			if rounds.Load() != 131 {
				t.Fatalf("got %d model rounds, want 131: %v", rounds.Load(), d.err)
			}
			wantStatus := "completed"
			if stop {
				wantStatus = "cancelled"
			}
			runs := dispatchRuns(t, s, d)
			if len(runs) != 1 || runs[0].Status != wantStatus {
				t.Fatalf("unexpected runs: %+v", runs)
			}
			if !stop {
				messages, err := s.messages(c.ID)
				if err != nil {
					t.Fatal(err)
				}
				if last := messages[len(messages)-1]; last.Content != "Finished after 130 tool rounds." {
					t.Fatalf("missing final response: %+v", last)
				}
			}
		})
	}
}
