// Package sandbox owns Echo's opt-in, per-workspace Linux sandbox lifecycle.
// Portable policy remains in .echo/workspace.json; everything in this package
// is machine-local runtime state or an in-memory credential/lease.
package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/brent/echo/internal/workspaces"
)

const (
	ProtocolVersion = "1"
	SetupRecipePath = ".echo/sandbox/setup.sh"
)

// Release builds replace these source-build defaults with immutable digest
// references. Source builds use the public channel for this protocol version
// so the one-click installers can pull compatible images without requiring a
// local Docker build.
var (
	WorkbenchImage = "ghcr.io/brentfarris/echo-sandbox-workbench:protocol-1"
	DesktopImage   = "ghcr.io/brentfarris/echo-sandbox-desktop:protocol-1"
	GatewayImage   = "ghcr.io/brentfarris/echo-sandbox-egress:protocol-1"
)

type State string

const (
	StateDisabled    State = "disabled"
	StateUnavailable State = "unavailable"
	StatePulling     State = "pulling"
	StateCreating    State = "creating"
	StateStarting    State = "starting"
	StateReady       State = "ready"
	StateStopping    State = "stopping"
	StateStopped     State = "stopped"
	StateError       State = "error"
)

type LeaseOwner string

const (
	LeaseNone LeaseOwner = "none"
	LeaseAI   LeaseOwner = "ai"
	LeaseUser LeaseOwner = "user"
)

var (
	ErrDisabled          = &Error{Code: "sandbox_disabled", Message: "the workspace sandbox is disabled"}
	ErrUnavailable       = &Error{Code: "sandbox_unavailable", Message: "Docker is unavailable or incompatible"}
	ErrProtocolMismatch  = &Error{Code: "sandbox_protocol_mismatch", Message: "sandbox images are incompatible with this Echo build; recreate the sandbox"}
	ErrUserControlActive = &Error{Code: "user_control_active", Message: "desktop control belongs to the user"}
	ErrControlConflict   = &Error{Code: "desktop_control_conflict", Message: "desktop control belongs to another session"}
	ErrSetupApproval     = &Error{Code: "setup_approval_required", Message: "the changed sandbox setup recipe requires owner approval"}
	ErrPolicyTransition  = &Error{Code: "sandbox_transitioning", Message: "the workspace execution target is changing; retry after the transition completes"}
)

// Error carries a stable public failure code without leaking daemon details.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Cause   error  `json:"-"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Cause }

func Wrap(code, message string, cause error) error {
	if cause == nil {
		return nil
	}
	return &Error{Code: code, Message: message, Cause: cause}
}

func ErrorCode(err error) string {
	var sandboxError *Error
	if errors.As(err, &sandboxError) {
		return sandboxError.Code
	}
	return "sandbox_error"
}

type ImageSet struct {
	Workbench string `json:"workbench"`
	Desktop   string `json:"desktop"`
	Gateway   string `json:"gateway"`
}

func BuildImages() ImageSet {
	return ImageSet{Workbench: WorkbenchImage, Desktop: DesktopImage, Gateway: GatewayImage}
}

func (images ImageSet) Roles() map[string]string {
	return map[string]string{"workbench": images.Workbench, "desktop": images.Desktop, "gateway": images.Gateway}
}

func (images ImageSet) Immutable() bool {
	for _, reference := range images.Roles() {
		if !strings.Contains(reference, "@sha256:") {
			return false
		}
	}
	return true
}

type ImageStatus struct {
	Reference string `json:"reference"`
	Present   bool   `json:"present"`
	ID        string `json:"id,omitempty"`
}

type HostStatus struct {
	Available       bool                   `json:"available"`
	Supported       bool                   `json:"supported"`
	LinuxEngine     bool                   `json:"linuxEngine"`
	Architecture    string                 `json:"architecture,omitempty"`
	OperatingSystem string                 `json:"operatingSystem,omitempty"`
	ServerVersion   string                 `json:"serverVersion,omitempty"`
	ProtocolVersion string                 `json:"protocolVersion"`
	ImagesImmutable bool                   `json:"imagesImmutable"`
	Images          map[string]ImageStatus `json:"images"`
	ErrorCode       string                 `json:"errorCode,omitempty"`
	Message         string                 `json:"message,omitempty"`
}

