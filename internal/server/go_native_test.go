package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
)

func goFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func invokeGo(ctx context.Context, s *Server, bot domain.Bot, directory, command string, args ...string) (broker.ProcessResult, error) {
	raw, _ := json.Marshal(map[string]any{"command": command, "args": args, "directory": directory})
	// Schema arrays must be arrays, including for commands with no arguments.
	if args == nil {
		raw, _ = json.Marshal(map[string]any{"command": command, "directory": directory})
	}
	value, err := s.Plugins.Invoke(ctx, domain.ExecutionScope{Bot: bot}, "go_command", raw)
	var result broker.ProcessResult
	encoded, _ := json.Marshal(value)
	json.Unmarshal(encoded, &result)
	return result, err
}

func nativeGo(t *testing.T, s *Server, bot domain.Bot, directory, command string, args ...string) broker.ProcessResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result, err := invokeGo(ctx, s, bot, directory, command, args...)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("go %s %v: %v\n%s", command, args, err, result.Output)
	}
	return result
}

func TestNativeGoPluginOfflineWorkflow(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1")
	}
	s := testServer(t)
	root := t.TempDir()
	project := filepath.Join(root, "project")
	os.Mkdir(project, 0700)
	bot, err := s.saveBot(domain.Bot{Handle: "go-builder", Roots: []string{root}, Plugins: map[string]bool{"shell": false}})
	if err != nil {
		t.Fatal(err)
	}
	// Serve a pinned dependency from an in-scope file proxy. This exercises a
	// real module download through Go without requiring an external service.
	proxy := filepath.Join(root, "proxy")
	version := filepath.Join(proxy, "fixture.invalid", "dependency", "@v")
	module := "module fixture.invalid/dependency\ngo 1.26.0\n"
	goFixtureFile(t, filepath.Join(version, "v1.0.0.mod"), module)
	goFixtureFile(t, filepath.Join(version, "v1.0.0.info"), `{"Version":"v1.0.0","Time":"2026-01-01T00:00:00Z"}`)
	goFixtureFile(t, filepath.Join(version, "list"), "v1.0.0\n")
	archive, err := os.Create(filepath.Join(version, "v1.0.0.zip"))
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(archive)
	for name, content := range map[string]string{"go.mod": module, "answer.go": "package dependency\nconst Answer = 42\n"} {
		file, err := writer.Create("fixture.invalid/dependency@v1.0.0/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive.Close()
	proxyPath := filepath.ToSlash(proxy)
	if !strings.HasPrefix(proxyPath, "/") {
		proxyPath = "/" + proxyPath
	}
	m, _ := s.Plugins.Get("go")
	m.Config = map[string]any{"proxy": (&url.URL{Scheme: "file", Path: proxyPath}).String()}
	s.Plugins.Save(m)
	nativeGo(t, s, bot, project, "mod", "init", "fixture.invalid/project")
	nativeGo(t, s, bot, project, "mod", "edit", "-require=fixture.invalid/dependency@v1.0.0")
	// Checksums of the exact fixture above keep verification enabled offline.
	goFixtureFile(t, filepath.Join(project, "go.sum"), "fixture.invalid/dependency v1.0.0 h1:dRFhIVVVkBk0yUV19QpblAx3wgXd1G5Ch4/BNl4UEJU=\nfixture.invalid/dependency v1.0.0/go.mod h1:TSXpS2Atdl1vvRsZE1mbncgvJWM3oxzTel3hoyAowGo=\n")
	nativeGo(t, s, bot, project, "mod", "download")
	goFixtureFile(t, filepath.Join(project, "main.go"), "package main\nimport (\"fmt\"; \"fixture.invalid/dependency\")\nfunc main(){fmt.Println(dependency.Answer)}\n")
	goFixtureFile(t, filepath.Join(project, "main_test.go"), "package main\nimport (\"testing\"; \"fixture.invalid/dependency\")\nfunc TestAnswer(t *testing.T){if dependency.Answer != 42 {t.Fatal(dependency.Answer)}}\n")
	nativeGo(t, s, bot, project, "mod", "tidy")
	nativeGo(t, s, bot, project, "fmt", "./...")
	nativeGo(t, s, bot, project, "build", "-o", ".cache/build-output", "./...")
	nativeGo(t, s, bot, project, "vet", "./...")
	if out := nativeGo(t, s, bot, project, "test", "-json", "./...").Output; !strings.Contains(out, `"Action":"pass"`) {
		t.Fatal(out)
	}
	if out := nativeGo(t, s, bot, project, "run", ".").Output; !strings.Contains(out, "42") {
		t.Fatal(out)
	}
	nativeGo(t, s, bot, project, "install", ".")
	installed, _ := os.ReadDir(filepath.Join(project, ".cache", "go-bin"))
	if len(installed) != 1 {
		t.Fatal("installation did not persist", installed)
	}
	goFixtureFile(t, filepath.Join(project, "generate.go"), "package main\n//go:generate project\n")
	if out := nativeGo(t, s, bot, project, "generate", ".").Output; !strings.Contains(out, "42") {
		t.Fatal("installed binary not on PATH", out)
	}
	m.Config["proxy"] = "off"
	s.Plugins.Save(m)
	nativeGo(t, s, bot, project, "test", "-count=1", "./...")
	nativeGo(t, s, bot, project, "build", "./...")
	if result, err := invokeGo(context.Background(), s, bot, project, "mod", "download", "fixture.invalid/missing@v1.0.0"); err == nil && result.ExitCode == 0 {
		t.Fatal("uncached module succeeded offline")
	}
	local := filepath.Join(root, "local")
	goFixtureFile(t, filepath.Join(local, "go.mod"), "module fixture.invalid/local\ngo 1.26.0\n")
	goFixtureFile(t, filepath.Join(local, "local.go"), "package local\nconst Value = 7\n")
	goFixtureFile(t, filepath.Join(project, "local_test.go"), "package main\nimport(\"testing\";\"fixture.invalid/local\")\nfunc TestLocal(t *testing.T){if local.Value!=7{t.Fatal(local.Value)}}\n")
	nativeGo(t, s, bot, project, "mod", "edit", "-require=fixture.invalid/local@v0.0.0", "-replace=fixture.invalid/local=../local")
	nativeGo(t, s, bot, project, "test", "./...")
	nativeGo(t, s, bot, root, "work", "init", "./project", "./local")
	nativeGo(t, s, bot, project, "test", "./...")
	outside := t.TempDir()
	goFixtureFile(t, filepath.Join(outside, "go.mod"), "module fixture.invalid/dependency\ngo 1.26.0\n")
	goFixtureFile(t, filepath.Join(outside, "answer.go"), "package dependency\nconst Answer=42\n")
	nativeGo(t, s, bot, project, "mod", "edit", "-replace=fixture.invalid/dependency="+outside)
	if result, err := invokeGo(context.Background(), s, bot, project, "build", "."); err == nil && result.ExitCode == 0 {
		t.Fatal("out-of-scope replacement was readable")
	}
	nativeGo(t, s, bot, project, "mod", "edit", "-dropreplace=fixture.invalid/dependency")
	// A warm build makes cancellation tests independent of compiler startup.
	goFixtureFile(t, filepath.Join(project, "cmd", "wait", "main.go"), "package main\nimport(\"os\";\"time\")\nfunc main(){os.WriteFile(\"started\", []byte(\"yes\"),0600);time.Sleep(time.Minute);os.WriteFile(\"leaked\",[]byte(\"bad\"),0600)}\n")
	nativeGo(t, s, bot, project, "build", "./cmd/wait")
	raw, _ := json.Marshal(map[string]any{"command": "run", "args": []string{"./cmd/wait"}, "directory": project, "timeoutSeconds": 1})
	started := time.Now()
	if _, err := s.Plugins.Invoke(context.Background(), domain.ExecutionScope{Bot: bot}, "go_command", raw); err == nil || time.Since(started) > 15*time.Second {
		t.Fatal("timeout was not enforced", err)
	}
	additionalRoot := t.TempDir()
	for _, stop := range []string{"cancel", "scope", "disable"} {
		t.Run(stop, func(t *testing.T) {
			os.Remove(filepath.Join(project, "started"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := invokeGo(ctx, s, bot, project, "run", "./cmd/wait"); done <- err }()
			deadline := time.Now().Add(30 * time.Second)
			for {
				select {
				case err := <-done:
					t.Fatal("command exited before child started", err)
				default:
				}
				if _, err := os.Stat(filepath.Join(project, "started")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child did not start")
				}
				time.Sleep(50 * time.Millisecond)
			}
			if stop == "scope" {
				updated := bot
				updated.Roots = append(append([]string{}, bot.Roots...), additionalRoot)
				bot, err = s.updateBot(updated, nil)
				if err != nil {
					t.Fatal(err)
				}
			} else if stop == "cancel" {
				cancel()
			} else {
				m.Enabled = false
				s.Plugins.Save(m)
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancelled invocation succeeded")
				}
			case <-time.After(15 * time.Second):
				t.Fatal("child did not terminate")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(project, "leaked")); err == nil {
		t.Fatal("child survived cancellation")
	}
	journals, _ := filepath.Glob(filepath.Join(s.Process.Dir, "*.json"))
	if len(journals) != 0 {
		t.Fatal("sandbox grants leaked", journals)
	}
}

func TestNativeGoPluginInternet(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" || os.Getenv("ECHO2_GO_NETWORK_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1 and ECHO2_GO_NETWORK_TESTS=1 for live Internet acceptance")
	}
	s := testServer(t)
	root := t.TempDir()
	bot, err := s.saveBot(domain.Bot{Handle: "go-network", Roots: []string{root}, Plugins: map[string]bool{"shell": false}})
	if err != nil {
		t.Fatal(err)
	}
	nativeGo(t, s, bot, root, "mod", "init", "fixture.invalid/network")
	// First confirm that the same download fails with the default grants.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	result, err := invokeGo(ctx, s, bot, root, "mod", "download", "github.com/google/uuid@v1.6.0")
	cancel()
	if err == nil && result.ExitCode == 0 {
		t.Fatal("download worked without Internet grant")
	}
	m, _ := s.Plugins.Get("go")
	m.Config["internet"] = true
	s.Plugins.Save(m)
	nativeGo(t, s, bot, root, "get", "github.com/google/uuid@v1.6.0")
	goFixtureFile(t, filepath.Join(root, "main_test.go"), "package network\nimport(\"testing\";\"github.com/google/uuid\")\nfunc TestUUID(t *testing.T){if uuid.NewString()==\"\"{t.Fatal(\"empty\")}}\n")
	nativeGo(t, s, bot, root, "test", "./...")
	goFixtureFile(t, filepath.Join(root, "cmd", "network", "main.go"), "package main\nimport(\"net/http\";\"time\";\"os\")\nfunc main(){c:=http.Client{Timeout:5*time.Second};r,e:=c.Get(\"https://proxy.golang.org/github.com/google/uuid/@v/v1.6.0.info\");if e!=nil{os.Exit(2)};r.Body.Close();if r.StatusCode!=200{os.Exit(3)}}\n")
	nativeGo(t, s, bot, root, "run", "./cmd/network")
	m.Config["internet"] = false
	m.Config["privateNetwork"] = true
	s.Plugins.Save(m)
	if result, err := invokeGo(context.Background(), s, bot, root, "run", "./cmd/network"); err == nil && result.ExitCode == 0 {
		t.Fatal("private-network grant permitted Internet access")
	}
	m.Config["privateNetwork"] = false
	s.Plugins.Save(m)
	nativeGo(t, s, bot, root, "test", "-count=1", "./...")
}
