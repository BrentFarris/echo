package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/workspaces"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/gorilla/websocket"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	agentPort                 = "7777/tcp"
	vncPort                   = "5900/tcp"
	playwrightPort            = "3000/tcp"
	gatewayProxyPort          = "3129/tcp"
	workbenchAgentForwardPort = "17777/tcp"
	desktopAgentForwardPort   = "27777/tcp"
	desktopVNCForwardPort     = "25900/tcp"
	desktopBrowserForwardPort = "23000/tcp"
)

type DockerEngine struct {
	client *client.Client
	mu     sync.Mutex
	secret map[string]RuntimeSecrets
}

func NewDockerEngine() (*DockerEngine, error) {
	apiClient, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("create Docker client: %w", err)
	}
	return &DockerEngine{client: apiClient, secret: make(map[string]RuntimeSecrets)}, nil
}

func (e *DockerEngine) Close() error { return e.client.Close() }

func (e *DockerEngine) Host(ctx context.Context, images ImageSet) HostStatus {
	status := HostStatus{ProtocolVersion: ProtocolVersion, Images: make(map[string]ImageStatus)}
	requestContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := e.client.Info(requestContext, client.InfoOptions{})
	if err != nil {
		status.ErrorCode, status.Message = "docker_unavailable", "Docker Engine is not available"
		return status
	}
	info := result.Info
	status.Available = true
	status.LinuxEngine = strings.EqualFold(info.OSType, "linux")
	status.Architecture = info.Architecture
	status.OperatingSystem = info.OperatingSystem
	status.ServerVersion = info.ServerVersion
	status.Supported = status.LinuxEngine && (strings.EqualFold(info.Architecture, "x86_64") || strings.EqualFold(info.Architecture, "amd64"))
	if !status.LinuxEngine {
		status.ErrorCode, status.Message = "docker_linux_engine_required", "Docker must be running Linux containers"
	} else if !status.Supported {
		status.ErrorCode, status.Message = "docker_architecture_unsupported", "The v1 sandbox requires a linux/amd64 Docker Engine"
	}
	for role, reference := range images.Roles() {
		imageStatus := ImageStatus{Reference: reference}
		if inspect, inspectErr := e.client.ImageInspect(requestContext, reference); inspectErr == nil {
			imageStatus.Present, imageStatus.ID = true, inspect.ID
		}
		status.Images[role] = imageStatus
	}
	return status
}

func (e *DockerEngine) ProbeWorkspace(ctx context.Context, spec WorkspaceSpec) error {
	name := deterministicPrefix(spec.Installation, spec.ID) + "-probe-" + fmt.Sprintf("%d", time.Now().UnixNano())
	mounts := workspaceDockerMounts(spec.Roots)
	commandParts := make([]string, 0, len(spec.Roots)*2)
	for _, root := range spec.Roots {
		commandParts = append(commandParts,
			"test -d '"+root.GuestPath+"'",
			"probe=$(mktemp '"+root.GuestPath+"/.echo-sandbox-write-probe.XXXXXX') && rm -f \"$probe\"",
		)
		if _, err := os.Stat(filepath.Join(root.HostPath, ".echo")); err == nil {
			commandParts = append(commandParts, "test ! -w '"+root.GuestPath+"/.echo'")
		}
	}
	created, err := e.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name, Platform: &ocispec.Platform{OS: "linux", Architecture: "amd64"},
		Config: &container.Config{
			Image: BuildImages().Workbench, User: strconv.Itoa(sandboxHostUID()) + ":1000",
			Entrypoint: []string{"/bin/bash", "-lc"}, Cmd: []string{strings.Join(commandParts, " && ")},
			Labels: ResourceLabels(spec.Installation, spec.ID, "probe", BuildImages().Workbench),
		},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(network.NetworkNone), Mounts: mounts, AutoRemove: false, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges=true"}},
	})
	if err != nil {
		return Wrap("workspace_mount_probe_failed", "Docker could not mount the workspace", err)
	}
	defer func() {
		_, _ = e.client.ContainerRemove(context.WithoutCancel(ctx), created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err := e.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return Wrap("workspace_mount_probe_failed", "Docker could not start the workspace mount probe", err)
	}
	waited := e.waitContainer(ctx, created.ID)
	if waited != 0 {
		return &Error{Code: "workspace_mount_probe_failed", Message: "Docker could not write the workspace mount or .echo was not read-only"}
	}
	return nil
}