type ResourceUsage struct {
	CPUNanos        uint64 `json:"cpuNanos,omitempty"`
	MemoryBytes     uint64 `json:"memoryBytes,omitempty"`
	MemoryLimit     uint64 `json:"memoryLimitBytes,omitempty"`
	DiskBytes       uint64 `json:"diskBytes,omitempty"`
	ActiveProcesses int    `json:"activeProcesses,omitempty"`
}

type SetupStatus struct {
	RecipeDigest   string    `json:"recipeDigest,omitempty"`
	ApprovedDigest string    `json:"approvedDigest,omitempty"`
	State          string    `json:"state,omitempty"`
	LastRole       string    `json:"lastRole,omitempty"`
	LastRunAt      time.Time `json:"lastRunAt,omitempty"`
	ExitCode       int       `json:"exitCode,omitempty"`
	Message        string    `json:"message,omitempty"`
}

type DesktopLease struct {
	Owner            LeaseOwner `json:"owner"`
	ChatTurnID       string     `json:"chatTurnId,omitempty"`
	BrowserSessionID string     `json:"browserSessionId,omitempty"`
	ExpiresAt        time.Time  `json:"expiresAt,omitempty"`
	Revision         uint64     `json:"revision"`
}

type SandboxStatus struct {
	State           State         `json:"state"`
	Enabled         bool          `json:"enabled"`
	ErrorCode       string        `json:"errorCode,omitempty"`
	Message         string        `json:"message,omitempty"`
	ImageVersion    ImageSet      `json:"imageVersion"`
	ProtocolVersion string        `json:"protocolVersion"`
	Resources       ResourceUsage `json:"resources"`
	Setup           SetupStatus   `json:"setup"`
	ActiveViewers   int           `json:"activeViewers"`
	ControlOwner    LeaseOwner    `json:"controlOwner"`
	DesktopLease    DesktopLease  `json:"desktopLease"`
	UpdatedAt       time.Time     `json:"updatedAt"`
}

