// Package externalagent delegates complete turns to installed, authenticated CLIs.
package externalagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
)

type Executor interface {
	RunHost(context.Context, broker.ProcessRequest, func(string)) (broker.ProcessResult, error)
}

type Runner struct{ Process Executor }
type Check struct {
	Executable    string `json:"executable"`
	Version       string `json:"version"`
	Authenticated bool   `json:"authenticated"`
	Message       string `json:"message"`
}
type Request struct {
	Endpoint                                      domain.Endpoint
	Directory, Model, Thinking, SessionID, Prompt string
}
type Event struct {
	ID, Name, Content, Status string
	Tool                      bool
}
type Result struct {
	SessionID, Content string
	Usage              *domain.TokenUsage
	ToolCalls          int64
}

func Resolve(e domain.Endpoint) (string, error) {
	name := e.Executable
	if name == "" {
		name = "codex"
		if e.Kind == "claude-code-cli" {
			name = "claude"
		}
	}
	p, err := broker.NativeExecutable(name)
	if err != nil {
		return "", fmt.Errorf("%s CLI is unavailable; install it or set its executable path: %w", e.Kind, err)
	}
	return p, nil
}

func Directory(e domain.Endpoint, roots []string) (string, error) {
	dir := e.WorkingDirectory
	if dir == "" && len(roots) != 0 {
		dir = roots[0]
	}
	if dir == "" {
		var err error
		dir, err = os.UserHomeDir()
		if err != nil {
			return "", err
		}
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("external agent starting directory: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("external agent starting directory is not a folder")
	}
	return dir, nil
}

func authEnvironment(e domain.Endpoint) error {
	keys := []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL"}
	if e.Kind == "claude-code-cli" {
		keys = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"}
	}
	for _, key := range keys {
		if os.Getenv(key) != "" {
			return fmt.Errorf("subscription connection conflicts with %s in Echo's environment; remove the override and restart Echo", key)
		}
	}
	return nil
}

func (r Runner) Check(ctx context.Context, e domain.Endpoint, dir string) (check Check, err error) {
	if !e.External() {
		return check, errors.New("not an external agent connection")
	}
	check.Executable, err = Resolve(e)
	if err != nil {
		return check, err
	}
	if err = authEnvironment(e); err != nil {
		return check, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	call := func(args ...string) (string, error) {
		out, err := r.Process.RunHost(ctx, broker.ProcessRequest{Executable: check.Executable, Directory: dir, Args: args, SeparateOutput: true, OutputLimit: 64 << 10}, nil)
		if err != nil {
			return "", err
		}
		if out.ExitCode != 0 {
			return "", fmt.Errorf("CLI check failed: %s", strings.TrimSpace(out.Stdout+"\n"+out.Stderr))
		}
		return strings.TrimSpace(out.Stdout + "\n" + out.Stderr), nil
	}
	check.Version, err = call("--version")
	if err != nil {
		return check, err
	}
	if e.Kind == "codex-cli" {
		var status string
		status, err = call("login", "status")
		if err != nil {
			err = fmt.Errorf("Codex subscription login unavailable; run codex login under the account running Echo: %w", err)
		}
		if err == nil && !strings.Contains(strings.ToLower(status), "logged in using chatgpt") {
			err = errors.New("Codex requires ChatGPT subscription login; run codex login with the account running Echo")
		}
	} else {
		// Authentication JSON is stdout only; diagnostic stderr is not JSON.
		out, runErr := r.Process.RunHost(ctx, broker.ProcessRequest{Executable: check.Executable, Directory: dir, Args: []string{"auth", "status"}, SeparateOutput: true, OutputLimit: 64 << 10}, nil)
		var status struct {
			LoggedIn         bool   `json:"loggedIn"`
			AuthMethod       string `json:"authMethod"`
			APIProvider      string `json:"apiProvider"`
			SubscriptionType string `json:"subscriptionType"`
		}
		err = runErr
		if err == nil {
			err = json.Unmarshal([]byte(out.Stdout), &status)
		}
		if err != nil || out.ExitCode != 0 || !status.LoggedIn || status.AuthMethod != "claude.ai" || (status.APIProvider != "" && status.APIProvider != "firstParty") || status.SubscriptionType == "" {
			err = errors.New("Claude Code requires a saved Claude subscription login; run claude auth login with the account running Echo (update the CLI if auth status is unsupported)")
		}
	}
	if err != nil {
		check.Message = err.Error()
		return check, err
	}
	check.Authenticated, check.Message = true, "Subscription login ready. External agent runs with full access."
	return check, nil
}

func ValidateThinking(kind, thinking string) error {
	levels := []string{"", "none", "minimal", "low", "medium", "high", "xhigh", "max"}
	if kind == "claude-code-cli" {
		levels = []string{"", "low", "medium", "high", "xhigh", "max"}
	}
	if !slices.Contains(levels, thinking) {
		return fmt.Errorf("thinking level %q is unsupported by %s", thinking, kind)
	}
	return nil
}

func Arguments(q Request) []string {
	var args []string
	if q.Endpoint.Kind == "codex-cli" {
		args = []string{"exec"}
		if q.SessionID != "" {
			args = append(args, "resume", q.SessionID)
		}
		args = append(args, "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "-c", `model_provider="openai"`, "-c", `forced_login_method="chatgpt"`)
		if q.Model != "" {
			args = append(args, "--model", q.Model)
		}
		if q.Thinking != "" {
			args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(q.Thinking))
		}
		return append(args, "-")
	}
	args = []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--dangerously-skip-permissions", "--permission-prompts", "none", "--settings", `{"sandbox":{"enabled":false}}`}
	if q.SessionID != "" {
		args = append(args, "--resume", q.SessionID)
	}
	if q.Model != "" {
		args = append(args, "--model", q.Model)
	}
	if q.Thinking != "" {
		args = append(args, "--effort", q.Thinking)
	}
	return args
}

func (r Runner) Run(ctx context.Context, q Request, emit func(Event) error) (Result, error) {
	if err := ValidateThinking(q.Endpoint.Kind, q.Thinking); err != nil {
		return Result{}, err
	}
	check, err := r.Check(ctx, q.Endpoint, q.Directory)
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p := parser{kind: q.Endpoint.Kind, emit: emit, tools: map[string]bool{}, texts: map[string]string{}}
	var pending []byte
	var parseErr error
	read := func(data []byte) {
		if parseErr != nil {
			return
		}
		pending = append(pending, data...)
		for {
			i := bytes.IndexByte(pending, '\n')
			if i < 0 {
				break
			}
			line := bytes.TrimSpace(pending[:i])
			pending = pending[i+1:]
			if len(line) == 0 {
				continue
			}
			if len(line) > 8<<20 {
				parseErr = errors.New("CLI event exceeds 8 MiB")
			} else {
				parseErr = p.line(line)
			}
			if parseErr != nil {
				cancel()
				return
			}
		}
		if len(pending) > 8<<20 {
			parseErr = errors.New("CLI event exceeds 8 MiB")
			cancel()
		}
	}
	out, runErr := r.Process.RunHost(ctx, broker.ProcessRequest{Executable: check.Executable, Args: Arguments(q), Directory: q.Directory, Input: q.Prompt, SeparateOutput: true, OutputLimit: 64 << 10, Stdout: read}, nil)
	if parseErr == nil && len(bytes.TrimSpace(pending)) > 0 {
		parseErr = p.line(pending)
	}
	if parseErr != nil {
		return p.result, fmt.Errorf("external agent stream: %w", parseErr)
	}
	if runErr != nil {
		return p.result, runErr
	}
	if ctx.Err() != nil {
		return p.result, ctx.Err()
	}
	if p.failure != "" {
		return p.result, nativeFailure(p.failure)
	}
	if out.ExitCode != 0 {
		detail := strings.TrimSpace(out.Stderr)
		if p.notice != "" {
			detail = strings.TrimSpace(p.notice + "\n" + detail)
		}
		return p.result, nativeFailure(fmt.Sprintf("external agent exited with code %d: %s", out.ExitCode, detail))
	}
	if !p.completed || p.result.SessionID == "" {
		if p.notice != "" {
			return p.result, nativeFailure("external agent ended without a successful result/session ID: " + p.notice)
		}
		return p.result, errors.New("external agent ended without a successful result/session ID; check CLI version and login")
	}
	return p.result, nil
}

func nativeFailure(message string) error {
	// A connection failure, usage limit, or policy denial does not mean that a
	// saved conversation is missing. Only give reset advice for that diagnosis.
	lower := strings.ToLower(message)
	for _, phrase := range []string{"session not found", "thread not found", "conversation not found", "no session found", "no saved session found", "no thread found", "no conversation found"} {
		if strings.Contains(lower, phrase) {
			return errors.New(message + "; use Reset external sessions to start a new native conversation")
		}
	}
	return errors.New(message)
}

type parser struct {
	kind      string
	emit      func(Event) error
	result    Result
	completed bool
	failure   string
	notice    string // Latest non-terminal diagnostic, retained if the process ends without success.
	tools     map[string]bool
	texts     map[string]string
	messageID string
}
type object map[string]json.RawMessage

func (o object) str(k string) string { var s string; _ = json.Unmarshal(o[k], &s); return s }
func (o object) obj(k string) object { var v object; _ = json.Unmarshal(o[k], &v); return v }
func (o object) num(k string) int    { var n int; _ = json.Unmarshal(o[k], &n); return n }
func (p *parser) event(e Event) error {
	if e.ID == "" {
		e.ID = domain.ID()
	}
	if e.Tool && !p.tools[e.ID] {
		p.tools[e.ID] = true
		p.result.ToolCalls++
	}
	if len(e.Content) > 256<<10 {
		e.Content = e.Content[:256<<10] + "\n[Activity display truncated]"
	}
	if p.emit != nil {
		return p.emit(e)
	}
	return nil
}
func usage(o object, claude bool) *domain.TokenUsage {
	if o == nil {
		return nil
	}
	u := &domain.TokenUsage{InputTokens: o.num("input_tokens"), OutputTokens: o.num("output_tokens")}
	_, in := o["input_tokens"]
	_, out := o["output_tokens"]
	u.InputMissing, u.OutputMissing = !in, !out
	key := "cached_input_tokens"
	if claude {
		key = "cache_read_input_tokens"
		u.InputTokens += o.num(key) + o.num("cache_creation_input_tokens")
	}
	if _, ok := o[key]; ok {
		v := o.num(key)
		u.CachedInputTokens = &v
	}
	u.TotalTokens = u.InputTokens + u.OutputTokens
	return u
}

func (p *parser) line(line []byte) error {
	var o object
	if err := json.Unmarshal(line, &o); err != nil {
		return fmt.Errorf("invalid JSON from CLI: %w", err)
	}
	if o.str("type") == "" {
		return errors.New("CLI event has no type")
	}
	if p.kind == "codex-cli" {
		return p.codex(o)
	}
	return p.claude(o)
}
func (p *parser) codex(o object) error {
	switch o.str("type") {
	case "thread.started":
		p.result.SessionID = o.str("thread_id")
	case "turn.completed":
		p.completed = true
		p.result.Usage = usage(o.obj("usage"), false)
	case "turn.failed":
		p.failure = codexErrorMessage(o)
	case "error":
		// Codex also emits error events while reconnecting. A later successful
		// turn and zero exit status are authoritative; these notices must not
		// poison the outcome or discard a successfully recovered native session.
		p.notice = codexErrorMessage(o)
		return p.event(Event{Name: "Codex notice", Content: p.notice, Status: "completed"})
	case "item.started", "item.updated", "item.completed":
		i := o.obj("item")
		kind, id := i.str("type"), i.str("id")
		status := "running"
		if o.str("type") == "item.completed" {
			status = "completed"
		}
		if i.str("status") == "failed" {
			status = "failed"
		}
		if kind == "agent_message" {
			text := i.str("text")
			p.result.Content = text
			return p.event(Event{ID: id, Name: "Response", Content: text, Status: status})
		}
		return p.event(Event{ID: id, Name: kind, Content: string(o["item"]), Status: status, Tool: kind == "command_execution" || kind == "file_change" || kind == "mcp_tool_call" || kind == "web_search"})
	}
	return nil
}

func codexErrorMessage(o object) string {
	if message := o.obj("error").str("message"); message != "" {
		return message
	}
	if message := o.str("message"); message != "" {
		return message
	}
	if message := o.str("error"); message != "" {
		return message
	}
	if message := string(o["error"]); message != "" && message != "null" {
		return message
	}
	return "Codex reported an error"
}

func (p *parser) claude(o object) error {
	if id := o.str("session_id"); id != "" && o.str("parent_tool_use_id") == "" {
		p.result.SessionID = id
	}
	switch o.str("type") {
	case "stream_event":
		if o.str("parent_tool_use_id") != "" {
			return nil
		}
		e := o.obj("event")
		switch e.str("type") {
		case "message_start":
			p.messageID = e.obj("message").str("id")
		case "content_block_delta":
			if p.messageID == "" {
				return nil
			}
			if d := e.obj("delta"); d.str("type") == "text_delta" && o.str("parent_tool_use_id") == "" {
				id := p.messageID
				p.texts[id] += d.str("text")
				if len(p.texts[id]) > 8<<20 {
					return errors.New("CLI response exceeds 8 MiB")
				}
				return p.event(Event{ID: id, Name: "Response", Content: p.texts[id], Status: "running"})
			}
		}
	case "assistant", "user":
		msg := o.obj("message")
		var blocks []object
		_ = json.Unmarshal(msg["content"], &blocks)
		var text []string
		for _, b := range blocks {
			switch b.str("type") {
			case "text":
				text = append(text, b.str("text"))
			case "tool_use":
				if err := p.event(Event{ID: b.str("id"), Name: b.str("name"), Content: string(b["input"]), Status: "running", Tool: true}); err != nil {
					return err
				}
			case "tool_result":
				status := "completed"
				if string(b["is_error"]) == "true" {
					status = "failed"
				}
				if err := p.event(Event{ID: b.str("tool_use_id"), Content: string(b["content"]), Status: status}); err != nil {
					return err
				}
			}
		}
		if len(text) > 0 && o.str("type") == "assistant" {
			delete(p.texts, msg.str("id"))
			content := strings.Join(text, "\n\n")
			if o.str("parent_tool_use_id") == "" {
				p.result.Content = content
			}
			return p.event(Event{ID: msg.str("id"), Name: "Response", Content: content, Status: "completed"})
		}
	case "result":
		p.result.Usage = usage(o.obj("usage"), true)
		if string(o["is_error"]) == "true" || o.str("subtype") != "success" {
			p.failure = o.str("result")
			if p.failure == "" {
				p.failure = string(o["errors"])
			}
			if p.failure == "" {
				p.failure = "Claude Code run failed: " + o.str("subtype")
			}
		} else {
			p.completed = true
			if text := o.str("result"); text != "" {
				p.result.Content = text
			}
		}
	case "system":
		if o.str("subtype") == "init" {
			return nil
		}
		return p.event(Event{Name: o.str("subtype"), Content: string(mustJSON(o)), Status: "completed"})
	}
	return nil
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
