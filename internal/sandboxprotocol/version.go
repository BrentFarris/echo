// Package sandboxprotocol identifies the protocol built into Echo's sandbox
// services. Images embed this version; they must not take it from the host's
// environment, which would hide incompatible images.
package sandboxprotocol

import (
	_ "embed"
	"strings"
)

//go:embed version.txt
var version string

// Version is shared by Echo, the agent, the browser image, and image publishing.
var Version = strings.TrimSpace(version)
