package server

import (
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
	"github.com/brent/echo/internal/editorfs"
	"github.com/brent/echo/internal/gotool"
	"github.com/brent/echo/internal/languageservice"
)

func TestGoLanguageURIContainment(t *testing.T) {
	root := t.TempDir()
	private := t.TempDir()
	file := filepath.Join(root, "main.go")
	os.WriteFile(file, []byte("package main"), 0600)
	bot := domain.Bot{ID: "fixture", Roots: []string{root}}
	fs := editorfs.New(func(string) (domain.Bot, error) { return bot, nil }, private)
	defer fs.Close()
	g := &goLanguageSession{id: "session", bot: bot, fs: fs, sources: map[string]string{}, request: broker.ProcessRequest{Environment: map[string]string{"GOMODCACHE": filepath.Join(root, ".cache", "go-mod")}}}
	ref, err := fs.ReferenceForHostPath(bot.ID, file)
	if err != nil {
		t.Fatal(err)
	}
	wire := (&url.URL{Scheme: "echo-workspace", Host: ref.RootID, Path: "/" + ref.Path}).String()
	uri, err := g.mapURI(wire, true)
	if err != nil || uri != hostURI(file) {
		t.Fatal(uri, err)
	}
	back, err := g.mapURI(uri, false)
	if err != nil || back != wire {
		t.Fatal(back, err)
	}
	for _, value := range []string{hostURI(file), "echo-workspace://" + ref.RootID + "/../secret.go", "echo-go://other/nope"} {
		if _, err = g.mapURI(value, true); err == nil {
			t.Fatal("accepted unscoped URI", value)
		}
	}
	outside := filepath.Join(t.TempDir(), "secret.go")
	os.WriteFile(outside, []byte("secret"), 0600)
	if _, err = g.mapURI(hostURI(outside), false); err == nil {
		t.Fatal("returned outside path")
	}
	if _, err = g.translate(json.RawMessage(`{"documentChanges":[{"kind":"delete","uri":"`+wire+`"}]}`), true); err == nil {
		t.Fatal("accepted resource edit")
	}
	dep := filepath.Join(root, ".cache", "go-mod", "pkg", "source.go")
	goFixtureFile(t, dep, "package pkg")
	handle, err := g.mapURI(hostURI(dep), false)
	if err != nil || !strings.HasPrefix(handle, "echo-go://") {
		t.Fatal(handle, err)
	}
	if back, err = g.mapURI(handle, true); err != nil || back != hostURI(dep) {
		t.Fatal(back, err)
	}
}

func TestGoInspectContracts(t *testing.T) {
	s := testServer(t)
	m, _ := s.Plugins.Get("go")
	bot, _ := s.Bot("echo")
	if _, err := s.goConfig(m, map[string]any{"goplsExecutable": "/expanded/gopls"}, false); err == nil {
		t.Fatal("bot expanded executable grants")
	}
	for _, fake := range []domain.PluginManifest{{ID: "go"}, {ID: "shell", Builtin: true}} {
		if _, err := s.inspectGo(context.Background(), domain.ExecutionScope{Bot: bot, PluginID: fake.ID}, fake, nil); err == nil {
			t.Fatal("foreign plugin inspected Go")
		}
	}
	m.Config["languageServices"] = false
	s.Plugins.Save(m)
	if _, _, err := s.goLanguageBot(bot.ID, false); err == nil {
		t.Fatal("disabled language services available")
	}
}

func TestGoLanguageLeaseRevocation(t *testing.T) {
	s := testServer(t)
	m, _ := s.Plugins.Get("go")
	m.Config["internet"] = true
	s.Plugins.Save(m)
	ctx, _, current, release, err := s.leaseGoLanguage(context.Background(), "echo", false)
	if err != nil || current.Config["internet"] != true {
		t.Fatal(err, current.Config)
	}
	defer release()
	m.Config["internet"] = false
	s.Plugins.Save(m)
	select {
	case <-ctx.Done():
	default:
		t.Fatal("settings did not revoke language lease")
	}
	ctx, _, current, nextRelease, err := s.leaseGoLanguage(context.Background(), "echo", false)
	if err != nil || current.Config["internet"] != false {
		t.Fatal(err, current.Config)
	}
	defer nextRelease()
	finish := s.stopBot("echo")
	defer finish()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("scope change did not revoke language lease")
	}
	if _, _, _, _, err = s.leaseGoLanguage(context.Background(), "echo", false); err == nil {
		t.Fatal("started language session during scope revocation")
	}
}

