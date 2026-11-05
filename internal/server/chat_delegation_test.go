package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/calendar"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
)

func delegationScope(t *testing.T, s *Server, bot domain.Bot, gen domain.Generation) domain.ExecutionScope {
	t.Helper()
	parent := chat(t, s, gen)
	run := domain.Run{ID: domain.ID(), ChatID: parent.ID, BotID: bot.ID, Generation: gen, Status: "running"}
	if err := s.Store.Put("runs", run.ID, run); err != nil {
		t.Fatal(err)
	}
	return domain.ExecutionScope{ChatID: parent.ID, RunID: run.ID, Bot: bot}
}

func TestScheduledChatDelegation(t *testing.T) {
	s := testServer(t)
	poller, err := s.saveBot(domain.Bot{Handle: "poller"})
	if err != nil {
		t.Fatal(err)
	}
	var workers []domain.BotGroupMember
	for _, handle := range []string{"dev", "reviewer"} {
		b, err := s.saveBot(domain.Bot{Handle: handle})
		if err != nil {
			t.Fatal(err)
		}
		workers = append(workers, domain.BotGroupMember{BotID: b.ID})
	}
	if _, err := s.saveBotGroup(t.Context(), "", domain.BotGroup{Handle: "modbots", Members: workers}); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	prompts := []string{"JIRA-11: Implement and commit the @echo fix", "JIRA-12: Implement and commit the @unknown.name fix"}
	dir := t.TempDir()
	tickets := filepath.Join(dir, "tickets.txt")
	if err := os.WriteFile(tickets, []byte(strings.Join(prompts, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	// Use the same ticket fixture on hosts without native command support.
	pollTool, resultField := "file_read", "content"
	pollArgs := map[string]string{"path": tickets}
	if runtime.GOOS == "windows" && os.Getenv("ECHO2_NATIVE_TESTS") == "1" {
		if err := os.WriteFile(filepath.Join(dir, "jira.ps1"), []byte("Get-Content -LiteralPath (Join-Path $PSScriptRoot 'tickets.txt')"), 0600); err != nil {
			t.Fatal(err)
		}
		pollTool, resultField = "shell_command", "output"
		pollArgs = map[string]string{"command": "./jira.ps1", "directory": dir}
	}
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []model.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		last := request.Messages[len(request.Messages)-1]
		if strings.Contains(request.Messages[0].Content, "You are @poller.") {
			if last.Role == "user" {
				args, _ := json.Marshal(pollArgs)
				stream(w, "", []domain.ToolCall{{ID: "poll", Type: "function", Function: domain.FunctionCall{Name: pollTool, Arguments: string(args)}}})
				return
			}
			if last.CallID != "poll" {
				stream(w, "Tickets dispatched.", nil)
				return
			}
			var result map[string]any
			if err := json.Unmarshal([]byte(last.Content), &result); err != nil {
				t.Error(err)
				return
			}
			output, _ := result[resultField].(string)
			if strings.TrimSpace(output) == "" {
				t.Errorf("poll returned no tickets: %s", last.Content)
				return
			}
			var calls []domain.ToolCall
			for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
				prompt := strings.TrimSpace(line)
				key, _, _ := strings.Cut(prompt, ":")
				args, _ := json.Marshal(map[string]string{"to": "@ModBots", "prompt": prompt, "taskKey": key})
				calls = append(calls, domain.ToolCall{ID: key, Type: "function", Function: domain.FunctionCall{Name: "chat_start", Arguments: string(args)}})
			}
			stream(w, "", calls)
			return
		}
		if len(request.Messages) != 2 || !strings.HasPrefix(last.Content, "Delegated by @poller") || strings.Contains(last.Content, "./jira.ps1") {
			t.Error("unexpected inherited context", request.Messages)
		}
		select {
		case <-release:
			stream(w, "Implemented and committed.", nil)
		case <-r.Context().Done():
		}
	})
	t.Cleanup(s.cancel)
	occ := calendar.Occurrence{BotID: poller.ID, Generation: g, Recurrence: "every 10 minutes", Prompt: "Run ./jira.ps1 and delegate each ticket to modbots."}
	// Both occurrences finish while the workers are still busy.
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		err := s.scheduled(ctx, occ)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	chats, err := s.Store.Chats()
	if err != nil || len(chats) != 4 {
		t.Fatalf("expected two polling chats and two task chats, got %+v (%v)", chats, err)
	}
	receipts := map[string]chatReceipt{}
	for _, c := range chats {
		msgs, err := s.Store.Messages(c.ID)
		if err != nil || len(msgs) == 0 {
			t.Fatal(msgs, err)
		}
		if strings.HasPrefix(c.Title, "Scheduled:") {
			for _, m := range msgs {
				if m.Role == "tool" && m.CallID != "poll" {
					var receipt chatReceipt
					if err := json.Unmarshal([]byte(m.Content), &receipt); err != nil || receipt.ChatID == "" || receipt.DispatchID == "" || receipt.NodeID != s.Swarm.Node().ID {
						t.Fatal(m)
					}
					if previous, ok := receipts[m.CallID]; ok && previous != receipt {
						t.Fatal("task key returned a different dispatch", previous, receipt)
					}
					receipts[m.CallID] = receipt
				}
			}
			continue
		}
		input := msgs[0]
		if !slices.Contains(prompts, c.Title) || c.Generation != g || input.Content != "@modbots\n"+c.Title || input.Author != "Delegated by @poller" ||
			len(input.Segments) != 2 || input.Segments[0].Type != "group" || !slices.Equal(input.Recipients, []string{workers[0].BotID, workers[1].BotID}) {
			t.Fatal(c, input)
		}
	}
	if len(receipts) != 2 {
		t.Fatal(receipts)
	}
	close(release)
	for _, receipt := range receipts {
		eventually(t, func() bool {
			runs, _ := s.Store.ChatRuns(receipt.ChatID)
			return len(runs) == 2 && runs[0].Status == "completed" && runs[1].Status == "completed"
		})
	}
}

