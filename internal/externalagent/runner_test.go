package externalagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/externalagent/testdata/mockcli"
)

// Opt-in: consumes the signed-in account's subscription and writes only inside
// the test's temporary directory. Normal tests never invoke real inference.
func TestLiveCodexSubscriptionSmoke(t *testing.T) {
	if os.Getenv("ECHO_LIVE_CODEX_TEST") != "1" {
		t.Skip("set ECHO_LIVE_CODEX_TEST=1 for live subscription acceptance")
	}
	dir := t.TempDir()
	r := Runner{Process: &broker.ProcessBroker{Dir: dir}}
	token := domain.ID()
	q := Request{Endpoint: domain.Endpoint{Kind: "codex-cli"}, Directory: dir, Prompt: "Work only in the current temporary directory. Create echo-smoke.txt containing exactly 'echo cli smoke'. Remember the token " + token + " for my next message. Do not start services or modify other folders. Reply 'ready' when finished."}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	result, err := r.Run(ctx, q, func(e Event) error {
		if e.Tool {
			t.Logf("native activity: %s %s", e.Name, e.Status)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "echo-smoke.txt"))
	if err != nil || strings.TrimSpace(string(data)) != "echo cli smoke" {
		t.Fatal("file creation failed", err, string(data))
	}
	t.Log("file creation and native activity verified")
	q.SessionID = result.SessionID
	q.Prompt = "What token did I ask you to remember? Reply with that token only. Do not read files."
	result, err = r.Run(ctx, q, nil)
	if err != nil || !strings.Contains(result.Content, token) {
		t.Fatal("native session did not remember context", err, result.Content)
	}
	t.Log("native follow-up retained context")
	q.Prompt = "In the current temporary directory run a shell command that sleeps for 60 seconds, wait for it to finish, then reply done. Do not write or modify any files."
	stopCtx, stop := context.WithCancel(ctx)
	defer stop()
	started := false
	_, err = r.Run(stopCtx, q, func(e Event) error {
		if e.Tool && e.Status == "running" && strings.Contains(strings.ToLower(e.Content), "sleep") {
			started = true
			stop()
		}
		return nil
	})
	if !started || !errors.Is(err, context.Canceled) {
		t.Fatal("live cancellation was not observed", started, err)
	}
	t.Log("live CLI cancellation verified")
}

func init() { mockcli.Run() }
func fixture(t *testing.T, kind string) (Runner, Request) {
	t.Helper()
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		t.Setenv(key, "")
	}
	t.Setenv("ECHO_EXTERNAL_TEST_CLI", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	return Runner{Process: &broker.ProcessBroker{Dir: dir}}, Request{Endpoint: domain.Endpoint{Kind: kind, Executable: exe}, Directory: dir, Model: `model " ; $(echo wrong)`, Prompt: "remember\nUnicode: 日本語\n; $(echo never execute)"}
}
func TestNativeCLIStreamingAndResume(t *testing.T) {
	for _, kind := range []string{"codex-cli", "claude-code-cli"} {
		t.Run(kind, func(t *testing.T) {
			r, q := fixture(t, kind)
			log := filepath.Join(q.Directory, "calls.jsonl")
			t.Setenv("ECHO_EXTERNAL_TEST_LOG", log)
			var events []Event
			result, err := r.Run(context.Background(), q, func(e Event) error { events = append(events, e); return nil })
			if err != nil || result.Content != "Remembered" || result.SessionID == "" || result.Usage.InputTokens != 20 || result.ToolCalls != 1 {
				t.Fatal(result, err)
			}
			if len(events) < 3 || strings.Contains(result.Content, "diagnostic") {
				t.Fatal(events)
			}
			q.SessionID = result.SessionID
			q.Prompt = "followup"
			again, err := r.Run(context.Background(), q, nil)
			if err != nil || again.SessionID != q.SessionID {
				t.Fatal(again, err)
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			var calls []struct {
				Args             []string
				Input, Directory string
			}
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				var c struct {
					Args             []string
					Input, Directory string
				}
				if err := json.Unmarshal([]byte(line), &c); err != nil {
					t.Fatal(err)
				}
				calls = append(calls, c)
			}
			if len(calls) != 2 || !slices.Contains(calls[0].Args, q.Model) || calls[0].Directory != q.Directory || !strings.Contains(calls[0].Input, "日本語") || !slices.Contains(calls[1].Args, q.SessionID) || slices.Contains(calls[0].Args, "--last") {
				t.Fatal(calls)
			}
		})
	}
}
func TestNativeCLIFailures(t *testing.T) {
	for _, mode := range []string{"malformed", "missing-session", "crash", "api-auth"} {
		t.Run(mode, func(t *testing.T) {
			kind := "codex-cli"
			if mode == "api-auth" {
				kind = "claude-code-cli"
			}
			r, q := fixture(t, kind)
			t.Setenv("ECHO_EXTERNAL_TEST_MODE", mode)
			_, err := r.Run(context.Background(), q, nil)
			if err == nil {
				t.Fatal("failure reported as success")
			}
			if mode == "missing-session" && !strings.Contains(err.Error(), "Reset external sessions") {
				t.Fatal(err)
			}
		})
	}
}
func TestSubscriptionOverrideStopsBeforeInference(t *testing.T) {
	r, q := fixture(t, "codex-cli")
	t.Setenv("CODEX_API_KEY", "do-not-log-this-secret")
	_, err := r.Run(context.Background(), q, nil)
	if err == nil || !strings.Contains(err.Error(), "CODEX_API_KEY") || strings.Contains(err.Error(), "do-not-log") {
		t.Fatal(err)
	}
}
func TestCancellationKillsNativeProcessTree(t *testing.T) {
	r, q := fixture(t, "codex-cli")
	t.Setenv("ECHO_EXTERNAL_TEST_MODE", "cancel")
	started := filepath.Join(q.Directory, "started")
	marker := filepath.Join(q.Directory, "child")
	t.Setenv("ECHO_EXTERNAL_STARTED", started)
	t.Setenv("ECHO_EXTERNAL_CHILD_MARKER", marker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx, q, nil); done <- err }()
	deadline := time.After(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("exited before cancellation: %v", err)
		case <-deadline:
			t.Fatal("fixture did not start")
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled run succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("process tree did not stop")
	}
	time.Sleep(2200 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("child process survived cancellation", err)
	}
}

