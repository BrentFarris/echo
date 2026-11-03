package godebug

import (
	"context"
	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/debugger"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoConfigurationResolution(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "pkg")
	if err := os.Mkdir(pkg, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "vars.env"), []byte("FROM_FILE=yes\nORDER=file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	directory := ""
	provider := &Provider{Project: func(_ context.Context, dir string) (broker.ProcessRequest, error) {
		directory = dir
		return broker.ProcessRequest{Environment: map[string]string{"ORDER": "host"}}, nil
	}, Executable: func(broker.ProcessRequest, debugconfig.Configuration) (string, error) { return "dlv", nil }}
	config := debugconfig.Configuration{ID: "test", Name: "Test", Request: "launch", AdapterProfileID: "go", Folder: root, Settings: map[string]any{"go.delveConfig": map[string]any{"showGlobalVariables": true, "args": []any{"default"}}, "go.toolsEnvVars": map[string]any{"ORDER": "tools", "FROM_TOOLS": "yes"}}, Arguments: map[string]any{"mode": "auto", "program": "${file}", "cwd": "runtime", "envFile": "vars.env", "env": map[string]any{"ORDER": "launch"}, "args": []any{"launch"}, "buildFlags": []any{"-tags", "a b"}}}
	scope := debugger.LaunchContext{CurrentFile: filepath.Join(pkg, "main_test.go")}
	launch, err := provider.Prepare(context.Background(), scope, config)
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Cleanup()
	args := launch.Arguments
	env := args["env"].(map[string]string)
	if directory != pkg || args["program"] != pkg || args["cwd"] != filepath.Join(root, "runtime") || args["mode"] != "test" {
		t.Fatal(directory, args)
	}
	if env["ORDER"] != "launch" || env["FROM_TOOLS"] != "yes" || env["FROM_FILE"] != "yes" || args["showGlobalVariables"] != true || args["args"].([]any)[0] != "launch" {
		t.Fatal(args)
	}
	next, err := provider.Prepare(context.Background(), scope, config)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Cleanup()
	if args["output"] == next.Arguments["output"] {
		t.Fatal("debug binaries must be session-owned")
	}
	owned := filepath.Dir(args["output"].(string))
	launch.Cleanup()
	if _, err = os.Stat(owned); !os.IsNotExist(err) {
		t.Fatal("owned artifact not cleaned", err)
	}
	for key, value := range map[string]any{"preLaunchTask": "build", "console": "externalTerminal", "debugAdapter": "legacy", "port": 1234, "mode": "remote"} {
		bad := config
		bad.Arguments = map[string]any{"program": pkg, key: value}
		if _, err = provider.Prepare(context.Background(), scope, bad); err == nil {
			t.Fatal("accepted", key)
		}
	}
}

func TestGoProcessConfiguration(t *testing.T) {
	p := &Provider{}
	for _, pid := range []any{nil, 0, "${command:pickProcess}", "${command:pickGoProcess}", "my-program"} {
		c, inputs := p.Configure(debugconfig.Configuration{ID: "Attach", Request: "attach", Arguments: map[string]any{"processId": pid}})
		if len(inputs) != 1 || !strings.HasPrefix(c.Arguments["processId"].(string), "${input:") {
			t.Fatal(pid, c, inputs)
		}
	}
	for _, pid := range []any{123, "${command:unknown}", "${input:pid}"} {
		_, inputs := p.Configure(debugconfig.Configuration{Request: "attach", Arguments: map[string]any{"processId": pid}})
		if len(inputs) != 0 {
			t.Fatal(pid)
		}
	}
}

func TestCPointerTranslationPreservesLiterals(t *testing.T) {
	for input, want := range map[string]string{`ptr->field`: `ptr.field`, `ptr->items[1] == "a->b"`: `ptr.items[1] == "a->b"`, `'a' == ptr->letter`: `'a' == ptr.letter`} {
		if got := delveCExpression(input); got != want {
			t.Fatalf("%q => %q, want %q", input, got, want)
		}
	}
}
