package sandbox

import (
	"context"
	_ "embed"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

//go:embed migrate_volumes_test.py
var migrationPythonTests string

func TestDockerIntegrationMigration(t *testing.T) {
	if os.Getenv("ECHO_SANDBOX_INTEGRATION") != "1" {
		t.Skip("enable Docker integration to test Linux volume migration")
	}
	image := os.Getenv("ECHO_SANDBOX_RUNTIME_IMAGE")
	if image == "" {
		image = "echo-sandbox-runtime:dev"
	}
	e, err := NewDockerEngine()
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// Exercise metadata and symlink rules on the actual Linux filesystem.
	runMigrationPython(t, ctx, e, image, nil, "__name__='migration_library'\n"+migrateVolumesScript+"\n__name__='__main__'\n"+migrationPythonTests)
	id := "migration-test-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	original := legacyMachine("echo-ci", id)
	candidate := DefaultMachineState("echo-ci", id, ImageSet{Runtime: image})
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		scope := DeleteScope{Containers: true, Network: true, Runtime: true, Workbench: true, Desktop: true, Browser: true, Exchange: true}
		_ = e.Delete(cleanup, original, scope)
		_ = e.Delete(cleanup, candidate, scope)
	}()
	var mounts []mount.Mount
	for _, role := range []string{"workbench", "desktop", "browser", "exchange"} {
		_, err := e.client.VolumeCreate(ctx, client.VolumeCreateOptions{Name: original.VolumeNames[role], Labels: ResourceLabels("echo-ci", id, role, "")})
		if err != nil {
			t.Fatal(err)
		}
		mounts = append(mounts, mount.Mount{Type: mount.TypeVolume, Source: original.VolumeNames[role], Target: "/fixture/" + role})
	}
	runMigrationPython(t, ctx, e, image, mounts, `from pathlib import Path
for role in ('workbench','desktop','browser','exchange'):
    (Path('/fixture') / role / 'marker').write_text(role)
`)
	if err := e.PreflightLegacyVolumes(ctx, "echo-ci", original, candidate); err != nil {
		t.Fatal(err)
	}
	if err := e.CopyLegacyVolumes(ctx, "echo-ci", original, candidate); err != nil {
		t.Fatal(err)
	}
	for index := range mounts {
		mounts[index].ReadOnly = true
	}
	for _, role := range []string{"runtime", "browser", "exchange"} {
		mounts = append(mounts, mount.Mount{Type: mount.TypeVolume, Source: candidate.VolumeNames[role], Target: "/candidate/" + role, ReadOnly: true})
	}
	runMigrationPython(t, ctx, e, image, mounts, `from pathlib import Path
for role in ('workbench','desktop','browser','exchange'):
    assert (Path('/fixture') / role / 'marker').read_text() == role
for role, expected in [('runtime','workbench'),('browser','browser'),('exchange','exchange')]:
    assert (Path('/candidate') / role / 'marker').read_text() == expected
archive, = Path('/candidate/runtime').glob('sandbox-migration-conflicts-*')
assert (archive / 'desktop/marker').read_text() == 'desktop'
`)
	// Retry on real named volumes, then verify missing originals fail closed.
	if err := e.CopyLegacyVolumes(ctx, "echo-ci", original, candidate); err != nil {
		t.Fatal(err)
	}
	original.VolumeNames["desktop"] += "-missing"
	if err := e.CopyLegacyVolumes(ctx, "echo-ci", original, candidate); ErrorCode(err) != "migration_source_missing" {
		t.Fatalf("missing source: %v", err)
	}
	original.VolumeNames["desktop"] = strings.TrimSuffix(original.VolumeNames["desktop"], "-missing")
}

func runMigrationPython(t *testing.T, ctx context.Context, e *DockerEngine, image string, mounts []mount.Mount, script string) {
	t.Helper()
	created, err := e.client.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{Image: image, Entrypoint: []string{"python3", "-c"}, Cmd: []string{script}}, HostConfig: &container.HostConfig{NetworkMode: "none", Mounts: mounts}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = e.client.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err := e.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	if code := e.waitContainer(ctx, created.ID); code != 0 {
		logs, err := e.client.ContainerLogs(ctx, created.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
		if err == nil {
			defer logs.Close()
			data, _ := io.ReadAll(io.LimitReader(logs, 16<<10))
			t.Fatalf("migration fixture exit %d: %s", code, data)
		}
		t.Fatalf("migration fixture exit %d: %v", code, err)
	}
}
