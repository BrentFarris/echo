package broker

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWorkspaceGoCaches(t *testing.T) {
	root := t.TempDir()
	module := filepath.Join(root, "project")
	subdirectory := filepath.Join(module, "web")
	for _, name := range []string{"web", ".cache/go-mod", ".cache/go-build"} {
		if err := os.MkdirAll(filepath.Join(module, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte("module fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r := ProcessRequest{Roots: []string{root}, Directory: subdirectory}
	for key, name := range map[string]string{"GOMODCACHE": "go-mod", "GOCACHE": "go-build"} {
		want, err := filepath.EvalSymlinks(filepath.Join(module, ".cache", name))
		if err != nil || workspaceGoCaches(r)[key] != want {
			t.Fatalf("%s = %q, want %q: %v", key, workspaceGoCaches(r)[key], want, err)
		}
	}
	// A bot bound only to web cannot read its parent module's dependencies.
	r.Roots = []string{subdirectory}
	if env := workspaceGoCaches(r); len(env) != 0 {
		t.Fatal("selected cache outside the bot's roots", env)
	}
	r.Roots = nil
	if env := workspaceGoCaches(r); len(env) != 0 {
		t.Fatal("unbound commands should retain their existing environment", env)
	}
}

func TestWorkspaceGoCachesRejectsEscapingLinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".cache"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, ".cache", "go-mod")
	if runtime.GOOS == "windows" {
		if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
			t.Fatalf("create junction: %s: %v", output, err)
		}
	} else if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if env := workspaceGoCaches(ProcessRequest{Roots: []string{root}, Directory: root}); len(env) != 0 {
		t.Fatal("selected an out-of-scope cache", env)
	}
}

func TestWorkspaceGoCachesUsesNearestModule(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".cache", "go-mod"), 0700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{root, nested} {
		if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte("module fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if env := workspaceGoCaches(ProcessRequest{Roots: []string{root}, Directory: nested}); len(env) != 0 {
		t.Fatal("used a parent module's cache for an unprepared nested module", env)
	}
}