func (e *DockerEngine) waitContainer(ctx context.Context, containerID string) int {
	for {
		inspect, err := e.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
		if err != nil {
			return -1
		}
		if !inspect.Container.State.Running {
			return inspect.Container.State.ExitCode
		}
		select {
		case <-ctx.Done():
			return -1
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (e *DockerEngine) Pull(ctx context.Context, images ImageSet, progress func(string, string, int)) error {
	for _, role := range []string{"gateway", "workbench", "desktop"} {
		reference := images.Roles()[role]
		if progress != nil {
			progress(role, "Pulling "+reference, 0)
		}
		response, err := e.client.ImagePull(ctx, reference, client.ImagePullOptions{Platforms: []ocispec.Platform{{OS: "linux", Architecture: "amd64"}}})
		if err != nil {
			return imagePullError(role, reference, err)
		}
		if err := response.Wait(ctx); err != nil {
			_ = response.Close()
			return imagePullError(role, reference, err)
		}
		_ = response.Close()
		if progress != nil {
			progress(role, role+" image is ready", 100)
		}
	}
	return nil
}

func imagePullError(role, reference string, cause error) error {
	message := fmt.Sprintf("Could not pull the %s sandbox image %q", role, reference)
	detail := strings.ToLower(cause.Error())
	switch {
	case strings.Contains(detail, "denied"), strings.Contains(detail, "unauthorized"):
		message += ": the registry denied access; verify that the image is published publicly"
	case strings.Contains(detail, "manifest unknown"), strings.Contains(detail, "not found"):
		message += ": the image or tag is not published"
	case errors.Is(cause, context.Canceled):
		message += ": the pull was canceled"
	case errors.Is(cause, context.DeadlineExceeded):
		message += ": the registry request timed out"
	default:
		message += ": Docker could not download it; check Docker's registry and proxy connectivity"
	}
	return Wrap("image_pull_failed", message, cause)
}

func (e *DockerEngine) Ensure(ctx context.Context, spec WorkspaceSpec, state MachineState, secrets RuntimeSecrets) (MachineState, error) {
	e.mu.Lock()
	previousSecrets := e.secret[spec.ID]
	e.mu.Unlock()
	freshCredentials := previousSecrets.WorkbenchAgentToken == ""
	if state.WorkspaceID == "" {
		state = DefaultMachineState(spec.Installation, spec.ID, BuildImages())
	}
	if state.ProtocolVersion != "" && state.ProtocolVersion != ProtocolVersion {
		return state, ErrProtocolMismatch
	}
	defaults := DefaultMachineState(spec.Installation, spec.ID, BuildImages())
	if state.VolumeNames == nil {
		state.VolumeNames = make(map[string]string)
	}
	for role, name := range defaults.VolumeNames {
		if state.VolumeNames[role] == "" {
			state.VolumeNames[role] = name
		}
	}
	if state.ContainerNames == nil {
		state.ContainerNames = make(map[string]string)
	}
	for role, name := range defaults.ContainerNames {
		if state.ContainerNames[role] == "" {
			state.ContainerNames[role] = name
		}
	}
	if state.NetworkName == "" {
		state.NetworkName = defaults.NetworkName
	}
	state.Images = BuildImages()
	state.ProtocolVersion = ProtocolVersion
	labels := func(role, image string) map[string]string {
		return ResourceLabels(spec.Installation, spec.ID, role, image)
	}
	for role, name := range state.VolumeNames {
		if _, err := e.client.VolumeInspect(ctx, name, client.VolumeInspectOptions{}); err == nil {
			continue
		} else if !cerrdefs.IsNotFound(err) {
			return state, Wrap("docker_volume_error", "Could not inspect sandbox volume", err)
		}
		if _, err := e.client.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Labels: labels(role, "")}); err != nil {
			return state, Wrap("docker_volume_error", "Could not create sandbox volume", err)
		}
	}
	if _, err := e.client.NetworkInspect(ctx, state.NetworkName, client.NetworkInspectOptions{}); err != nil {
		if !cerrdefs.IsNotFound(err) {
			return state, Wrap("docker_network_error", "Could not inspect sandbox network", err)
		}
		if err := e.createInternalNetwork(ctx, spec, state.NetworkName, labels("network", "")); err != nil {
			return state, Wrap("docker_network_error", "Could not create sandbox network", err)
		}
	}
	networkDetails, err := e.client.NetworkInspect(ctx, state.NetworkName, client.NetworkInspectOptions{})
	if err != nil {
		return state, Wrap("docker_network_error", "Could not inspect sandbox network addressing", err)
	}
	gatewayAddress, err := internalGatewayAddress(networkDetails.Network.IPAM.Config)
	if err != nil {
		return state, Wrap("docker_network_error", "Could not allocate sandbox DNS gateway address", err)
	}
	for _, role := range []string{"gateway", "workbench", "desktop"} {
		image := state.Images.Roles()[role]
		name := state.ContainerNames[role]
		if inspect, err := e.client.ContainerInspect(ctx, name, client.ContainerInspectOptions{}); err == nil {
			containerLabels := inspect.Container.Config.Labels
			if containerLabels[LabelProtocol] != ProtocolVersion || containerLabels[LabelImage] != image || containerLabels[LabelWorkspace] != spec.ID {
				return state, ErrProtocolMismatch
			}
			// Runtime credentials are deliberately not persisted. After an Echo
			// restart, stop compatible survivors so PID 1 (especially TigerVNC)
			// reloads the newly generated in-memory credentials on Start.
			if freshCredentials && inspect.Container.State.Running {
				timeout := 10
				if _, stopErr := e.client.ContainerStop(ctx, name, client.ContainerStopOptions{Timeout: &timeout}); stopErr != nil {
					return state, Wrap("docker_stop_failed", "Could not rotate sandbox runtime credentials", stopErr)
				}
			}
			resources := roleResources(role, spec.Config)
			if _, updateErr := e.client.ContainerUpdate(ctx, name, client.ContainerUpdateOptions{Resources: &resources}); updateErr != nil {
				return state, Wrap("docker_resource_update_failed", "Could not apply sandbox resource limits", updateErr)
			}
			continue
		} else if !cerrdefs.IsNotFound(err) {
			return state, Wrap("docker_container_error", "Could not inspect sandbox container", err)
		}
		configuration, hostConfiguration, networking, err := dockerContainerConfig(role, image, spec, state, gatewayAddress, labels(role, image))
		if err != nil {
			return state, err
		}
		if _, err := e.client.ContainerCreate(ctx, client.ContainerCreateOptions{
			Config: configuration, HostConfig: hostConfiguration, NetworkingConfig: networking,
			Platform: &ocispec.Platform{OS: "linux", Architecture: "amd64"}, Name: name,
		}); err != nil {
			return state, Wrap("docker_container_error", "Could not create the "+role+" sandbox container", err)
		}
		if role == "gateway" {
			if _, err := e.client.NetworkConnect(ctx, network.NetworkBridge, client.NetworkConnectOptions{Container: name, EndpointConfig: &network.EndpointSettings{GwPriority: 1}}); err != nil {
				return state, Wrap("docker_network_error", "Could not connect the egress gateway", err)
			}
		}
	}
	e.mu.Lock()
	e.secret[spec.ID] = secrets
	e.mu.Unlock()
	return state, nil
}

func (e *DockerEngine) createInternalNetwork(ctx context.Context, spec WorkspaceSpec, name string, resourceLabels map[string]string) error {
	var lastErr error
	for attempt := uint16(0); attempt < 64; attempt++ {
		subnet := sandboxNetworkSubnet(spec.Installation, spec.ID, attempt)
		gateway := subnet.Addr().Next()
		_, err := e.client.NetworkCreate(ctx, name, client.NetworkCreateOptions{
			Driver: "bridge", Internal: true, Labels: resourceLabels,
			IPAM: &network.IPAM{Config: []network.IPAMConfig{{Subnet: subnet, Gateway: gateway}}},
		})
		if err == nil {
			return nil
		}
		lastErr = err
		if !strings.Contains(strings.ToLower(err.Error()), "overlap") {
			return err
		}
	}
	return fmt.Errorf("could not allocate a non-overlapping private subnet: %w", lastErr)
}

func dockerContainerConfig(role, image string, spec WorkspaceSpec, state MachineState, gatewayAddress netip.Addr, labels map[string]string) (*container.Config, *container.HostConfig, *network.NetworkingConfig, error) {
	configuration := &container.Config{
		Image: image, Labels: labels, WorkingDir: mainGuestPath(spec.Roots),
		Env: []string{"ECHO_SANDBOX_PROTOCOL=" + ProtocolVersion, "ECHO_SANDBOX_ROLE=" + role},
	}
	host := &container.HostConfig{
		NetworkMode: container.NetworkMode(state.NetworkName), Privileged: false,
		CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges=true"},
		Resources: roleResources(role, spec.Config),
		Init:      boolPointer(true),
		Tmpfs:     map[string]string{"/run/echo": "rw,noexec,nosuid,size=16m", "/tmp": "rw,nosuid,size=1g"},
		LogConfig: container.LogConfig{Type: "local", Config: map[string]string{"max-size": "10m", "max-file": "2"}},
	}
	networking := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
		state.NetworkName: {Aliases: []string{role}},
	}}
	roleAddress, err := sandboxRoleAddress(gatewayAddress, role)
	if err != nil {
		return nil, nil, nil, err
	}
	networking.EndpointsConfig[state.NetworkName].IPAMConfig = &network.EndpointIPAMConfig{IPv4Address: roleAddress}
	if role == "gateway" {
		networking.EndpointsConfig[state.NetworkName].GwPriority = -1
	} else {
		host.DNS = []netip.Addr{gatewayAddress}
		host.DNSOptions = []string{"ndots:0"}
	}
	switch role {
	case "workbench":
		configuration.Env = append(configuration.Env, "ECHO_SANDBOX_UID="+strconv.Itoa(sandboxHostUID()))
		host.SecurityOpt = nil // setup.sh may intentionally use passwordless sudo.
		host.CapAdd = []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "NET_BIND_SERVICE", "SETFCAP", "SETGID", "SETUID", "SYS_CHROOT"}
		host.Mounts = workspaceDockerMounts(spec.Roots)
		host.Mounts = append(host.Mounts,
			mount.Mount{Type: mount.TypeVolume, Source: state.VolumeNames["workbench"], Target: "/home/echo", VolumeOptions: &mount.VolumeOptions{NoCopy: false}},
			mount.Mount{Type: mount.TypeVolume, Source: state.VolumeNames["exchange"], Target: "/exchange"},
		)
		configuration.Env = append(configuration.Env, proxyEnvironment()...)
		configuration.ExposedPorts = portSet(agentPort)
	case "desktop":
		configuration.Env = append(configuration.Env, "ECHO_SANDBOX_UID="+strconv.Itoa(sandboxHostUID()))
		host.SecurityOpt = []string{"no-new-privileges=true", "seccomp=" + chromiumSeccompJSON}
		host.CapAdd = []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "KILL", "SETGID", "SETUID", "SYS_CHROOT"}
		host.ShmSize = 1 << 30
		host.Mounts = workspaceDockerMounts(spec.Roots)
		host.Mounts = append(host.Mounts,
			mount.Mount{Type: mount.TypeVolume, Source: state.VolumeNames["desktop"], Target: "/home/echo", VolumeOptions: &mount.VolumeOptions{NoCopy: false}},
			mount.Mount{Type: mount.TypeVolume, Source: state.VolumeNames["browser"], Target: "/home/echo/.config/chromium"},
			mount.Mount{Type: mount.TypeVolume, Source: state.VolumeNames["exchange"], Target: "/exchange"},
		)
		configuration.Env = append(configuration.Env, proxyEnvironment()...)
		configuration.ExposedPorts = portSet(agentPort, vncPort, playwrightPort)
	case "gateway":
		workbenchAddress, _ := sandboxRoleAddress(gatewayAddress, "workbench")
		desktopAddress, _ := sandboxRoleAddress(gatewayAddress, "desktop")
		configuration.Env = append(configuration.Env,
			"ECHO_WORKBENCH_AGENT_TARGET="+net.JoinHostPort(workbenchAddress.String(), "7777"),
			"ECHO_DESKTOP_AGENT_TARGET="+net.JoinHostPort(desktopAddress.String(), "7777"),
			"ECHO_DESKTOP_VNC_TARGET="+net.JoinHostPort(desktopAddress.String(), "5900"),
			"ECHO_DESKTOP_BROWSER_TARGET="+net.JoinHostPort(desktopAddress.String(), "3000"),
		)
		host.ReadonlyRootfs = true
		host.CapAdd = []string{"NET_BIND_SERVICE"}
		host.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: state.VolumeNames["gateway"], Target: "/var/lib/echo-egress"}}
		configuration.WorkingDir = "/"
		configuration.ExposedPorts = portSet("1080/tcp", "1081/tcp", "3128/tcp", gatewayProxyPort, "53/tcp", "53/udp", workbenchAgentForwardPort, desktopAgentForwardPort, desktopVNCForwardPort, desktopBrowserForwardPort)
		host.PortBindings = localhostBindings("1081/tcp", gatewayProxyPort, workbenchAgentForwardPort, desktopAgentForwardPort, desktopVNCForwardPort, desktopBrowserForwardPort)
	default:
		return nil, nil, nil, fmt.Errorf("unknown sandbox role %q", role)
	}
	return configuration, host, networking, nil
}

