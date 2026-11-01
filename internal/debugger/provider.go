package debugger

import (
	"context"
	"encoding/json"
	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/editorfs"
	"io"
)

// Provider is a trusted, host-registered language plugin contribution. The
// editor never chooses an executable or implements language-specific behavior.
type Provider interface {
	Metadata() debugconfig.AdapterProfile
	Configure(debugconfig.Configuration) (debugconfig.Configuration, []debugconfig.Input)
	Prepare(context.Context, LaunchContext, debugconfig.Configuration) (*Launch, error)
}
type LaunchContext struct {
	RunInTerminal                 func(context.Context, map[string]any) (map[string]any, error)
	StopTerminals                 func()
	SessionID, BotID, CurrentFile string
	Roots                         []editorfs.Root
	Inputs                        map[string]string
	SelectedText                  string
	NoDebug                       bool
}
type Launch struct {
	Arguments  map[string]any
	Start      func(context.Context, func(string, string)) (*Adapter, error)
	Cleanup    func()
	Step       func(context.Context, StepClient, string, map[string]any) (*StopEvent, string, error)
	Expression func(Frame, string) string
}
type Adapter struct {
	Transport io.ReadWriteCloser
	Stop      func()
}
type Frame struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	PC     string `json:"instructionPointerReference"`
	Source Source `json:"source"`
}
type Source struct {
	Name      string `json:"name,omitempty"`
	Path      string `json:"path,omitempty"`
	Reference int    `json:"sourceReference,omitempty"`
}
type StopEvent struct {
	Reason   string          `json:"reason"`
	ThreadID int             `json:"threadId"`
	Body     json.RawMessage `json:"-"`
}
type StepClient interface {
	Request(context.Context, string, map[string]any) (json.RawMessage, error)
	Execute(context.Context, string, map[string]any) (*StopEvent, error)
	Frames(context.Context, int, int) ([]Frame, error)
	Source(context.Context, Source) (string, error)
}
type Options struct {
	FS             *editorfs.Service
	State          *debugconfig.StateStore
	Resolve        func(string) error
	Configurations func(string) (debugconfig.WorkspaceConfig, error)
	Provider       func(string, string) (Provider, error)
	Lease          func(context.Context, string, string) (context.Context, func(), error)
	AllowedSource  func(string) bool
}
