package p4

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/workspacefs"
)

func (p *Provider) SetNotifier(notify func(sourcecontrol.Event)) {
	p.mu.Lock()
	p.notify = notify
	p.mu.Unlock()
}
func (p *Provider) SetWatcher(watch func(string, bool) error) {
	p.mu.Lock()
	p.watch = watch
	workspaces := []string{}
	for id := range p.config.Workspaces {
		workspaces = append(workspaces, id)
	}
	p.mu.Unlock()
	for _, id := range workspaces {
		p.updateWatch(id)
	}
}
func (p *Provider) updateWatch(workspace string) {
	settings := p.Settings(workspace)
	enabled := false
	for _, value := range settings.Repositories {
		enabled = enabled || value.Tracking
	}
	enabled = enabled && !p.disabled(workspace)
	p.mu.Lock()
	watch := p.watch
	previous := p.watching[workspace]
	if watch != nil {
		p.watching[workspace] = enabled
	}
	p.mu.Unlock()
	if watch != nil && previous != enabled {
		if err := watch(workspace, enabled); err != nil {
			p.mu.Lock()
			p.watching[workspace] = previous
			p.mu.Unlock()
		}
	}
}
func (p *Provider) HandleFileEvent(event workspacefs.WatchEvent) {
	if p.disabled(event.WorkspaceID) {
		return
	}
	p.mu.Lock()
	states := append([]*repository{}, p.discovery[event.WorkspaceID].repos...)
	p.mu.Unlock()
	settings := p.Settings(event.WorkspaceID)
	if len(states) == 0 && len(event.Changes) > 0 {
		enabled := false
		for _, setting := range settings.Repositories {
			enabled = enabled || setting.Tracking
		}
		if enabled {
			states, _ = p.discover(context.Background(), event.WorkspaceID)
		}
	}
	for _, r := range states {
		if !settings.Repositories[r.id].Tracking {
			continue
		}
		r.mu.Lock()
		if r.detached {
			r.mu.Unlock()
			continue
		}
		if event.ResyncRequired || event.Type == "fs_resync_required" {
			r.incomplete = true
		}
		for _, change := range event.Changes {
			if change.IsDirectory {
				r.incomplete = true
				continue
			}
			for _, root := range r.roots {
				if change.Ref.RootID != root.ID {
					continue
				}
				path := filepath.Join(root.HostPath, filepath.FromSlash(change.Ref.Path))
				if workspacefs.IsProtectedWorkspaceMetadataPath(change.Ref.Path) || strings.Contains(filepath.Base(path), ".echo-") {
					continue
				}
				if until := r.suppressed[path]; time.Now().Before(until) && r.ownedStamps[path] == fileStamp(path) {
					continue
				}
				delete(r.suppressed, path)
				delete(r.ownedStamps, path)
				if info, err := os.Stat(path); err == nil && info.IsDir() {
					r.incomplete = true
					continue
				}
				if len(r.candidates) >= sourcecontrol.StatusLimit {
					r.incomplete = true
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				ignored, ignoreErr := p.ignored(ctx, r, path)
				if ignoreErr == nil && ignored {
					meta, err := p.stat(ctx, r, path)
					cancel()
					if err == nil && meta["depotFile"] == "" {
						continue
					}
				} else {
					cancel()
				}
				r.candidates[path] = ""
			}
		}
		r.mu.Unlock()
	}
}
func (p *Provider) Subscribe(ctx context.Context, workspace string) error {
	p.mu.Lock()
	if p.subscriptions[workspace] != nil {
		p.mu.Unlock()
		return nil
	}
	pollCtx, cancel := context.WithCancel(context.Background())
	p.subscriptions[workspace] = cancel
	p.mu.Unlock()
	go func() {
		delay := 5 * time.Second
		for {
			timer := time.NewTimer(delay)
			select {
			case <-pollCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			delay = 5 * time.Second
			repos, err := p.Repositories(pollCtx, workspace)
			if err != nil {
				delay = 30 * time.Second
				continue
			}
			for _, repo := range repos {
				status, err := p.Status(pollCtx, workspace, repo.ID)
				if err != nil {
					delay = 30 * time.Second
					continue
				}
				if status.Stale {
					delay = 30 * time.Second
				}
				p.mu.Lock()
				notify := p.notify
				p.mu.Unlock()
				if notify != nil {
					notify(sourcecontrol.Event{Type: "source_control_status", WorkspaceID: workspace, RepositoryID: repo.ID, ProviderID: ID, Status: &status})
				}
			}
		}
	}()
	return nil
}
func (p *Provider) Unsubscribe(workspace string) {
	p.mu.Lock()
	if cancel := p.subscriptions[workspace]; cancel != nil {
		cancel()
	}
	delete(p.subscriptions, workspace)
	p.mu.Unlock()
}
func (p *Provider) InvalidateWorkspace(string) {}
func (p *Provider) ResetWorkspace(ctx context.Context, workspace string) error {
	p.mu.Lock()
	delete(p.discovery, workspace)
	p.mu.Unlock()
	p.updateWatch(workspace)
	return nil
}
func (p *Provider) RemoveWorkspace(workspace string) {
	p.Unsubscribe(workspace)
	p.mu.Lock()
	watch, enabled := p.watch, p.watching[workspace]
	delete(p.watching, workspace)
	delete(p.discovery, workspace)
	p.mu.Unlock()
	if enabled && watch != nil {
		_ = watch(workspace, false)
	}
}
func (p *Provider) StopWorkspaceProcesses(workspace string) {
	p.Unsubscribe(workspace)
	p.updateWatch(workspace)
}
func (p *Provider) Close() {
	p.mu.Lock()
	ids := []string{}
	for id := range p.subscriptions {
		ids = append(ids, id)
	}
	for id := range p.watching {
		ids = append(ids, id)
	}
	p.mu.Unlock()
	for _, id := range ids {
		p.RemoveWorkspace(id)
	}
}
