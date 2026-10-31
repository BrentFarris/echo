package gotool

import (
	"context"
	"debug/buildinfo"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
)

const GoplsVersion = "v0.24.0"

var ErrGoplsMissing = errors.New("Go language server installation required; use Install Go language server in the editor")

func GoplsPath(r broker.ProcessRequest) string {
	name := "gopls"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(r.Environment["GOBIN"], name)
}
func PrepareLanguage(ctx context.Context, files *broker.Files, bot domain.Bot, settings Settings, directory, configured string) (broker.ProcessRequest, error) {
	r, err := PrepareLanguageProject(ctx, files, bot, settings, directory)
	if err != nil {
		return r, err
	}
	binary := configured
	if binary == "" {
		binary = GoplsPath(r)
	}
	if !filepath.IsAbs(binary) {
		return r, errors.New("gopls executable must be absolute")
	}
	real, err := filepath.EvalSymlinks(binary)
	if os.IsNotExist(err) {
		return r, ErrGoplsMissing
	}
	if err != nil {
		return r, err
	}
	if configured == "" && real != binary {
		return r, errors.New("managed gopls must not be a symlink or junction")
	}
	if !files.Allowed(domain.Bot{}, real) {
		return r, errors.New("gopls cannot be inside Echo private state")
	}
	info, err := buildinfo.ReadFile(real)
	if err != nil || info.Path != "golang.org/x/tools/gopls" {
		return r, errors.New("selected executable is not a Go-built gopls binary")
	}
	if configured == "" && info.Main.Version != GoplsVersion {
		return r, errors.New("Go language server update required; use Update Go language server")
	}
	inScope := false
	for _, root := range r.Roots {
		if broker.Within(root, real) {
			inScope = true
		}
	}
	if !inScope {
		dir := filepath.Dir(real)
		if files.Private != "" && (broker.Within(dir, files.Private) || broker.Within(files.Private, dir)) {
			return r, errors.New("gopls runtime grant cannot include Echo private state")
		}
		r.Toolchains = append(r.Toolchains, dir)
	}
	r.Executable = real
	r.Args = []string{"serve"}
	return r, nil
}
