package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type migrationEngine struct {
	fakeEngine
	copyErr        error
	startErr       error
	validationExit int
	copies         int
}

func (e *migrationEngine) CopyLegacyVolumes(ctx context.Context, _ string, _, _ MachineState) error {
	e.copies++
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return e.copyErr
}
func (e *migrationEngine) Start(ctx context.Context, state MachineState) error {
	if e.startErr != nil {
		return e.startErr
	}
	return e.fakeEngine.Start(ctx, state)
}
func (e *migrationEngine) Exec(ctx context.Context, state MachineState, request ExecRequest) (ExecResult, error) {
	result, err := e.fakeEngine.Exec(ctx, state, request)
	result.ExitCode = e.validationExit
	return result, err
}

func legacyMachine(installation, id string) MachineState {
	prefix := deterministicPrefix(installation, id)
	return MachineState{Version: 1, WorkspaceID: id, ProtocolVersion: "2", Images: ImageSet{Workbench: "old-workbench", Desktop: "old-desktop", Gateway: "old-gateway"}, NetworkName: prefix + "-internal",
		ContainerNames: map[string]string{"workbench": prefix + "-workbench", "desktop": prefix + "-desktop", "gateway": prefix + "-gateway"},
		VolumeNames:    map[string]string{"workbench": prefix + "-home", "desktop": prefix + "-desktop-home", "browser": prefix + "-browser", "exchange": prefix + "-exchange", "gateway": prefix + "-gateway-data"}}
}

func TestLegacyUpgradePreservesRecoveryAndRequiresSetupReview(t *testing.T) {
	e := &migrationEngine{}
	m, w, root := newSandboxManagerForTest(t, &e.fakeEngine)
	m.engine = e
	old := legacyMachine(m.installation, w.ID)
	old.NetworkGrants = []NetworkGrant{{ID: "grant", Host: "192.168.1.2", Port: 8080, Label: "dev"}}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(SetupRecipePath)), []byte("echo setup\n"), 0600); err != nil {
		t.Fatal(err)
	}
	old.ApprovedSetupDigest, _, _ = setupDigest(filepath.Join(root, filepath.FromSlash(SetupRecipePath)))
	if err := m.store.Save(old); err != nil {
		t.Fatal(err)
	}
	for _, action := range []func() error{func() error { return m.Start(context.Background(), w.ID) }, func() error { return m.Recreate(context.Background(), w.ID) }, func() error { return m.Reset(context.Background(), w.ID, "runtime") }} {
		if !errors.Is(action(), ErrUpgradeRequired) {
			t.Fatal("legacy sandbox was silently recreated")
		}
	}
	if started, err := m.BeginUpgrade(w.ID); !started || err != nil {
		t.Fatalf("begin: %v %v", started, err)
	}
	if started, err := m.BeginUpgrade(w.ID); started || err != nil {
		t.Fatalf("duplicate: %v %v", started, err)
	}
	if !errors.Is(m.AdmitAIAction(w.ID, 0), ErrPolicyTransition) {
		t.Fatal("upgrade admitted workspace execution")
	}
	if err := m.Upgrade(context.Background(), w.ID); err != nil {
		t.Fatal(err)
	}
	state, _, _ := m.store.Load(w.ID)
	if state.NeedsUpgrade() || len(state.Recovery) != 1 || state.Recovery[0].VolumeNames["workbench"] != old.VolumeNames["workbench"] {
		t.Fatalf("lost recovery state: %+v", state)
	}
	if !reflect.DeepEqual(state.NetworkGrants, old.NetworkGrants) || state.ApprovedSetupProtocol != "" || state.LastSetup.State != "approval_required" {
		t.Fatal("grants or setup review lost")
	}
	if _, err := m.RunSetup(context.Background(), w.ID, ""); !errors.Is(err, ErrSetupApproval) {
		t.Fatalf("legacy setup approved without review: %v", err)
	}
	if started, err := m.BeginUpgrade(w.ID); started || err != nil {
		t.Fatal("completed upgrade was started again")
	}
	if err := m.Upgrade(context.Background(), w.ID); err != nil || e.copies != 1 {
		t.Fatal("committed upgrade copied a second time")
	}
	if err := m.Reset(context.Background(), w.ID, "runtime"); err != nil {
		t.Fatal(err)
	}
	state, _, _ = m.store.Load(w.ID)
	if len(state.Recovery) != 1 {
		t.Fatal("reset removed recovery")
	}
}

func TestFailedUpgradeIsRetryableWithoutChangingOriginalState(t *testing.T) {
	for _, failure := range []string{"copy", "startup", "validation"} {
		t.Run(failure, func(t *testing.T) {
			e := &migrationEngine{}
			switch failure {
			case "copy":
				e.copyErr = errors.New("interrupted copy")
			case "startup":
				e.startErr = errors.New("startup failed")
			case "validation":
				e.validationExit = 1
			}
			m, w, _ := newSandboxManagerForTest(t, &e.fakeEngine)
			m.engine = e
			old := legacyMachine(m.installation, w.ID)
			if err := m.store.Save(old); err != nil {
				t.Fatal(err)
			}
			before, _, _ := m.store.Load(w.ID)
			_, _ = m.BeginUpgrade(w.ID)
			if err := m.Upgrade(context.Background(), w.ID); err == nil {
				t.Fatal("expected failure")
			}
			after, _, _ := m.store.Load(w.ID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("original machine state changed after failure")
			}
			journal, exists, err := m.store.LoadMigration(w.ID)
			if err != nil || !exists || journal.Candidate.VolumeNames["browser"] == old.VolumeNames["browser"] {
				t.Fatal("missing isolated retry journal")
			}
			// Reopen the persisted journal as a new Echo process would.
			restarted := NewManager(m.workspaces, m.store.Root(), m.installation, e)
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = restarted.Shutdown(ctx)
			})
			m = restarted
			e.copyErr = nil
			e.startErr = nil
			e.validationExit = 0
			_, _ = m.BeginUpgrade(w.ID)
			if err := m.Upgrade(context.Background(), w.ID); err != nil {
				t.Fatal(err)
			}
			if e.copies != 2 {
				t.Fatalf("retry did not restart candidate copy: %d", e.copies)
			}
		})
	}
}
