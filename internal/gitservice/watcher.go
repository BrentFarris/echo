package gitservice

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type workspaceWatch struct {
	service      *Service
	workspaceID  string
	watcher      *fsnotify.Watcher
	references   int
	stop         chan struct{}
	done         chan struct{}
	mu           sync.RWMutex
	directories  map[string]bool
	repositories map[string]*repositoryState
}

func (s *Service) syncWorkspaceWatch(workspaceID string, states []*repositoryState) {
	s.mu.RLock()
	watch := s.watches[workspaceID]
	s.mu.RUnlock()
	if watch != nil {
		watch.replaceRepositories(states)
	}
}

func (s *Service) Subscribe(ctx context.Context, workspaceID string) error {
	workspaceID = strings.TrimSpace(workspaceID)
	s.mu.Lock()
	if existing := s.watches[workspaceID]; existing != nil {
		existing.references++
		s.mu.Unlock()
		states, err := s.discover(ctx, workspaceID)
		if err == nil {
			for _, state := range states {
				existing.addRepository(state)
				s.scheduleStatusRefresh(state)
			}
		}
		return err
	}
	s.mu.Unlock()

	states, err := s.discover(ctx, workspaceID)
	if err != nil {
		return err
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	created := &workspaceWatch{
		service: s, workspaceID: workspaceID, watcher: watcher, references: 1,
		stop: make(chan struct{}), done: make(chan struct{}), directories: make(map[string]bool),
		repositories: make(map[string]*repositoryState),
	}
	s.mu.Lock()
	if existing := s.watches[workspaceID]; existing != nil {
		existing.references++
		s.mu.Unlock()
		_ = watcher.Close()
		for _, state := range states {
			existing.addRepository(state)
			s.scheduleStatusRefresh(state)
		}
		return nil
	}
	s.watches[workspaceID] = created
	s.mu.Unlock()
	for _, state := range states {
		created.addRepository(state)
		s.scheduleStatusRefresh(state)
	}
	go created.run()
	return nil
}

func (s *Service) Unsubscribe(workspaceID string) {
	s.mu.Lock()
	watch := s.watches[workspaceID]
	if watch == nil {
		s.mu.Unlock()
		return
	}
	watch.references--
	if watch.references > 0 {
		s.mu.Unlock()
		return
	}
	delete(s.watches, workspaceID)
	close(watch.stop)
	s.mu.Unlock()
	<-watch.done
}

func (s *Service) Close() {
	s.mu.Lock()
	watches := make([]*workspaceWatch, 0, len(s.watches))
	for workspaceID, watch := range s.watches {
		delete(s.watches, workspaceID)
		close(watch.stop)
		watches = append(watches, watch)
	}
	for _, repositories := range s.repos {
		for _, state := range repositories {
			state.scheduleMu.Lock()
			if state.refreshTimer != nil {
				state.refreshTimer.Stop()
			}
			state.scheduleMu.Unlock()
		}
	}
	s.mu.Unlock()
	for _, watch := range watches {
		<-watch.done
	}
}

// InvalidateWorkspace is called by the shared workspace watcher. A single
// worktree event may affect more than one nested repository, so each cached
// repository receives a coalesced refresh rather than trying to infer Git
// ownership from an incomplete filesystem event.
func (s *Service) InvalidateWorkspace(workspaceID string) {
	s.mu.RLock()
	repositories := s.repos[workspaceID]
	states := make([]*repositoryState, 0, len(repositories))
	for _, state := range repositories {
		states = append(states, state)
	}
	s.mu.RUnlock()
	for _, state := range states {
		state.revision.Add(1)
		s.scheduleStatusRefresh(state)
	}
}

// ResetWorkspace clears repository state after the workspace registry is
// rebound, then recreates any active subscriptions against the new roots.
func (s *Service) ResetWorkspace(ctx context.Context, workspaceID string) error {
	s.mu.Lock()
	watch := s.watches[workspaceID]
	references := 0
	if watch != nil {
		references = watch.references
		delete(s.watches, workspaceID)
		close(watch.stop)
	}
	for _, state := range s.repos[workspaceID] {
		state.scheduleMu.Lock()
		if state.refreshTimer != nil {
			state.refreshTimer.Stop()
		}
		state.scheduleMu.Unlock()
	}
	delete(s.repos, workspaceID)
	s.mu.Unlock()
	if watch != nil {
		<-watch.done
	}
	for index := 0; index < references; index++ {
		if err := s.Subscribe(ctx, workspaceID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) scheduleStatusRefresh(state *repositoryState) {
	state.scheduleMu.Lock()
	if state.refreshTimer != nil {
		state.refreshTimer.Stop()
	}
	state.refreshTimer = time.AfterFunc(100*time.Millisecond, func() {
		s.refreshState(state)
	})
	state.scheduleMu.Unlock()
}

func (s *Service) refreshState(state *repositoryState) {
	state.refreshMu.Lock()
	if state.refreshing {
		state.refreshAgain = true
		state.refreshMu.Unlock()
		return
	}
	state.refreshing = true
	state.refreshMu.Unlock()
	for {
		state.mutationMu.Lock()
		snapshot, err := s.loadStatus(context.Background(), state)
		state.mutationMu.Unlock()

		state.refreshMu.Lock()
		again := state.refreshAgain
		state.refreshAgain = false
		if err == nil && snapshot.Revision != state.revision.Load() {
			again = true
		}
		if !again {
			state.refreshing = false
		}
		state.refreshMu.Unlock()

		if err != nil {
			s.emit(Event{Type: "git_resync_required", WorkspaceID: state.workspaceID, RepositoryID: repositoryID(state.workspaceID, state.root)})
			return
		}
		if snapshot.Revision == state.revision.Load() {
			s.emit(Event{Type: "git_status", WorkspaceID: state.workspaceID, RepositoryID: snapshot.RepositoryID, Status: &snapshot})
		}
		if !again {
			return
		}
	}
}

func (w *workspaceWatch) addRepository(state *repositoryState) {
	id := repositoryID(state.workspaceID, state.root)
	w.mu.Lock()
	w.repositories[id] = state
	w.mu.Unlock()
	for _, directory := range []string{state.gitDir, state.commonDir} {
		if directory == "" {
			continue
		}
		w.addDirectory(directory)
		w.addTree(filepath.Join(directory, "refs"))
		w.addTree(filepath.Join(directory, "worktrees"))
	}
}

func (w *workspaceWatch) replaceRepositories(states []*repositoryState) {
	wanted := make(map[string]bool, len(states))
	for _, state := range states {
		wanted[repositoryID(state.workspaceID, state.root)] = true
	}
	w.mu.Lock()
	for id := range w.repositories {
		if !wanted[id] {
			delete(w.repositories, id)
		}
	}
	w.mu.Unlock()
	for _, state := range states {
		w.addRepository(state)
	}
}

func (w *workspaceWatch) addTree(root string) {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			w.addDirectory(path)
		}
		return nil
	})
}

