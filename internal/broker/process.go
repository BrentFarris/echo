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
	SandboxSession     string // Host-only opt-in: reuse an identical native sandbox's filesystem grants.
	OutputFileArgument string // Host-only marker replaced by a file inside command scratch.
	BotID              string
	Roots              []string
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
	TimeoutSeconds     int
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
type ProcessBroker struct {
	Dir        string
	mu         sync.Mutex
	sessionsMu sync.Mutex
	sessions   map[string]*processSession
}

type processSession struct {
	botID   string
	ctx     context.Context
	cancel  context.CancelFunc
	gate    chan struct{}
	journal aclJournal
	aliases map[string]string
}
type aclJournal struct {
	Name  string   `json:"name"`
	SID   string   `json:"sid"`
	Paths []string `json:"paths"`
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
	if e := ctx.Err(); e != nil {
		return ProcessResult{}, e
	}
	if r.OutputFileArgument != "" && (!r.SeparateOutput || r.Executable == "") {
		return ProcessResult{}, errors.New("file capture requires a native structured command")
	}
	if r.SandboxSession != "" && (r.Command != "" || r.Executable == "" || !r.SeparateOutput) {
		return ProcessResult{}, errors.New("sandbox sessions require host-owned structured native commands")
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
		if !valid {
			return ProcessResult{}, errors.New("working directory is outside the bot's folders")
		}
	}
	return b.run(ctx, r, emit)
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
