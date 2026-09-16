package workspacefs

import (
	"context"
	"path"
	"path/filepath"
	"strings"

	"github.com/brent/echo/internal/mutation"
)

// SetMutationCoordinator is installed once during server construction.
func (s *Service) SetMutationCoordinator(coordinator mutation.Coordinator) {
	s.coordinator = coordinator
}
func (s *Service) LockMutationPaths(paths ...string) func() { return s.lockPaths(paths...) }
func (s *Service) coordinateContext(ctx context.Context, op mutation.Operation, apply func() error) (mutation.Result, error) {
	if err := ctx.Err(); err != nil {
		return mutation.Result{}, err
	}
	if s.coordinator == nil {
		return mutation.Apply(op, apply)
	}
	return s.coordinator.Run(ctx, op, apply)
}

// Run gives structured tools the same path-lock and checkout boundary as the
// editor. Tool permission checks and sandbox routing occur before this call.
func (s *Service) Run(ctx context.Context, op mutation.Operation, apply func() error) (mutation.Result, error) {
	unlock := s.lockPaths(op.Path, op.Destination)
	defer unlock()
	return s.coordinateContext(ctx, op, apply)
}
func (s *Service) Tracks(workspace, path string) bool {
	return s.coordinator != nil && s.coordinator.Tracks(workspace, path)
}

func (s *Service) ReferenceForHostPath(workspace, hostPath string) (FileRef, error) {
	roots, err := s.resolvedRoots(workspace)
	if err != nil {
		return FileRef{}, err
	}
	for _, root := range roots {
		rel, err := filepath.Rel(root.realPath, hostPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		ref := FileRef{RootID: root.ID, Path: filepath.ToSlash(rel)}
		if _, _, _, err := s.resolveEntry(workspace, ref, false, true); err != nil {
			return FileRef{}, err
		}
		return ref, nil
	}
	return FileRef{}, ErrOutsideRoot
}

// MoveTo preserves an explicit rename pair for structured tools.
func (s *Service) MoveTo(workspace string, source, destination FileRef) (Entry, error) {
	return s.MoveToContext(context.Background(), workspace, source, destination)
}

func (s *Service) MoveToContext(ctx context.Context, workspace string, source, destination FileRef) (Entry, error) {
	parent, name := path.Split(destination.Path)
	return s.moveEntry(ctx, workspace, source, FileRef{RootID: destination.RootID, Path: parent}, name)
}
