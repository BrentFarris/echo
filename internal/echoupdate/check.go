// Package echoupdate checks the official Echo repository for source updates.
package echoupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const (
	RepositoryURL = "https://github.com/BrentFarris/echo.git"
	MasterBranch  = "master"
	MasterRef     = "refs/heads/" + MasterBranch
)

var commitPattern = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)

// Status describes the last successful comparison between local and GitHub
// master. Any hash difference is intentionally treated as an available update.
type Status struct {
	UpdateAvailable    bool      `json:"updateAvailable"`
	LocalMasterCommit  string    `json:"localMasterCommit"`
	RemoteMasterCommit string    `json:"remoteMasterCommit"`
	CheckedAt          time.Time `json:"checkedAt"`
}

type outputRunner func(context.Context, string, string, ...string) (string, error)

// Checker owns the command seam used by deterministic tests.
type Checker struct {
	run outputRunner
	now func() time.Time
}

func NewChecker() *Checker {
	return &Checker{run: runOutput, now: time.Now}
}

// Check compares refs/heads/master in sourceDir with the official GitHub
// master ref without fetching or changing the local repository.
func (c *Checker) Check(ctx context.Context, sourceDir string) (Status, error) {
	localOutput, err := c.run(ctx, sourceDir, "git", "rev-parse", "--verify", MasterRef+"^{commit}")
	if err != nil {
		return Status{}, fmt.Errorf("read local master commit: %w", err)
	}
	localCommit, err := parseCommit(localOutput)
	if err != nil {
		return Status{}, fmt.Errorf("read local master commit: %w", err)
	}

	remoteOutput, err := c.run(ctx, sourceDir, "git", "ls-remote", "--exit-code", RepositoryURL, MasterRef)
	if err != nil {
		return Status{}, fmt.Errorf("read GitHub master commit: %w", err)
	}
	remoteCommit, err := parseRemoteCommit(remoteOutput)
	if err != nil {
		return Status{}, fmt.Errorf("read GitHub master commit: %w", err)
	}

	return Status{
		UpdateAvailable:    localCommit != remoteCommit,
		LocalMasterCommit:  localCommit,
		RemoteMasterCommit: remoteCommit,
		CheckedAt:          c.now().UTC(),
	}, nil
}

func parseCommit(output string) (string, error) {
	commit := strings.TrimSpace(output)
	if !commitPattern.MatchString(commit) {
		return "", fmt.Errorf("unexpected commit hash %q", commit)
	}
	return strings.ToLower(commit), nil
}

func parseRemoteCommit(output string) (string, error) {
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == MasterRef {
			return parseCommit(fields[0])
		}
	}
	return "", fmt.Errorf("remote did not return %s", MasterRef)
}

func runOutput(ctx context.Context, dir, name string, arguments ...string) (string, error) {
	commandPath, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("find %s: %w", name, err)
	}
	command := exec.CommandContext(ctx, commandPath, arguments...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message != "" {
			return "", fmt.Errorf("run %s: %w: %s", name, err, message)
		}
		return "", fmt.Errorf("run %s: %w", name, err)
	}
	return string(output), nil
}
