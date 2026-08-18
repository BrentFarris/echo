package workspacefs

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	ignore "github.com/sabhiram/go-gitignore"
)

const maximumWatchedDirectories = 8192

type Change struct {
	Op  string  `json:"op"`
	Ref FileRef `json:"ref"`
}

type WatchEvent struct {
	Type           string   `json:"type"`
	WorkspaceID    string   `json:"workspaceId"`
	Sequence       uint64   `json:"sequence"`
	Changes        []Change `json:"changes,omitempty"`
	ResyncRequired bool     `json:"resyncRequired,omitempty"`
}

type watchedWorkspace struct {
	id         string
	watcher    *fsnotify.Watcher
	roots      []resolvedRoot
	matchers   map[string]*ignore.GitIgnore
	references int
	sequence   uint64
	stop       chan struct{}
	done       chan struct{}
	mu         sync.Mutex
	pending    map[string]Change
	watchCount int
	watched    map[string]bool
	overflowed bool
	onEvent    func(WatchEvent)
	applyIndex func(string, []Change)
}

// WatchManager reference-counts recursive directory watchers by workspace.
type WatchManager struct {
	service    *Service
	onEvent    func(WatchEvent)
	mu         sync.Mutex
	workspaces map[string]*watchedWorkspace
}

func NewWatchManager(service *Service, onEvent func(WatchEvent)) *WatchManager {
	return &WatchManager{service: service, onEvent: onEvent, workspaces: make(map[string]*watchedWorkspace)}
}

func (m *WatchManager) Subscribe(workspaceID string) error {
	m.mu.Lock()
	if existing := m.workspaces[workspaceID]; existing != nil {
		existing.references++
		m.mu.Unlock()
		return nil
	}
	roots, err := m.service.resolvedRoots(workspaceID)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		m.mu.Unlock()
		return err
	}
	roots = availableResolvedRoots(roots)
	workspace := &watchedWorkspace{
		id: workspaceID, watcher: watcher, roots: roots, references: 1,
		stop: make(chan struct{}), done: make(chan struct{}), pending: make(map[string]Change),
		matchers: make(map[string]*ignore.GitIgnore), watched: make(map[string]bool),
		onEvent: m.onEvent, applyIndex: m.service.index.ApplyChanges,
	}
	m.workspaces[workspaceID] = workspace
	m.mu.Unlock()

	for _, root := range roots {
		if matcher, compileErr := ignore.CompileIgnoreFile(filepath.Join(root.realPath, ".gitignore")); compileErr == nil {
			workspace.matchers[root.ID] = matcher
		}
		if err := workspace.addTree(root, root.realPath, false); err != nil {
			workspace.emitResync()
		}
	}
	m.service.StartIndex(workspaceID)
	go workspace.run()
	return nil
}

// AddReferences ensures ignored directories containing an expanded tree node
// or open file are watched while the Code view needs them.
func (m *WatchManager) AddReferences(workspaceID string, refs []FileRef) {
	m.mu.Lock()
	workspace := m.workspaces[workspaceID]
	m.mu.Unlock()
	if workspace == nil {
		return
	}
	for _, ref := range refs {
		root, resolved, _, err := m.service.resolve(workspaceID, ref, true, false)
		if err != nil {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			resolved = filepath.Dir(resolved)
		}
		if err := workspace.addTree(root, resolved, true); err != nil {
			workspace.emitResync()
		}
	}
}

func (m *WatchManager) Unsubscribe(workspaceID string) {
	m.mu.Lock()
	workspace := m.workspaces[workspaceID]
	if workspace == nil {
		m.mu.Unlock()
		return
	}
	workspace.references--
	if workspace.references > 0 {
		m.mu.Unlock()
		return
	}
	delete(m.workspaces, workspaceID)
	close(workspace.stop)
	m.mu.Unlock()
	<-workspace.done
}

// Refresh replaces an active watcher while preserving its subscription count.
// It is a no-op when the workspace is not currently watched.
func (m *WatchManager) Refresh(workspaceID string) {
	m.mu.Lock()
	workspace := m.workspaces[workspaceID]
	if workspace == nil {
		m.mu.Unlock()
		return
	}
	references := workspace.references
	delete(m.workspaces, workspaceID)
	close(workspace.stop)
	m.mu.Unlock()
	<-workspace.done
	for index := 0; index < references; index++ {
		if err := m.Subscribe(workspaceID); err != nil {
			return
		}
	}
}

func (m *WatchManager) Close() {
	m.mu.Lock()
	workspaces := make([]*watchedWorkspace, 0, len(m.workspaces))
	for id, workspace := range m.workspaces {
		delete(m.workspaces, id)
		workspaces = append(workspaces, workspace)
		close(workspace.stop)
	}
	m.mu.Unlock()
	for _, workspace := range workspaces {
		<-workspace.done
	}
}

