//go:build windows

package broker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func recoveryFixture(t *testing.T, b *ProcessBroker, paths ...string) aclJournal {
	t.Helper()
	name := fmt.Sprintf("Echo2.recoverytest.%d", time.Now().UnixNano())
	sid, err := profile(name)
	if err != nil {
		t.Fatal(err)
	}
	j := aclJournal{Name: name, SID: sid.String(), Paths: paths}
	freeSID.Call(uintptr(unsafe.Pointer(sid)))
	if err := b.journal(j); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := os.Stat(filepath.Join(b.Dir, j.Name+".json")); err == nil {
			if err := b.cleanup(j); err != nil {
				t.Error(err)
			}
		}
	})
	return j
}

func hasRecoveryGrant(t *testing.T, path, sid string) bool {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); acl != nil && i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) == nil && ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE && (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String() == sid {
			return true
		}
	}
	return false
}

func TestNativeRecoveryBatchesGrants(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1")
	}
	b := &ProcessBroker{Dir: t.TempDir()}
	root := t.TempDir()
	child := filepath.Join(root, "child.txt")
	if err := os.WriteFile(child, []byte("preserve content"), 0600); err != nil {
		t.Fatal(err)
	}
	var journals []aclJournal
	for range 3 {
		j := recoveryFixture(t, b, root, strings.ToUpper(root))
		sid, err := windows.StringToSid(j.SID)
		if err != nil {
			t.Fatal(err)
		}
		if err := changeAccess(root, sid, false, false); err != nil {
			t.Fatal(err)
		}
		journals = append(journals, j)
	}
	calls := 0
	err := b.cleanupJournals(journals[:2], func(path string, sids []*windows.SID) error {
		calls++
		if !strings.EqualFold(path, root) || len(sids) != 2 {
			t.Fatalf("unexpected recovery batch: %s, %d identities", path, len(sids))
		}
		return revokeAccessBatch(path, sids)
	})
	if err != nil || calls != 1 {
		t.Fatalf("recovery: %d traversals, %v", calls, err)
	}
	for _, path := range []string{root, child} {
		for i, j := range journals {
			if got := hasRecoveryGrant(t, path, j.SID); got != (i == 2) {
				t.Fatalf("grant %d on %s: %v", i, path, got)
			}
		}
	}
	if data, err := os.ReadFile(child); err != nil || string(data) != "preserve content" {
		t.Fatalf("file changed: %q %v", data, err)
	}
	// Recover consumes the remaining on-disk journal and is idempotent.
	for range 2 {
		if err := b.Recover(); err != nil {
			t.Fatal(err)
		}
	}
	if hasRecoveryGrant(t, child, journals[2].SID) {
		t.Fatal("remaining inherited grant survived recovery")
	}
}

func TestNativeRecoveryRetainsFailedJournals(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1")
	}
	b := &ProcessBroker{Dir: t.TempDir()}
	first, second := t.TempDir(), t.TempDir()
	failed := recoveryFixture(t, b, first, second)
	healthy := recoveryFixture(t, b, first)
	injected := errors.New("injected ACL failure")
	err := b.cleanupJournals([]aclJournal{failed, healthy}, func(path string, sids []*windows.SID) error {
		if path == second {
			return injected
		}
		return revokeAccessBatch(path, sids)
	})
	if !errors.Is(err, injected) {
		t.Fatalf("missing failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b.Dir, failed.Name+".json")); err != nil {
		t.Fatalf("lost failed recovery journal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b.Dir, healthy.Name+".json")); !os.IsNotExist(err) {
		t.Fatalf("healthy journal not retired: %v", err)
	}
	if err := b.Recover(); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b.Dir, failed.Name+".json")); !os.IsNotExist(err) {
		t.Fatalf("retried journal not retired: %v", err)
	}
}
