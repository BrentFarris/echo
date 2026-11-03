// Package buildinfo identifies the source compiled into the running executable.
package buildinfo

import "runtime/debug"

// Revision is supplied by the native build scripts, never by a checkout at runtime.
var Revision string

func Current() string {
	if Revision != "" {
		return Revision
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	revision, dirty := "unknown", false
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			revision = setting.Value
		}
		if setting.Key == "vcs.modified" {
			dirty = setting.Value == "true"
		}
	}
	if dirty {
		return revision + "-modified"
	}
	return revision
}