func TestParserIgnoresUnknownEventsAndCountsOnlyOnce(t *testing.T) {
	p := parser{kind: "codex-cli", tools: map[string]bool{}, texts: map[string]string{}}
	for _, line := range []string{`{"type":"future.event"}`, `{"type":"item.completed","item":{"type":"mcp_tool_call","id":"same"}}`, `{"type":"item.completed","item":{"type":"mcp_tool_call","id":"same"}}`, `{"type":"turn.failed","error":{"message":"usage limit"}}`} {
		if err := p.line([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if p.result.ToolCalls != 1 || p.failure != "usage limit" || p.completed {
		t.Fatal(p)
	}
}

func TestClaudeSubagentDoesNotReplaceMainSession(t *testing.T) {
	p := parser{kind: "claude-code-cli", tools: map[string]bool{}, texts: map[string]string{}}
	for _, line := range []string{
		`{"type":"system","subtype":"init","session_id":"main"}`,
		`{"type":"stream_event","session_id":"subagent","parent_tool_use_id":"tool","event":{"type":"message_start","message":{"id":"child"}}}`,
	} {
		if err := p.line([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if p.result.SessionID != "main" || p.messageID != "" {
		t.Fatal(p)
	}
}

func TestNativeCLIArguments(t *testing.T) {
	q := Request{Endpoint: domain.Endpoint{Kind: "codex-cli"}}
	want := []string{"exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "-c", `model_provider="openai"`, "-c", `forced_login_method="chatgpt"`, "-"}
	if !slices.Equal(Arguments(q), want) {
		t.Fatal(Arguments(q))
	}
	q.SessionID = "exact-id"
	if !slices.Equal(Arguments(q)[:3], []string{"exec", "resume", "exact-id"}) {
		t.Fatal(Arguments(q))
	}
	q.Endpoint.Kind = "claude-code-cli"
	want = []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--dangerously-skip-permissions", "--permission-prompts", "none", "--settings", `{"sandbox":{"enabled":false}}`, "--resume", "exact-id"}
	if !slices.Equal(Arguments(q), want) {
		t.Fatal(Arguments(q))
	}
}

func TestCheckNeverRunsInferenceAndDirectoryPrecedence(t *testing.T) {
	for _, kind := range []string{"codex-cli", "claude-code-cli"} {
		t.Run(kind, func(t *testing.T) {
			r, q := fixture(t, kind)
			log := filepath.Join(q.Directory, "inference.jsonl")
			t.Setenv("ECHO_EXTERNAL_TEST_LOG", log)
			check, err := r.Check(context.Background(), q.Endpoint, q.Directory)
			if err != nil || !check.Authenticated || check.Version == "" || check.Executable == "" {
				t.Fatal(check, err)
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatal("connection check invoked inference", err)
			}
			q.Endpoint.WorkingDirectory = q.Directory
			if dir, err := Directory(q.Endpoint, []string{t.TempDir()}); err != nil || dir != q.Directory {
				t.Fatal(dir, err)
			}
			q.Endpoint.WorkingDirectory = ""
			if dir, err := Directory(q.Endpoint, []string{q.Directory}); err != nil || dir != q.Directory {
				t.Fatal(dir, err)
			}
			home, _ := os.UserHomeDir()
			if dir, err := Directory(q.Endpoint, nil); err != nil || dir != home {
				t.Fatal(dir, err)
			}
		})
	}
}
