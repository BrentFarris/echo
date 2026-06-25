package services

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/brent/echo/internal/tools"
)

func TestClangdLSPLanguageRegistration(t *testing.T) {
	for _, path := range []string{
		"main.c",
		"main.cc",
		"main.cpp",
		"main.cxx",
		"main.c++",
		"header.h",
		"header.hh",
		"header.hpp",
		"header.hxx",
		"detail.ipp",
		"detail.inl",
		"module.ixx",
		"module.cppm",
	} {
		languageID, ok := lspLanguageIDForPath(path)
		if !ok || languageID != "cpp" {
			t.Fatalf("expected %s to use clangd-backed cpp LSP language, got %q, %v", path, languageID, ok)
		}
	}
	command, ok := registeredLSPCommandForLanguage("cpp")
	if !ok || command.name != "clangd" {
		t.Fatalf("expected C/C++ LSP command to be clangd, got %#v, %v", command, ok)
	}
	if got := lspDocumentLanguageIDForPath("cpp", "main.c"); got != "c" {
		t.Fatalf("expected .c documents to open as c, got %q", got)
	}
	if got := lspDocumentLanguageIDForPath("cpp", "main.cpp"); got != "cpp" {
		t.Fatalf("expected .cpp documents to open as cpp, got %q", got)
	}
	if got := lspLanguageDisplayName("cpp"); got != "C/C++" {
		t.Fatalf("expected C/C++ display name, got %q", got)
	}
}

func TestSystemServiceFindWorkspaceFileDefinitionWithClangd(t *testing.T) {
	if os.Getenv("ECHO_RUN_CLANGD_INTEGRATION") != "1" {
		t.Skip("set ECHO_RUN_CLANGD_INTEGRATION=1 to run the real clangd integration test")
	}
	if _, err := exec.LookPath("clangd"); err != nil {
		t.Skipf("clangd was not found on PATH: %v", err)
	}
	base := os.Getenv("ECHO_CLANGD_INTEGRATION_DIR")
	if base == "" {
		if runtime.GOOS == "windows" {
			base = filepath.Join(`C:\tmp`, "echo-clangd-integration")
		} else {
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			base = filepath.Join(cwd, "..", "..", ".gotmp", "clangd-integration")
		}
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "workspace-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(root)
	})

	service := NewSystemServiceWithStorePath(filepath.Join(root, "state.json"))
	state, err := service.AddWorkspace(root)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID
	defer service.Shutdown()

	if err := os.WriteFile(filepath.Join(root, "compile_flags.txt"), []byte("-std=c++17\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	content := "int helper() { return 41; }\n\nint main() {\n\treturn helper();\n}\n"
	if err := os.WriteFile(filepath.Join(root, "main.cpp"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	filePath := labeledTestPath(t, service, workspaceID, "main.cpp")
	callPosition := utf16Length(content[:strings.LastIndex(content, "helper()")+len("helper")])

	definition, err := service.FindWorkspaceFileDefinition(workspaceID, WorkspaceDefinitionRequest{
		FilePath: filePath,
		Content:  content,
		Position: callPosition,
	})
	if err != nil {
		t.Fatalf("find C++ definition: %v", err)
	}
	if !definition.Found || definition.TargetPath != filePath || definition.Line != 0 {
		t.Fatalf("expected helper definition in main.cpp line 0, got %#v", definition)
	}

	workspace, _, err := service.workspaceAndSettings(workspaceID)
	if err != nil {
		t.Fatalf("workspace settings: %v", err)
	}
	symbols, err := service.codeNavigator(workspace).QueryCode(context.Background(), tools.CodeNavigationRequest{
		Operation:  "document_symbols",
		Path:       filePath,
		MaxResults: 20,
	})
	if err != nil {
		t.Fatalf("query C++ symbols: %v", err)
	}
	if !symbols.Found || !codeSymbolsContain(symbols.Symbols, "helper") {
		t.Fatalf("expected helper document symbol, got %#v", symbols.Symbols)
	}
}

func codeSymbolsContain(symbols []tools.CodeSymbol, name string) bool {
	for _, symbol := range symbols {
		if symbol.Name == name {
			return true
		}
	}
	return false
}
