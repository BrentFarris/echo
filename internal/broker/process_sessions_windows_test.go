//go:build windows

package broker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestNativeSCMSandboxSessionScopeAndCleanup(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1")
	}
	git, err := NativeExecutable("git")
	if err != nil {
		t.Skip(err)
	}
	first, second := t.TempDir(), t.TempDir()
	for _, root := range []string{first, second} {
		if output, err := exec.Command(git, "init", root).CombinedOutput(); err != nil {
			t.Fatalf("fixture: %s %v", output, err)
		}
	}
	file := filepath.Join(first, "input.txt")
	if err := os.WriteFile(file, []byte("scoped data"), 0600); err != nil {
		t.Fatal(err)
	}
	b := &ProcessBroker{Dir: t.TempDir()}
	t.Cleanup(func() {
		if err := b.CloseSessions(""); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request := ProcessRequest{BotID: "fixture", SandboxSession: "scm:git", Roots: []string{first}, Directory: first, Executable: git, Toolchains: []string{filepath.Dir(filepath.Dir(git))}, Args: []string{"hash-object", file}, SeparateOutput: true}
	for range 2 {
		out, err := b.Run(ctx, request, nil)
		if err != nil || out.ExitCode != 0 || out.Stdout == "" {
			t.Fatalf("scoped read: %+v %v", out, err)
		}
	}
	if len(b.sessions) != 1 {
		t.Fatal("identical scope did not reuse its sandbox")
	}
	var original *processSession
	for _, session := range b.sessions {
		original = session
	}
	request.Roots, request.Directory = []string{second}, second
	out, err := b.Run(ctx, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.ExitCode == 0 {
		t.Fatal("changed scope retained access to the previous workspace")
	}
	if len(b.sessions) != 2 {
		t.Fatal("changed scope reused the previous sandbox identity")
	}
	// Closing a session cancels its current lease and waits for its job to end.
	original.gate <- struct{}{}
	closed := make(chan error, 1)
	go func() { closed <- b.CloseSessions("fixture") }()
	select {
	case <-original.ctx.Done():
	case <-ctx.Done():
		t.Fatal("scope close did not cancel the active command")
	}
	select {
	case <-closed:
		t.Fatal("scope close did not wait for its active command")
	default:
	}
	<-original.gate
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	journals, err := filepath.Glob(filepath.Join(b.Dir, "*.json"))
	if err != nil || len(journals) != 0 {
		t.Fatalf("cleanup journals remain: %v %v", journals, err)
	}
	for _, path := range []string{first, file} {
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		acl, _, err := sd.DACL()
		if err != nil {
			t.Fatal(err)
		}
		for i := uint32(0); acl != nil && i < uint32(acl.AceCount); i++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			if windows.GetAce(acl, i, &ace) == nil && ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE && (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String() == original.journal.SID {
				t.Fatalf("sandbox grant remains on %s", path)
			}
		}
	}
}
