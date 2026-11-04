package gitservice

import "context"

// Editor operations and bot commands share this gate. A queued bot command
// must honor Stop and its timeout even while another operation holds the lock.
type repositoryLock struct{ gate chan struct{} }

func (l *repositoryLock) Lock()   { l.gate <- struct{}{} }
func (l *repositoryLock) Unlock() { <-l.gate }
func (l *repositoryLock) LockContext(ctx context.Context) error {
	select {
	case l.gate <- struct{}{}:
		// Cancellation and an available lock can become ready together.
		if err := ctx.Err(); err != nil {
			l.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RunCommand coordinates the bot's native Git CLI with editor mutations.
// The host authorizes the checkout before calling this method; arguments,
// working directory and native Git behavior belong to the process runner.
func (s *Service) RunCommand(ctx context.Context, workspaceID, commonDir string, run func(context.Context) error) error {
	s.mu.Lock()
	lock := s.commandLock(commonDir)
	s.mu.Unlock()
	if err := lock.LockContext(ctx); err != nil {
		return err
	}
	defer lock.Unlock()
	defer s.InvalidateWorkspace(workspaceID)
	return run(ctx)
}
