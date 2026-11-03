package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/brent/echo/internal/coordinator"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/swarm"
)

func TestBotWaitWithoutTimeLimit(t *testing.T) {
	for _, federated := range []bool{false, true} {
		for _, stop := range []bool{false, true} {
			t.Run(fmt.Sprintf("swarm=%t/stop=%t", federated, stop), func(t *testing.T) {
				s := testServer(t)
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					from, to := "echo", "peer"
					node := s.Swarm.Node().ID
					if federated {
						from, to = swarm.Key(node, from), swarm.Key(node, to)
					}
					group := coordinator.New([]string{from, to})
					defer group.Close()
					d := &dispatch{id: "wait-test", ctx: ctx, cancel: cancel, group: group, admitted: map[string]chan struct{}{to: make(chan struct{})}}
					close(d.admitted[to])
					if federated {
						req := workerRequest{ID: d.id, Token: "grant", Runs: []domain.Run{{BotID: "echo"}}}
						d.federation = &swarmDispatch{group: group, Hosts: map[string]workerRequest{node: req}}
						d.worker = &swarmWorker{s: s, d: d, request: req, localHome: true}
						d.group = &workerGroup{worker: d.worker}
						s.mu.Lock()
						s.dispatches[d.id] = d
						s.mu.Unlock()
						defer func() { s.mu.Lock(); delete(s.dispatches, d.id); s.mu.Unlock() }()
					}
					request, err := group.Send(from, to, "work", "handoff")
					if err != nil {
						t.Fatal(err)
					}
					go func() {
						time.Sleep(10 * time.Minute)
						if stop {
							cancel()
						} else if err := group.Reply(to, request.ID, "finished"); err != nil {
							t.Error(err)
						}
					}()
					result, err := s.tool(ctx, d, domain.ExecutionScope{Bot: domain.Bot{ID: "echo"}}, domain.ToolCall{Function: domain.FunctionCall{Name: "bot_wait", Arguments: `{"requestId":"handoff"}`}})
					if stop {
						if !errors.Is(err, context.Canceled) {
							t.Fatalf("wait did not preserve cancellation: %v", err)
						}
					} else if err != nil || result.(coordinator.Request).Reply != "finished" {
						t.Fatalf("long handoff wait: %+v, %v", result, err)
					}
				})
			})
		}
	}
}

func TestDispatchAdmissionWithoutTimeLimit(t *testing.T) {
	s := testServer(t)
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		d := &dispatch{id: "unlimited", ctx: ctx, cancel: cancel, admitted: map[string]chan struct{}{"echo": make(chan struct{})}}
		s.mu.Lock()
		s.enqueueLocked(d)
		s.mu.Unlock()
		if err := d.awaitAdmission(ctx, "echo"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(24 * time.Hour)
		if err := ctx.Err(); err != nil {
			t.Fatalf("admitted dispatch expired: %v", err)
		}
		cancel()
		if err := d.awaitAdmission(ctx, "echo"); err != context.Canceled {
			t.Fatalf("explicit cancellation lost: %v", err)
		}
		s.mu.Lock()
		s.releaseLocked(d)
		reserved := len(s.botReservations["echo"])
		s.mu.Unlock()
		if reserved != 0 {
			t.Fatal("cancelled dispatch retained its reservation")
		}
	})
}

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
