//go:build windows

package development

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Exercise the actual build script with real Windows DLL locks, while stubbing
// the compilers/package installer so the test needs neither a network nor npm.
func TestBuildStopsOnlyCheckoutDependencyProcesses(t *testing.T) {
	dir := buildScriptFixture(t)
	versionDLL, err := os.ReadFile(filepath.Join(os.Getenv("SystemRoot"), "System32", "version.dll"))
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(dir, "node.exe")
	if err := os.WriteFile(node, self, 0600); err != nil {
		t.Fatal(err)
	}
	startNode := func(modules string) *exec.Cmd {
		t.Helper()
		dll := filepath.Join(dir, "web", modules, "native.dll")
		writeTest(t, dll, string(versionDLL))
		ready := dll + ".ready"
		command := exec.Command(node, "-test.run=^TestBuildDependencyNodeProcess$")
		command.Env = append(os.Environ(), "ECHO_BUILD_TEST_DLL="+dll, "ECHO_BUILD_TEST_READY="+ready)
		command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(ready); err == nil {
				return command
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("native module helper did not become ready")
		return nil
	}
	locking := startNode("node_modules")
	// A shared textual prefix must not match a different dependency directory.
	unrelated := startNode("node_modules-other")
	output, err := runBuildScriptFixture(t, dir, "")
	if err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	if !strings.Contains(output, "Stopping Node process") {
		t.Fatalf("locking process was not logged: %s", output)
	}
	_ = locking.Wait()
	if _, err := os.Stat(filepath.Join(dir, "installed")); err != nil {
		t.Fatal("npm ci did not run after the native module lock was released", err)
	}
	// The unrelated process still holds its DLL open.
	if err := os.Remove(filepath.Join(dir, "web", "node_modules-other", "native.dll")); err == nil {
		t.Fatalf("unrelated Node process %d was stopped", unrelated.Process.Pid)
	}
}

func TestBuildDependencyStopFailuresAndExitRace(t *testing.T) {
	for _, scenario := range []string{"denied", "timeout", "exited"} {
		t.Run(scenario, func(t *testing.T) {
			dir := buildScriptFixture(t)
			mock := `
$global:fixtureNode = [pscustomobject]@{ Id = 4242; HasExited = $false; Modules = @([pscustomobject]@{ FileName = (Join-Path $PSScriptRoot 'web\node_modules\native.dll') }) }
$global:fixtureNode | Add-Member ScriptMethod WaitForExit { param($timeout) if ($timeout -ne 10000) { throw 'wrong timeout' }; return $false }
function Get-Process { param($Name, $ErrorAction) return $global:fixtureNode }
`
			switch scenario {
			case "denied":
				mock += "function Stop-Process { param($InputObject, [switch]$Force, $ErrorAction) throw 'fixture access denied' }\n"
			case "timeout":
				mock += "function Stop-Process { param($InputObject, [switch]$Force, $ErrorAction) }\n"
			case "exited":
				mock += "function Stop-Process { param($InputObject, [switch]$Force, $ErrorAction) $InputObject.HasExited = $true; throw 'already exited' }\n"
			}
			output, err := runBuildScriptFixture(t, dir, mock)
			_, installedErr := os.Stat(filepath.Join(dir, "installed"))
			if scenario == "exited" {
				if err != nil || installedErr != nil {
					t.Fatalf("process exit race prevented build: %v, %v\n%s", err, installedErr, output)
				}
			} else if err == nil || !os.IsNotExist(installedErr) || !strings.Contains(output, "npm ci has not been started") || !strings.Contains(output, "4242") {
				t.Fatalf("installation was not safely blocked: %v, %v\n%s", err, installedErr, output)
			}
		})
	}
}

func buildScriptFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script, err := os.ReadFile(filepath.Join("..", "..", "build.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(dir, "build.ps1"), string(script))
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runBuildScriptFixture(t *testing.T, dir, mock string) (string, error) {
	t.Helper()
	script := `
$ErrorActionPreference = 'Stop'
function go { $global:LASTEXITCODE = 0 }
function git { $global:LASTEXITCODE = 0 }
function npm {
    if ($args[0] -eq 'ci') {
        # npm ci deletes native modules; this must succeed before installation.
        $dll = Join-Path $PSScriptRoot 'web\node_modules\native.dll'
        if (Test-Path -LiteralPath $dll) { Remove-Item -LiteralPath $dll -Force }
        Set-Content -LiteralPath (Join-Path $PSScriptRoot 'installed') -Value yes
    }
    $global:LASTEXITCODE = 0
}
` + mock + "\n& (Join-Path $PSScriptRoot 'build.ps1')\n"
	path := filepath.Join(dir, "run.ps1")
	writeTest(t, path, script)
	shell, err := findPowerShell()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	output, err := command.CombinedOutput()
	return string(output), err
}

func TestBuildDependencyNodeProcess(t *testing.T) {
	path := os.Getenv("ECHO_BUILD_TEST_DLL")
	if path == "" {
		return
	}
	dll, err := syscall.LoadDLL(path)
	if err != nil {
		t.Fatal(err)
	}
	defer dll.Release()
	if err := os.WriteFile(os.Getenv("ECHO_BUILD_TEST_READY"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Minute)
}
