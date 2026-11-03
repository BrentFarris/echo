package broker

import (
	"context"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/mutation"
	"os"
	"path/filepath"
)

// CoordinateFile retains the broker's confined path checks before allowing a
// VCS checkout. The actual write repeats its normal os.Root validation.
func (f *Files) CoordinateFile(ctx context.Context, bot domain.Bot, kind, path, destination string, apply func() error) (mutation.Result, error) {
	resolve := func(path string) (string, error) {
		r, name, e := f.resolve(bot, path)
		if e != nil {
			return "", e
		}
		defer r.Close()
		return filepath.Join(r.Name(), name), nil
	}
	absolute, e := resolve(path)
	if e != nil {
		return mutation.Result{}, e
	}
	target := ""
	if destination != "" {
		target, e = resolve(destination)
		if e != nil {
			return mutation.Result{}, e
		}
	}
	if kind == "edit" {
		if _, e := os.Stat(absolute); os.IsNotExist(e) {
			kind = "create"
		}
	}
	op := mutation.Operation{WorkspaceID: bot.ID, Kind: kind, Path: absolute, Destination: target, Origin: "file-tool", Validate: func() error {
		_, e := resolve(path)
		if e == nil && destination != "" {
			_, e = resolve(destination)
		}
		return e
	}}
	if f.Coordinator == nil {
		return mutation.Apply(op, apply)
	}
	return f.Coordinator.Run(ctx, op, apply)
}
