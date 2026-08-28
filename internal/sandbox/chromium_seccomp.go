package sandbox

import _ "embed"

// chromiumSeccompJSON starts with Moby's v0.1.0 default profile and adds only
// clone, setns, and unshare for Chromium's user-namespace sandbox. Keeping the
// profile embedded makes Windows Docker Desktop and Linux Engine behavior
// identical without relying on a daemon-host file path.
//
//go:embed chromium-seccomp.json
var chromiumSeccompJSON string
