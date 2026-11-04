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

func TestNativeRecoveryNonrecursiveReadGrants(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1")
	}
	b := &ProcessBroker{Dir: t.TempDir()}
	parent := t.TempDir()
	metadata := filepath.Join(parent, ".gitignore")
	sibling := filepath.Join(parent, "unrelated.txt")
	for _, path := range []string{metadata, sibling} {
		if err := os.WriteFile(path, []byte("preserve content"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	j := recoveryFixture(t, b, parent, metadata)
	sid, err := windows.StringToSid(j.SID)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range j.Paths {
		if err := changeAccessInheritance(path, sid, false, true, windows.NO_INHERITANCE); err != nil {
			t.Fatal(err)
		}
		if !hasRecoveryGrant(t, path, j.SID) {
			t.Fatalf("missing explicit grant on %s", path)
		}
	}
	if hasRecoveryGrant(t, sibling, j.SID) {
		t.Fatal("parent read grant was inherited by an unrelated file")
	}
	if err := b.Recover(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{parent, metadata, sibling} {
		if hasRecoveryGrant(t, path, j.SID) {
			t.Fatalf("read grant survived recovery on %s", path)
		}
	}
}

func TestNativeNonrecursiveGrantWithOpenDirectory(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1")
	}
	b := &ProcessBroker{Dir: t.TempDir()}
	parent := t.TempDir()
	child := filepath.Join(parent, "child.txt")
	if err := os.WriteFile(child, []byte("outside the grant"), 0600); err != nil {
		t.Fatal(err)
	}
	j := recoveryFixture(t, b, parent)
	sid, err := windows.StringToSid(j.SID)
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(parent)
	if err != nil {
		t.Fatal(err)
	}
	// A terminal's current directory (or another reader) can legitimately
	// deny delete sharing without preventing changes to the directory ACL.
	handle, err := windows.CreateFile(name, windows.FILE_LIST_DIRECTORY, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if err := changeAccessInheritance(parent, sid, false, true, windows.NO_INHERITANCE); err != nil {
		t.Fatalf("grant on an open directory: %v", err)
	}
	if !hasRecoveryGrant(t, parent, j.SID) || hasRecoveryGrant(t, child, j.SID) {
		t.Fatal("grant was missing or reached a child")
	}
	if err := b.cleanup(j); err != nil {
		t.Fatalf("revoke on an open directory: %v", err)
	}
}

func TestNativeNonrecursiveACLDoesNotPropagateExistingEntries(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1")
	}
	b := &ProcessBroker{Dir: t.TempDir()}
	parent := t.TempDir()
	child := filepath.Join(parent, "child.txt")
	if err := os.WriteFile(child, []byte("unchanged ACL"), 0600); err != nil {
		t.Fatal(err)
	}
	j := recoveryFixture(t, b, parent)
	sid, err := windows.StringToSid(j.SID)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	merged, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{AccessPermissions: windows.FILE_GENERIC_READ, AccessMode: windows.GRANT_ACCESS, Inheritance: windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_UNKNOWN, TrusteeValue: windows.TrusteeValueFromSID(sid)}}}, acl)
	if err != nil {
		t.Fatal(err)
	}
	// Even if the DACL contains other inheritable entries, an ancestor-only
	// update must never propagate them through a potentially huge checkout.
	before, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if err := setPathAccess(parent, merged, false); err != nil {
		t.Fatal(err)
	}
	if !hasRecoveryGrant(t, parent, j.SID) || hasRecoveryGrant(t, child, j.SID) {
		t.Fatal("nonrecursive ACL update propagated an inheritable entry")
	}
	sd, err = windows.GetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := sd.Control()
	const inheritance = windows.SE_DACL_PROTECTED | windows.SE_DACL_AUTO_INHERITED
	if err != nil || before&inheritance != after&inheritance {
		t.Fatalf("inheritance controls changed: %#x to %#x, %v", before, after, err)
	}
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