func sandboxRoleAddress(gatewayAddress netip.Addr, role string) (netip.Addr, error) {
	switch role {
	case "gateway":
		return gatewayAddress, nil
	case "workbench":
		return gatewayAddress.Next(), nil
	case "desktop":
		return gatewayAddress.Next().Next(), nil
	default:
		return netip.Addr{}, fmt.Errorf("unknown sandbox role %q", role)
	}
}

func internalGatewayAddress(config []network.IPAMConfig) (netip.Addr, error) {
	for _, item := range config {
		if !item.Subnet.IsValid() || !item.Subnet.Addr().Is4() {
			continue
		}
		address := item.Subnet.Masked().Addr().Next().Next()
		if address.IsValid() && item.Subnet.Contains(address) {
			return address, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("internal network has no usable IPv4 subnet")
}

func roleResources(role string, config workspaces.SandboxConfig) container.Resources {
	totalCPU := int64(config.CPULimit) * 1_000_000_000
	totalMemory := int64(config.MemoryMiB) << 20
	gatewayCPU, gatewayMemory := int64(250_000_000), int64(256<<20)
	remainingCPU, remainingMemory := totalCPU-gatewayCPU, totalMemory-gatewayMemory
	if remainingCPU < 500_000_000 {
		remainingCPU = 500_000_000
	}
	if remainingMemory < 1024<<20 {
		remainingMemory = 1024 << 20
	}
	switch role {
	case "gateway":
		return container.Resources{NanoCPUs: gatewayCPU, Memory: gatewayMemory, PidsLimit: int64Pointer(128)}
	case "desktop":
		return container.Resources{NanoCPUs: remainingCPU * 35 / 100, Memory: remainingMemory * 40 / 100, PidsLimit: int64Pointer(2048)}
	default:
		return container.Resources{NanoCPUs: remainingCPU - remainingCPU*35/100, Memory: remainingMemory - remainingMemory*40/100, PidsLimit: int64Pointer(2048)}
	}
}

func workspaceDockerMounts(roots []RootMount) []mount.Mount {
	mounts := make([]mount.Mount, 0, len(roots)+1)
	for _, root := range roots {
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: root.HostPath, Target: root.GuestPath, BindOptions: &mount.BindOptions{Propagation: mount.PropagationRPrivate}})
		metadata := filepath.Join(root.HostPath, workspacesEchoDir())
		if info, err := os.Stat(metadata); err == nil && info.IsDir() {
			mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: metadata, Target: root.GuestPath + "/.echo", ReadOnly: true, BindOptions: &mount.BindOptions{Propagation: mount.PropagationRPrivate, ReadOnlyForceRecursive: true}})
		}
	}
	return mounts
}

