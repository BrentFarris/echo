//go:build windows

package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows propagates inheritable ACL edits through every existing descendant.
// Keep those grants for an identical host-owned SCM scope instead of rewriting
// the checkout twice for each short Git command. Jobs and scratch remain per
// invocation; a session runs only one job at a time and retains no credentials.
// Journals still recover grants after a crash. Scope changes and shutdown revoke
// sessions after cancelling and joining their current commands.
func (b *ProcessBroker) acquireProcessSession(ctx context.Context, r ProcessRequest) (*processSession, func(), error) {
	data, _ := json.Marshal([]any{r.BotID, r.SandboxSession, r.Roots, r.ReadPaths, r.Toolchains, r.Executable, r.Internet, r.PrivateNetwork})
	sum := sha256.Sum256(data)
	key := hex.EncodeToString(sum[:])
	b.sessionsMu.Lock()
	if b.sessions == nil {
		b.sessions = make(map[string]*processSession)
	}
	s := b.sessions[key]
	if s == nil {
		if len(b.sessions) >= 64 {
			b.sessionsMu.Unlock()
			return nil, nil, errors.New("native sandbox session limit reached; restart Echo to release inactive scopes")
		}
		lifetime, cancel := context.WithCancel(context.Background())
		s = &processSession{botID: r.BotID, name: r.SandboxSession, ctx: lifetime, cancel: cancel, gate: make(chan struct{}, 1)}
		b.sessions[key] = s
	}
	b.sessionsMu.Unlock()
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-s.ctx.Done():
		return nil, nil, s.ctx.Err()
	}
	release := func() { <-s.gate }
	if err := s.ctx.Err(); err != nil {
		release()
		return nil, nil, err
	}
	var sid *windows.SID
	var err error
	if s.journal.Name == "" {
		nonce := sha256.Sum256([]byte(key + time.Now().String()))
		name := "Echo2.SCM." + hex.EncodeToString(nonce[:12])
		sid, err = profile(name)
		if err == nil {
			defer freeSID.Call(uintptr(unsafe.Pointer(sid)))
			s.journal = aclJournal{Name: name, SID: sid.String()}
			s.aliases = map[string]string{}
		}
	} else {
		sid, err = windows.StringToSid(s.journal.SID)
	}
	remember := func(path string) error {
		if slices.Contains(s.journal.Paths, path) {
			return nil
		}
		s.journal.Paths = append(s.journal.Paths, path)
		if err := b.journal(s.journal); err != nil {
			return fmt.Errorf("write permission journal in %s: %w", b.Dir, err)
		}
		return nil
	}
	if err == nil {
		// A saved session is not proof that its ACL entries still exist:
		// other processes can replace/reset permissions.
		// Recheck each grant; changeAccessInheritance leaves intact ACLs alone.
		for _, item := range []struct {
			paths    []string
			readOnly bool
			inherit  uint32
		}{{r.Roots, false, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT}, {r.Toolchains, true, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT}, {r.ReadPaths, true, windows.NO_INHERITANCE}} {
			for _, path := range item.paths {
				if err = ctx.Err(); err != nil {
					break
				}
				mirror := s.aliases[path]
				if mirror != "" {
					path = mirror
				}
				if err = remember(path); err != nil {
					break
				}
				err = changeAccessInheritance(path, sid, false, item.readOnly, item.inherit)
				if err == windows.ERROR_ACCESS_DENIED && mirror == "" && item.readOnly && item.inherit != windows.NO_INHERITANCE {
					mirror, err = b.mirrorToolchain(ctx, path)
					if err == nil {
						err = remember(mirror)
						if err == nil {
							err = changeAccess(mirror, sid, false, true)
						}
						if err == nil {
							s.aliases[path] = mirror
						}
					}
				}
				if err != nil {
					err = fmt.Errorf("grant sandbox access to %s: %w", path, err)
					break
				}
			}
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		s.cancel()
		if s.journal.Name != "" {
			err = errors.Join(err, b.cleanup(s.journal))
		}
		b.sessionsMu.Lock()
		if b.sessions[key] == s {
			delete(b.sessions, key)
		}
		b.sessionsMu.Unlock()
		release()
		return nil, nil, fmt.Errorf("prepare native sandbox: %w", err)
	}
	return s, release, nil
}

// CloseSessions cancels commands before removing their shared filesystem grants.
// An empty bot ID closes every session. Failed cleanup retains its durable journal.
func (b *ProcessBroker) CloseSessions(botID string) error {
	return b.closeSessions(botID, "")
}

// CloseNamedSessions retires only this kind of sandbox across bot workspaces.
func (b *ProcessBroker) CloseNamedSessions(name string) error {
	if name == "" {
		return errors.New("sandbox session name required")
	}
	return b.closeSessions("", name)
}

func (b *ProcessBroker) closeSessions(botID, name string) error {
	b.sessionsMu.Lock()
	var sessions []*processSession
	for key, s := range b.sessions {
		if (botID == "" || s.botID == botID) && (name == "" || s.name == name) {
			s.cancel()
			sessions = append(sessions, s)
			delete(b.sessions, key)
		}
	}
	b.sessionsMu.Unlock()
	var journals []aclJournal
	for _, s := range sessions {
		s.gate <- struct{}{}
		if s.journal.Name != "" {
			journals = append(journals, s.journal)
		}
		<-s.gate
	}
	return b.cleanupJournals(journals, revokeAccessBatch)
}

func sessionExecutable(program string, aliases map[string]string) string {
	for path, mirror := range aliases {
		if Within(path, program) {
			rel, _ := filepath.Rel(path, program)
			return filepath.Join(mirror, rel)
		}
	}
	return program
}
