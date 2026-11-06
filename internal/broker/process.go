package broker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type ProcessRequest struct {
	Stdout             func([]byte)   `json:"-"` // Host-only, untruncated stream callbacks.
	Stderr             func([]byte)   `json:"-"`
	duplex             *processDuplex // Host-internal persistent stdio; never accepted from plugin input.
	SandboxSession     string         // Host-only opt-in: reuse an identical native sandbox's filesystem grants.
	Progress           func(string)   `json:"-"` // Host-only execution phase; never mixed with command output.
	OutputFileArgument string         // Host-only marker replaced by a file inside command scratch.
	BotID              string
	Roots              []string
	ReadPaths          []string // Host-only, nonrecursive read/execute access to files or directory entries.
	Command            string
	Executable         string
	Args               []string
	Input              string
	Environment        map[string]string // Host-owned; never forwarded from plugin input.
	PathPrepend        []string          // Host-owned executable search directories; remapped with runtime aliases.
	Internet           bool
	PrivateNetwork     bool
	Directory          string
	Toolchains         []string
	TimeoutSeconds     int  // Optional execution limit; zero runs until exit or cancellation.
	SeparateOutput     bool // Preserve byte-exact stdout independently of diagnostics.
	OutputLimit        int  // Host-selected bound; defaults to 1 MiB, maximum 64 MiB.
}
type ProcessResult struct {
	Output    string `json:"output"`
	ExitCode  uint32 `json:"exitCode"`
	Truncated bool   `json:"truncated"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
}

func (r ProcessRequest) ReportProgress(phase string) {
	if r.Progress != nil {
		r.Progress(phase)
	}
}

type ProcessBroker struct {
	Dir        string
	mu         sync.Mutex
	sessionsMu sync.Mutex
	sessions   map[string]*processSession
}

type processSession struct {
	botID   string
	name    string
	ctx     context.Context
	cancel  context.CancelFunc
	gate    chan struct{}
	journal aclJournal
	aliases map[string]string
}
type aclJournal struct {
	Name   string            `json:"name"`
	SID    string            `json:"sid"`
	Paths  []string          `json:"paths"`
	Drives map[string]string `json:"drives,omitempty"`
}

func (b *ProcessBroker) journal(j aclJournal) error {
	if err := os.MkdirAll(b.Dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	path := filepath.Join(b.Dir, j.Name+".json")
	file, err := os.OpenFile(path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	file.Close()
	if err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}
func (b *ProcessBroker) Run(ctx context.Context, r ProcessRequest, emit func(string)) (ProcessResult, error) {
	if IsRestricted(ctx) && (len(r.Roots) == 0 || r.PrivateNetwork) {
		return ProcessResult{}, errors.New("owner-level execution requires Full swarm access; use bound folders without private-network execution")
	}
	return b.runRequest(ctx, r, emit, false)
}

// RunHost runs a native executable as the Echo account. Only a host-authorized
// caller may select this entry point; tool arguments cannot select it.
func (b *ProcessBroker) RunHost(ctx context.Context, r ProcessRequest, emit func(string)) (ProcessResult, error) {
	if IsRestricted(ctx) {
		return ProcessResult{}, errors.New("owner-level execution requires Full swarm access")
	}
	return b.runHost(ctx, r, emit)
}

// PinnedCLI is given only to the external-agent runner for an owner-authorized
// bot turn. All three native invocations use the same executable and directory.
type PinnedCLI struct {
	Broker                *ProcessBroker
	Executable, Directory string
}

func (p PinnedCLI) RunHost(ctx context.Context, r ProcessRequest, emit func(string)) (ProcessResult, error) {
	if p.Executable == "" || r.Executable != p.Executable || r.Directory != p.Directory {
		return ProcessResult{}, errors.New("process does not match the authorized external CLI")
	}
	return p.Broker.runHost(ctx, r, emit)
}

func (b *ProcessBroker) runHost(ctx context.Context, r ProcessRequest, emit func(string)) (ProcessResult, error) {
	if r.Executable == "" || r.Command != "" || r.duplex != nil || r.SandboxSession != "" || len(r.Roots)+len(r.Toolchains)+len(r.ReadPaths) != 0 || r.OutputFileArgument != "" {
		return ProcessResult{}, errors.New("host execution requires a direct executable without a sandbox scope")
	}
	return b.runRequest(ctx, r, emit, true)
}

func (b *ProcessBroker) runRequest(ctx context.Context, r ProcessRequest, emit func(string), host bool) (ProcessResult, error) {
	if e := ctx.Err(); e != nil {
		return ProcessResult{}, e
	}
	if r.TimeoutSeconds < 0 || int64(r.TimeoutSeconds) > int64((1<<63-1)/time.Second) {
		return ProcessResult{}, errors.New("timeoutSeconds must be nonnegative and fit in a duration")
	}
	if r.duplex == nil && r.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(r.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	if r.OutputFileArgument != "" && (!r.SeparateOutput || r.Executable == "") {
		return ProcessResult{}, errors.New("file capture requires a native structured command")
	}
	if r.SandboxSession != "" && (r.Command != "" || r.Executable == "") {
		return ProcessResult{}, errors.New("sandbox sessions require host-owned structured native commands")
	}
	if len(r.ReadPaths) > 0 && (len(r.Roots) == 0 || r.Executable == "") {
		return ProcessResult{}, errors.New("additional read paths require a scoped native command")
	}
	r.ReadPaths = append([]string(nil), r.ReadPaths...)
	for i, path := range r.ReadPaths {
		absolute, err := filepath.Abs(path)
		real := absolute
		if err == nil {
			real, err = filepath.EvalSymlinks(real)
		}
		if err != nil {
			return ProcessResult{}, err
		}
		if real != absolute && !(runtime.GOOS == "windows" && strings.EqualFold(real, absolute)) {
			return ProcessResult{}, errors.New("additional read paths must not traverse links")
		}
		info, err := os.Stat(real)
		if err != nil || !info.IsDir() && !info.Mode().IsRegular() {
			return ProcessResult{}, errors.New("additional read paths must be existing files or directories")
		}
		r.ReadPaths[i] = real
	}
	if (r.Command == "") == (r.Executable == "") {
		return ProcessResult{}, errors.New("provide exactly one shell command or native executable")
	}
	inputLimit := 1 << 20
	if r.SeparateOutput {
		inputLimit = 64 << 20
	}
	if len(r.Input) > inputLimit {
		return ProcessResult{}, errors.New("standard input exceeds the command input limit")
	}
	if r.Executable != "" && (!filepath.IsAbs(r.Executable) || runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(r.Executable), ".exe")) {
		return ProcessResult{}, errors.New("native executable must be an absolute executable path")
	}
	for _, value := range append([]string{r.Command, r.Executable, r.Directory}, r.Args...) {
		if strings.ContainsRune(value, 0) {
			return ProcessResult{}, errors.New("native arguments cannot contain NUL")
		}
	}
	for key, value := range r.Environment {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return ProcessResult{}, errors.New("invalid process environment")
		}
	}
	if r.Directory == "" {
		if len(r.Roots) > 0 {
			r.Directory = r.Roots[0]
		} else {
			r.Directory, _ = os.UserHomeDir()
		}
	}
	r.Directory, _ = filepath.Abs(r.Directory)
	if len(r.Roots) > 0 {
		valid := false
		real, err := filepath.EvalSymlinks(r.Directory)
		if err != nil {
			return ProcessResult{}, err
		}
		for _, root := range r.Roots {
			if Within(root, real) {
				valid = true
			}
		}
		for _, path := range r.ReadPaths {
			if path == real || runtime.GOOS == "windows" && strings.EqualFold(path, real) {
				valid = true
			}
		}
		if !valid {
			return ProcessResult{}, errors.New("working directory is outside the bot's folders")
		}
	}
	if host {
		return runHost(ctx, r, emit)
	}
	r.ReportProgress("preparing command access")
	return b.run(ctx, r, emit)
}

// Start with the account environment, including Git config, SSH agent and
// credential helpers. exec.Cmd applies platform-specific key precedence.
func hostCommand(ctx context.Context, r ProcessRequest) *exec.Cmd {
	cmd := exec.CommandContext(ctx, r.Executable, r.Args...)
	cmd.Dir = r.Directory
	cmd.Env = cmd.Environ()
	for key, value := range r.Environment {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	return cmd
}

func DefaultToolchains() []string {
	roots := []string{}
	for _, name := range []string{"go", "git", "node", "dotnet"} {
		if binary, e := NativeExecutable(name); e == nil {
			root := filepath.Dir(binary)
			if name == "go" || name == "git" {
				root = filepath.Dir(root)
			}
			if canonical, e := CanonicalRoots([]string{root}); e == nil {
				roots = append(roots, canonical...)
			}
		}
	}
	return roots
}

// Resolve Go wrappers (including Snap) to the actual installed toolchain. A
// release cross-compiled on another OS cannot use its compiled-in GOROOT.
func NativeExecutable(name string) (string, error) {
	binary, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	if name == "go" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		root, queryErr := exec.CommandContext(ctx, binary, "env", "GOROOT").Output()
		if queryErr != nil {
			return "", queryErr
		}
		suffix := ""
		if runtime.GOOS == "windows" {
			suffix = ".exe"
		}
		binary = filepath.Join(strings.TrimSpace(string(root)), "bin", "go"+suffix)
	}
	return filepath.EvalSymlinks(binary)
}
