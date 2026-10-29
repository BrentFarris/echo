package development

import (
	"strings"
	"time"
)

// UpdateNotice is cached: reading it never fetches from the upstream repository.
type UpdateNotice struct {
	AutoCheckUpdates bool `json:"autoCheckUpdates"`
	UpdateAvailable  bool `json:"updateAvailable"`
}

func comparisonAvailable(c *Comparison, revision string) bool {
	if c == nil {
		return false
	}
	// A checkout may already have been pulled while the executable is older.
	revision = strings.TrimSuffix(revision, "-modified")
	if commitPattern.MatchString(revision) {
		return revision != c.RemoteCommit && (c.State == "behind" || c.State == "up-to-date")
	}
	return c.State == "behind"
}

func (m *Manager) UpdateNotice() UpdateNotice {
	m.mu.Lock()
	defer m.mu.Unlock()
	return UpdateNotice{AutoCheckUpdates: m.config.AutoCheckUpdates, UpdateAvailable: comparisonAvailable(m.comparison, m.revision)}
}

// StartAutomaticChecks runs once after recovery, then hourly, without a browser.
func (m *Manager) StartAutomaticChecks() { m.startAutomaticChecks(nil) }

// A supplied tick channel lets tests exercise hours without wall-clock waits.
func (m *Manager) startAutomaticChecks(ticks <-chan time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.autoStarted {
		return
	}
	m.autoStarted = true
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		if ticks == nil {
			timer := time.NewTicker(time.Hour)
			defer timer.Stop()
			ticks = timer.C
		}
		// Busy operations are never interrupted. Retry a skipped check shortly.
		retry := time.NewTimer(time.Hour)
		retry.Stop()
		defer retry.Stop()
		var retryTick <-chan time.Time
		for {
			m.mu.Lock()
			if !m.closed && m.config.AutoCheckUpdates && m.config.RepositoryPath != "" {
				if m.busy() {
					retry.Reset(time.Minute)
					retryTick = retry.C
				} else {
					_, _ = m.startLocked("check-updates")
					retryTick = nil
					retry.Stop()
				}
			} else {
				retryTick = nil
				retry.Stop()
			}
			m.mu.Unlock()
			select {
			case <-m.ctx.Done():
				return
			case <-ticks:
			case <-retryTick:
			case <-m.autoWake:
			}
		}
	}()
}
