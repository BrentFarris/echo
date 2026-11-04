package gitservice

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/brent/echo/internal/scmexec"
)

func TestRepositoryDiscoveryAllowsSlowSetupAndCancellation(t *testing.T) {
	directory := t.TempDir()
	for _, probe := range []struct {
		name string
		run  func(context.Context, string) (string, error)
	}{{"worktree", discoverRepositoryRoot}, {"gitdir", discoverGitDir}, {"common", discoverCommonGitDir}} {
		t.Run(probe.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				runner := &scmexec.Runtime{
					Execute: func(ctx context.Context, _ string, _ scmexec.Request) (scmexec.Result, error) {
						select {
						case <-time.After(45 * time.Second):
							return scmexec.Result{Stdout: []byte(directory)}, nil
						case <-ctx.Done():
							return scmexec.Result{}, ctx.Err()
						}
					},
					ValidatePath: func(_, path string) (string, error) { return path, nil },
				}
				ctx := context.WithValue(context.Background(), gitExecutionContextKey{}, gitExecutionContext{manager: runner, stop: context.Background()})
				if result, err := probe.run(ctx, directory); err != nil || pathIdentity(result) != pathIdentity(directory) {
					t.Fatalf("slow sandbox preparation: %q %v", result, err)
				}
				canceled, cancel := context.WithCancel(ctx)
				time.AfterFunc(time.Second, cancel)
				if _, err := probe.run(canceled, directory); !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled discovery: %v", err)
				}
			})
		})
	}
}