type NetworkGrant struct {
	ID           string    `json:"id"`
	Host         string    `json:"host"`
	Port         int       `json:"port"`
	Label        string    `json:"label"`
	SandboxAlias string    `json:"sandboxAlias,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

type DesktopSession struct {
	ID             string    `json:"id"`
	BrowserSession string    `json:"browserSessionId"`
	ViewOnly       bool      `json:"viewOnly"`
	CreatedAt      time.Time `json:"createdAt"`
	ExpiresAt      time.Time `json:"expiresAt"`
	Credential     string    `json:"credential,omitempty"`
}

type Event struct {
	Type        string         `json:"type"`
	WorkspaceID string         `json:"workspaceId"`
	Event       string         `json:"event"`
	Status      *SandboxStatus `json:"status,omitempty"`
	Message     string         `json:"message,omitempty"`
	Progress    int            `json:"progress,omitempty"`
	Role        string         `json:"role,omitempty"`
	Stream      string         `json:"stream,omitempty"`
	Data        string         `json:"data,omitempty"`
	At          time.Time      `json:"at"`
}

type RootMount struct {
	ID        string `json:"id"`
	HostPath  string `json:"hostPath"`
	GuestPath string `json:"guestPath"`
	Main      bool   `json:"main"`
}

type WorkspaceSpec struct {
	ID           string
	Config       workspaces.SandboxConfig
	Roots        []RootMount
	SetupPath    string
	Installation string
}

type MachineState struct {
	Version             int               `json:"version"`
	WorkspaceID         string            `json:"workspaceId"`
	ProtocolVersion     string            `json:"protocolVersion"`
	Images              ImageSet          `json:"images"`
	ApprovedSetupDigest string            `json:"approvedSetupDigest,omitempty"`
	LastSetup           SetupStatus       `json:"lastSetup,omitempty"`
	NetworkGrants       []NetworkGrant    `json:"networkGrants,omitempty"`
	VolumeNames         map[string]string `json:"volumeNames"`
	ContainerNames      map[string]string `json:"containerNames"`
	NetworkName         string            `json:"networkName"`
	UpdatedAt           time.Time         `json:"updatedAt"`
}

type RuntimeSecrets struct {
	WorkbenchAgentToken string
	DesktopAgentToken   string
	VNCToken            string
	ProxyToken          string
	BrowserToken        string
}

type ExecRequest struct {
	Role             string
	Command          []string
	WorkingDirectory string
	Environment      []string
	Input            []byte
	OutputLimit      int
	TTY              bool
	Root             bool
	Columns          int
	Rows             int
}

type ExecResult struct {
	ExitCode        int
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool
}

type PTY interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	Wait() (int, error)
	Kill() error
}

type Process interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Wait() (int, error)
	Kill() error
}

type DeleteScope struct {
	Containers bool
	Network    bool
	Workbench  bool
	Desktop    bool
	Browser    bool
	Exchange   bool
}

type DesktopActionRequest struct {
	Action     string `json:"action"`
	X          int    `json:"x,omitempty"`
	Y          int    `json:"y,omitempty"`
	X2         int    `json:"x2,omitempty"`
	Y2         int    `json:"y2,omitempty"`
	Button     int    `json:"button,omitempty"`
	Clicks     int    `json:"clicks,omitempty"`
	DeltaX     int    `json:"deltaX,omitempty"`
	DeltaY     int    `json:"deltaY,omitempty"`
	DurationMS int    `json:"durationMs,omitempty"`
	Text       string `json:"text,omitempty"`
	Key        string `json:"key,omitempty"`
}

// Engine is the provider-neutral boundary. DockerEngine is the v1 provider;
// fakes exercise orchestration without a daemon in unit tests.
type Engine interface {
	Host(context.Context, ImageSet) HostStatus
	ProbeWorkspace(context.Context, WorkspaceSpec) error
	Pull(context.Context, ImageSet, func(role, message string, progress int)) error
	Ensure(context.Context, WorkspaceSpec, MachineState, RuntimeSecrets) (MachineState, error)
	Start(context.Context, MachineState) error
	UpdateResources(context.Context, MachineState, workspaces.SandboxConfig, workspaces.SandboxConfig) error
	Stop(context.Context, MachineState) error
	Delete(context.Context, MachineState, DeleteScope) error
	Exec(context.Context, MachineState, ExecRequest) (ExecResult, error)
	OpenPTY(context.Context, MachineState, ExecRequest) (PTY, error)
	OpenProcess(context.Context, MachineState, ExecRequest) (Process, error)
	Usage(context.Context, MachineState) (ResourceUsage, error)
	ApplyNetworkGrants(context.Context, MachineState, []NetworkGrant) error
	Heartbeat(context.Context, MachineState) error
	OpenDesktop(context.Context, MachineState) (io.ReadWriteCloser, error)
	BrowserCall(context.Context, MachineState, string, json.RawMessage) (json.RawMessage, error)
	DesktopAction(context.Context, MachineState, DesktopActionRequest) error
	DesktopScreenshot(context.Context, MachineState) ([]byte, string, error)
	ProxyEndpoint(context.Context, MachineState) (string, error)
	Reconcile(context.Context, string, string, []string) error
	Close() error
}

type unavailableEngine struct{ cause error }

func NewUnavailableEngine(cause error) Engine { return &unavailableEngine{cause: cause} }

func (e *unavailableEngine) failure() error {
	return &Error{Code: ErrUnavailable.Code, Message: ErrUnavailable.Message, Cause: e.cause}
}

func (e *unavailableEngine) Host(context.Context, ImageSet) HostStatus {
	return HostStatus{ProtocolVersion: ProtocolVersion, ErrorCode: ErrUnavailable.Code, Message: ErrUnavailable.Message}
}
func (e *unavailableEngine) ProbeWorkspace(context.Context, WorkspaceSpec) error { return e.failure() }
func (e *unavailableEngine) Pull(context.Context, ImageSet, func(string, string, int)) error {
	return e.failure()
}
func (e *unavailableEngine) Ensure(context.Context, WorkspaceSpec, MachineState, RuntimeSecrets) (MachineState, error) {
	return MachineState{}, e.failure()
}
func (e *unavailableEngine) Start(context.Context, MachineState) error { return e.failure() }
func (e *unavailableEngine) UpdateResources(context.Context, MachineState, workspaces.SandboxConfig, workspaces.SandboxConfig) error {
	return e.failure()
}
func (e *unavailableEngine) Stop(context.Context, MachineState) error { return e.failure() }
func (e *unavailableEngine) Delete(context.Context, MachineState, DeleteScope) error {
	return e.failure()
}
func (e *unavailableEngine) Exec(context.Context, MachineState, ExecRequest) (ExecResult, error) {
	return ExecResult{}, e.failure()
}
func (e *unavailableEngine) OpenPTY(context.Context, MachineState, ExecRequest) (PTY, error) {
	return nil, e.failure()
}
func (e *unavailableEngine) OpenProcess(context.Context, MachineState, ExecRequest) (Process, error) {
	return nil, e.failure()
}
func (e *unavailableEngine) Usage(context.Context, MachineState) (ResourceUsage, error) {
	return ResourceUsage{}, e.failure()
}
func (e *unavailableEngine) ApplyNetworkGrants(context.Context, MachineState, []NetworkGrant) error {
	return e.failure()
}
func (e *unavailableEngine) Heartbeat(context.Context, MachineState) error { return e.failure() }
func (e *unavailableEngine) OpenDesktop(context.Context, MachineState) (io.ReadWriteCloser, error) {
	return nil, e.failure()
}
func (e *unavailableEngine) BrowserCall(context.Context, MachineState, string, json.RawMessage) (json.RawMessage, error) {
	return nil, e.failure()
}
func (e *unavailableEngine) DesktopAction(context.Context, MachineState, DesktopActionRequest) error {
	return e.failure()
}
func (e *unavailableEngine) DesktopScreenshot(context.Context, MachineState) ([]byte, string, error) {
	return nil, "", e.failure()
}
func (e *unavailableEngine) ProxyEndpoint(context.Context, MachineState) (string, error) {
	return "", e.failure()
}
func (e *unavailableEngine) Reconcile(context.Context, string, string, []string) error {
	return e.failure()
}
func (e *unavailableEngine) Close() error { return nil }

func ValidateNetworkGrant(grant NetworkGrant) error {
	host := strings.TrimSpace(grant.Host)
	if host == "" {
		return fmt.Errorf("host is required")
	}
	if len(host) > 253 || !validExactNetworkHost(host, true) {
		return fmt.Errorf("host must be one exact hostname or IP address")
	}
	if grant.Port < 1 || grant.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	label := strings.TrimSpace(grant.Label)
	if label == "" || len(label) > 100 || strings.ContainsAny(label, "\r\n\x00") {
		return fmt.Errorf("label is required and must be at most 100 characters")
	}
	alias := strings.TrimSpace(grant.SandboxAlias)
	if alias != "" {
		if len(alias) > 253 || !validExactNetworkHost(alias, false) {
			return fmt.Errorf("sandboxAlias is not a valid hostname")
		}
		reserved := strings.ToLower(strings.TrimSuffix(alias, "."))
		if reserved == "gateway" || reserved == "workbench" || reserved == "desktop" || reserved == "localhost" || strings.HasSuffix(reserved, ".echo.internal") {
			return fmt.Errorf("sandboxAlias conflicts with a reserved sandbox hostname")
		}
	}
	return nil
}

func validExactNetworkHost(value string, allowIP bool) bool {
	value = strings.TrimSuffix(strings.TrimSpace(value), ".")
	if value == "" || strings.ContainsAny(value, "*/\\[]:") {
		if allowIP {
			_, err := netip.ParseAddr(strings.Trim(value, "[]"))
			return err == nil
		}
		return false
	}
	if allowIP {
		if _, err := netip.ParseAddr(value); err == nil {
			return true
		}
	}
	for _, part := range strings.Split(value, ".") {
		if len(part) == 0 || len(part) > 63 || part[0] == '-' || part[len(part)-1] == '-' {
			return false
		}
		for _, character := range part {
			if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-') {
				return false
			}
		}
	}
	return true
}

// HTTPClientProvider is implemented by Manager for tools that must traverse
// the sandbox egress gateway instead of using the host network.
type HTTPClientProvider interface {
	HTTPClient(context.Context, string, time.Duration) (*http.Client, error)
}
