package gotool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
)

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareGoProject(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	sub := filepath.Join(project, "sub")
	writeFixture(t, filepath.Join(project, "go.mod"), "module fixture\ngo 1.26\n")
	writeFixture(t, filepath.Join(root, "go.work"), "go 1.26\nuse ./project\n")
	os.Mkdir(sub, 0700)
	t.Setenv("GOFLAGS", "-invalid-host-flag")
	t.Setenv("GOTOOLCHAIN", "go999.0.0")
	t.Setenv("GOPROXY", "https://invalid.invalid")
	bot := domain.Bot{ID: "go-test", Roots: []string{root}}
	args := []string{"-ldflags", `-X 'main.value=a ; $(literal)'`, "./..."}
	r, err := Prepare(context.Background(), &broker.Files{}, bot, Settings{Internet: true}, Command{Command: "build", Args: args, Directory: sub})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Args, append([]string{"build"}, args...)) || r.Command != "" || !r.Internet || r.PrivateNetwork || r.TimeoutSeconds != 300 {
		t.Fatalf("bad request: %+v", r)
	}
	for key, name := range map[string]string{"GOMODCACHE": "go-mod", "GOCACHE": "go-build", "GOBIN": "go-bin", "GOPATH": "go-path"} {
		want := filepath.Join(project, ".cache", name)
		if r.Environment[key] != want {
			t.Fatalf("%s=%q want %q", key, r.Environment[key], want)
		}
		if info, err := os.Stat(want); err != nil || !info.IsDir() {
			t.Fatal(want, err)
		}
	}
	if r.Environment["GOENV"] != "off" || r.Environment["GOWORK"] != filepath.Join(root, "go.work") || r.Environment["GOTOOLCHAIN"] != "auto" || r.Environment["GOFLAGS"] != "" || r.Environment["GOPROXY"] != "https://proxy.golang.org,direct" {
		t.Fatal(r.Environment)
	}
	marker := filepath.Join(r.Environment["GOMODCACHE"], "keep")
	writeFixture(t, marker, "persistent")
	if _, err = Prepare(context.Background(), &broker.Files{}, bot, Settings{}, Command{Command: "test", Directory: sub}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(marker); err != nil {
		t.Fatal("cache lost", err)
	}
	// A nested module selects its own cache; a subfolder-only grant cannot
	// select either the parent module or the parent go.work file.
	writeFixture(t, filepath.Join(sub, "go.mod"), "module nested\ngo 1.26\n")
	bot.Roots = []string{sub}
	r, err = Prepare(context.Background(), &broker.Files{}, bot, Settings{}, Command{Command: "env", Directory: sub})
	if err != nil || r.Environment["GOCACHE"] != filepath.Join(sub, ".cache", "go-build") || r.Environment["GOWORK"] != "off" {
		t.Fatal(r, err)
	}
}

func TestPrepareGoNewProjectAndDenials(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	bot := domain.Bot{ID: "test", Roots: []string{root}}
	files := &broker.Files{}
	r, err := Prepare(context.Background(), files, bot, Settings{}, Command{Command: "mod", Args: []string{"init", "fixture"}})
	if err != nil || r.Environment["GOMODCACHE"] != filepath.Join(root, ".cache", "go-mod") {
		t.Fatal(r, err)
	}
	for _, command := range []Command{{Command: "version", Directory: outside}, {Command: "build;evil"}, {Command: "env", Args: []string{"-w", "GOPROXY=off"}}, {Command: "env", Args: []string{"-u=GOPROXY"}}, {Command: "build", TimeoutSeconds: 301}} {
		if _, err := Prepare(context.Background(), files, bot, Settings{}, command); err == nil {
			t.Fatal("accepted", command)
		}
	}
	files.Private = root
	if _, err := Prepare(context.Background(), files, bot, Settings{}, Command{Command: "env"}); err == nil {
		t.Fatal("private state accepted")
	}
}

func TestGoCacheRejectsLinks(t *testing.T) {
	for _, cache := range []string{".cache", ".cache/go-mod", ".cache/go-bin"} {
		t.Run(strings.ReplaceAll(cache, "/", "_"), func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			link := filepath.Join(root, filepath.FromSlash(cache))
			os.MkdirAll(filepath.Dir(link), 0700)
			if runtime.GOOS == "windows" {
				if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
					t.Fatal(string(out), err)
				}
			} else if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			if _, err := Prepare(context.Background(), &broker.Files{}, domain.Bot{Roots: []string{root}}, Settings{}, Command{Command: "version", Directory: root}); err == nil {
				t.Fatal("cache link accepted")
			}
			entries, _ := os.ReadDir(outside)
			if len(entries) != 0 {
				t.Fatal("wrote outside workspace")
			}
		})
	}
}

func TestGoProxy(t *testing.T) {
	for _, value := range []string{"", "off", "direct", "https://proxy.golang.org,direct", "https://proxy.example|https://backup.example", "file:///tmp/modules"} {
		if err := ValidateProxy(value); err != nil {
			t.Fatal(value, err)
		}
	}
	for _, value := range []string{"https://user:secret@proxy.example", "https://proxy.example?token=secret", "ftp://proxy.example", "proxy.example"} {
		if ValidateProxy(value) == nil {
			t.Fatal("accepted", value)
		}
	}
}

func TestGoDiscoveryDoesNotExecute(t *testing.T) {
	root := t.TempDir()
	name := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(root, "bin", name)
	// This is deliberately not an executable. Discovery must only inspect
	// the layout; any execution of owner-selected binaries belongs in broker.
	writeFixture(t, binary, "not executable")
	writeFixture(t, filepath.Join(root, "VERSION"), "go1.26.0\n")
	os.MkdirAll(filepath.Join(root, "src"), 0700)
	os.MkdirAll(filepath.Join(root, "pkg", "tool"), 0700)
	got, install, err := discover(context.Background(), binary, &broker.Files{})
	if err != nil || got != binary || install != root {
		t.Fatal(got, install, err)
	}
	if _, _, err = discover(context.Background(), binary, &broker.Files{Private: root}); err == nil {
		t.Fatal("private installation accepted")
	}
}
