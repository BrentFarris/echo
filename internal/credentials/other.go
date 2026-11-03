//go:build !windows

package credentials

import (
	"os"
	"path/filepath"
)

// Servers need not have a logged-in desktop or an unlocked session keychain.
func New(profile ...string) SecretStore {
	dir := ""
	if len(profile) > 0 {
		dir = profile[0]
	}
	if dir == "" {
		base, _ := os.UserConfigDir()
		dir = filepath.Join(base, "Echo2")
	}
	return NewFileStore(filepath.Join(dir, "secrets"))
}
