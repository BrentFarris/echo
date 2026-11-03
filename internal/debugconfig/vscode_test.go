package debugconfig

import (
	"errors"
	"github.com/brent/echo/internal/editorfs"
	"github.com/brent/echo/internal/store"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWorkspaceJSONCFreshMultiRoot(t *testing.T) {
	roots := []editorfs.Root{{ID: "a", Label: "first", HostPath: t.TempDir()}, {ID: "b", Label: "second", HostPath: t.TempDir()}}
	files := map[string]string{
		"a/.vscode/settings.json": `{/*keep*/ "go.toolsEnvVars":{"URL":"https://example.test//"},"answer":42,}`,
		"a/.vscode/launch.json":   `{"configurations":[{"name":"Run","type":"go","request":"launch","program":"${workspaceFolder}","args":["${input:who}"],"windows":{"cwd":"win"},"linux":{"cwd":"linux"},"osx":{"cwd":"mac"},}],"inputs":[{"id":"who","type":"pickString","options":[{"label":"Me","value":"me"},"you"],}],"compounds":[{"name":"Both","stopAll":true,"configurations":["Run",{"folder":"second","name":"Run"}]}]}`,
		"b/.vscode/launch.json":   `{"configurations":[{"name":"Run","type":"go","request":"launch","program":"."}]}`,
	}
	read := func(ref editorfs.FileRef) ([]byte, error) {
		v, ok := files[ref.RootID+"/"+ref.Path]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return []byte(v), nil
	}
	config := Read(roots, read)
	if len(config.Diagnostics) != 0 || len(config.Configurations) != 2 {
		t.Fatalf("%+v", config)
	}
	a, b := config.Configurations[0], config.Configurations[1]
	if a.ID == b.ID || a.Name == b.Name {
		t.Fatal("ambiguous multi-root configuration")
	}
	if a.Arguments["cwd"] != map[string]string{"windows": "win", "linux": "linux", "darwin": "mac"}[runtime.GOOS] {
		t.Fatal(a.Arguments)
	}
	if config.Inputs[0].ID != "a:who" || a.Arguments["args"].([]any)[0] != "${input:a:who}" {
		t.Fatal("input not scoped to root")
	}
	if c := config.Compounds[0]; !c.StopAll || c.ConfigurationIDs[1] != b.ID {
		t.Fatal(c)
	}
	files["a/.vscode/launch.json"] = `{"configurations":[{"name":"Changed","type":"go","request":"launch","preLaunchTask":{"type":"shell"}}]}`
	next := Read(roots, read)
	if next.Configurations[0].ID == a.ID || !strings.Contains(next.Configurations[0].Error, "preLaunchTask") {
		t.Fatal(next)
	}
	files["a/.vscode/settings.json"] = `{bad`
	next = Read(roots, read)
	if len(next.Diagnostics) != 1 || len(next.Configurations) != 1 || next.Configurations[0].RootID != "b" {
		t.Fatal(next)
	}
}

func TestExpandWorkspaceVariables(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	file := filepath.Join(other, "pkg", "main_test.go")
	opts := ExpandOptions{WorkspaceFolder: root, WorkspaceFolders: map[string]string{"other": other}, CurrentFile: file, Settings: map[string]any{"answer": float64(42)}, Inputs: map[string]string{"Root:Name": "hello"}, Environment: map[string]string{"TOKEN": "value"}}
	for input, want := range map[string]string{"${fileWorkspaceFolder}": other, "${workspaceFolder:other}": other, "${fileBasenameNoExtension}": "main_test", "${fileDirnameBasename}": "pkg", "${config:answer}": "42", "${input:Root:Name}": "hello", "${env:TOKEN}": "value"} {
		got, err := ExpandString(input, opts)
		if err != nil || got != want {
			t.Fatalf("%s = %q, %v", input, got, err)
		}
	}
	for _, input := range []string{"${command:untrusted}", "${config:missing}", "${input:missing}", "${workspaceFolder:missing}", "${unsupported}"} {
		if _, err := ExpandString(input, opts); err == nil {
			t.Fatal(input)
		}
	}
	for _, input := range []string{`{"a":1,}`, `{"a":"// /* */",/*comment*/}`, "{\n//comment\n\"b\":[1,2,],}"} {
		var data any
		if err := DecodeJSONC([]byte(input), &data); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []string{`{"a":1} junk`, `{/* unterminated`, `{"a":"unterminated}`} {
		var data any
		if err := DecodeJSONC([]byte(input), &data); err == nil {
			t.Fatal(input)
		}
	}
}

func TestPersistentStateRevision(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	state := NewStateStore(db)
	first, err := state.Save("bot", 0, State{SelectedConfiguration: "Root:Run", Watches: []Watch{{ID: "watch", Expression: "x", Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = state.Save("bot", 0, State{})
	var conflict *RevisionConflict
	if !errors.As(err, &conflict) {
		t.Fatal(err)
	}
	saved, err := state.Load("bot")
	if err != nil || saved.Revision != first.Revision || saved.SelectedConfiguration != "Root:Run" {
		t.Fatalf("%+v %v", saved, err)
	}
}

func TestBOMPlatformDescriptorAndNestedConfig(t *testing.T) {
	platform := map[string]string{"windows": "windows", "linux": "linux", "darwin": "osx"}[runtime.GOOS]
	raw := []byte("\xef\xbb\xbf" + `{"configurations":[{"name":"Run","type":"old","request":"attach","` + platform + `":{"type":"go","request":"launch","program":"."}}]}`)
	result := Read([]editorfs.Root{{ID: "root", HostPath: t.TempDir()}}, func(ref editorfs.FileRef) ([]byte, error) {
		if ref.Path == ".vscode/settings.json" {
			return nil, fs.ErrNotExist
		}
		return raw, nil
	})
	if len(result.Configurations) != 1 || result.Configurations[0].AdapterProfileID != "go" || result.Configurations[0].Request != "launch" {
		t.Fatal(result)
	}
	value, err := ExpandString("${config:go.delveConfig.maxStringLen}", ExpandOptions{Settings: map[string]any{"go.delveConfig": map[string]any{"maxStringLen": float64(1024)}}})
	if err != nil || value != "1024" {
		t.Fatal(value, err)
	}
}
