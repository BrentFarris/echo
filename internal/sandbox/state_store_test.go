package sandbox

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateStoreIsolatesWorkspacesAndNeverPersistsRuntimeSecrets(t *testing.T) {
	root := t.TempDir()
	store := NewStateStore(root)
	first := DefaultMachineState("installation", "workspace-one", BuildImages())
	first.NetworkGrants = []NetworkGrant{{ID: "grant", Host: "10.0.0.5", Port: 443, Label: "internal"}}
	if err := store.Save(first); err != nil {
		t.Fatal(err)
	}
	first.LastSetup.Message = "second save exercises atomic replacement"
	if err := store.Save(first); err != nil {
		t.Fatal(err)
	}
	second := DefaultMachineState("installation", "workspace-two", BuildImages())
	if err := store.Save(second); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "workspace-one", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"agentToken", "workbenchAgentToken", "desktopAgentToken", "vncToken", "proxyToken", "leaseToken", "browserToken", "credential"} {
		if strings.Contains(strings.ToLower(string(data)), strings.ToLower(forbidden)) {
			t.Fatalf("machine state persisted %q", forbidden)
		}
	}
	if err := store.Delete("workspace-one"); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := store.Load("workspace-one"); err != nil || exists {
		t.Fatalf("deleted state still exists: %v %v", exists, err)
	}
	if _, exists, err := store.Load("workspace-two"); err != nil || !exists {
		t.Fatalf("other workspace state was affected: %v %v", exists, err)
	}
	if err := store.Delete("../"); err == nil {
		t.Fatal("unsafe workspace id was accepted")
	}
}

func TestDeterministicNamesAndLabels(t *testing.T) {
	left := DefaultMachineState("installation", "workspace", BuildImages())
	right := DefaultMachineState("installation", "workspace", BuildImages())
	other := DefaultMachineState("installation", "other", BuildImages())
	if left.NetworkName != right.NetworkName || left.ContainerNames["desktop"] != right.ContainerNames["desktop"] {
		t.Fatal("resource names are not deterministic")
	}
	for _, role := range []string{"workbench", "desktop", "browser", "exchange", "gateway"} {
		if left.VolumeNames[role] == "" {
			t.Fatalf("missing deterministic %s volume name", role)
		}
	}
	if left.NetworkName == other.NetworkName {
		t.Fatal("workspaces share resource names")
	}
	labels := ResourceLabels("install", "workspace", "desktop", "image@sha256:abc")
	for _, key := range []string{LabelManaged, LabelInstallation, LabelWorkspace, LabelRole, LabelImage, LabelProtocol} {
		if labels[key] == "" {
			t.Fatalf("missing label %s", key)
		}
	}
}

func TestSandboxNetworkSubnetIsDeterministicAndAdvances(t *testing.T) {
	first := sandboxNetworkSubnet("installation", "workspace", 0)
	if first != sandboxNetworkSubnet("installation", "workspace", 0) {
		t.Fatal("sandbox subnet is not deterministic")
	}
	if first == sandboxNetworkSubnet("installation", "workspace", 1) {
		t.Fatal("subnet retry did not advance to a new candidate")
	}
	if first.Bits() != 24 || !netip.MustParsePrefix("10.64.0.0/10").Contains(first.Addr()) {
		t.Fatalf("sandbox subnet %s is outside the reserved candidate range", first)
	}
}
