//go:build windows

package broker

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

var workspaceDriveMu sync.Mutex

// gopls validates path spelling by enumerating ancestors. A temporary per-logon
// DOS drive exposes an already granted root without granting parent/sibling
// access. Underlying ACL checks still apply. Journal before creating the alias.
func (b *ProcessBroker) workspaceDrive(root string, j *aclJournal) (string, error) {
	workspaceDriveMu.Lock()
	defer workspaceDriveMu.Unlock()
	for letter := 'Z'; letter >= 'D'; letter-- {
		name := string(letter) + ":"
		p, _ := windows.UTF16PtrFromString(name)
		buffer := make([]uint16, 32768)
		if _, err := windows.QueryDosDevice(p, &buffer[0], uint32(len(buffer))); !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			continue
		}
		target := `\??\` + root
		if strings.HasPrefix(root, `\\`) {
			target = `\??\UNC\` + strings.TrimPrefix(root, `\\`)
		}
		j.Drives[name] = target
		if err := b.journal(*j); err != nil {
			return "", err
		}
		t, _ := windows.UTF16PtrFromString(target)
		if err := windows.DefineDosDevice(windows.DDD_RAW_TARGET_PATH|windows.DDD_NO_BROADCAST_SYSTEM, p, t); err != nil {
			return "", fmt.Errorf("create confined workspace drive: %w", err)
		}
		return name, nil
	}
	return "", errors.New("no free workspace drive letters for Go language services; close unused sessions")
}
func removeWorkspaceDrives(drives map[string]string) error {
	workspaceDriveMu.Lock()
	defer workspaceDriveMu.Unlock()
	var result error
	for name, target := range drives {
		p, _ := windows.UTF16PtrFromString(name)
		t, _ := windows.UTF16PtrFromString(target)
		buffer := make([]uint16, 32768)
		if _, err := windows.QueryDosDevice(p, &buffer[0], uint32(len(buffer))); errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			continue
		} else if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if windows.UTF16ToString(buffer) != target {
			continue
		}
		result = errors.Join(result, windows.DefineDosDevice(windows.DDD_REMOVE_DEFINITION|windows.DDD_EXACT_MATCH_ON_REMOVE|windows.DDD_RAW_TARGET_PATH|windows.DDD_NO_BROADCAST_SYSTEM, p, t))
	}
	return result
}
