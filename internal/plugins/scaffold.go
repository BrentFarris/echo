package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type ScaffoldOptions struct {
	Template    string
	ID          string
	Name        string
	Description string
}

type ScaffoldResult struct {
	Path      string   `json:"path"`
	Template  string   `json:"template"`
	PluginID  string   `json:"pluginId"`
	Files     []string `json:"files"`
	NextSteps []string `json:"nextSteps"`
}

func Scaffold(destination string, options ScaffoldOptions) (ScaffoldResult, error) {
	options.Template = strings.TrimSpace(options.Template)
	options.ID = strings.TrimSpace(options.ID)
	options.Name = strings.TrimSpace(options.Name)
	options.Description = strings.TrimSpace(options.Description)
	if options.Template != "ui-only" && options.Template != "tool-only" && options.Template != "hybrid" {
		return ScaffoldResult{}, fmt.Errorf("template must be ui-only, tool-only, or hybrid")
	}
	if !pluginIDPattern.MatchString(options.ID) || len(options.ID) > 64 {
		return ScaffoldResult{}, fmt.Errorf("plugin id must be lowercase kebab-case")
	}
	if options.Name == "" || len(options.Name) > 100 {
		return ScaffoldResult{}, fmt.Errorf("plugin name is required and must be 100 characters or fewer")
	}
	if options.Description == "" {
		options.Description = options.Name + " extension for Echo"
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return ScaffoldResult{}, err
	}
	if entries, readErr := os.ReadDir(destination); readErr == nil && len(entries) > 0 {
		return ScaffoldResult{}, fmt.Errorf("plugin destination must be new or empty")
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return ScaffoldResult{}, readErr
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return ScaffoldResult{}, err
	}
	result := ScaffoldResult{Path: destination, Template: options.Template, PluginID: options.ID}
	write := func(relative, content string) error {
		path, pathErr := packagePath(destination, relative)
		if pathErr != nil {
			return pathErr
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
		result.Files = append(result.Files, filepath.ToSlash(relative))
		return nil
	}

	manifest := Manifest{ManifestVersion: 1, ID: options.ID, Name: options.Name, Version: "0.1.0", Description: options.Description, Echo: Compatibility{API: "^1"}, Permissions: []Permission{}}
	if options.Template == "ui-only" || options.Template == "hybrid" {
		manifest.Contributes.Views = []ViewContribution{{ID: "main", Kind: "page", Title: options.Name, Icon: "assets/icon.svg", Entry: "ui/main/index.html"}}
	}
	if options.Template == "tool-only" || options.Template == "hybrid" {
		manifest.Runtime = &Runtime{Protocol: RPCProtocol, Targets: map[string]RuntimeTarget{
			"windows-amd64": {Path: filepath.ToSlash(filepath.Join("backend", "windows-amd64", options.ID+".exe"))},
			"linux-amd64":   {Path: filepath.ToSlash(filepath.Join("backend", "linux-amd64", options.ID))},
			"darwin-amd64":  {Path: filepath.ToSlash(filepath.Join("backend", "darwin-amd64", options.ID))},
			"darwin-arm64":  {Path: filepath.ToSlash(filepath.Join("backend", "darwin-arm64", options.ID))},
		}}
		method := "tools.example"
		manifest.Contributes.Tools = []ToolContribution{{
			Name: strings.ReplaceAll(options.ID, "-", "_") + "_example", Description: "Run the example plugin operation.",
			InputSchema:  map[string]any{"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string"}}, "required": []any{"message"}, "additionalProperties": false},
			OutputSchema: map[string]any{"type": "object"}, Method: method, TimeoutSeconds: 30, ReadOnly: true,
		}}
		if options.Template == "hybrid" {
			manifest.Contributes.RPC = []RPCContribution{{Method: "example.ping", TimeoutSeconds: 15}}
		}
	}
	manifestData, _ := json.MarshalIndent(manifest, "", "  ")
	if err := write(ManifestFileName, string(manifestData)+"\n"); err != nil {
		return ScaffoldResult{}, err
	}
	if err := write("README.md", fmt.Sprintf("# %s\n\n%s\n\nThis package targets Echo Plugin API v1. Review requested permissions and build artifacts before staging.\n", options.Name, options.Description)); err != nil {
		return ScaffoldResult{}, err
	}
	if err := write("LICENSE", "Choose and include a license before sharing this plugin.\n"); err != nil {
		return ScaffoldResult{}, err
	}
	if options.Template == "ui-only" || options.Template == "hybrid" {
		for relative, content := range scaffoldUIFiles(options) {
			if err := write(relative, content); err != nil {
				return ScaffoldResult{}, err
			}
		}
	}
	if options.Template == "tool-only" || options.Template == "hybrid" {
		for relative, content := range scaffoldBackendFiles(options) {
			if err := write(relative, content); err != nil {
				return ScaffoldResult{}, err
			}
		}
		result.NextSteps = append(result.NextSteps, "Run go test ./backend-src and build every declared backend target before validation (or remove targets you do not publish).")
	}
	result.NextSteps = append(result.NextSteps, "Implement and test the package, run echo_plugin_validate, then echo_plugin_stage for owner review.")
	sort.Strings(result.Files)
	return result, nil
}