func (w *workspaceWatch) addDirectory(directory string) {
	directory = filepath.Clean(directory)
	w.mu.Lock()
	if w.directories[pathIdentity(directory)] {
		w.mu.Unlock()
		return
	}
	if err := w.watcher.Add(directory); err == nil {
		w.directories[pathIdentity(directory)] = true
	}
	w.mu.Unlock()
}

func (w *workspaceWatch) run() {
	defer close(w.done)
	defer w.watcher.Close()
	for {
		select {
		case <-w.stop:
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				w.resync()
				return
			}
			if event.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					w.addTree(event.Name)
				}
			}
			for _, state := range w.statesForPath(event.Name) {
				state.revision.Add(1)
				w.service.scheduleStatusRefresh(state)
			}
		case _, ok := <-w.watcher.Errors:
			w.resync()
			if !ok {
				return
			}
		}
	}
}

func (w *workspaceWatch) statesForPath(path string) []*repositoryState {
	w.mu.RLock()
	defer w.mu.RUnlock()
	result := []*repositoryState{}
	for _, state := range w.repositories {
		if _, ok := relativeWithin(state.gitDir, path); ok {
			result = append(result, state)
			continue
		}
		if state.commonDir != "" {
			if _, ok := relativeWithin(state.commonDir, path); ok {
				result = append(result, state)
			}
		}
	}
	return result
}

func (w *workspaceWatch) resync() {
	w.service.emit(Event{Type: "git_resync_required", WorkspaceID: w.workspaceID})
	w.mu.RLock()
	states := make([]*repositoryState, 0, len(w.repositories))
	for _, state := range w.repositories {
		states = append(states, state)
	}
	w.mu.RUnlock()
	for _, state := range states {
		state.revision.Add(1)
		w.service.scheduleStatusRefresh(state)
	}
}
