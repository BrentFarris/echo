//go:build linux

package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeFileInstallerReplacesReadOnlyCredential(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "agent.token")
	if err := os.WriteFile(destination, []byte("old"), 0o400); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/sh", "-c", runtimeFileInstallerScript, "echo-secret", destination, "400")
	command.Stdin = strings.NewReader("new")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("replace read-only credential: %v: %s", err, output)
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new" {
		t.Fatalf("credential = %q, want new", content)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("credential mode = %o, want 400", info.Mode().Perm())
	}
}
