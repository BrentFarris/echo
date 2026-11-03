package broker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNativePersistentProcess(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "duplex")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	source := filepath.Join(root, "main.go")
	os.WriteFile(source, []byte(`package main
import("bufio";"fmt";"os";"time")
func main(){fmt.Fprintln(os.Stderr,"stderr-only");s:=bufio.NewScanner(os.Stdin);for s.Scan(){if s.Text()=="wait" {time.Sleep(1500*time.Millisecond)};_,err:=os.ReadFile(os.Getenv("FORBIDDEN"));fmt.Printf("%s:%t\n",s.Text(),err!=nil)}}`), 0600)
	cmd := exec.Command("go", "build", "-o", binary, source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	forbidden := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(forbidden, []byte("secret"), 0600)
	b := &ProcessBroker{Dir: filepath.Join(t.TempDir(), "runtime")}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := b.Start(ctx, ProcessRequest{BotID: "duplex", Executable: binary, Roots: []string{root}, Directory: root, TimeoutSeconds: 1, Environment: map[string]string{"FORBIDDEN": forbidden}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.HostPath(p.ProcessPath(source)) != source {
		t.Fatal("alias round trip failed")
	}
	reader := bufio.NewReader(p.Stdout)
	for _, value := range []string{"hello", "wait", "日本語 ; &"} {
		fmt.Fprintln(p.Stdin, value)
		line, err := reader.ReadString('\n')
		if err != nil || strings.TrimSpace(line) != value+":true" {
			t.Fatal(line, err)
		}
	}
	p.Close()
	result, err := p.Wait()
	if err == nil || !strings.Contains(result.Stderr, "stderr-only") {
		t.Fatal(result, err)
	}
	journals, _ := filepath.Glob(filepath.Join(b.Dir, "*.json"))
	if len(journals) != 0 {
		t.Fatal("sandbox grants left behind", journals)
	}
	for _, timeout := range []int{0, 1, 3600} {
		t.Run(fmt.Sprintf("command-timeout=%d", timeout), func(t *testing.T) {
			result, err := b.Run(ctx, ProcessRequest{BotID: "finite", Executable: binary, Roots: []string{root}, Directory: root, Input: "wait\n", TimeoutSeconds: timeout, Environment: map[string]string{"FORBIDDEN": forbidden}}, nil)
			if timeout == 1 {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("explicit timeout was not enforced: %+v, %v", result, err)
				}
			} else if err != nil || result.ExitCode != 0 || !strings.Contains(result.Output, "wait:true") {
				t.Fatalf("command failed without an expired deadline: %+v, %v", result, err)
			}
		})
	}
}
