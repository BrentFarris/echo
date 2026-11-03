package fossil

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/brent/echo/internal/scmexec"
	"github.com/brent/echo/internal/sourcecontrol"
)

const (
	localCommandTimeout   = 30 * time.Second
	networkCommandTimeout = 5 * time.Minute
	maximumCommandOutput  = 64 << 20
)

var credentialURLPattern = regexp.MustCompile(`(?i)((?:https?|ssh)://)([^/@\s]+)@`)
var credentialQueryPattern = regexp.MustCompile(`(?i)([?&](?:access_?token|auth|key|password|passwd|signature|token)=)[^&\s]+`)

type cappedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
	mu        sync.Mutex
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			b.truncated = true
			data = data[:remaining]
		}
		_, _ = b.buffer.Write(data)
	} else if len(data) > 0 {
		b.truncated = true
	}
	return original, nil
}

func (b *cappedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buffer.Bytes()...)
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func (b *cappedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

func fossilCommandEnvironment() []string {
	return []string{
		"LC_ALL=C", "LANG=C", "NO_COLOR=1", "FOSSIL_PAGER=cat", "PAGER=cat", "GIT_PAGER=cat",
		"FOSSIL_EDITOR=true", "VISUAL=true", "EDITOR=true",
	}
}

func (p *Provider) run(parent context.Context, workspaceID, root string, network bool, args ...string) ([]byte, error) {
	output, _, err := p.runLimited(parent, workspaceID, root, network, maximumCommandOutput, args...)
	return output, err
}

func (p *Provider) runLimited(parent context.Context, workspaceID, root string, network bool, outputLimit int, args ...string) ([]byte, bool, error) {
	if outputLimit <= 0 || outputLimit > maximumCommandOutput {
		outputLimit = maximumCommandOutput
	}
	timeout := localCommandTimeout
	if network {
		timeout = networkCommandTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	ctx, stop := p.executionContext(ctx, workspaceID)
	defer stop()
	result, err := p.runner.Execute(ctx, workspaceID, scmexec.Request{Command: append([]string{"fossil"}, args...), WorkingDirectory: root, Environment: fossilCommandEnvironment(), OutputLimit: outputLimit})
	if err != nil {
		return nil, false, err
	}
	if result.ExitCode != 0 {
		return result.Stdout, result.StdoutTruncated, &sourcecontrol.Error{Code: classifyCommandError(string(result.Stderr), false), Message: sanitizeOutput(commandMessage(result.Stderr, result.Stdout, result.ExitCode), root)}
	}
	return result.Stdout, result.StdoutTruncated, nil
}

func commandMessage(stderr, stdout []byte, exitCode int) string {
	message := strings.TrimSpace(strings.ToValidUTF8(string(stderr), "\uFFFD"))
	if message == "" {
		message = strings.TrimSpace(strings.ToValidUTF8(string(stdout), "\uFFFD"))
	}
	if message == "" {
		message = fmt.Sprintf("Fossil exited with code %d", exitCode)
	}
	return message
}

func classifyCommandError(message string, sandboxed bool) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "not found") && strings.Contains(lower, "fossil"):
		return "fossil_unavailable"
	case strings.Contains(lower, "not within an open checkout"), strings.Contains(lower, "not a valid checkout"), strings.Contains(lower, "cannot find repository"), strings.Contains(lower, "unable to open database"):
		if sandboxed {
			return "fossil_checkout_unavailable_in_sandbox"
		}
		return "invalid_fossil_checkout"
	case strings.Contains(lower, "login failed"), strings.Contains(lower, "authorization failed"), strings.Contains(lower, "password"):
		return "fossil_authentication_failed"
	default:
		return "fossil_command_failed"
	}
}

