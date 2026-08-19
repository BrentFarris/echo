// Package rebuild prepares a new Echo binary while the current server remains
// online, then starts a detached launcher that replaces and relaunches it.
package rebuild

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const echoModule = "github.com/brent/echo"

var ErrInProgress = errors.New("an Echo rebuild is already in progress")

// Request describes the current process and source tree that should replace it.
type Request struct {
	SourceDir  string
	DataDir    string
	ProcessID  int
	Arguments  []string
	WorkingDir string
}

// Result describes the prepared replacement process.
type Result struct {
	SourcePath string
	BinaryPath string
	LogPath    string
}

// BuildError identifies the stage that failed and where its output was logged.
type BuildError struct {
	Stage   string
	LogPath string
	Err     error
}

func (e *BuildError) Error() string {
	return fmt.Sprintf("%s failed: %v; see %s", e.Stage, e.Err, e.LogPath)
}

func (e *BuildError) Unwrap() error { return e.Err }

type commandRunner func(context.Context, string, io.Writer, string, ...string) error
type launcher func(launchSpec, string) error

// Coordinator serializes rebuilds and owns the command/launcher seams used by
// deterministic tests.
type Coordinator struct {
	mu      sync.Mutex
	running bool
	run     commandRunner
	launch  launcher
}

func NewCoordinator() *Coordinator {
	return &Coordinator{run: runCommand, launch: prepareAndLaunch}
}

// IsEchoSource reports whether dir has an exact Echo module declaration.
func IsEchoSource(dir string) bool {
	file, err := os.Open(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	defer file.Close()

	moduleMatches := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "module" {
			moduleMatches = strings.Trim(fields[1], "\"") == echoModule
			break
		}
	}
	if !moduleMatches {
		return false
	}
	packageInfo, err := os.Stat(filepath.Join(dir, "web", "package.json"))
	return err == nil && !packageInfo.IsDir()
}

// BuildAndPrepare builds the frontend and a staged server binary, then starts
// a detached launcher. The current process is not stopped by this method.
func (c *Coordinator) BuildAndPrepare(ctx context.Context, request Request) (Result, error) {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return Result{}, ErrInProgress
	}
	c.running = true
	c.mu.Unlock()
	prepared := false
	defer func() {
		if prepared {
			// A successful preparation is terminal for this process: keep the
			// coordinator locked until the host exits for the relaunch.
			return
		}
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
	}()

	sourceDir, err := filepath.Abs(request.SourceDir)
	if err != nil {
		return Result{}, fmt.Errorf("resolve Echo source path: %w", err)
	}
	if !IsEchoSource(sourceDir) {
		return Result{}, fmt.Errorf("source does not declare module %s", echoModule)
	}

	dataDir := request.DataDir
	if strings.TrimSpace(dataDir) == "" {
		dataDir = os.TempDir()
	}
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return Result{}, fmt.Errorf("resolve Echo data path: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("create Echo data directory: %w", err)
	}
	logPath := filepath.Join(dataDir, "rebuild-relaunch.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return Result{}, fmt.Errorf("open rebuild log: %w", err)
	}

	logLine(logFile, "=== Echo rebuild started ===")
	logLine(logFile, "Source: "+sourceDir)

	npmName := "npm"
	if runtime.GOOS == "windows" {
		npmName = "npm.cmd"
	}
	webDir := filepath.Join(sourceDir, "web")
	logLine(logFile, "Running npm run build...")
	if err := c.run(ctx, webDir, logFile, npmName, "run", "build"); err != nil {
		_ = logFile.Close()
		return Result{}, &BuildError{Stage: "frontend build", LogPath: logPath, Err: err}
	}

	binaryName := "echo"
	stagedName := "echo.rebuild"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
		stagedName += ".exe"
	}
	binaryPath := filepath.Join(sourceDir, binaryName)
	stagedPath := filepath.Join(sourceDir, stagedName)
	if err := os.Remove(stagedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = logFile.Close()
		return Result{}, &BuildError{Stage: "server build preparation", LogPath: logPath, Err: err}
	}

	logLine(logFile, "Running go build...")
	if err := c.run(ctx, sourceDir, logFile, "go", "build", "-o", stagedPath, "."); err != nil {
		_ = os.Remove(stagedPath)
		_ = logFile.Close()
		return Result{}, &BuildError{Stage: "server build", LogPath: logPath, Err: err}
	}

	workingDir := request.WorkingDir
	if strings.TrimSpace(workingDir) == "" {
		workingDir = sourceDir
	} else if absolute, resolveErr := filepath.Abs(workingDir); resolveErr == nil {
		workingDir = absolute
	}
	spec := launchSpec{
		ProcessID:   request.ProcessID,
		StagedPath:  stagedPath,
		BinaryPath:  binaryPath,
		Arguments:   sanitizeArguments(request.Arguments),
		WorkingDir:  workingDir,
		LogPath:     logPath,
		WaitSeconds: 15,
	}
	logLine(logFile, "Build succeeded. Preparing detached relaunch...")
	if err := logFile.Close(); err != nil {
		return Result{}, &BuildError{Stage: "rebuild logging", LogPath: logPath, Err: err}
	}
	if err := c.launch(spec, dataDir); err != nil {
		return Result{}, &BuildError{Stage: "relaunch preparation", LogPath: logPath, Err: err}
	}
	prepared = true

	return Result{SourcePath: sourceDir, BinaryPath: binaryPath, LogPath: logPath}, nil
}

func runCommand(ctx context.Context, dir string, output io.Writer, name string, arguments ...string) error {
	commandPath, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("find %s: %w", name, err)
	}
	command := exec.CommandContext(ctx, commandPath, arguments...)
	command.Dir = dir
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return fmt.Errorf("run %s: %w", name, err)
	}
	return nil
}

func sanitizeArguments(arguments []string) []string {
	cleaned := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		name := strings.TrimLeft(argument, "-")
		if separator := strings.IndexByte(name, '='); separator >= 0 {
			name = name[:separator]
		}
		if name == "reset-auth" {
			continue
		}
		cleaned = append(cleaned, argument)
	}
	return cleaned
}

func logLine(writer io.Writer, message string) {
	_, _ = fmt.Fprintf(writer, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), message)
}
