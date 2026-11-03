//go:build windows

package broker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNativeContainment(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1 to run AppContainer integration tests")
	}
	base := t.TempDir()
	allowed := filepath.Join(base, "workspace")
	denied := filepath.Join(base, "private")
	os.MkdirAll(allowed, 0700)
	os.MkdirAll(denied, 0700)
	os.WriteFile(filepath.Join(allowed, "input.txt"), []byte("allowed"), 0600)
	os.WriteFile(filepath.Join(denied, "secret.txt"), []byte("private"), 0600)
	b := &ProcessBroker{Dir: filepath.Join(base, "runtime")}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	command := "$value=Get-Content -LiteralPath " + quote(filepath.Join(allowed, "input.txt")) + "; Write-Output $value; Set-Content -LiteralPath " + quote(filepath.Join(allowed, "output.txt")) + " -Value 'created'; try { Get-Content -LiteralPath " + quote(filepath.Join(denied, "secret.txt")) + " -ErrorAction Stop; exit 71 } catch { Write-Output 'denied' }; & " + quote(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")) + " /c echo child-contained"
	r, err := b.Run(ctx, ProcessRequest{BotID: "test", Roots: []string{allowed}, Command: command}, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, r.Output)
	}
	if r.ExitCode != 0 || !strings.Contains(r.Output, "allowed") || !strings.Contains(r.Output, "denied") || !strings.Contains(r.Output, "child-contained") {
		t.Fatalf("unexpected result: %+v", r)
	}
	if _, err = os.Stat(filepath.Join(allowed, "output.txt")); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(b.Dir, "*.json"))
	if len(files) != 0 {
		t.Fatal("ACL cleanup journal was not cleared")
	}
}

func TestNativeToolchains(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1")
	}
	for _, tool := range []string{"go", "git", "node", "dotnet"} {
		t.Run(tool, func(t *testing.T) {
			binary, err := exec.LookPath(tool)
			if err != nil {
				if os.Getenv("ECHO2_REQUIRE_TOOLCHAINS") == "1" {
					t.Fatal(tool + " is required for release acceptance")
				}
				t.Skip(tool + " is not installed")
			}
			base := t.TempDir()
			root := filepath.Join(base, "workspace")
			os.MkdirAll(root, 0700)
			toolchains := []string{filepath.Dir(binary)}
			command := ""
			switch tool {
			case "go":
				toolchains = []string{runtime.GOROOT()}
				os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.26.0\n"), 0600)
				os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nimport \"fmt\"\nfunc main(){fmt.Println(\"go-contained\")}\n"), 0600)
				command = "go run ."
			case "git":
				toolchains = []string{filepath.Dir(filepath.Dir(binary))}
				command = "git init; if ($LASTEXITCODE) { exit $LASTEXITCODE }; Set-Content test.txt 'git-contained'; git add test.txt; if ($LASTEXITCODE) { exit $LASTEXITCODE }; git -c user.name=EchoTest -c user.email=echo@example.invalid commit -m test; if ($LASTEXITCODE) { exit $LASTEXITCODE }; git status --porcelain; Write-Output git-contained"
			case "node":
				os.WriteFile(filepath.Join(root, "test.js"), []byte("require('fs').writeFileSync('result.txt','node-contained'); console.log(require('fs').readFileSync('result.txt','utf8'))"), 0600)
				command = "node test.js"
			case "dotnet":
				command = "$version = dotnet --version; if ($LASTEXITCODE) { exit $LASTEXITCODE }; $major = $version.Split('.')[0]; $project = '<Project Sdk=\"Microsoft.NET.Sdk\"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>net' + $major + '.0</TargetFramework></PropertyGroup></Project>'; Set-Content fixture.csproj $project; Set-Content Program.cs 'System.Console.WriteLine(\"dotnet-contained\");'; dotnet run -p:NuGetAudit=false --no-launch-profile; if ($LASTEXITCODE) { exit $LASTEXITCODE }"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			b := &ProcessBroker{Dir: filepath.Join(base, "runtime")}
			result, err := b.Run(ctx, ProcessRequest{BotID: tool, Roots: []string{root}, Directory: root, Toolchains: toolchains, Command: command}, nil)
			if err != nil || result.ExitCode != 0 || !strings.Contains(result.Output, tool+"-contained") {
				t.Fatalf("native toolchain failed: %v\n%+v", err, result)
			}
		})
	}
}

func TestNativeChildCannotEscapeAndCancellation(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1")
	}
	base := t.TempDir()
	allowed := filepath.Join(base, "workspace")
	private := filepath.Join(base, "private")
	os.MkdirAll(allowed, 0700)
	os.MkdirAll(private, 0700)
	secret := filepath.Join(private, "secret.txt")
	os.WriteFile(secret, []byte("secret-value"), 0600)
	link := filepath.Join(allowed, "junction")
	cmd := exec.Command("cmd.exe", "/c", "mklink", "/J", link, private)
	if output, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("create junction fixture: %v %s", e, output)
	}
	b := &ProcessBroker{Dir: filepath.Join(base, "runtime")}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	command := "try { Get-Content -LiteralPath " + quote(filepath.Join(link, "secret.txt")) + " -ErrorAction Stop; exit 72 } catch { Write-Output junction-denied }; & " + quote(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")) + " /c type " + quote(secret) + "; if ($LASTEXITCODE -eq 0) { exit 73 }; Write-Output child-denied"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	result, e := b.Run(ctx, ProcessRequest{BotID: "escape", Roots: []string{allowed}, Command: command}, nil)
	cancel()
	if e != nil || result.ExitCode != 0 || !strings.Contains(result.Output, "junction-denied") || !strings.Contains(result.Output, "child-denied") || strings.Contains(result.Output, "secret-value") {
		t.Fatalf("escape boundary: %v %+v", e, result)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
	start := time.Now()
	_, e = b.Run(ctx, ProcessRequest{BotID: "cancel", Roots: []string{allowed}, Command: "Start-Sleep -Seconds 30"}, nil)
	cancel()
	if e == nil || time.Since(start) > 5*time.Second {
		t.Fatal("process cancellation failed", e)
	}
}
