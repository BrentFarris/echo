package sourcecontrol

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Service multiplexes provider capabilities behind one repository namespace.
type Service struct {
	mu            sync.RWMutex
	providers     map[string]Provider
	providerOrder []string
	repositories  map[string]map[string]string
	subscriptions map[string]int
	notify        func(Event)
	refreshTimers map[string]*time.Timer
}

func New() *Service {
	return &Service{
		providers: make(map[string]Provider), repositories: make(map[string]map[string]string),
		subscriptions: make(map[string]int), refreshTimers: make(map[string]*time.Timer),
	}
}

func (s *Service) Register(provider Provider) error {
	if provider == nil {
		return fmt.Errorf("source control provider is required")
	}
	descriptor := provider.Descriptor(context.Background(), "")
	id := strings.TrimSpace(descriptor.ID)
	if id == "" {
		return fmt.Errorf("source control provider ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.providers[id] != nil {
		return fmt.Errorf("duplicate source control provider %q", id)
	}
	s.providers[id] = provider
	s.providerOrder = append(s.providerOrder, id)
	return nil
}

func (s *Service) SetNotifier(notify func(Event)) {
	s.mu.Lock()
	s.notify = notify
	s.mu.Unlock()
}

func (s *Service) Emit(event Event) {
	if event.Type == "" {
		return
	}
	s.mu.RLock()
	notify := s.notify
	s.mu.RUnlock()
	if notify != nil {
		notify(event)
	}
}

func (s *Service) Providers(ctx context.Context, workspaceID string) []ProviderDescriptor {
	providers := s.providerSnapshot()
	result := make([]ProviderDescriptor, 0, len(providers))
	for _, provider := range providers {
		result = append(result, provider.Descriptor(ctx, workspaceID))
	}
	return result
}

func (s *Service) Repositories(ctx context.Context, workspaceID string) ([]Repository, error) {
	providers := s.providerSnapshot()
	repositories := make([]Repository, 0)
	providerByRepository := make(map[string]string)
	var firstErr error
	for _, provider := range providers {
		items, err := provider.Repositories(ctx, workspaceID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, repository := range items {
			if repository.ID == "" || repository.ProviderID == "" {
				continue
			}
			providerByRepository[repository.ID] = repository.ProviderID
			repositories = append(repositories, repository)
		}
	}
	if len(repositories) == 0 && firstErr != nil {
		return nil, firstErr
	}
	sort.SliceStable(repositories, func(i, j int) bool {
		left, right := strings.ToLower(repositories[i].Label), strings.ToLower(repositories[j].Label)
		if left != right {
			return left < right
		}
		if repositories[i].ProviderID != repositories[j].ProviderID {
			return repositories[i].ProviderID < repositories[j].ProviderID
		}
		return repositories[i].ID < repositories[j].ID
	})
	s.mu.Lock()
	s.repositories[workspaceID] = providerByRepository
	s.mu.Unlock()
	return repositories, nil
}

func (s *Service) Status(ctx context.Context, workspaceID, repositoryID string) (StatusSnapshot, error) {
	provider, err := s.providerForRepository(ctx, workspaceID, repositoryID)
	if err != nil {
		return StatusSnapshot{}, err
	}
	capability, ok := provider.(StatusProvider)
	if !ok {
		return StatusSnapshot{}, unsupported("status")
	}
	return capability.Status(ctx, workspaceID, repositoryID)
}

func (s *Service) Diff(ctx context.Context, workspaceID, repositoryID string, target DiffTarget) (DiffDocument, error) {
	provider, err := s.providerForRepository(ctx, workspaceID, repositoryID)
	if err != nil {
		return DiffDocument{}, err
	}
	capability, ok := provider.(DiffProvider)
	if !ok {
		return DiffDocument{}, unsupported("diff")
	}
	return capability.Diff(ctx, workspaceID, repositoryID, target)
}

func (s *Service) Metadata(ctx context.Context, workspaceID, repositoryID string) (Metadata, error) {
	provider, err := s.providerForRepository(ctx, workspaceID, repositoryID)
	if err != nil {
		return Metadata{}, err
	}
	capability, ok := provider.(MetadataProvider)
	if !ok {
		return Metadata{}, unsupported("metadata")
	}
	return capability.Metadata(ctx, workspaceID, repositoryID)
}

func (s *Service) History(ctx context.Context, workspaceID, repositoryID string, offset, limit int) (History, error) {
	provider, err := s.providerForRepository(ctx, workspaceID, repositoryID)
	if err != nil {
		return History{}, err
	}
	capability, ok := provider.(HistoryProvider)
	if !ok {
		return History{}, unsupported("history")
	}
	return capability.History(ctx, workspaceID, repositoryID, offset, limit)
}

func (s *Service) RevisionDetail(ctx context.Context, workspaceID, repositoryID, ref, kind string) (RevisionDetail, error) {
	provider, err := s.providerForRepository(ctx, workspaceID, repositoryID)
	if err != nil {
		return RevisionDetail{}, err
	}
	capability, ok := provider.(HistoryProvider)
	if !ok {
		return RevisionDetail{}, unsupported("history")
	}
	return capability.RevisionDetail(ctx, workspaceID, repositoryID, ref, kind)
}

func (s *Service) Annotate(ctx context.Context, workspaceID, repositoryID, path, ref string, startLine, endLine int) (Annotation, error) {
	provider, err := s.providerForRepository(ctx, workspaceID, repositoryID)
	if err != nil {
		return Annotation{}, err
	}
	capability, ok := provider.(AnnotateProvider)
	if !ok {
		return Annotation{}, unsupported("annotate")
	}
	return capability.Annotate(ctx, workspaceID, repositoryID, path, ref, startLine, endLine)
}

func (s *Service) Action(ctx context.Context, workspaceID, repositoryID string, request ActionRequest) (result ActionResult, resultErr error) {
	provider, err := s.providerForRepository(ctx, workspaceID, repositoryID)
	if err != nil {
		return ActionResult{}, err
	}
	capability, ok := provider.(ActionProvider)
	if !ok {
		return ActionResult{}, unsupported("actions")
	}
	descriptor := provider.Descriptor(context.Background(), "")
	operation := Operation{WorkspaceID: workspaceID, RepositoryID: repositoryID, ProviderID: descriptor.ID, RequestID: request.RequestID, Action: request.Action, State: "running"}
	s.Emit(Event{Type: "source_control_operation", WorkspaceID: workspaceID, RepositoryID: repositoryID, ProviderID: descriptor.ID, Operation: &operation})
	defer func() {
		operation.State = "completed"
		if resultErr != nil {
			operation.State = "failed"
			operation.Error = resultErr.Error()
		}
		s.Emit(Event{Type: "source_control_operation", WorkspaceID: workspaceID, RepositoryID: repositoryID, ProviderID: descriptor.ID, Operation: &operation})
		if resultErr == nil {
			if snapshot, statusErr := s.Status(context.Background(), workspaceID, repositoryID); statusErr == nil {
				s.Emit(Event{Type: "source_control_status", WorkspaceID: workspaceID, RepositoryID: repositoryID, ProviderID: descriptor.ID, Status: &snapshot})
			}
		}
	}()
	return capability.Action(ctx, workspaceID, repositoryID, request)
}

func (s *Service) Subscribe(ctx context.Context, workspaceID string) error {
	s.mu.Lock()
	s.subscriptions[workspaceID]++
	first := s.subscriptions[workspaceID] == 1
	s.mu.Unlock()
	if !first {
		return nil
	}
	for _, provider := range s.providerSnapshot() {
		if watcher, ok := provider.(WatchProvider); ok {
			if err := watcher.Subscribe(ctx, workspaceID); err != nil {
				s.Unsubscribe(workspaceID)
				return err
			}
		}
	}
	return nil
}

func (s *Service) Unsubscribe(workspaceID string) {
	s.mu.Lock()
	if s.subscriptions[workspaceID] > 1 {
		s.subscriptions[workspaceID]--
		s.mu.Unlock()
		return
	}
	delete(s.subscriptions, workspaceID)
	s.mu.Unlock()
	for _, provider := range s.providerSnapshot() {
		if watcher, ok := provider.(WatchProvider); ok {
			watcher.Unsubscribe(workspaceID)
		}
	}
}

func (s *Service) InvalidateWorkspace(workspaceID string) {
	for _, provider := range s.providerSnapshot() {
		if watcher, ok := provider.(WatchProvider); ok {
			watcher.InvalidateWorkspace(workspaceID)
		}
	}
	s.mu.Lock()
	if s.subscriptions[workspaceID] == 0 {
		s.mu.Unlock()
		return
	}
	if timer := s.refreshTimers[workspaceID]; timer != nil {
		timer.Stop()
	}
	s.refreshTimers[workspaceID] = time.AfterFunc(175*time.Millisecond, func() { s.refreshWorkspace(workspaceID) })
	s.mu.Unlock()
}

func (s *Service) refreshWorkspace(workspaceID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repositories, err := s.Repositories(ctx, workspaceID)
	if err != nil {
		s.Emit(Event{Type: "source_control_resync_required", WorkspaceID: workspaceID})
		return
	}
	for _, repository := range repositories {
		if !repository.Available {
			continue
		}
		status, statusErr := s.Status(ctx, workspaceID, repository.ID)
		if statusErr == nil {
			s.Emit(Event{Type: "source_control_status", WorkspaceID: workspaceID, RepositoryID: repository.ID, ProviderID: repository.ProviderID, Status: &status})
		}
	}
}

func (s *Service) ResetWorkspace(ctx context.Context, workspaceID string) error {
	s.mu.Lock()
	delete(s.repositories, workspaceID)
	s.mu.Unlock()
	for _, provider := range s.providerSnapshot() {
		if watcher, ok := provider.(WatchProvider); ok {
			if err := watcher.ResetWorkspace(ctx, workspaceID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) RemoveWorkspace(workspaceID string) {
	s.mu.Lock()
	delete(s.repositories, workspaceID)
	delete(s.subscriptions, workspaceID)
	if timer := s.refreshTimers[workspaceID]; timer != nil {
		timer.Stop()
	}
	delete(s.refreshTimers, workspaceID)
	s.mu.Unlock()
	for _, provider := range s.providerSnapshot() {
		if watcher, ok := provider.(WatchProvider); ok {
			watcher.RemoveWorkspace(workspaceID)
		}
	}
}

func (s *Service) StopWorkspaceProcesses(workspaceID string) {
	for _, provider := range s.providerSnapshot() {
		if watcher, ok := provider.(WatchProvider); ok {
			watcher.StopWorkspaceProcesses(workspaceID)
		}
	}
}

func (s *Service) Close() {
	s.mu.Lock()
	for _, timer := range s.refreshTimers {
		timer.Stop()
	}
	s.refreshTimers = make(map[string]*time.Timer)
	s.mu.Unlock()
	for _, provider := range s.providerSnapshot() {
		if watcher, ok := provider.(WatchProvider); ok {
			watcher.Close()
		}
	}
}

func (s *Service) providerForRepository(ctx context.Context, workspaceID, repositoryID string) (Provider, error) {
	s.mu.RLock()
	providerID := s.repositories[workspaceID][repositoryID]
	provider := s.providers[providerID]
	s.mu.RUnlock()
	if provider != nil {
		return provider, nil
	}
	if _, err := s.Repositories(ctx, workspaceID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	providerID = s.repositories[workspaceID][repositoryID]
	provider = s.providers[providerID]
	s.mu.RUnlock()
	if provider == nil {
		return nil, &Error{Code: "repository_not_found", Message: "source control repository not found", Cause: ErrNotFound}
	}
	return provider, nil
}

func (s *Service) providerSnapshot() []Provider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Provider, 0, len(s.providerOrder))
	for _, id := range s.providerOrder {
		if provider := s.providers[id]; provider != nil {
			result = append(result, provider)
		}
	}
	return result
}

func unsupported(capability string) error {
	return &Error{Code: "unsupported_source_control_capability", Message: "this repository does not support " + capability}
}
