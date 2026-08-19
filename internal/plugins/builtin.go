package plugins

import (
	"embed"
	"io/fs"
)

//go:embed builtin/calculator
var builtinPluginFiles embed.FS

func BuiltinPackages() map[string]fs.FS {
	calculator, err := fs.Sub(builtinPluginFiles, "builtin/calculator")
	if err != nil {
		return map[string]fs.FS{}
	}
	return map[string]fs.FS{"calculator": calculator}
}
