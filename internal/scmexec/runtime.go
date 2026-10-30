// Package scmexec is the dependency boundary between VCS adapters and Echo's
// scoped process broker. Adapters never launch commands on the host directly.
package scmexec

import "context"

const OutputFileToken = "__ECHO_SCM_OUTPUT_FILE__"

type Request struct {
	CaptureOutputFile bool
	Command           []string
	WorkingDirectory  string
	Environment       []string
	Input             []byte
	OutputLimit       int
}
type Result struct {
	Stdout, Stderr  []byte
	ExitCode        int
	StdoutTruncated bool
}
type Runtime struct {
	ValidateFossilRepository func(context.Context, string, string, string) (string, error)
	Execute                  func(context.Context, string, Request) (Result, error)
	Available                func(string, string) error
	Defaults                 func(string, string) map[string]string
	ValidatePath             func(string, string) (string, error)
}
type Workspace struct{ SearchParentRepositories, SearchParentGitRepositories bool }
type Workspaces struct {
	Get func(string) (Workspace, bool, error)
}
