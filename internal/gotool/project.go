package gotool

import (
	"context"
	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
	"path/filepath"
)

// PrepareProject shares command containment, cache validation and environment
// construction with persistent tools. It never launches a discovery process.
func PrepareProject(ctx context.Context, files *broker.Files, bot domain.Bot, settings Settings, directory string) (broker.ProcessRequest, error) {
	r, err := Prepare(ctx, files, bot, settings, Command{Command: "version", Directory: directory})
	r.Args = nil
	return r, err
}

// A go.work session must share overlays across its modules. Resolve its cache
// and installation destination consistently whether opened from a module or
// from the workspace folder itself.
func PrepareLanguageProject(ctx context.Context, files *broker.Files, bot domain.Bot, settings Settings, directory string) (broker.ProcessRequest, error) {
	r, err := PrepareProject(ctx, files, bot, settings, directory)
	if err != nil {
		return r, err
	}
	if work := r.Environment["GOWORK"]; work != "" && work != "off" {
		return PrepareProject(ctx, files, bot, settings, filepath.Dir(work))
	}
	return r, nil
}