func TestChatDelegationGroupQueueAndValidation(t *testing.T) {
	s := testServer(t)
	g, started := controlledModel(t, s)
	echo, _ := s.Bot("echo")
	worker, err := s.saveBot(domain.Bot{Handle: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	scope := delegationScope(t, s, echo, g)
	makeOneGroup(t, s, domain.BotGroupMember{BotID: worker.ID})

	for _, input := range [][2]string{{"", "task"}, {"missing", "task"}, {"pool @echo", "task"}, {"worker", " "}} {
		if _, err := s.startDelegatedChat(t.Context(), scope, input[0], input[1], "one"); err == nil {
			t.Fatal("accepted invalid input", input)
		}
	}
	chats, _ := s.Store.Chats()
	if len(chats) != 1 {
		t.Fatalf("failed sends left chats behind: %+v", chats)
	}
	busy, err := s.startDelegatedChat(t.Context(), scope, "worker", "Direct task mentioning @echo", "")
	if err != nil {
		t.Fatal(err)
	}
	first := nextInvocation(t, started, "worker", "Direct task mentioning @echo")
	type attempt struct {
		receipt chatReceipt
		err     error
	}
	results := make(chan attempt, 2)
	for i := 0; i < 2; i++ {
		go func() {
			ctx, cancel := context.WithCancel(t.Context())
			receipt, err := s.startDelegatedChat(ctx, scope, "pool", "Task from @not-a-recipient", "one")
			cancel()
			results <- attempt{receipt, err}
		}()
	}
	a, b := <-results, <-results
	if a.err != nil || b.err != nil || a.receipt != b.receipt || a.receipt.ChatID == busy.ChatID {
		t.Fatal(a, b)
	}
	queued := a.receipt
	ms, _ := s.Store.Messages(queued.ChatID)
	if len(ms) != 1 || ms[0].Status != "queued" || ms[0].Author != "Delegated by @echo" {
		t.Fatal(ms)
	}
	first.finish <- "done"
	next := nextInvocation(t, started, "worker", "Task from @not-a-recipient")
	ms, _ = s.Store.Messages(queued.ChatID)
	if ms[0].Status != "completed" || ms[0].Author != "Delegated by @echo" || !slices.Equal(ms[0].Recipients, []string{worker.ID}) || ms[0].Segments[0].DeliveryMode != "one" {
		t.Fatal(ms[0])
	}
	next.finish <- "done"
}

func TestChatDelegationSwarmGroup(t *testing.T) {
	s, _ := swarmFixture(t, "desktop", remotePassword)
	peer, host := swarmFixture(t, "worker", remotePassword)
	connectSwarm(t, s, host, remotePassword)
	reply := func(w http.ResponseWriter, r *http.Request) { stream(w, "Done", nil) }
	g := attachModel(t, s, reply)
	attachModel(t, peer, reply)
	makeOneGroup(t, peer, domain.BotGroupMember{BotID: "echo"})
	echo, _ := s.Bot("echo")
	scope := delegationScope(t, s, echo, g)
	result, err := s.startDelegatedChat(t.Context(), scope, "worker.pool", "Implement ticket reported by @desktop.echo", "T-1")
	if err != nil {
		t.Fatal(err)
	}
	eventuallySwarm(t, func() bool {
		runs, _ := s.Store.ChatRuns(result.ChatID)
		return len(runs) == 1 && runs[0].Status == "completed"
	})
	messages, err := s.Store.Messages(result.ChatID)
	if err != nil || len(messages) < 2 {
		t.Fatal(messages, err)
	}
	input := messages[0]
	remoteEcho := s.displayBot(peer.Swarm.Node().ID, "echo")
	if input.Author != "Delegated by @echo" || !slices.Equal(input.Recipients, []string{remoteEcho}) || input.Segments[0].DeliveryMode != "one" {
		t.Fatal(input)
	}
}

func TestChatDelegationRetryAfterRestartAndRerun(t *testing.T) {
	s := testServer(t)
	g, started := controlledModel(t, s)
	echo, _ := s.Bot("echo")
	scope := delegationScope(t, s, echo, g)
	makeOneGroup(t, s, domain.BotGroupMember{BotID: "echo"})
	busy := sendWork(t, s, g, "Hold this slot")
	first := nextInvocation(t, started, "echo", "Hold this slot")
	receipt, err := s.startDelegatedChat(t.Context(), scope, "pool", "Implement @unknown ticket", "T-1")
	if err != nil {
		t.Fatal(err)
	}
	stop := s.stopChat(receipt.ChatID)
	stop()
	first.finish <- "done"
	waitDispatch(t, busy)
	s = restartSwarmFixture(t, s)
	again, err := s.startDelegatedChat(t.Context(), scope, "pool", "Changed description", "T-1")
	if err != nil || again != receipt {
		t.Fatal(again, err)
	}
	msgs, _ := s.Store.Messages(receipt.ChatID)
	if len(msgs) != 1 || msgs[0].Status != "cancelled" {
		t.Fatal(msgs)
	}
	rerun, err := s.send(receipt.ChatID, SendRequest{RequestID: "rerun", rerunMessageID: msgs[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	nextInvocation(t, started, "echo", "Implement @unknown ticket").finish <- "done"
	waitDispatch(t, rerun)
	msgs, _ = s.Store.Messages(receipt.ChatID)
	if msgs[0].Author != "Delegated by @echo" || !slices.Equal(msgs[0].Recipients, []string{"echo"}) {
		t.Fatal(msgs)
	}
	if err := s.Store.DeleteChat(receipt.ChatID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.startDelegatedChat(t.Context(), scope, "pool", "Deleted task", "T-1"); err == nil {
		t.Fatal("deleted task was recreated")
	}
}

func TestChatDelegationPluginBoundary(t *testing.T) {
	s := testServer(t)
	bot, err := s.saveBot(domain.Bot{Handle: "poller", Plugins: map[string]bool{"chat-delegation": false}})
	if err != nil {
		t.Fatal(err)
	}
	scope := delegationScope(t, s, bot, domain.Generation{})
	args := json.RawMessage(`{"to":"echo","prompt":"A task"}`)
	if _, err := s.Plugins.Invoke(t.Context(), scope, "chat_start", args); err == nil {
		t.Fatal("disabled plugin invoked")
	}
	echo, _ := s.Bot("echo")
	if _, err := s.Plugins.Invoke(t.Context(), domain.ExecutionScope{Bot: echo, Standalone: true}, "chat_start", args); err == nil {
		t.Fatal("standalone invocation accepted")
	}
	if _, err := s.Plugins.Invoke(t.Context(), domain.ExecutionScope{Bot: echo}, "chat_start", args); err == nil {
		t.Fatal("invocation outside a chat run accepted")
	}
	scope.Bot = echo
	if _, err := s.Plugins.Invoke(t.Context(), scope, "chat_start", args); err == nil {
		t.Fatal("another bot's run accepted")
	}
	scope = delegationScope(t, s, echo, domain.Generation{})
	if _, err := s.Plugins.Invoke(t.Context(), scope, "chat_start", args); err == nil {
		t.Fatal("unconfigured model accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.startDelegatedChat(ctx, scope, "echo", "Cancelled", "T-1"); err == nil {
		t.Fatal("cancelled task accepted")
	}
	chats, _ := s.Store.Chats()
	if len(chats) != 2 {
		t.Fatal("failed invocations created chats", chats)
	}
}
