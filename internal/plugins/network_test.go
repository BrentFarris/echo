package plugins

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
)

func TestNetworkApprovalExpansionRepeatReductionRollback(t *testing.T) {
	h := appTestHost(t)
	m := domain.PluginManifest{API: 1, ID: "network-app", Name: "Network app", Network: &domain.PluginNetwork{Origins: []string{"HTTPS://EXAMPLE.COM:443/", "https://example.com"}}}
	installed, err := h.Install(m, nil)
	if err != nil || !installed.Pending || installed.Enabled || !slices.Equal(installed.Network.Origins, []string{"https://example.com"}) {
		t.Fatal(installed, err)
	}
	for i := 0; i < 2; i++ {
		installed, err = h.Install(installed, nil)
		if err != nil || !installed.Pending {
			t.Fatal("repeat bypass", installed, err)
		}
	}
	approved, err := h.ApproveDigest(m.ID, installed.Digest)
	if err != nil || approved.Pending || len(approved.RequestedNetworkOrigins) > 0 {
		t.Fatal(approved, err)
	}
	installed, err = h.Install(approved, nil)
	if err != nil || installed.Pending {
		t.Fatal("unchanged grant", installed, err)
	}
	oldDigest := installed.Digest
	installed.Network = &domain.PluginNetwork{Origins: []string{"https://example.com", "ws://localhost:8080"}}
	installed, err = h.Install(installed, nil)
	if err != nil || !installed.Pending || !slices.Equal(installed.RequestedNetworkOrigins, []string{"ws://localhost:8080"}) {
		t.Fatal(installed, err)
	}
	if _, err = h.ApproveDigest(m.ID, oldDigest); err == nil {
		t.Fatal("stale approval")
	}
	installed, err = h.Install(installed, nil)
	if err != nil || !installed.Pending {
		t.Fatal("repeat expansion bypass", err)
	}
	if err = h.Rollback(m.ID); err != nil {
		t.Fatal(err)
	}
	rolled, _ := h.Get(m.ID)
	if rolled.Pending || !slices.Equal(rolled.Network.Origins, []string{"https://example.com"}) {
		t.Fatal(rolled)
	}
	rolled.Network = nil
	reduced, err := h.Install(rolled, nil)
	if err != nil || reduced.Pending {
		t.Fatal("reduction needs approval", err)
	}
}

func TestNetworkLeaseRevokedOnSave(t *testing.T) {
	h := appTestHost(t)
	m, err := h.Install(domain.PluginManifest{API: 1, ID: "leased-app", Name: "Leased"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := h.Lease(context.Background(), domain.Bot{ID: "echo"}, m.ID, m.Digest)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	m.Enabled = false
	if err = h.Save(m); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("lease survived disablement")
	}
}