func (w *watchedWorkspace) addTree(root resolvedRoot, start string, forceStart bool) error {
	return filepath.WalkDir(start, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		relative, _ := filepath.Rel(root.realPath, current)
		relative = filepath.ToSlash(relative)
		if relative == ".git" || strings.HasPrefix(relative, ".git/") {
			return filepath.SkipDir
		}
		if (relative == ".echo" || strings.HasPrefix(relative, ".echo/")) && (current != start || !forceStart) {
			return filepath.SkipDir
		}
		if matcher := w.matchers[root.ID]; current != start || !forceStart {
			if relative != "." && matcher != nil && matcher.MatchesPath(relative+"/") {
				return filepath.SkipDir
			}
		}
		w.mu.Lock()
		if w.watched[current] {
			w.mu.Unlock()
			return filepath.SkipDir
		}
		if w.watchCount >= maximumWatchedDirectories {
			w.overflowed = true
			w.mu.Unlock()
			return filepath.SkipAll
		}
		w.mu.Unlock()
		if err := w.watcher.Add(current); err != nil {
			return err
		}
		w.mu.Lock()
		w.watched[current] = true
		w.watchCount++
		w.mu.Unlock()
		return nil
	})
}

func (w *watchedWorkspace) run() {
	defer close(w.done)
	defer w.watcher.Close()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			w.flush()
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				w.emitResync()
				return
			}
			w.handle(event)
		case _, ok := <-w.watcher.Errors:
			if !ok {
				w.emitResync()
				return
			}
			w.emitResync()
		case <-ticker.C:
			w.flush()
		}
	}
}

func (w *watchedWorkspace) handle(event fsnotify.Event) {
	ref, ok := w.refFor(event.Name)
	if !ok {
		return
	}
	op := "write"
	switch {
	case event.Op&fsnotify.Create != 0:
		op = "create"
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			for _, root := range w.roots {
				if ensureWithin(root.realPath, event.Name) == nil {
					_ = w.addTree(root, event.Name, false)
					break
				}
			}
		}
	case event.Op&fsnotify.Remove != 0:
		op = "delete"
	case event.Op&fsnotify.Rename != 0:
		op = "rename"
	case event.Op&fsnotify.Chmod != 0 && event.Op&fsnotify.Write == 0:
		op = "metadata"
	}
	w.mu.Lock()
	w.pending[ref.RootID+"\x00"+ref.Path] = Change{Op: op, Ref: ref}
	w.mu.Unlock()
	if op == "delete" || op == "rename" {
		w.removeWatchedTree(event.Name)
	}
}

func (w *watchedWorkspace) removeWatchedTree(removed string) {
	prefix := removed + string(filepath.Separator)
	w.mu.Lock()
	paths := make([]string, 0)
	for watched := range w.watched {
		if watched == removed || strings.HasPrefix(watched, prefix) {
			delete(w.watched, watched)
			w.watchCount--
			paths = append(paths, watched)
		}
	}
	w.mu.Unlock()
	for _, watched := range paths {
		_ = w.watcher.Remove(watched)
	}
}

func (w *watchedWorkspace) refFor(filePath string) (FileRef, bool) {
	for _, root := range w.roots {
		relative, err := filepath.Rel(root.realPath, filePath)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		return FileRef{RootID: root.ID, Path: filepath.ToSlash(relative)}, true
	}
	return FileRef{}, false
}

func (w *watchedWorkspace) flush() {
	w.mu.Lock()
	if len(w.pending) == 0 {
		overflowed := w.overflowed
		w.overflowed = false
		w.mu.Unlock()
		if overflowed {
			w.emitResync()
		}
		return
	}
	changes := make([]Change, 0, len(w.pending))
	for _, change := range w.pending {
		changes = append(changes, change)
	}
	sort.Slice(changes, func(i, j int) bool {
		left := changes[i].Ref.RootID + "\x00" + changes[i].Ref.Path + "\x00" + changes[i].Op
		right := changes[j].Ref.RootID + "\x00" + changes[j].Ref.Path + "\x00" + changes[j].Op
		return left < right
	})
	w.pending = make(map[string]Change)
	w.sequence++
	sequence := w.sequence
	w.mu.Unlock()
	w.applyIndex(w.id, changes)
	if w.onEvent != nil {
		w.onEvent(WatchEvent{Type: "workspace_fs_changed", WorkspaceID: w.id, Sequence: sequence, Changes: changes})
	}
}

func (w *watchedWorkspace) emitResync() {
	w.mu.Lock()
	w.sequence++
	sequence := w.sequence
	w.mu.Unlock()
	if w.onEvent != nil {
		w.onEvent(WatchEvent{Type: "fs_resync_required", WorkspaceID: w.id, Sequence: sequence, ResyncRequired: true})
	}
}
