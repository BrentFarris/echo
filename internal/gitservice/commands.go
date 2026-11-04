package gitservice

import "context"

// RunCommand coordinates the bot's native Git CLI with editor mutations.
// The host authorizes the checkout before calling this method; arguments,
// working directory and native Git behavior belong to the process runner.
func (s *Service) RunCommand(ctx context.Context, workspaceID, commonDir string, run func(context.Context) error) error {
	s.mu.Lock()
	lock := s.commandLock(commonDir)
	s.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	defer s.InvalidateWorkspace(workspaceID)
	return run(ctx)
}
