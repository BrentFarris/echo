package broker

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedCLIExecutionBoundary(t *testing.T) {
	ctx := RestrictedExecution(t.Context())
	dir := t.TempDir()
	b := &ProcessBroker{Dir: dir}
	pin := PinnedCLI{Broker: b, Executable: filepath.Join(dir, "agent.exe"), Directory: dir}
	for _, req := range []ProcessRequest{
		{Executable: filepath.Join(dir, "other.exe"), Directory: dir},
		{Executable: pin.Executable, Directory: filepath.Dir(dir)},
	} {
		if _, err := pin.RunHost(ctx, req, nil); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("wrong executable or directory reached host execution: %v", err)
		}
	}
	req := ProcessRequest{Executable: pin.Executable, Directory: dir}
	if _, err := b.RunHost(ctx, req, nil); err == nil || !strings.Contains(err.Error(), "Full swarm access") {
		t.Fatalf("ordinary host execution gained the grant: %v", err)
	}
	if _, err := b.Run(ctx, req, nil); err == nil || !strings.Contains(err.Error(), "Full swarm access") {
		t.Fatalf("unscoped execution gained the grant: %v", err)
	}
}