func workspacesEchoDir() string { return ".echo" }

func proxyEnvironment() []string {
	return []string{
		"HTTP_PROXY=http://gateway:3128", "HTTPS_PROXY=http://gateway:3128",
		"ALL_PROXY=socks5h://gateway:1080", "NO_PROXY=localhost,127.0.0.1,::1,gateway,workbench,desktop",
	}
}

func portSet(values ...string) network.PortSet {
	ports := make(network.PortSet, len(values))
	for _, value := range values {
		if port, err := network.ParsePort(value); err == nil {
			ports[port] = struct{}{}
		}
	}
	return ports
}

func localhostBindings(values ...string) network.PortMap {
	bindings := make(network.PortMap, len(values))
	for _, value := range values {
		if port, err := network.ParsePort(value); err == nil {
			bindings[port] = []network.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1")}}
		}
	}
	return bindings
}

func boolPointer(value bool) *bool    { return &value }
func int64Pointer(value int64) *int64 { return &value }

func (e *DockerEngine) Start(ctx context.Context, state MachineState) error {
	for _, role := range []string{"gateway", "workbench", "desktop"} {
		name := state.ContainerNames[role]
		inspect, err := e.client.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
		if err != nil {
			return Wrap("docker_container_error", "Could not inspect the "+role+" container", err)
		}
		if !inspect.Container.State.Running {
			if _, err := e.client.ContainerStart(ctx, name, client.ContainerStartOptions{}); err != nil {
				return Wrap("docker_start_failed", "Could not start the "+role+" container", err)
			}
		}
	}
	e.mu.Lock()
	secrets := e.secret[state.WorkspaceID]
	e.mu.Unlock()
	if secrets.WorkbenchAgentToken == "" || secrets.DesktopAgentToken == "" || secrets.BrowserToken == "" {
		return &Error{Code: "sandbox_credentials_missing", Message: "sandbox runtime credentials are unavailable; recreate the sandbox"}
	}
	files := runtimeSecretFiles(secrets, state.NetworkGrants)
	for role, roleFiles := range files {
		if err := e.writeFilesWithExec(ctx, state.ContainerNames[role], roleFiles); err != nil {
			return Wrap("sandbox_secret_install_failed", "Could not install runtime credentials", err)
		}
	}
	if err := e.waitForServices(ctx, state); err != nil {
		return err
	}
	return e.Heartbeat(ctx, state)
}

func runtimeSecretFiles(secrets RuntimeSecrets, grants []NetworkGrant) map[string]map[string]secretFile {
	return map[string]map[string]secretFile{
		"gateway":   {"proxy.token": {data: []byte(secrets.ProxyToken), mode: 0o400}, "grants.json": {data: grantsJSON(grants), mode: 0o400}},
		"workbench": {"agent.token": {data: []byte(secrets.WorkbenchAgentToken), mode: 0o400}},
		"desktop":   {"agent.token": {data: []byte(secrets.DesktopAgentToken), mode: 0o400}, "vnc.password": {data: []byte(secrets.VNCToken), mode: 0o400}, "lease.token": {data: []byte(secrets.BrowserToken), mode: 0o400}},
	}
}

type secretFile struct {
	data []byte
	mode int64
}

// Runtime files live only in each container's /run/echo tmpfs. Delivering them
// over attached exec stdin works consistently with both read-only roots and
// Docker Desktop's internal-only networks, without putting secrets in env vars.
func (e *DockerEngine) writeFilesWithExec(ctx context.Context, containerName string, files map[string]secretFile) error {
	for name, file := range files {
		if path.Base(name) != name || name == "." || name == "" {
			return fmt.Errorf("invalid runtime file name %q", name)
		}
		destination := path.Join("/run/echo", name)
		created, err := e.client.ExecCreate(ctx, containerName, client.ExecCreateOptions{
			AttachStdin: true,
			Cmd:         []string{"/bin/sh", "-c", `umask 077; cat > "$1" && chmod "$2" "$1"`, "echo-secret", destination, strconv.FormatInt(file.mode, 8)},
		})
		if err != nil {
			return err
		}
		attached, err := e.client.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
		if err != nil {
			return err
		}
		_, writeErr := io.Copy(attached.Conn, bytes.NewReader(file.data))
		closeErr := attached.CloseWrite()
		_, readErr := io.Copy(io.Discard, attached.Reader)
		attached.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		if readErr != nil {
			return readErr
		}
		result, err := e.client.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("runtime file installer exited with code %d", result.ExitCode)
		}
	}
	return nil
}

func grantsJSON(grants []NetworkGrant) []byte { data, _ := json.Marshal(grants); return data }

func (e *DockerEngine) Stop(ctx context.Context, state MachineState) error {
	timeout := 10
	var joined error
	for _, role := range []string{"desktop", "workbench", "gateway"} {
		name := state.ContainerNames[role]
		inspect, err := e.client.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
		if cerrdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			joined = errors.Join(joined, err)
			continue
		}
		if inspect.Container.State.Running {
			if _, err := e.client.ContainerStop(ctx, name, client.ContainerStopOptions{Timeout: &timeout}); err != nil && !cerrdefs.IsNotFound(err) {
				joined = errors.Join(joined, err)
			}
		}
	}
	if joined != nil {
		return Wrap("docker_stop_failed", "Could not stop every sandbox container", joined)
	}
	return nil
}

