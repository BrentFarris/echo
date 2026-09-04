package fossil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brent/echo/internal/sandbox"
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
	buffer bytes.Buffer
	limit  int
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.buffer.Write(data)
	}
	return original, nil
}

func (b *cappedBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *cappedBuffer) String() string { return b.buffer.String() }

func (p *Provider) run(parent context.Context, workspaceID, root string, network bool, args ...string) ([]byte, error) {
	timeout := localCommandTimeout
	if network {
		timeout = networkCommandTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	environment := []string{
		"LC_ALL=C", "LANG=C", "NO_COLOR=1", "FOSSIL_PAGER=cat", "PAGER=cat", "GIT_PAGER=cat",
		"FOSSIL_EDITOR=true", "VISUAL=true", "EDITOR=true",
	}
	if p.sandbox != nil && p.sandbox.IsEnabled(workspaceID) {
		guestRoot, err := p.sandbox.HostToGuest(workspaceID, root)
		if err != nil {
			return nil, &sourcecontrol.Error{Code: "sandbox_path_mapping_failed", Message: "Fossil checkout could not be mapped into the sandbox", Cause: err}
		}
		result, executeErr := p.sandbox.Execute(ctx, workspaceID, sandbox.ExecRequest{
			Command: append([]string{"fossil"}, args...), WorkingDirectory: guestRoot,
			Environment: environment, OutputLimit: maximumCommandOutput,
		})
		if executeErr != nil {
			return nil, &sourcecontrol.Error{Code: sandbox.ErrorCode(executeErr), Message: executeErr.Error(), Cause: executeErr}
		}
		if result.ExitCode != 0 {
			message := commandMessage(result.Stderr, result.Stdout, result.ExitCode)
			code := classifyCommandError(message, true)
			if code == "fossil_checkout_unavailable_in_sandbox" {
				message = sandboxCheckoutDiagnostic
			}
			return result.Stdout, &sourcecontrol.Error{Code: code, Message: sanitizeOutput(message, root)}
		}
		return result.Stdout, nil
	}

	command := exec.CommandContext(ctx, "fossil", args...)
	command.Dir = root
	command.Env = append(os.Environ(), environment...)
	var stdout, stderr cappedBuffer
	stdout.limit = maximumCommandOutput
	stderr.limit = 4 << 20
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err != nil {
		message := strings.TrimSpace(strings.ToValidUTF8(stderr.String(), "\uFFFD"))
		if message == "" {
			message = strings.TrimSpace(strings.ToValidUTF8(stdout.String(), "\uFFFD"))
		}
		if message == "" {
			message = err.Error()
		}
		code := classifyCommandError(message, false)
		if ctx.Err() != nil {
			code, message = "fossil_timeout", "Fossil operation timed out or was cancelled"
		} else if errors.Is(err, exec.ErrNotFound) {
			code, message = "fossil_unavailable", "Fossil is not installed or is not available on PATH"
		}
		return stdout.Bytes(), &sourcecontrol.Error{Code: code, Message: sanitizeOutput(message, root), Cause: err}
	}
	return stdout.Bytes(), nil
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

const sandboxCheckoutDiagnostic = "Fossil cannot open this checkout's repository database inside the sandbox; disable the sandbox or reopen the checkout with its repository database inside a workspace folder"

func (p *Provider) checkoutInfo(ctx context.Context, workspaceID, root string) (checkoutInfo, error) {
	output, err := p.run(ctx, workspaceID, root, false, "info")
	if err != nil {
		return checkoutInfo{}, err
	}
	info := parseInfo(string(output))
	if info.LocalRoot == "" {
		return checkoutInfo{}, &sourcecontrol.Error{Code: "invalid_fossil_checkout", Message: "Fossil did not report a checkout root"}
	}
	if p.sandbox != nil && p.sandbox.IsEnabled(workspaceID) {
		hostRoot, mapErr := p.sandbox.GuestToHost(workspaceID, info.LocalRoot)
		if mapErr != nil {
			return checkoutInfo{}, &sourcecontrol.Error{Code: "fossil_checkout_unavailable_in_sandbox", Message: "Fossil checkout is outside the sandbox workspace mounts", Cause: mapErr}
		}
		info.LocalRoot = hostRoot
		if info.Repository != "" {
			hostRepository, repositoryErr := p.sandbox.GuestToHost(workspaceID, info.Repository)
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