func TestNativeGoLanguageServices(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1")
	}
	if os.Getenv("ECHO_GOPLS") == "" && os.Getenv("ECHO2_LIVE_NETWORK") != "1" {
		t.Skip("set ECHO_GOPLS or ECHO2_LIVE_NETWORK=1 for explicit installation")
	}
	s := testServer(t)
	root := t.TempDir()
	goFixtureFile(t, filepath.Join(root, "go.mod"), "module example.com/lspfixture\ngo 1.26.0\nrequire example.com/library v0.0.0\nreplace example.com/library => ./library\n")
	goFixtureFile(t, filepath.Join(root, "go.work"), "go 1.26.0\nuse (\n.\n./library\n)\n")
	goFixtureFile(t, filepath.Join(root, "library", "go.mod"), "module example.com/library\ngo 1.26.0\n")
	library := filepath.Join(root, "library", "library.go")
	goFixtureFile(t, library, "package library\nconst Value = 1\n")
	file := filepath.Join(root, "main.go")
	content := "package main\nimport (\"fmt\"; \"example.com/library\")\nfunc answer() int { return 42 }\nfunc main() { fmt.Println(answer() + library.Value) }\n"
	goFixtureFile(t, file, content)
	bot, err := s.saveBot(domain.Bot{Handle: "language", Roots: []string{root}, Plugins: map[string]bool{"shell": false}})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := s.Plugins.Get("go")
	if binary := os.Getenv("ECHO_GOPLS"); binary != "" {
		m.Config["goplsExecutable"] = binary
		s.Plugins.Save(m)
	} else {
		m.Config["internet"] = true
		s.Plugins.Save(m)
		nativeGo(t, s, bot, root, "install", "golang.org/x/tools/gopls@"+gotool.GoplsVersion)
		m.Config["internet"] = false
		s.Plugins.Save(m)
	}
	m, _ = s.Plugins.Get("go")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	events := make(chan languageservice.Message, 128)
	g, err := s.startGoLanguage(ctx, bot, m, root, true, func(m languageservice.Message) {
		select {
		case events <- m:
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { g.close(); <-g.done }()
	ref, err := g.fs.ReferenceForHostPath(bot.ID, file)
	if err != nil {
		t.Fatal(err)
	}
	uri := (&url.URL{Scheme: "echo-workspace", Host: ref.RootID, Path: "/" + ref.Path}).String()
	notify := func(method string, params any) {
		raw, _ := json.Marshal(params)
		if err := g.notifyEditor(ctx, method, raw); err != nil {
			t.Fatal(err)
		}
	}
	notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "go", "version": 1, "text": strings.Replace(content, "return 42", "return missing", 1)}})
	deadline := time.NewTimer(40 * time.Second)
	defer deadline.Stop()
	found := false
	for !found {
		select {
		case event := <-events:
			if event.Method == "textDocument/publishDiagnostics" && strings.Contains(string(event.Params), "missing") {
				found = true
			}
		case <-deadline.C:
			t.Fatal("unsaved diagnostic missing", g.log())
		}
	}
	raw, err := g.callEditor(ctx, "textDocument/documentSymbol", json.RawMessage(`{"textDocument":{"uri":"`+uri+`"}}`))
	if err != nil || !strings.Contains(string(raw), "answer") {
		t.Fatal(string(raw), err, g.log())
	}
	notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": 2}, "contentChanges": []any{map[string]any{"text": content}}})
	call := func(method string, params map[string]any) json.RawMessage {
		params["textDocument"] = map[string]any{"uri": uri}
		raw, _ := json.Marshal(params)
		result, e := g.callEditor(ctx, method, raw)
		if e != nil {
			t.Fatal(method, e, g.log())
		}
		return result
	}
	position := map[string]int{"line": 3, "character": strings.Index(strings.Split(content, "\n")[3], "answer") + 1}
	if result := call("textDocument/definition", map[string]any{"position": position}); !strings.Contains(string(result), "echo-workspace") {
		t.Fatal("definition", string(result))
	}
	if result := call("textDocument/hover", map[string]any{"position": position}); !strings.Contains(string(result), "answer") {
		t.Fatal("hover", string(result))
	}
	if result := call("textDocument/references", map[string]any{"position": position, "context": map[string]any{"includeDeclaration": true}}); !strings.Contains(string(result), "echo-workspace") {
		t.Fatal("references", string(result))
	}
	if result := call("textDocument/rename", map[string]any{"position": position, "newName": "renamedAnswer"}); !strings.Contains(string(result), "renamedAnswer") || !strings.Contains(string(result), "revisions") {
		t.Fatal("rename", string(result))
	}
	if disk, _ := os.ReadFile(file); string(disk) != content {
		t.Fatal("rename changed saved files")
	}
	if result := call("textDocument/formatting", map[string]any{"options": map[string]any{"tabSize": 4, "insertSpaces": false}}); !strings.Contains(string(result), "edit") {
		t.Fatal("formatting", string(result))
	}
	fmtPosition := map[string]int{"line": 3, "character": strings.Index(strings.Split(content, "\n")[3], "Println") + 1}
	if result := call("textDocument/definition", map[string]any{"position": fmtPosition}); !strings.Contains(string(result), "echo-go") {
		t.Fatal("standard library navigation", string(result))
	}
	inspect := func(a map[string]any) string {
		payload, _ := json.Marshal(a)
		value, e := s.Plugins.Invoke(ctx, domain.ExecutionScope{Bot: bot}, "go_inspect", payload)
		if e != nil {
			t.Fatal(e)
		}
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
	value := inspect(map[string]any{"action": "diagnostics", "directory": root, "files": []string{file}})
	if strings.Contains(value, "missing") || !strings.Contains(value, `"incomplete":false`) {
		t.Fatal("editor overlay leaked into bot analysis", value)
	}
	goFixtureFile(t, file, strings.Replace(content, "return 42", "return savedMissing", 1))
	value = inspect(map[string]any{"action": "diagnostics", "directory": root, "files": []string{file}})
	if !strings.Contains(value, "savedMissing") {
		t.Fatal(value)
	}
	goFixtureFile(t, file, content)
	value = inspect(map[string]any{"action": "diagnostics", "directory": root, "files": []string{file}})
	if strings.Contains(value, "savedMissing") {
		t.Fatal("stale diagnostic after fix", value)
	}
	goFixtureFile(t, library, "package library\nconst Value = \"changed type\"\n")
	value = inspect(map[string]any{"action": "diagnostics", "directory": root, "files": []string{file}})
	if !strings.Contains(value, "mismatched types") {
		t.Fatal("imported saved change was not synchronized", value)
	}
	// Immediate grant revocation cancels the process and waits for ACL cleanup.
	m.Enabled = false
	if err = s.Plugins.Save(m); err != nil {
		t.Fatal(err)
	}
	select {
	case <-g.done:
	case <-time.After(10 * time.Second):
		t.Fatal("disablement did not stop language server")
	}
}
