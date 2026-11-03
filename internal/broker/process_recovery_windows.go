//go:build windows

package broker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A single inheritable ACL edit walks the entire directory tree on Windows.
// Revoke all journaled identities for a path together, rather than repeating
// that traversal for every interrupted command. Only successful journals are
// retired; a failed path keeps every affected record available for recovery.
func (b *ProcessBroker) cleanupJournals(journals []aclJournal, revoke func(string, []*windows.SID) error) error {
	type location struct {
		path     string
		sids     map[string]*windows.SID
		journals map[int]bool
	}
	locations := map[string]*location{}
	failed := map[int]bool{}
	var failures []error
	for index, journal := range journals {
		sid, err := windows.StringToSid(journal.SID)
		if err != nil {
			failed[index] = true
			failures = append(failures, fmt.Errorf("sandbox %s: %w", journal.Name, err))
			continue
		}
		for _, path := range journal.Paths {
			key := strings.ToLower(filepath.Clean(path))
			item := locations[key]
			if item == nil {
				item = &location{path: path, sids: map[string]*windows.SID{}, journals: map[int]bool{}}
				locations[key] = item
			}
			item.sids[journal.SID] = sid
			item.journals[index] = true
		}
	}
	keys := make([]string, 0, len(locations))
	for key := range locations {
		keys = append(keys, key)
	}
	// Parent grants are removed before explicitly granted child locations.
	sort.Strings(keys)
	for _, key := range keys {
		item := locations[key]
		sids := make([]*windows.SID, 0, len(item.sids))
		for _, sid := range item.sids {
			sids = append(sids, sid)
		}
		if err := revoke(item.path, sids); err != nil && !os.IsNotExist(err) {
			for index := range item.journals {
				failed[index] = true
			}
			failures = append(failures, fmt.Errorf("sandbox ACL cleanup pending for %s: %w", item.path, err))
		}
	}
	for index, journal := range journals {
		if !failed[index] {
			if err := b.removeProfileJournal(journal); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

func revokeAccessBatch(path string, sids []*windows.SID) error {
	processAccessMu.Lock()
	defer processAccessMu.Unlock()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	wanted := map[string]*windows.SID{}
	for _, sid := range sids {
		wanted[sid.String()] = sid
	}
	owned := map[string]bool{}
	for i := uint32(0); acl != nil && i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		value := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if wanted[value] != nil {
			owned[value] = true
		}
	}
	if len(owned) == 0 {
		return nil
	}
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(owned))
	for value := range owned {
		entries = append(entries, windows.EXPLICIT_ACCESS{AccessMode: windows.REVOKE_ACCESS, Inheritance: windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_UNKNOWN, TrusteeValue: windows.TrusteeValueFromSID(wanted[value])}})
	}
	merged, err := windows.ACLFromEntries(entries, acl)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, merged, nil)
}