func scaffoldUIFiles(options ScaffoldOptions) map[string]string {
	return map[string]string{
		"assets/icon.svg": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M12 3v18M3 12h18"/><circle cx="12" cy="12" r="8"/></svg>` + "\n",
		"ui/main/index.html": `<!doctype html>
<html lang="en"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>` + htmlText(options.Name) + `</title><link rel="stylesheet" href="style.css"></head>
<body><main><h1>` + htmlText(options.Name) + `</h1><p id="status">Connected to an isolated Echo plugin view.</p><button id="ping" type="button">Save a visit</button></main><script type="module" src="app.js"></script></body></html>
`,
		"ui/main/style.css": `:root{font-family:ui-sans-serif,system-ui;color:var(--echo-text,#eee);background:var(--echo-background,#121214)}*{box-sizing:border-box}body{margin:0;padding:32px}main{max-width:760px;margin:auto;padding:24px;border:1px solid var(--echo-border,#343139);border-radius:14px;background:var(--echo-surface,#1b1b1e)}button{padding:9px 13px;border:0;border-radius:8px;color:var(--echo-accent-contrast,#fff);background:var(--echo-accent,#60a5fa);cursor:pointer}
`,
		"ui/main/app.js": `let host=null;let sequence=0;const pending=new Map();
function request(method,params={}){if(!host)return Promise.reject(new Error("Echo bridge is not ready"));const id="request-"+(++sequence);parent.postMessage({type:"echo-plugin-request",nonce:host.nonce,pluginId:host.pluginId,viewId:host.viewId,id,method,params},"*");return new Promise((resolve,reject)=>pending.set(id,{resolve,reject}));}
addEventListener("message",event=>{if(event.source!==parent||!event.data)return;const message=event.data;if(message.type==="echo-plugin-init"){host=message;for(const [key,value] of Object.entries(message.theme||{}))document.documentElement.style.setProperty(key,value);return;}if(!host||message.nonce!==host.nonce||message.pluginId!==host.pluginId||message.viewId!==host.viewId)return;if(message.type==="echo-plugin-theme"){for(const [key,value] of Object.entries(message.theme||{}))document.documentElement.style.setProperty(key,value);}else if(message.type==="echo-plugin-response"){const call=pending.get(message.id);if(call){pending.delete(message.id);message.error?call.reject(new Error(message.error)):call.resolve(message.result);}}});
document.querySelector("#ping").addEventListener("click",async()=>{await request("storage.set",{scope:"workspace",key:"last-visit",value:new Date().toISOString()});document.querySelector("#status").textContent="Visit saved in namespaced workspace storage.";});
parent.postMessage({type:"echo-plugin-ready",protocol:"echo-ui-bridge-1"},"*");
`,
	}
}

func scaffoldBackendFiles(options ScaffoldOptions) map[string]string {
	return map[string]string{
		"backend-src/go.mod": "module example.com/" + options.ID + "\n\ngo 1.24\n",
		"backend-src/main.go": `package main

import (
 "bufio"
 "encoding/json"
 "fmt"
 "os"
)

type request struct { JSONRPC string ` + "`json:\"jsonrpc\"`" + `; ID json.RawMessage ` + "`json:\"id\"`" + `; Method string ` + "`json:\"method\"`" + `; Params json.RawMessage ` + "`json:\"params\"`" + ` }
func respond(id json.RawMessage, result any, callErr error) { response:=map[string]any{"jsonrpc":"2.0","id":id}; if callErr!=nil { response["error"]=map[string]any{"code":-32000,"message":callErr.Error()} } else { response["result"]=result }; _=json.NewEncoder(os.Stdout).Encode(response) }
func main(){ scanner:=bufio.NewScanner(os.Stdin); scanner.Buffer(make([]byte,64*1024),8*1024*1024); for scanner.Scan(){ var call request; if json.Unmarshal(scanner.Bytes(),&call)!=nil||call.Method=="" { continue }; if len(call.ID)==0 { continue }; switch call.Method { case "echo.initialize": respond(call.ID,map[string]any{"protocol":"echo-jsonrpc-1"},nil); case "echo.shutdown": respond(call.ID,map[string]any{"ok":true},nil); return; case "tools.example": var params struct { Arguments map[string]any ` + "`json:\"arguments\"`" + ` }; _=json.Unmarshal(call.Params,&params); respond(call.ID,map[string]any{"message":params.Arguments["message"]},nil); case "example.ping": respond(call.ID,map[string]any{"ok":true},nil); default: respond(call.ID,nil,fmt.Errorf("method not found")) } }; if err:=scanner.Err();err!=nil { fmt.Fprintln(os.Stderr,err) } }
`,
		"backend-src/main_test.go": `package main
import "testing"
func TestProtocolConstant(t *testing.T) { if "echo-jsonrpc-1" == "" { t.Fatal("protocol missing") } }
`,
		".github/workflows/build-plugin.yml": `name: Build Echo plugin package
on: [push, pull_request, workflow_dispatch]
permissions:
  contents: read
jobs:
  package:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.24.x"
          cache-dependency-path: backend-src/go.mod
      - run: go test ./...
        working-directory: backend-src
      - name: Build declared targets
        shell: bash
        run: |
          set -euo pipefail
          for target in windows-amd64 linux-amd64 darwin-amd64 darwin-arm64; do
            goos="${target%-*}"
            goarch="${target#*-}"
            executable="` + options.ID + `"
            if [ "$goos" = windows ]; then executable="` + options.ID + `.exe"; fi
            mkdir -p "backend/$target"
            (cd backend-src && GOOS="$goos" GOARCH="$goarch" go build -trimpath -o "../backend/$target/$executable" .)
          done
      - name: Assemble shareable package
        shell: bash
        run: |
          set -euo pipefail
          mkdir -p dist/package
          cp echo-plugin.json README.md LICENSE dist/package/
          for item in assets ui backend; do
            if [ -e "$item" ]; then cp -R "$item" dist/package/; fi
          done
          tar -czf dist/echo-plugin-` + options.ID + `.tar.gz -C dist/package .
      - uses: actions/upload-artifact@v4
        with:
          name: echo-plugin-` + options.ID + `
          path: dist/echo-plugin-` + options.ID + `.tar.gz
          if-no-files-found: error
`,
	}
}

func htmlText(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return value
}
