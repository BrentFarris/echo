package plugins

import (
	"embed"
	"io/fs"
)

//go:embed builtin/calculator builtin/notes
var builtinPluginFiles embed.FS

func BuiltinPackages() map[string]fs.FS {
	packages := map[string]fs.FS{}
	for _, id := range []string{"calculator", "notes"} {
		plugin, err := fs.Sub(builtinPluginFiles, "builtin/"+id)
		if err == nil {
			packages[id] = plugin
		}
	}
	return packages
}
