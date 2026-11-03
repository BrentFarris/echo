package development

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type upstream struct{ Name, Remote, Ref string }

type Comparison struct {
	State        string `json:"state"`
	Branch       string `json:"branch"`
	Upstream     string `json:"upstream"`
	Ahead        int    `json:"ahead"`
	Behind       int    `json:"behind"`
	LocalCommit  string `json:"localCommit"`
	RemoteCommit string `json:"remoteCommit"`
	remote, ref  string
}

func (m *Manager) git(ctx context.Context, dir string, args ...string) (string, error) {
	var out bytes.Buffer
	err := m.run(ctx, dir, &out, "git", args...)
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(out.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func (m *Manager) validate(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("enter an absolute Echo Git repository path on the server")
	}
	module, err := os.ReadFile(filepath.Join(path, "go.mod"))
	if err != nil {
		return fmt.Errorf("read Echo go.mod: %w", err)
	}
	matched := false
	for _, line := range strings.Split(string(module), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" && strings.Trim(fields[1], "\"") == "github.com/brent/echo" {
			matched = true
		}
	}
	if !matched {
		return errors.New("repository does not declare the Echo module")
	}
	buildScript:="build.sh";if runtime.GOOS=="windows"{buildScript="build.ps1"}
	for _, file := range []string{"main.go", buildScript, "web/package.json", "web/package-lock.json", "internal/plugins/guest/main.go", "internal/store/store.go"} {
		info, err := os.Stat(filepath.Join(path, filepath.FromSlash(file)))
		if err != nil || info.IsDir() {
			return fmt.Errorf("not a new Echo checkout: missing %s", file)
		}
	}
	root, err := m.git(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("not a Git checkout: %w", err)
	}
	actual, err := os.Stat(root)
	if err != nil {
		return err
	}
	selected, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(actual, selected) {
		return errors.New("select the Git checkout root, not a subdirectory")
	}
	return nil
}

func (m *Manager) tracking(ctx context.Context, dir string) (string, upstream, error) {
	branch, err := m.git(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", upstream{}, errors.New("updates require a checked-out branch; HEAD is detached or unavailable")
	}
	value, err := m.git(ctx, dir, "for-each-ref", "--format=%(upstream:short)%09%(upstream:remotename)%09%(upstream:remoteref)", "refs/heads/"+branch)
	if err != nil {
		return branch, upstream{}, err
	}
	fields := strings.Split(value, "\t")
	if len(fields) != 3 || fields[0] == "" || fields[1] == "" || fields[2] == "" {
		return branch, upstream{}, fmt.Errorf("branch %s has no upstream; configure its tracking branch in Git before checking for updates", branch)
	}
	return branch, upstream{Name: fields[0], Remote: fields[1], Ref: fields[2]}, nil
}

func (m *Manager) compare(ctx context.Context, dir string, output io.Writer) (Comparison, error) {
	branch, up, err := m.tracking(ctx, dir)
	if err != nil {
		return Comparison{}, err
	}
	local, err := m.git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return Comparison{}, err
	}
	// Fetch only the configured upstream. FETCH_HEAD is resolved immediately and
	// subsequent comparisons/merge use that immutable commit, not a moving ref.
	if err = m.run(ctx, dir, output, "git", "fetch", "--no-tags", "--", up.Remote, up.Ref); err != nil {
		return Comparison{}, fmt.Errorf("fetch upstream: %w", err)
	}
	remote, err := m.git(ctx, dir, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return Comparison{}, err
	}
	c := Comparison{Branch: branch, Upstream: up.Name, LocalCommit: local, RemoteCommit: remote, remote: up.Remote, ref: up.Ref}
	if err = m.unchanged(ctx, dir, c); err != nil {
		return Comparison{}, err
	}
	counts, err := m.git(ctx, dir, "rev-list", "--left-right", "--count", local+"..."+remote)
	if err != nil {
		return Comparison{}, err
	}
	fields := strings.Fields(counts)
	if len(fields) != 2 {
		return Comparison{}, errors.New("unexpected upstream comparison from Git")
	}
	c.Ahead, err = strconv.Atoi(fields[0])
	if err != nil {
		return Comparison{}, err
	}
	c.Behind, err = strconv.Atoi(fields[1])
	if err != nil {
		return Comparison{}, err
	}
	switch {
	case c.Ahead > 0 && c.Behind > 0:
		c.State = "diverged"
	case c.Ahead > 0:
		c.State = "ahead"
	case c.Behind > 0:
		c.State = "behind"
	default:
		c.State = "up-to-date"
	}
	fmt.Fprintf(output, "%s → %s: %s (%d ahead, %d behind)\n", branch, up.Name, c.State, c.Ahead, c.Behind)
	return c, nil
}

func (m *Manager) unchanged(ctx context.Context, dir string, c Comparison) error {
	branch, up, err := m.tracking(ctx, dir)
	if err != nil {
		return err
	}
	local, err := m.git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if branch != c.Branch || up.Name != c.Upstream || up.Remote != c.remote || up.Ref != c.ref || local != c.LocalCommit {
		return errors.New("the branch, upstream, or HEAD changed during the update; retry after Git activity finishes")
	}
	return nil
}

func (m *Manager) fastForward(ctx context.Context, dir string, c Comparison, output io.Writer) error {
	if err := m.unchanged(ctx, dir, c); err != nil {
		return err
	}
	if err := m.run(ctx, dir, output, "git", "-c", "merge.autostash=false", "merge", "--ff-only", "--no-edit", c.RemoteCommit); err != nil {
		return fmt.Errorf("fast-forward failed; resolve Git conflicts without discarding your edits: %w", err)
	}
	return nil
}
