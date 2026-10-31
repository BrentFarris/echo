// Package gotool prepares workspace-scoped Go commands for bot and editor clients.
// Execution and presentation belong to the caller and the native process broker.
package gotool

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
)

type Settings struct {
	Executable               string
	Toolchains               []string
	Internet, PrivateNetwork bool
	Proxy, Private           string
}

type Command struct {
	Command          string
	Args             []string
	Directory, Input string
	TimeoutSeconds   int
}

var subcommand = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func ValidateProxy(value string) error {
	for _, entry := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '|' }) {
		if entry == "direct" || entry == "off" {
			continue
		}
		u, err := url.Parse(entry)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "file") || (u.Scheme != "file" && u.Host == "") {
			return errors.New("Go proxy must contain unauthenticated HTTP(S)/file URLs, direct or off; credentials and query strings are unsupported")
		}
	}
	return nil
}

func Prepare(ctx context.Context, files *broker.Files, bot domain.Bot, settings Settings, command Command) (broker.ProcessRequest, error) {
	r := broker.ProcessRequest{BotID: bot.ID, Roots: bot.Roots, Input: command.Input, Internet: settings.Internet, PrivateNetwork: settings.PrivateNetwork, TimeoutSeconds: command.TimeoutSeconds}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	if !subcommand.MatchString(command.Command) || len(command.Args) > 512 {
		return r, errors.New("provide one Go subcommand and at most 512 literal arguments")
	}
	for _, arg := range command.Args {
		if strings.ContainsRune(arg, 0) {
			return r, errors.New("Go arguments cannot contain NUL")
		}
		if command.Command == "env" && (arg == "-w" || arg == "-u" || strings.HasPrefix(arg, "-w=") || strings.HasPrefix(arg, "-u=")) {
			return r, errors.New("go env -w/-u are unavailable; use Go plugin settings")
		}
	}
	if r.TimeoutSeconds == 0 {
		r.TimeoutSeconds = 300
	}
	if r.TimeoutSeconds < 1 || r.TimeoutSeconds > 300 {
		return r, errors.New("timeoutSeconds must be 1-300")
	}
	if err := ValidateProxy(settings.Proxy); err != nil {
		return r, err
	}
	directory := command.Directory
	if directory == "" {
		if len(bot.Roots) > 0 {
			directory = bot.Roots[0]
		} else {
			directory, _ = os.UserHomeDir()
		}
	}
	if !filepath.IsAbs(directory) && len(bot.Roots) > 0 {
		directory = filepath.Join(bot.Roots[0], directory)
	}
	directory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return r, err
	}
	if kind, err := files.ReferenceKind(bot, directory); err != nil || kind != "directory" {
		return r, errors.New("Go working directory must be an accessible folder inside the bot's scope")
	}
	r.Directory = directory
	roots, err := broker.CanonicalRoots(bot.Roots)
	if err != nil {
		return r, err
	}
	r.Roots = roots
	allowed := func(path string) bool {
		if !files.Allowed(bot, path) {
			return false
		}
		if len(roots) == 0 {
			return true
		}
		for _, root := range roots {
			if broker.Within(root, path) {
				return true
			}
		}
		return false
	}
	base, work := directory, "off"
	foundModule := false
	for dir := directory; allowed(dir); dir = filepath.Dir(dir) {
		if !foundModule && files.Allowed(bot, filepath.Join(dir, "go.mod")) {
			base, foundModule = dir, true
		}
		if work == "off" && files.Allowed(bot, filepath.Join(dir, "go.work")) {
			work = filepath.Join(dir, "go.work")
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	// All host-side preparation stays beneath an opened workspace root. Reject
	// cache links (including Windows junctions) rather than following them.
	root, err := os.OpenRoot(base)
	if err != nil {
		return r, err
	}
	defer root.Close()
	for _, name := range []string{".cache", ".cache/go-mod", ".cache/go-build", ".cache/go-bin", ".cache/go-path"} {
		path := filepath.Join(base, filepath.FromSlash(name))
		if files.Private != "" && (broker.Within(files.Private, path) || broker.Within(path, files.Private)) {
			return r, errors.New("Go cache cannot include Echo private data")
		}
		info, err := root.Lstat(name)
		if err != nil && !os.IsNotExist(err) {
			return r, err
		}
		if err == nil && (!info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0) {
			return r, fmt.Errorf("Go cache must be a real directory, not a link: %s", path)
		}
		if err = root.MkdirAll(name, 0700); err != nil {
			return r, err
		}
		if !allowed(path) {
			return r, errors.New("Go cache is outside the bot's folders")
		}
	}
	binary, goroot, err := discover(ctx, settings.Executable, files)
	if err != nil {
		return r, err
	}
	r.Executable = binary
	r.Args = append([]string{command.Command}, command.Args...)
	toolchains, err := broker.CanonicalRoots(append([]string{goroot}, settings.Toolchains...))
	if err != nil {
		return r, err
	}
	for _, toolchain := range toolchains {
		if files.Private != "" && (broker.Within(toolchain, files.Private) || broker.Within(files.Private, toolchain)) {
			return r, errors.New("Go runtime grants cannot include Echo private data")
		}
		inWorkspace := false
		for _, workspace := range roots {
			if broker.Within(workspace, toolchain) {
				inWorkspace = true
				break
			}
		}
		if !inWorkspace {
			r.Toolchains = append(r.Toolchains, toolchain)
		}
	}
	cache := filepath.Join(base, ".cache")
	r.Environment = map[string]string{
		"GOCACHE": filepath.Join(cache, "go-build"), "GOMODCACHE": filepath.Join(cache, "go-mod"),
		"GOBIN": filepath.Join(cache, "go-bin"), "GOPATH": filepath.Join(cache, "go-path"),
		"GOENV": "off", "GOWORK": work, "GOTOOLCHAIN": "auto",
		"GOPROXY": "https://proxy.golang.org,direct", "GOSUMDB": "sum.golang.org",
		"GOPRIVATE": settings.Private, "GIT_TERMINAL_PROMPT": "0",
		"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull, "GCM_INTERACTIVE": "never",
	}
	if settings.Proxy != "" {
		r.Environment["GOPROXY"] = settings.Proxy
	}
	r.PathPrepend = []string{filepath.Join(cache, "go-bin"), filepath.Join(goroot, "bin")}
	for _, path := range toolchains {
		r.PathPrepend = append(r.PathPrepend, path, filepath.Join(path, "bin"))
	}
	return r, nil
}

func discover(ctx context.Context, configured string, files *broker.Files) (string, string, error) {
	binary := configured
	var err error
	if binary == "" {
		binary, err = exec.LookPath("go")
	}
	if err != nil {
		return "", "", errors.New("Go is not installed; install Go or select its executable in Go settings")
	}
	if !filepath.IsAbs(binary) {
		return "", "", errors.New("Go executable must be an absolute path")
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return "", "", err
	}
	if !files.Allowed(domain.Bot{}, binary) {
		return "", "", errors.New("Go executable cannot be inside Echo private data")
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	// Do not execute a configured binary on the host to discover GOROOT: it
	// may be writable by a scoped bot. Standard installations (and symlinks
	// to them) have bin/go next to VERSION, src and pkg/tool. Wrappers must
	// be configured to point to the actual installation instead.
	goroot := filepath.Dir(filepath.Dir(binary))
	for _, relative := range []string{"VERSION", "src", "pkg/tool"} {
		path := filepath.Join(goroot, filepath.FromSlash(relative))
		if !files.Allowed(domain.Bot{}, path) {
			return "", "", errors.New("cannot locate the Go installation; set Go's executable to the installed bin/go (wrappers are unsupported)")
		}
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	if !strings.EqualFold(filepath.Base(binary), "go"+suffix) || !strings.EqualFold(filepath.Base(filepath.Dir(binary)), "bin") || !files.Allowed(domain.Bot{}, binary) {
		return "", "", errors.New("Go installation is inaccessible or inside Echo private data")
	}
	return binary, goroot, nil
}