func (e *DockerEngine) UpdateResources(ctx context.Context, state MachineState, previous, next workspaces.SandboxConfig) error {
	updated := make([]string, 0, 3)
	for _, role := range []string{"gateway", "workbench", "desktop"} {
		resources := roleResources(role, next)
		if _, err := e.client.ContainerUpdate(ctx, state.ContainerNames[role], client.ContainerUpdateOptions{Resources: &resources}); err != nil {
			for _, rollbackRole := range updated {
				rollback := roleResources(rollbackRole, previous)
				_, _ = e.client.ContainerUpdate(context.WithoutCancel(ctx), state.ContainerNames[rollbackRole], client.ContainerUpdateOptions{Resources: &rollback})
			}
			return Wrap("docker_resource_update_failed", "Could not apply sandbox resource limits", err)
		}
		updated = append(updated, role)
	}
	return nil
}

func (e *DockerEngine) Delete(ctx context.Context, state MachineState, scope DeleteScope) error {
	roles := []string{}
	if scope.Containers {
		switch {
		case scope.Workbench && !scope.Browser:
			roles = []string{"workbench"}
		case scope.Browser && !scope.Workbench:
			roles = []string{"desktop"}
		default:
			roles = []string{"desktop", "workbench", "gateway"}
		}
	}
	var joined error
	for _, role := range roles {
		if _, err := e.client.ContainerRemove(ctx, state.ContainerNames[role], client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			joined = errors.Join(joined, err)
		}
	}
	volumeRoles := []string{}
	if scope.Workbench {
		volumeRoles = append(volumeRoles, "workbench")
	}
	if scope.Desktop {
		volumeRoles = append(volumeRoles, "desktop")
	}
	if scope.Browser {
		volumeRoles = append(volumeRoles, "browser")
	}
	if scope.Exchange {
		volumeRoles = append(volumeRoles, "exchange")
	}
	if scope.Network && scope.Workbench && scope.Desktop && scope.Browser && scope.Exchange {
		volumeRoles = append(volumeRoles, "gateway")
	}
	for _, role := range volumeRoles {
		if _, err := e.client.VolumeRemove(ctx, state.VolumeNames[role], client.VolumeRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			joined = errors.Join(joined, err)
		}
	}
	if scope.Network {
		if _, err := e.client.NetworkRemove(ctx, state.NetworkName, client.NetworkRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
			joined = errors.Join(joined, err)
		}
	}
	if joined != nil {
		return Wrap("sandbox_delete_failed", "Could not delete all requested sandbox resources", joined)
	}
	e.mu.Lock()
	delete(e.secret, state.WorkspaceID)
	e.mu.Unlock()
	return nil
}

func (e *DockerEngine) Exec(ctx context.Context, state MachineState, request ExecRequest) (ExecResult, error) {
	role := request.Role
	if role == "" {
		role = "workbench"
	}
	if state.ContainerNames[role] == "" {
		return ExecResult{}, &Error{Code: "invalid_sandbox_role", Message: "sandbox execution role is invalid"}
	}
	if len(request.Command) == 0 {
		return ExecResult{}, fmt.Errorf("command is required")
	}
	limit := request.OutputLimit
	if limit <= 0 {
		limit = 256 << 10
	}
	if limit > 4<<20 {
		limit = 4 << 20
	}
	timeoutSeconds := 0
	if deadline, ok := ctx.Deadline(); ok {
		timeoutSeconds = max(1, int(time.Until(deadline).Seconds()+0.999))
	}
	payload, err := json.Marshal(struct {
		Command        []string `json:"command"`
		Dir            string   `json:"dir,omitempty"`
		Env            []string `json:"env,omitempty"`
		Input          []byte   `json:"input,omitempty"`
		OutputLimit    int      `json:"outputLimit"`
		TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
		Root           bool     `json:"root,omitempty"`
	}{request.Command, request.WorkingDirectory, request.Environment, request.Input, limit, timeoutSeconds, request.Root})
	if err != nil {
		return ExecResult{}, err
	}
	token := e.agentToken(state.WorkspaceID, role)
	response, _, err := e.serviceRequest(ctx, state, role, agentPort, http.MethodPost, "/v1/exec", token, payload, int64(limit*3+(64<<10)))
	if err != nil {
		if ctx.Err() != nil {
			return ExecResult{ExitCode: -1}, ctx.Err()
		}
		return ExecResult{}, err
	}
	var result struct {
		ExitCode        int    `json:"exitCode"`
		Stdout          []byte `json:"stdout"`
		Stderr          []byte `json:"stderr"`
		StdoutTruncated bool   `json:"stdoutTruncated"`
		StderrTruncated bool   `json:"stderrTruncated"`
	}
	if err := json.Unmarshal(response, &result); err != nil {
		return ExecResult{}, Wrap("sandbox_protocol_error", "sandbox agent returned an invalid exec response", err)
	}
	return ExecResult(result), nil
}

func (e *DockerEngine) OpenPTY(ctx context.Context, state MachineState, request ExecRequest) (PTY, error) {
	if len(request.Command) == 0 {
		request.Command = []string{"/bin/bash", "-l"}
	}
	connection, err := e.agentWebSocket(ctx, state, "workbench", "/v1/pty")
	if err != nil {
		return nil, err
	}
	if err := connection.WriteJSON(map[string]any{
		"command": request.Command, "dir": request.WorkingDirectory, "env": request.Environment,
		"cols": request.Columns, "rows": request.Rows, "root": request.Root,
	}); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return newAgentPTY(connection), nil
}

func (e *DockerEngine) OpenProcess(ctx context.Context, state MachineState, request ExecRequest) (Process, error) {
	role := request.Role
	if role == "" {
		role = "workbench"
	}
	containerName := state.ContainerNames[role]
	if containerName == "" || len(request.Command) == 0 {
		return nil, &Error{Code: "invalid_sandbox_process", Message: "sandbox process request is invalid"}
	}
	connection, err := e.agentWebSocket(ctx, state, role, "/v1/process")
	if err != nil {
		return nil, err
	}
	if err := connection.WriteJSON(map[string]any{"command": request.Command, "dir": request.WorkingDirectory, "env": request.Environment, "root": request.Root}); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return newAgentProcess(ctx, connection)
}

