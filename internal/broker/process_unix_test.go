//go:build linux || darwin

package broker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func nativeUnix(t *testing.T) (*ProcessBroker, string) {
	t.Helper()
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1 for native containment acceptance")
	}
	root := t.TempDir()
	private := t.TempDir()
	if err := os.WriteFile(filepath.Join(private, "private.txt"), []byte("private credential"), 0600); err != nil {
		t.Fatal(err)
	}
	return &ProcessBroker{Dir: filepath.Join(private, "native")}, root
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
func TestNativeUnixWorkspaceAndPrivateProfile(t *testing.T) {
	b, root := nativeUnix(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	os.WriteFile(outside, []byte("outside credential"), 0600)
	private := filepath.Join(filepath.Dir(b.Dir), "private.txt")
	command := "printf scoped > inside.txt; test -r " + shellQuote(outside) + " && exit 11; test -r " + shellQuote(private) + " && exit 12; printf contained"
	result, err := b.Run(context.Background(), ProcessRequest{Roots: []string{root}, Command: command}, nil)
	if err != nil || result.ExitCode != 0 || !strings.Contains(result.Output, "contained") {
		t.Fatalf("containment: %+v %v", result, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "inside.txt")); err != nil || string(data) != "scoped" {
		t.Fatalf("workspace write %q %v", data, err)
	}
	result, err = b.Run(context.Background(), ProcessRequest{Roots: []string{root}, Executable: "/bin/cat", Input: "direct stdin", Args: nil}, nil)
	if err != nil || result.ExitCode != 0 || result.Output != "direct stdin" {
		t.Fatalf("direct execution: %+v %v", result, err)
	}
}
func TestNativeUnixFilteredNetworkAndCancellation(t *testing.T) {
	b, root := nativeUnix(t)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("reachable")) }))
	defer host.Close()
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Fatal("native acceptance requires curl")
	}
	for _, allow := range []bool{false, true} {
		result, err := b.Run(context.Background(), ProcessRequest{Roots: []string{root}, PrivateNetwork: allow, Executable: curl, Args: []string{"-fsS", "--max-time", "4", host.URL}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if allow && (result.ExitCode != 0 || result.Output != "reachable") || !allow && result.ExitCode == 0 {
			t.Fatalf("private network=%v: %+v", allow, result)
		}
	}
	result, err := b.Run(context.Background(), ProcessRequest{Roots: []string{root}, PrivateNetwork: true, Executable: curl, Args: []string{"-fsS", "--max-time", "2", "--noproxy", "*", host.URL}}, nil)
	if err != nil || result.ExitCode == 0 {
		t.Fatalf("direct network bypass: %+v %v", result, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	result, err = b.Run(ctx, ProcessRequest{Roots: []string{root}, Command: "(sleep 2; printf escaped > late.txt) & wait"}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel: %+v %v", result, err)
	}
	time.Sleep(2200 * time.Millisecond)
	if _, err = os.Stat(filepath.Join(root, "late.txt")); !os.IsNotExist(err) {
		t.Fatal("descendant survived cancellation", err)
	}
}
func TestNativeUnixWorkspaceBuildAndGit(t *testing.T) {
	b, root := nativeUnix(t)
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() { println(\"built inside workspace\") }\n"), 0600)
	tools := DefaultToolchains()
	result, err := b.Run(context.Background(), ProcessRequest{Roots: []string{root}, Toolchains: tools, Command: "go build -o app main.go && ./app && git init . && git add main.go && git -c user.name=Echo -c user.email=test@example.invalid commit -m initial && git status --short", TimeoutSeconds: 120}, nil)
	if err != nil || result.ExitCode != 0 || !strings.Contains(result.Output, "built inside workspace") {
		t.Fatalf("workspace build: %+v %v (toolchains %v)", result, err, tools)
	}
}
