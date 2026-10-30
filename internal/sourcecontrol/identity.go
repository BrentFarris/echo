package sourcecontrol

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"runtime"
	"strings"
)

// RepositoryID returns an opaque, provider-qualified checkout identity.
func RepositoryID(workspaceID, providerID, root string) string {
	identity := filepath.Clean(root)
	if runtime.GOOS == "windows" {
		identity = strings.ToLower(identity)
	}
	digest := sha256.Sum256([]byte(workspaceID + "\x00" + providerID + "\x00" + identity))
	return hex.EncodeToString(digest[:16])
}