func (e *DockerEngine) agentToken(workspaceID, role string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if role == "desktop" {
		return e.secret[workspaceID].DesktopAgentToken
	}
	return e.secret[workspaceID].WorkbenchAgentToken
}

func (e *DockerEngine) agentWebSocket(ctx context.Context, state MachineState, role, requestPath string) (*websocket.Conn, error) {
	token := e.agentToken(state.WorkspaceID, role)
	if token == "" {
		return nil, &Error{Code: "sandbox_credentials_missing", Message: "sandbox runtime credentials are unavailable; recreate the sandbox"}
	}
	endpoint, err := e.endpointForPort(ctx, state, role, agentPort)
	if err != nil {
		return nil, err
	}
	headers := http.Header{"Authorization": []string{"Bearer " + token}}
	dialer := websocket.Dialer{Proxy: nil, HandshakeTimeout: 5 * time.Second, ReadBufferSize: 32 << 10, WriteBufferSize: 32 << 10}
	connection, response, err := dialer.DialContext(ctx, "ws://"+endpoint+requestPath, headers)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, Wrap("sandbox_agent_unavailable", "sandbox agent stream is unavailable", err)
	}
	return connection, nil
}

type processResult struct {
	code int
	err  error
}

type agentPTY struct {
	connection *websocket.Conn
	reader     io.Reader
	writeMu    sync.Mutex
	exitOnce   sync.Once
	exit       chan processResult
}

func newAgentPTY(connection *websocket.Conn) *agentPTY {
	return &agentPTY{connection: connection, exit: make(chan processResult, 1)}
}
func (p *agentPTY) finish(result processResult) {
	p.exitOnce.Do(func() { p.exit <- result; close(p.exit) })
}
func (p *agentPTY) Read(data []byte) (int, error) {
	for {
		if p.reader != nil {
			count, err := p.reader.Read(data)
			if errors.Is(err, io.EOF) {
				p.reader = nil
				if count > 0 {
					return count, nil
				}
				continue
			}
			return count, err
		}
		messageType, message, err := p.connection.ReadMessage()
		if err != nil {
			p.finish(processResult{code: -1, err: err})
			return 0, err
		}
		if messageType == websocket.BinaryMessage {
			p.reader = bytes.NewReader(message)
			continue
		}
		var event struct {
			Type     string `json:"type"`
			ExitCode int    `json:"exitCode"`
			Error    string `json:"error"`
		}
		if json.Unmarshal(message, &event) != nil {
			continue
		}
		if event.Type == "exit" {
			p.finish(processResult{code: event.ExitCode})
			return 0, io.EOF
		}
		if event.Type == "error" {
			err := errors.New(event.Error)
			p.finish(processResult{code: -1, err: err})
			return 0, err
		}
	}
}
func (p *agentPTY) write(messageType int, data []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return p.connection.WriteMessage(messageType, data)
}
func (p *agentPTY) Write(data []byte) (int, error) {
	if err := p.write(websocket.BinaryMessage, data); err != nil {
		return 0, err
	}
	return len(data), nil
}
func (p *agentPTY) Close() error {
	_ = p.write(websocket.TextMessage, []byte(`{"type":"kill"}`))
	err := p.connection.Close()
	p.finish(processResult{code: -1, err: io.ErrClosedPipe})
	return err
}
func (p *agentPTY) Resize(cols, rows int) error {
	payload, _ := json.Marshal(map[string]any{"type": "resize", "cols": cols, "rows": rows})
	return p.write(websocket.TextMessage, payload)
}
func (p *agentPTY) Wait() (int, error) {
	result := <-p.exit
	return result.code, result.err
}
func (p *agentPTY) Kill() error { return p.Close() }

type agentProcess struct {
	connection            *websocket.Conn
	stdin                 *agentProcessStdin
	stdout, stderr        *io.PipeReader
	stdoutWriter          *io.PipeWriter
	stderrWriter          *io.PipeWriter
	writeMu               sync.Mutex
	finishOnce, closeOnce sync.Once
	done                  chan processResult
	started               chan error
}

type agentProcessStdin struct {
	process *agentProcess
	once    sync.Once
}

func newAgentProcess(ctx context.Context, connection *websocket.Conn) (*agentProcess, error) {
	stdout, stdoutWriter := io.Pipe()
	stderr, stderrWriter := io.Pipe()
	process := &agentProcess{
		connection: connection, stdout: stdout, stderr: stderr, stdoutWriter: stdoutWriter, stderrWriter: stderrWriter,
		done: make(chan processResult, 1), started: make(chan error, 1),
	}
	process.stdin = &agentProcessStdin{process: process}
	go process.read()
	select {
	case err := <-process.started:
		if err != nil {
			_ = connection.Close()
			return nil, err
		}
		return process, nil
	case <-ctx.Done():
		_ = connection.Close()
		return nil, ctx.Err()
	case <-time.After(10 * time.Second):
		_ = connection.Close()
		return nil, &Error{Code: "sandbox_agent_unavailable", Message: "sandbox process did not start"}
	}
}

func (p *agentProcess) read() {
	started := false
	defer func() {
		if !started {
			select {
			case p.started <- errors.New("sandbox process stream closed before start"):
			default:
			}
		}
	}()
	for {
		messageType, message, err := p.connection.ReadMessage()
		if err != nil {
			p.finish(processResult{code: -1, err: err})
			return
		}
		if messageType == websocket.BinaryMessage {
			if len(message) < 1 {
				continue
			}
			var writeErr error
			switch message[0] {
			case 1:
				_, writeErr = p.stdoutWriter.Write(message[1:])
			case 2:
				_, writeErr = p.stderrWriter.Write(message[1:])
			}
			if writeErr != nil {
				p.finish(processResult{code: -1, err: writeErr})
				return
			}
			continue
		}
		var event struct {
			Type     string `json:"type"`
			ExitCode int    `json:"exitCode"`
			Error    string `json:"error"`
		}
		if json.Unmarshal(message, &event) != nil {
			continue
		}
		switch event.Type {
		case "started":
			if !started {
				started = true
				p.started <- nil
			}
		case "exit":
			p.finish(processResult{code: event.ExitCode})
			return
		case "error":
			err := errors.New(event.Error)
			if !started {
				started = true
				p.started <- err
			}
			p.finish(processResult{code: -1, err: err})
			return
		}
	}
}