func sanitizeOutput(message, root string) string {
	message = strings.ToValidUTF8(message, "\uFFFD")
	message = strings.ReplaceAll(message, "\x00", "")
	message = credentialURLPattern.ReplaceAllString(message, "$1***@")
	message = credentialQueryPattern.ReplaceAllString(message, "$1***")
	if root != "" {
		for _, value := range []string{root, strings.ReplaceAll(root, "\\", "/")} {
			if value == "" {
				continue
			}
			if runtime.GOOS == "windows" {
				message = regexp.MustCompile(`(?i)`+regexp.QuoteMeta(value)).ReplaceAllString(message, "<checkout>")
			} else {
				message = strings.ReplaceAll(message, value, "<checkout>")
			}
		}
	}
	if !utf8.ValidString(message) {
		message = strings.ToValidUTF8(message, "\uFFFD")
	}
	const safeOutputLimit = 8 << 10
	if len(message) > safeOutputLimit {
		message = strings.ToValidUTF8(message[:safeOutputLimit], "\uFFFD") + "…"
	}
	return strings.TrimSpace(message)
}

type checkoutInfo struct {
	LocalRoot  string
	Repository string
	Checkout   string
	Branch     string
	Parent     string
}

const sandboxCheckoutDiagnostic = "Fossil cannot access this checkout's repository database; place it in the checkout's parent folder or add its folder to the bot's workspace"

func (p *Provider) checkoutInfo(ctx context.Context, workspaceID, root string) (checkoutInfo, error) {
	output, err := p.run(ctx, workspaceID, root, false, "info")
	if err != nil {
		return checkoutInfo{}, err
	}
	info := parseInfo(string(output))
	if info.LocalRoot == "" {
		return checkoutInfo{}, &sourcecontrol.Error{Code: "invalid_fossil_checkout", Message: "Fossil did not report a checkout root"}
	}
	if p.runner != nil {
		hostRoot, mapErr := p.runner.ValidatePath(workspaceID, info.LocalRoot)
		if mapErr != nil {
			return checkoutInfo{}, &sourcecontrol.Error{Code: "fossil_checkout_unavailable_in_sandbox", Message: "Fossil checkout is outside the sandbox workspace mounts", Cause: mapErr}
		}
		info.LocalRoot = hostRoot
		if info.Repository != "" {
			hostRepository, repositoryErr := p.runner.ValidatePath(workspaceID, info.Repository)
			if p.runner.ValidateFossilRepository != nil {
				hostRepository, repositoryErr = p.runner.ValidateFossilRepository(ctx, workspaceID, hostRoot, info.Repository)
			}
			if repositoryErr != nil {
				return checkoutInfo{}, &sourcecontrol.Error{Code: "fossil_checkout_unavailable_in_sandbox", Message: sandboxCheckoutDiagnostic, Cause: repositoryErr}
			}
			info.Repository = hostRepository
		}
	}
	canonicalRoot, err := canonicalExisting(strings.TrimRight(info.LocalRoot, "/\\"))
	if err != nil {
		return checkoutInfo{}, &sourcecontrol.Error{Code: "invalid_fossil_checkout", Message: "Fossil checkout root is unavailable", Cause: err}
	}
	info.LocalRoot = canonicalRoot
	if info.Repository != "" {
		if canonicalRepository, repositoryErr := canonicalExisting(info.Repository); repositoryErr == nil {
			info.Repository = canonicalRepository
		}
	}
	return info, nil
}

func parseInfo(output string) checkoutInfo {
	var info checkoutInfo
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "local-root":
			info.LocalRoot = value
		case "repository":
			info.Repository = value
		case "checkout":
			info.Checkout = firstField(value)
		case "parent":
			info.Parent = firstField(value)
		case "tags":
			if fields := strings.Fields(value); len(fields) > 0 {
				info.Branch = strings.Trim(fields[0], ",")
			}
		}
	}
	return info
}

func firstField(value string) string {
	if fields := strings.Fields(value); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

func (p *Provider) executionContext(parent context.Context, workspace string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	p.execMu.Lock()
	if p.closed {
		p.execMu.Unlock()
		cancel()
		return ctx, cancel
	}
	lifetime := p.execContexts[workspace]
	if lifetime == nil {
		var stop context.CancelFunc
		lifetime, stop = context.WithCancel(context.Background())
		p.execContexts[workspace] = lifetime
		p.execStops[workspace] = stop
	}
	p.execMu.Unlock()
	stop := context.AfterFunc(lifetime, cancel)
	return ctx, func() { stop(); cancel() }
}
