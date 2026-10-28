//go:build windows

package broker

import (
	"archive/zip"
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNativeWorkspaceGoTestsOffline(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1")
	}
	root, proxy := t.TempDir(), t.TempDir()
	module := "fixture.invalid/dependency"
	moduleFile := "module " + module + "\n\ngo 1.26.0\n"
	versionDir := filepath.Join(proxy, filepath.FromSlash(module), "@v")
	if err := os.MkdirAll(versionDir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"v1.0.0.mod": moduleFile, "v1.0.0.info": `{"Version":"v1.0.0","Time":"2026-01-01T00:00:00Z"}`, "list": "v1.0.0\n"} {
		if err := os.WriteFile(filepath.Join(versionDir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	archive, err := os.Create(filepath.Join(versionDir, "v1.0.0.zip"))
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(archive)
	for name, content := range map[string]string{"go.mod": moduleFile, "answer.go": "package dependency\nconst Answer = 42\n"} {
		file, err := writer.Create(module + "@v1.0.0/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"go.mod":       "module fixture.invalid/project\n\ngo 1.26.0\nrequire " + module + " v1.0.0\n",
		"main_test.go": "package project\nimport (\"testing\"; \"fixture.invalid/dependency\")\nfunc TestDependency(t *testing.T) { if dependency.Answer != 42 { t.Fatal(\"bad dependency\") } }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cache := filepath.Join(root, ".cache", "go-mod")
	buildCache := filepath.Join(root, ".cache", "go-build")
	if err := os.MkdirAll(buildCache, 0700); err != nil {
		t.Fatal(err)
	}
	// Prepare from a local proxy outside the eventual sandbox. No network or
	// host-wide module-cache grant is needed by this fixture or its tests.
	proxyURL := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(proxy)}).String()
	prepare := exec.Command("go", "mod", "download")
	prepare.Dir = root
	prepare.Env = append(os.Environ(), "GOMODCACHE="+cache, "GOCACHE="+buildCache, "GOPROXY="+proxyURL, "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off")
	if output, err := prepare.CombinedOutput(); err != nil {
		t.Fatalf("prepare dependencies: %s: %v", output, err)
	}
	b := &ProcessBroker{Dir: filepath.Join(t.TempDir(), "runtime")}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := b.Run(ctx, ProcessRequest{Roots: []string{root}, Directory: root, Executable: filepath.Join(runtime.GOROOT(), "bin", "go.exe"), Args: []string{"test", "-mod=mod", "-count=1", "-v", "."}, Toolchains: []string{runtime.GOROOT()}, Environment: map[string]string{"GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local"}}, nil)
	if err != nil || result.ExitCode != 0 || !strings.Contains(result.Output, "PASS") {
		t.Fatalf("offline Go test: %v\n%+v", err, result)
	}
}

func TestNativeFrontendTests(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1")
	}
	node, err := NativeExecutable("node")
	if err != nil {
		t.Fatal(err)
	}
	web, err := filepath.Abs(filepath.Join("..", "..", "web"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(web, "node_modules", "vitest", "vitest.mjs")); err != nil {
		if os.Getenv("ECHO2_REQUIRE_TOOLCHAINS") == "1" {
			t.Fatal("install frontend dependencies before native acceptance", err)
		}
		t.Skip("install frontend dependencies before native acceptance")
	}
	b := &ProcessBroker{Dir: filepath.Join(t.TempDir(), "runtime")}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result, err := b.Run(ctx, ProcessRequest{Roots: []string{web}, Directory: web, Executable: node, Args: []string{"node_modules/vitest/vitest.mjs", "run", "--config", "vitest.config.mjs", "--configLoader", "native", "src/fileChanges.test.ts", "--maxWorkers=1"}, Toolchains: []string{filepath.Dir(node)}}, nil)
	if err != nil || result.ExitCode != 0 || !strings.Contains(result.Output, "3 passed") {
		t.Fatalf("sandboxed frontend tests: %v\n%+v", err, result)
	}
}
