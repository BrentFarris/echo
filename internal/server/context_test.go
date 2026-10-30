package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/brent/echo/internal/contextmanager"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
)

type testCompressor func(context.Context, contextmanager.CompressionRequest) ([]model.Message, error)

func (f testCompressor) Compress(ctx context.Context, r contextmanager.CompressionRequest) ([]model.Message, error) {
	return f(ctx, r)
}

func rejectContext(w http.ResponseWriter) {
	// Enough partial content to exercise persisted chunks, followed by a stream error.
	raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": "rejected partial " + strings.Repeat("x", 70000)}}}})
	fmt.Fprintf(w, "data: %s\n\ndata: {\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"context too long\"}}\n\n", raw)
}

func TestBotContextRecoveryPreservesToolExecutionAndTranscript(t *testing.T) {
	for _, mode := range []string{"compress", "clean", "omit"} {
		t.Run(mode, func(t *testing.T) {
			s := testServer(t)
			s.ContextCompressor = testCompressor(func(_ context.Context, r contextmanager.CompressionRequest) ([]model.Message, error) {
				if mode != "compress" {
					return nil, &contextmanager.CompressionError{Reason: "no summary"}
				}
				var next []model.Message
				for _, m := range r.History {
					if !strings.Contains(m.Content, "historical-marker") {
						next = append(next, m)
					}
				}
				return next, nil
			})
			requests := 0
			g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
				requests++
				var input struct {
					Messages []model.Message `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
					return
				}
				last := input.Messages[len(input.Messages)-1]
				if last.Role != "tool" {
					stream(w, "", []domain.ToolCall{{ID: "create-once", Type: "function", Function: domain.FunctionCall{Name: "bot_save", Arguments: `{"handle":"once","name":"Once","prompt":"test"}`}}})
					return
				}
				old := false
				for _, m := range input.Messages {
					old = old || strings.Contains(m.Content, "historical-marker")
				}
				if old || mode == "omit" && !strings.Contains(last.Content, "already executed") {
					rejectContext(w)
					return
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Recovered\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":500,\"completion_tokens\":2,\"total_tokens\":502}}\n\ndata: [DONE]\n\n")
			})
			c := chat(t, s, g)
			if err := s.Store.AddMessage(domain.Message{ID: domain.ID(), ChatID: c.ID, Role: "user", Recipients: []string{"echo"}, Content: "historical-marker", Status: "completed", CreatedAt: domain.Now()}); err != nil {
				t.Fatal(err)
			}
			d, err := s.send(c.ID, SendRequest{Content: "Create a bot once", Generation: g})
			if err != nil {
				t.Fatal(err)
			}
			waitDispatch(t, d)
			messages, err := s.Store.Messages(c.ID)
			if err != nil {
				t.Fatal(err)
			}
			last := messages[len(messages)-1]
			if last.Status != "completed" || last.Content != "Recovered" || last.Usage == nil || last.Usage.InputTokens != 500 {
				t.Fatal(last, d.err)
			}
			tools := 0
			for _, m := range messages {
				if m.Role == "tool" {
					tools++
					if mode == "omit" && (m.ModelContent == nil || strings.Contains(m.Content, "already executed")) {
						t.Fatal("original tool output lost", m)
					}
				}
			}
			want := 3
			if mode == "omit" {
				want = 4
			}
			if tools != 1 || requests != want {
				t.Fatal("tool rerun or unexpected requests", tools, requests)
			}
			bot, err := s.Bot("echo")
			if err != nil {
				t.Fatal(err)
			}
			history, err := s.history(bot, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range history {
				if m.Role == "tool" && mode == "omit" && (!m.Omitted || !strings.Contains(m.Content, "already executed")) {
					t.Fatal("omission not restored", m)
				}
			}
		})
	}
}

func TestInlineContextRecoveryKeepsCandidateAndDoesNotRepeatProposal(t *testing.T) {
	for _, omit := range []bool{false, true} {
		t.Run(fmt.Sprint(omit), func(t *testing.T) {
			s := testServer(t)
			s.ContextCompressor = testCompressor(func(context.Context, contextmanager.CompressionRequest) ([]model.Message, error) {
				return nil, &contextmanager.CompressionError{Reason: "no summary"}
			})
			bot, input, path := inlineFixture(t, s)
			setRemotePassword(t, s)
			cookie := remoteLogin(t, s)
			input.History = []inlineChatMessage{{Role: "user", Content: "historical-marker"}}
			requests := 0
			input.Generation = attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
				requests++
				var body struct {
					Messages []model.Message `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				last := body.Messages[len(body.Messages)-1]
				if last.Role != "tool" {
					stream(w, "Preview prepared. ", []domain.ToolCall{{ID: "edit-once", Type: "function", Function: domain.FunctionCall{Name: inlineEditTool, Arguments: `{"edits":[{"oldText":"const value = 1;","newText":"const value = 2;"}]}`}}})
					return
				}
				old, current := false, false
				for _, m := range body.Messages {
					old = old || strings.Contains(m.Content, "historical-marker")
					if strings.HasPrefix(m.Content, "Current editor context") {
						current = strings.Contains(m.Content, "const value = 2;")
					}
				}
				if !current {
					t.Error("protected current candidate lost")
				}
				if old || omit && !strings.Contains(last.Content, "already executed") {
					rejectContext(w)
					return
				}
				stream(w, "Recovered", nil)
			})
			events := inlineEvents(t, inlineHTTP(s, bot, input, cookie, context.Background()))
			proposals, resets := 0, 0
			for _, e := range events {
				if e.Type == "proposal" {
					proposals++
					if e.Content == nil || *e.Content != "const value = 2;\n" {
						t.Fatal(e)
					}
				}
				if e.Type == "reset" {
					resets++
					if e.Text != "Preview prepared. " {
						t.Fatal("accepted round erased", e)
					}
				}
			}
			want := 3
			if omit {
				want = 4
			}
			if proposals != 1 || requests != want || resets != want-2 || events[len(events)-1].Type != "done" {
				t.Fatal(proposals, requests, resets, events[len(events)-1])
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "const value = 0;\n" {
				t.Fatal("disk changed", err)
			}
		})
	}
}

