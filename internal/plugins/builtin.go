package plugins

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed builtin/calculator builtin/notes builtin/bookmarks
var builtinPluginFiles embed.FS

func BuiltinPackages() map[string]fs.FS {
	packages := map[string]fs.FS{}
	for _, id := range []string{"calculator", "notes", "bookmarks"} {
		plugin, err := fs.Sub(builtinPluginFiles, "builtin/"+id)
		if err == nil {
			packages[id] = plugin
		}
	}
	return packages
}

// Native Code views are owned by Echo, never supplied by installed JavaScript.
func validateBuiltinContributions(manifest Manifest, source Source) error {
	for _, view := range manifest.Contributes.Views {
		if view.Kind == "code-sidebar" && (manifest.ID != "bookmarks" || view.ID != "bookmarks" || source.Type != "builtin" || source.Builtin != "bookmarks") {
			return fmt.Errorf("code-sidebar views are reserved for built-in Bookmarks")
		}
	}
	return nil
}