func (p *agentProcess) finish(result processResult) {
	p.finishOnce.Do(func() {
		_ = p.stdoutWriter.CloseWithError(result.err)
		_ = p.stderrWriter.CloseWithError(result.err)
		p.done <- result
		close(p.done)
	})
}
func (p *agentProcess) write(messageType int, data []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return p.connection.WriteMessage(messageType, data)
}
func (p *agentProcess) close() error {
	var err error
	p.closeOnce.Do(func() { err = p.connection.Close() })
	return err
}
func (p *agentProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *agentProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *agentProcess) Stderr() io.ReadCloser { return p.stderr }
func (p *agentProcess) Wait() (int, error) {
	result := <-p.done
	_ = p.close()
	return result.code, result.err
}
func (p *agentProcess) Kill() error {
	_ = p.write(websocket.TextMessage, []byte(`{"type":"kill"}`))
	err := p.close()
	p.finish(processResult{code: -1, err: context.Canceled})
	return err
}
func (w *agentProcessStdin) Write(data []byte) (int, error) {
	frame := make([]byte, len(data)+1)
	copy(frame[1:], data)
	if err := w.process.write(websocket.BinaryMessage, frame); err != nil {
		return 0, err
	}
	return len(data), nil
}
func (w *agentProcessStdin) Close() error {
	var err error
	w.once.Do(func() { err = w.process.write(websocket.TextMessage, []byte(`{"type":"close_stdin"}`)) })
	return err
}

func (e *DockerEngine) Usage(ctx context.Context, state MachineState) (ResourceUsage, error) {
	var usage ResourceUsage
	for _, role := range []string{"workbench", "desktop", "gateway"} {
		stats, err := e.client.ContainerStats(ctx, state.ContainerNames[role], client.ContainerStatsOptions{Stream: false})
		if err != nil {
			if cerrdefs.IsNotFound(err) {
				continue
			}
			return usage, err
		}
		var sample container.StatsResponse
		err = json.NewDecoder(stats.Body).Decode(&sample)
		_ = stats.Body.Close()
		if err != nil {
			return usage, err
		}
		usage.CPUNanos += sample.CPUStats.CPUUsage.TotalUsage
		usage.MemoryBytes += sample.MemoryStats.Usage
		usage.MemoryLimit += sample.MemoryStats.Limit
		usage.ActiveProcesses += int(sample.PidsStats.Current)
	}
	// Docker's cross-platform volume API does not expose usage on ordinary
	// inspect calls. Ask the daemon for verbose disk usage, then sum only this
	// workspace's named volumes and writable container layers.
	if disk, err := e.client.DiskUsage(ctx, client.DiskUsageOptions{Containers: true, Volumes: true, Verbose: true}); err == nil {
		volumeNames := make(map[string]bool, len(state.VolumeNames))
		for _, name := range state.VolumeNames {
			volumeNames[name] = true
		}
		for _, volume := range disk.Volumes.Items {
			if volumeNames[volume.Name] && volume.UsageData != nil && volume.UsageData.Size > 0 {
				usage.DiskBytes += uint64(volume.UsageData.Size)
			}
		}
		containerNames := make(map[string]bool, len(state.ContainerNames))
		for _, name := range state.ContainerNames {
			containerNames["/"+name] = true
		}
		for _, summary := range disk.Containers.Items {
			for _, name := range summary.Names {
				if containerNames[name] && summary.SizeRw > 0 {
					usage.DiskBytes += uint64(summary.SizeRw)
					break
				}
			}
		}
	}
	return usage, nil
}

func (e *DockerEngine) ApplyNetworkGrants(ctx context.Context, state MachineState, grants []NetworkGrant) error {
	inspect, err := e.client.ContainerInspect(ctx, state.ContainerNames["gateway"], client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) || (err == nil && !inspect.Container.State.Running) {
		return nil
	}
	if err != nil {
		return err
	}
	return e.writeFilesWithExec(ctx, state.ContainerNames["gateway"], map[string]secretFile{"grants.json": {data: grantsJSON(grants), mode: 0o400}})
}

func (e *DockerEngine) Heartbeat(ctx context.Context, state MachineState) error {
	var joined error
	for _, role := range []string{"workbench", "desktop"} {
		_, _, err := e.serviceRequest(ctx, state, role, agentPort, http.MethodPost, "/v1/heartbeat", e.agentToken(state.WorkspaceID, role), nil, 64<<10)
		if err != nil {
			joined = errors.Join(joined, err)
		}
	}
	created, err := e.client.ExecCreate(ctx, state.ContainerNames["gateway"], client.ExecCreateOptions{Cmd: []string{"/bin/touch", "/run/echo/heartbeat"}})
	if err != nil {
		joined = errors.Join(joined, err)
	} else if _, err := e.client.ExecStart(ctx, created.ID, client.ExecStartOptions{Detach: true}); err != nil {
		joined = errors.Join(joined, err)
	}
	return joined
}

func (e *DockerEngine) OpenDesktop(ctx context.Context, state MachineState) (io.ReadWriteCloser, error) {
	endpoint, err := e.endpointForPort(ctx, state, "desktop", vncPort)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	return dialer.DialContext(ctx, "tcp", endpoint)
}

