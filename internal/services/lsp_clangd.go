package services

import (
	"path/filepath"
	"strings"
)

func init() {
	registerLSPLanguage(lspLanguageDefinition{
		ID:          "cpp",
		DisplayName: "C/C++",
		Extensions: []string{
			".c",
			".cc",
			".cpp",
			".cxx",
			".c++",
			".h",
			".hh",
			".hpp",
			".hxx",
			".ipp",
			".inl",
			".ixx",
			".cppm",
		},
		WorkspaceMarkers: []string{
			".clangd",
			"CMakeLists.txt",
			"Makefile",
			"compile_commands.json",
			"compile_flags.txt",
			"meson.build",
			"build/compile_commands.json",
		},
		Command:            lspServerCommand{name: "clangd"},
		DocumentLanguageID: clangdDocumentLanguageID,
	})
}

func clangdDocumentLanguageID(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".c":
		return "c"
	default:
		return "cpp"
	}
}