func TestOmittedGeneratedMediaIsNotReloaded(t *testing.T) {
	s := testServer(t)
	notice := "too large"
	original := domain.Message{ID: "tool", BotID: "echo", Role: "tool", Status: "completed", CallID: "a", Content: `{"imageId":"missing"}`, Attachments: []domain.Attachment{{ID: "missing", MediaType: "image/png"}}, ModelContent: &notice}
	history := []model.Message{{ID: "tool", Role: "tool", CallID: "a", Content: notice, Omitted: true}}
	next, err := s.withGeneratedImages("chat", "echo", []domain.Message{original}, history)
	if err != nil || len(next) != 1 {
		t.Fatal(next, err)
	}
}

func TestProviderHostPreservesUsageAndOverflow(t *testing.T) {
	s := testServer(t)
	failing := false
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		if failing {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"code":"context_length_exceeded","message":"too large secret","context_length":1024}}`)
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
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
	invocation := &providerInvocation{Scope: domain.ExecutionScope{Bot: bot}, Endpoint: endpoint, Generation: g, Credential: "secret"}
	ctx := context.WithValue(context.Background(), providerKey{}, invocation)
	scope := domain.ExecutionScope{Bot: bot, PluginID: "cloud-models"}
	value, err := s.providerOperation(ctx, scope, "model.complete", json.RawMessage(`{"messages":[],"tools":[]}`))
	if err != nil || value.(model.Result).Usage == nil || value.(model.Result).Usage.InputTokens != 10 {
		t.Fatal(value, err)
	}
	failing = true
	_, err = s.providerOperation(ctx, scope, "model.complete", json.RawMessage(`{"messages":[],"tools":[]}`))
	var overflow *model.ContextOverflowError
	if !errors.As(invocation.Err, &overflow) || !model.IsContextOverflow(err) || overflow.LimitTokens != 1024 || strings.Contains(err.Error(), "secret") {
		t.Fatal(invocation.Err, err)
	}
	// The optional custom-provider result fields have the same JSON representation.
	raw, _ := json.Marshal(model.Result{Usage: &domain.TokenUsage{InputTokens: 10}, Overflow: overflow})
	var result model.Result
	if err := json.Unmarshal(raw, &result); err != nil || result.Usage == nil || result.Overflow == nil {
		t.Fatal(result, err)
	}
}