func (e *DockerEngine) BrowserCall(ctx context.Context, state MachineState, method string, params json.RawMessage) (json.RawMessage, error) {
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	payload, err := json.Marshal(struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}{Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	token := e.secret[state.WorkspaceID].BrowserToken
	e.mu.Unlock()
	response, _, err := e.serviceRequest(ctx, state, "desktop", playwrightPort, http.MethodPost, "/v1/call", token, payload, 8<<20)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Code  string          `json:"code"`
		Error string          `json:"error"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return nil, Wrap("browser_protocol_error", "browser bridge returned an invalid response", err)
	}
	if !envelope.OK {
		if envelope.Code == "" {
			envelope.Code = "browser_action_failed"
		}
		if envelope.Error == "" {
			envelope.Error = "browser action failed"
		}
		return nil, &Error{Code: envelope.Code, Message: envelope.Error}
	}
	return envelope.Data, nil
}

func (e *DockerEngine) DesktopAction(ctx context.Context, state MachineState, action DesktopActionRequest) error {
	payload, err := json.Marshal(action)
	if err != nil {
		return err
	}
	e.mu.Lock()
	token := e.secret[state.WorkspaceID].DesktopAgentToken
	e.mu.Unlock()
	_, _, err = e.serviceRequest(ctx, state, "desktop", agentPort, http.MethodPost, "/v1/desktop/action", token, payload, 64<<10)
	return err
}

func (e *DockerEngine) DesktopScreenshot(ctx context.Context, state MachineState) ([]byte, string, error) {
	e.mu.Lock()
	token := e.secret[state.WorkspaceID].DesktopAgentToken
	e.mu.Unlock()
	data, mediaType, err := e.serviceRequest(ctx, state, "desktop", agentPort, http.MethodGet, "/v1/screenshot", token, nil, (5<<20)+1)
	if err != nil {
		return nil, "", err
	}
	if len(data) > 5<<20 {
		return nil, "", &Error{Code: "screenshot_too_large", Message: "desktop screenshot exceeds the 5 MiB limit"}
	}
	mediaType = strings.Split(mediaType, ";")[0]
	if mediaType != "image/png" && mediaType != "image/jpeg" {
		return nil, "", &Error{Code: "desktop_protocol_error", Message: "desktop returned an unsupported screenshot format"}
	}
	return data, mediaType, nil
}

func (e *DockerEngine) serviceRequest(ctx context.Context, state MachineState, role, portName, method, requestPath, token string, payload []byte, limit int64) ([]byte, string, error) {
	if token == "" {
		return nil, "", &Error{Code: "sandbox_credentials_missing", Message: "sandbox runtime credentials are unavailable; recreate the sandbox"}
	}
	endpoint, err := e.endpointForPort(ctx, state, role, portName)
	if err != nil {
		return nil, "", err
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://"+endpoint+requestPath, bytes.NewReader(payload))
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if len(payload) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return nil, "", Wrap("sandbox_service_unavailable", "sandbox service is unavailable", err)
	}
	defer response.Body.Close()
	if limit <= 0 {
		limit = 1 << 20
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > limit {
		return nil, "", &Error{Code: "sandbox_response_too_large", Message: "sandbox service response exceeded its limit"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &envelope)
		if envelope.Code == "" {
			envelope.Code = "sandbox_service_error"
		}
		if envelope.Error == "" {
			envelope.Error = "sandbox service rejected the request"
		}
		return nil, "", &Error{Code: envelope.Code, Message: envelope.Error}
	}
	return data, response.Header.Get("Content-Type"), nil
}

func (e *DockerEngine) endpointForPort(ctx context.Context, state MachineState, role, portName string) (string, error) {
	forwardPort, ok := forwardedManagementPort(role, portName)
	if !ok {
		return "", &Error{Code: "sandbox_service_unavailable", Message: "sandbox service endpoint is unavailable"}
	}
	inspect, err := e.client.ContainerInspect(ctx, state.ContainerNames["gateway"], client.ContainerInspectOptions{})
	if err != nil {
		return "", err
	}
	port, err := network.ParsePort(forwardPort)
	if err != nil {
		return "", err
	}
	bindings := inspect.Container.NetworkSettings.Ports[port]
	if len(bindings) == 0 || bindings[0].HostPort == "" {
		return "", &Error{Code: "sandbox_service_unavailable", Message: "sandbox service endpoint is unavailable"}
	}
	return net.JoinHostPort("127.0.0.1", bindings[0].HostPort), nil
}

func forwardedManagementPort(role, portName string) (string, bool) {
	switch role + "/" + portName {
	case "workbench/" + agentPort:
		return workbenchAgentForwardPort, true
	case "desktop/" + agentPort:
		return desktopAgentForwardPort, true
	case "desktop/" + vncPort:
		return desktopVNCForwardPort, true
	case "desktop/" + playwrightPort:
		return desktopBrowserForwardPort, true
	default:
		return "", false
	}
}

func (e *DockerEngine) waitForServices(ctx context.Context, state MachineState) error {
	waitContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	e.mu.Lock()
	secrets := e.secret[state.WorkspaceID]
	e.mu.Unlock()
	checks := []struct {
		role  string
		port  string
		token string
	}{
		{"workbench", agentPort, secrets.WorkbenchAgentToken},
		{"desktop", agentPort, secrets.DesktopAgentToken},
		{"desktop", playwrightPort, secrets.BrowserToken},
	}
	for _, check := range checks {
		for {
			data, _, err := e.serviceRequest(waitContext, state, check.role, check.port, http.MethodGet, "/v1/health", check.token, nil, 64<<10)
			if err == nil {
				var health struct {
					ProtocolVersion string `json:"protocolVersion"`
				}
				if json.Unmarshal(data, &health) != nil || health.ProtocolVersion != ProtocolVersion {
					return ErrProtocolMismatch
				}
				break
			}
			select {
			case <-waitContext.Done():
				return &Error{Code: "sandbox_agent_unavailable", Message: "sandbox services did not become ready", Cause: err}
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	return nil
}

func (e *DockerEngine) ProxyEndpoint(ctx context.Context, state MachineState) (string, error) {
	inspect, err := e.client.ContainerInspect(ctx, state.ContainerNames["gateway"], client.ContainerInspectOptions{})
	if err != nil {
		return "", err
	}
	port, err := network.ParsePort(gatewayProxyPort)
	if err != nil {
		return "", err
	}
	bindings := inspect.Container.NetworkSettings.Ports[port]
	if len(bindings) == 0 || bindings[0].HostPort == "" {
		return "", &Error{Code: "egress_unavailable", Message: "sandbox egress proxy is unavailable"}
	}
	return "http://" + net.JoinHostPort("127.0.0.1", bindings[0].HostPort), nil
}

func (e *DockerEngine) Reconcile(ctx context.Context, installation, _ string, _ []string) error {
	filters := make(client.Filters).Add("label", LabelManaged+"=true").Add("label", LabelInstallation+"="+installation)
	listed, err := e.client.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return err
	}
	timeout := 10
	for _, summary := range listed.Items {
		if summary.State != "running" {
			continue
		}
		// Runtime credentials are memory-only. Even compatible survivors must
		// stop during startup reconciliation so they cannot outlive the Echo
		// process that owned their management secrets. Volumes are retained.
		_, stopErr := e.client.ContainerStop(ctx, summary.ID, client.ContainerStopOptions{Timeout: &timeout})
		if stopErr != nil && !cerrdefs.IsNotFound(stopErr) {
			return stopErr
		}
	}
	return nil
}
