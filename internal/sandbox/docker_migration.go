package sandbox

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

//go:embed migrate_volumes.py
var migrateVolumesScript string

func (e *DockerEngine) CopyLegacyVolumes(ctx context.Context, installation string, original, candidate MachineState) error {
	return e.legacyVolumes(ctx, installation, original, candidate, false)
}
func (e *DockerEngine) PreflightLegacyVolumes(ctx context.Context, installation string, original, candidate MachineState) error {
	return e.legacyVolumes(ctx, installation, original, candidate, true)
}
func (e *DockerEngine) legacyVolumes(ctx context.Context, installation string, original, candidate MachineState, preflight bool) error {
	if original.WorkspaceID != candidate.WorkspaceID || !original.NeedsUpgrade() || candidate.NeedsUpgrade() {
		return fmt.Errorf("invalid sandbox migration source or destination")
	}
	var mounts []mount.Mount
	sources := make(map[string]bool)
	for _, role := range []string{"workbench", "desktop", "browser", "exchange"} {
		name := original.VolumeNames[role]
		if name == "" {
			return fmt.Errorf("missing legacy %s volume", role)
		}
		volume, err := e.client.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
		if err != nil {
			return Wrap("migration_source_missing", "Could not inspect the original "+role+" volume; no data was changed", err)
		}
		if volume.Volume.Labels[LabelWorkspace] != original.WorkspaceID || volume.Volume.Labels[LabelInstallation] != installation {
			return fmt.Errorf("legacy %s volume ownership does not match this sandbox", role)
		}
		sources[name] = true
		mounts = append(mounts, mount.Mount{Type: mount.TypeVolume, Source: name, Target: "/source/" + role, ReadOnly: true})
	}
	for _, role := range []string{"runtime", "browser", "exchange"} {
		name := candidate.VolumeNames[role]
		if name == "" || sources[name] {
			return fmt.Errorf("migration destination overlaps the original data")
		}
		volume, err := e.client.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Labels: ResourceLabels(installation, original.WorkspaceID, role, "")})
		if err != nil {
			return err
		}
		if volume.Volume.Labels[LabelWorkspace] != original.WorkspaceID || volume.Volume.Labels[LabelInstallation] != installation {
			return fmt.Errorf("migration target ownership does not match this sandbox")
		}
		target := role
		if role == "runtime" {
			target = "home"
		}
		mounts = append(mounts, mount.Mount{Type: mount.TypeVolume, Source: name, Target: "/target/" + target, VolumeOptions: &mount.VolumeOptions{NoCopy: true}})
	}
	created, err := e.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:       deterministicPrefix(installation, original.WorkspaceID) + fmt.Sprintf("-migration-%d", time.Now().UnixNano()),
		Config:     &container.Config{Image: candidate.Images.Runtime, Env: []string{fmt.Sprintf("ECHO_MIGRATION_PREFLIGHT=%t", preflight)}, Entrypoint: []string{"python3", "-c"}, Cmd: []string{migrateVolumesScript}, Labels: ResourceLabels(installation, original.WorkspaceID, "migration", candidate.Images.Runtime)},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(network.NetworkNone), Mounts: mounts, CapDrop: []string{"ALL"}, CapAdd: []string{"CHOWN", "DAC_OVERRIDE", "FOWNER"}, SecurityOpt: []string{"no-new-privileges=true"}},
	})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = e.client.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err := e.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return err
	}
	if code := e.waitContainer(ctx, created.ID); code != 0 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		message := "volume copy failed; original data is retained"
		if logs, err := e.client.ContainerLogs(ctx, created.ID, client.ContainerLogsOptions{ShowStderr: true, Tail: "12"}); err == nil {
			defer logs.Close()
			var output bytes.Buffer
			_, _ = stdcopy.StdCopy(&output, &output, io.LimitReader(logs, 4096))
			data := output.Bytes()
			// No file contents or credentials are emitted by the helper.
			message += ": " + strings.ToValidUTF8(string(data), "")
		}
		return &Error{Code: "migration_copy_failed", Message: message}
	}
	return nil
}
