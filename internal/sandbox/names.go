package sandbox

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"strings"
)

const (
	LabelManaged      = "com.echo.sandbox.managed"
	LabelInstallation = "com.echo.sandbox.installation"
	LabelWorkspace    = "com.echo.sandbox.workspace"
	LabelRole         = "com.echo.sandbox.role"
	LabelImage        = "com.echo.sandbox.image"
	LabelProtocol     = "com.echo.sandbox.protocol"
)

func deterministicPrefix(installation, workspaceID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(installation) + "\x00" + strings.TrimSpace(workspaceID)))
	return "echo-sbx-" + hex.EncodeToString(digest[:8])
}

// sandboxNetworkSubnet returns deterministic /24 candidates from a private
// range that Docker does not normally consume for its default bridge. Ensure
// advances the attempt when another Docker network already owns a candidate.
func sandboxNetworkSubnet(installation, workspaceID string, attempt uint16) netip.Prefix {
	digest := sha256.Sum256([]byte(strings.TrimSpace(installation) + "\x00" + strings.TrimSpace(workspaceID) + "\x00network"))
	const candidateCount = 64 * 256 // 10.64.0.0/10 split into /24 networks.
	index := (uint32(binary.BigEndian.Uint16(digest[:2])) + uint32(attempt)) % candidateCount
	address := netip.AddrFrom4([4]byte{10, byte(64 + index/256), byte(index % 256), 0})
	return netip.PrefixFrom(address, 24)
}

// InstallationID is stable for one Echo configuration directory without
// exposing its host path in Docker labels.
func InstallationID(settingsPath string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(settingsPath)))
	return hex.EncodeToString(digest[:12])
}

func DefaultMachineState(installation, workspaceID string, images ImageSet) MachineState {
	prefix := deterministicPrefix(installation, workspaceID)
	return MachineState{
		Version: 1, WorkspaceID: workspaceID, ProtocolVersion: ProtocolVersion, Images: images,
		VolumeNames: map[string]string{
			"workbench": prefix + "-home",
			"desktop":   prefix + "-desktop-home",
			"browser":   prefix + "-browser",
			"exchange":  prefix + "-exchange",
			"gateway":   prefix + "-gateway",
		},
		ContainerNames: map[string]string{
			"workbench": prefix + "-workbench",
			"desktop":   prefix + "-desktop",
			"gateway":   prefix + "-gateway",
		},
		NetworkName: prefix + "-internal",
	}
}

func ResourceLabels(installation, workspaceID, role, image string) map[string]string {
	return map[string]string{
		LabelManaged: "true", LabelInstallation: installation, LabelWorkspace: workspaceID,
		LabelRole: role, LabelImage: image, LabelProtocol: ProtocolVersion,
	}
}
