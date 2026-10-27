package server

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
)

type fileChangesKey struct{}
type fileChangeCollector struct {
	mu         sync.Mutex
	changes    []domain.FileChange
	incomplete bool
}

func (s *Server) mutateFile(ctx context.Context, bot domain.Bot, method string, args map[string]any) (any, error) {
	paths := []string{text(args, "path")}
	if method == "file.move" {
		paths = []string{text(args, "from"), text(args, "to")}
	}
	return s.trackFileChanges(ctx, bot, paths, func() (any, error) {
		switch method {
		case "file.write":
			return s.Files.Write(bot, text(args, "path"), text(args, "content"), text(args, "revision"))
		case "file.delete":
			return "deleted", s.Files.Delete(bot, text(args, "path"))
		case "file.move":
			return "moved", s.Files.Move(bot, text(args, "from"), text(args, "to"))
		default:
			return "created", s.Files.Mkdir(bot, text(args, "path"))
		}
	})
}

func (s *Server) trackFileChanges(ctx context.Context, bot domain.Bot, paths []string, operation func() (any, error)) (any, error) {
	collector, _ := ctx.Value(fileChangesKey{}).(*fileChangeCollector)
	if collector == nil {
		return operation()
	}
	before := s.Files.SnapshotFiles(ctx, bot, paths)
	value, err := operation()
	// A cancelled or failed native command can still have changed files.
	after := s.Files.SnapshotFiles(context.WithoutCancel(ctx), bot, paths)
	collector.mu.Lock()
	collector.changes = append(collector.changes, broker.DiffFiles(bot, before, after)...)
	collector.incomplete = collector.incomplete || !before.Complete || !after.Complete
	collector.mu.Unlock()
	return value, err
}

func (s *Server) runTrackedProcess(ctx context.Context, bot domain.Bot, request broker.ProcessRequest, emit func(string)) (broker.ProcessResult, error) {
	directory := request.Directory
	if directory == "" {
		if len(bot.Roots) > 0 {
			directory = bot.Roots[0]
		} else {
			directory, _ = os.UserHomeDir()
		}
	}
	var result broker.ProcessResult
	directory, _ = filepath.Abs(directory)
	paths := bot.Roots
	if len(paths) == 0 {
		paths = []string{directory}
	}
	_, err := s.trackFileChanges(ctx, bot, paths, func() (any, error) {
		var err error
		result, err = s.Process.Run(ctx, request, emit)
		return result, err
	})
	return result, err
}
