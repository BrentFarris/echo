package debugger

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/sandbox"
)

// Delve compiles the debuggee for launch sessions into a binary named
// "__debug_bin" in the Go module directory. When that name is already taken,
// Delve appends a random suffix instead of replacing it, which is how folders
// end up with debris like "__debug_bin.exe329938022". Echo therefore sweeps
// leftovers when a Delve launch session starts and removes the freshly built
// binary once the session finishes.
const delveBinaryPrefix = "__debug_bin"

// delveBuildsBinary reports whether a Delve launch will compile the debuggee
// into __debug_bin* rather than executing a prebuilt binary or attaching to a
// running process. It must be evaluated after prepareLaunchArguments resolved
// the "auto" mode into a concrete one.
func delveBuildsBinary(profile debugconfig.AdapterProfile, request string, arguments map[string]any) bool {
	if !strings.EqualFold(profile.AdapterID, "go") || !strings.EqualFold(request, "launch") {
		return false
	}
	mode, _ := arguments["mode"].(string)
	mode = strings.ToLower(strings.TrimSpace(mode))
	// An unset mode is treated like "debug", matching
	// launchAdapterWorkingDirectory.
	return mode == "" || mode == "debug" || mode == "test"
}

// sweepDelveBinaries removes compiled debug binaries left by earlier sessions
// before Delve compiles a new one, so Delve reuses the plain name instead of
// stacking random suffixes. Removal failures are ignored: a concurrently
// running debug session still holds its binary locked on Windows, and a live
// Unix process keeps running from a deleted inode.
func (s *Service) sweepDelveBinaries(workspaceID, directory string, sandboxed bool) {
	if sandboxed {
		s.removeSandboxDelveBinaries(workspaceID, directory)
		return
	}
	removeHostDelveBinaries(directory)
}

// removeDelveBinariesAfterSession deletes the compiled debug binary once a
// session has finished. The adapter and debuggee are terminated as a process
// tree before this runs, but Windows can take a moment to release the file
// lock, so removal is retried briefly. It never blocks session teardown.
func (s *Service) removeDelveBinariesAfterSession(workspaceID, sessionID, directory string, sandboxed bool) {
	for attempt := 0; attempt < 12; attempt++ {
		if attempt > 0 {
			time.Sleep(250 * time.Millisecond)
		}
		// A replacement session may already have rebuilt the binary in this
		// directory while removal was still retrying; it owns the name now and
		// will clean it up when it finishes.
		if s.delveBinaryOwnedByLiveSession(workspaceID, sessionID, directory) {
			return
		}
		var remaining bool
		if sandboxed {
			remaining = !s.removeSandboxDelveBinaries(workspaceID, directory)
		} else {
			remaining = !removeHostDelveBinaries(directory)
		}
		if !remaining {
			return
		}
	}
}

func (s *Service) delveBinaryOwnedByLiveSession(workspaceID, sessionID, directory string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	runtime := s.runtimes[workspaceID]
	if runtime == nil {
		return false
	}
	for _, active := range runtime.sessions {
		if active.id == sessionID {
			continue
		}
		if active.debugBinDir == directory && !isTerminalStatus(active.status) {
			return true
		}
	}
	return false
}

func removeHostDelveBinaries(directory string) bool {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return true
	}
	remaining := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), delveBinaryPrefix) {
			continue
		}
		if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
			remaining = true
		}
	}
	return !remaining
}

func (s *Service) removeSandboxDelveBinaries(workspaceID, directory string) bool {
	manager := s.sandboxManager()
	if manager == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := manager.Execute(ctx, workspaceID, sandbox.ExecRequest{
		Role:             "runtime",
		Command:          []string{"/bin/sh", "-c", "rm -f -- __debug_bin*"},
		WorkingDirectory: directory,
	})
	return err == nil && result.ExitCode == 0
}
