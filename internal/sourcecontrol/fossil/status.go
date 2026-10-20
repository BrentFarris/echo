package fossil

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/brent/echo/internal/sourcecontrol"
)

type statusRecord struct {
	code    string
	path    string
	oldPath string
	group   string
	kind    string
}

var fossilRenameSeparator = regexp.MustCompile(`\s+->\s+`)

// Cache contract: `fossil changes` costs O(checkout size) (~25µs/file measured)
// no matter how few paths it is scoped to, so interactive requests must never
// wait on one. Status serves the last computed snapshot for the current
// revision and refreshes in the background (emitting source_control_status
// when done); a cold cache computes synchronously. Change records are reused
// for a short window by protect and the diff conflict check, which is what
// makes those paths spawn-free in practice. Caches are invalidated whenever
// the revision advances through a checkout-changing action or a file event.
const (
	statusFreshInterval  = 2 * time.Second
	recordsFreshInterval = 5 * time.Second
	refreshMinInterval   = 3 * time.Second
)

type cachedStatus struct {
	snapshot sourcecontrol.StatusSnapshot
	revision uint64
	at       time.Time
}

type cachedRecords struct {
	records []statusRecord
	at      time.Time
}

// cachedStatus returns a snapshot whose revision matches the current token
// and that is younger than statusFreshInterval.
func (r *repositoryState) cachedStatus() (sourcecontrol.StatusSnapshot, bool) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if r.statusCache == nil || r.statusCache.revision != r.revision.Load() {
		return sourcecontrol.StatusSnapshot{}, false
	}
	if time.Since(r.statusCache.at) > statusFreshInterval {
		return sourcecontrol.StatusSnapshot{}, false
	}
	return r.statusCache.snapshot, true
}

// staleStatus returns a revision-matching snapshot of any age, for
// stale-while-revalidate serving.
func (r *repositoryState) staleStatus() (sourcecontrol.StatusSnapshot, bool) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if r.statusCache == nil || r.statusCache.revision != r.revision.Load() {
		return sourcecontrol.StatusSnapshot{}, false
	}
	return r.statusCache.snapshot, true
}

func (r *repositoryState) storeStatus(snapshot sourcecontrol.StatusSnapshot, revision uint64) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	r.statusCache = &cachedStatus{snapshot: snapshot, revision: revision, at: time.Now()}
}

// freshRecords returns change records young enough to reuse for protect and
// the diff conflict check. Records are dropped on any checkout-changing
// mutation or file event, so age is only a safety net.
func (r *repositoryState) freshRecords() ([]statusRecord, bool) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if r.recordsCache == nil || time.Since(r.recordsCache.at) > recordsFreshInterval {
		return nil, false
	}
	return r.recordsCache.records, true
}

func (r *repositoryState) storeRecords(records []statusRecord) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	r.recordsCache = &cachedRecords{records: records, at: time.Now()}
}

func (r *repositoryState) invalidateCaches() {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	r.statusCache, r.recordsCache = nil, nil
}

func (p *Provider) Status(ctx context.Context, workspaceID, repositoryID string) (sourcecontrol.StatusSnapshot, error) {
	state, err := p.repository(ctx, workspaceID, repositoryID)
	if err != nil {
		return sourcecontrol.StatusSnapshot{}, err
	}
	state.rootMu.Lock()
	journal, _ := p.checkpoints.LoadJournal(state.workspaceID, ID, state.repositoryID())
	recoveryErr := p.recoverProtectedCommitState(ctx, state)
	state.rootMu.Unlock()
	if recoveryErr != nil {
		return sourcecontrol.StatusSnapshot{}, recoveryErr
	}
	if journal != nil {
		// Recovery restored checkout state; anything cached before it is stale.
		state.invalidateCaches()
	}
	if p.cachingAllowed(workspaceID) {
		if snapshot, ok := state.cachedStatus(); ok {
			return snapshot, nil
		}
		if snapshot, stale := state.staleStatus(); stale {
			// Serve last-known state immediately and push a fresh snapshot when
			// the background refresh completes.
			p.queueStatusRefresh(state)
			return snapshot, nil
		}
	}
	return p.computeStatus(ctx, state)
}

// computeStatus builds and caches a fresh snapshot. Concurrent callers share
// one computation via double-checked locking instead of each spawning their
// own checkout-wide `fossil changes`.
func (p *Provider) computeStatus(ctx context.Context, state *repositoryState) (sourcecontrol.StatusSnapshot, error) {
	state.computeMu.Lock()
	defer state.computeMu.Unlock()
	if p.cachingAllowed(state.workspaceID) {
		if snapshot, ok := state.cachedStatus(); ok {
			return snapshot, nil
		}
	}
	state.rootMu.RLock()
	defer state.rootMu.RUnlock()
	snapshot, err := p.loadStatus(ctx, state)
	if err != nil {
		return sourcecontrol.StatusSnapshot{}, err
	}
	state.storeStatus(snapshot, state.revision.Load())
	return snapshot, nil
}

// queueStatusRefresh recomputes the snapshot in the background at a throttled
// rate and pushes the result to subscribers. Single-flight per repository.
func (p *Provider) queueStatusRefresh(state *repositoryState) {
	state.cacheMu.Lock()
	if state.refreshScheduled {
		state.cacheMu.Unlock()
		return
	}
	state.refreshScheduled = true
	delay := time.Duration(0)
	if !state.lastRefresh.IsZero() {
		if elapsed := time.Since(state.lastRefresh); elapsed < refreshMinInterval {
			delay = refreshMinInterval - elapsed
		}
	}
	state.cacheMu.Unlock()
	refresh := func() {
		state.cacheMu.Lock()
		state.lastRefresh = time.Now()
		state.cacheMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), localCommandTimeout)
		defer cancel()
		snapshot, err := p.computeStatus(ctx, state)
		state.cacheMu.Lock()
		state.refreshScheduled = false
		state.cacheMu.Unlock()
		if err == nil {
			p.emitStatus(state, snapshot)
		}
	}
	if delay > 0 {
		state.cacheMu.Lock()
		state.refreshTimer = time.AfterFunc(delay, refresh)
		state.cacheMu.Unlock()
		return
	}
	go refresh()
}

func (p *Provider) loadStatus(ctx context.Context, state *repositoryState) (sourcecontrol.StatusSnapshot, error) {
	// `fossil info` and `fossil changes` are independent; run them concurrently
	// so a status refresh costs one spawn latency instead of two.
	type checkoutInfoResult struct {
		info checkoutInfo
		err  error
	}
	infoResultCh := make(chan checkoutInfoResult, 1)
	go func() {
		info, err := p.checkoutInfo(ctx, state.workspaceID, state.root)
		infoResultCh <- checkoutInfoResult{info, err}
	}()
	records, err := p.rawStatus(ctx, state)
	infoResult := <-infoResultCh
	if err != nil {
		return sourcecontrol.StatusSnapshot{}, err
	}
	if infoResult.err != nil {
		return sourcecontrol.StatusSnapshot{}, infoResult.err
	}
	return p.buildStatus(ctx, state, infoResult.info, records)
}

func (p *Provider) buildStatus(ctx context.Context, state *repositoryState, info checkoutInfo, records []statusRecord) (sourcecontrol.StatusSnapshot, error) {
	manifest, err := p.loadCheckpoint(state)
	if err != nil {
		return sourcecontrol.StatusSnapshot{}, err
	}
	protectedEntries := checkpointEntries(manifest)
	protectedDiagnostic := checkpointStale(manifest, state, info.Checkout)

	groups := map[string][]sourcecontrol.Change{"conflicts": {}, protectedGroupID: {}, "working": {}, "untracked": {}}
	hidden := 0
	seen := make(map[string]bool)
	seenProtected := make(map[string]bool)
	merge := false
	for _, record := range records {
		key := record.group + "\x00" + pathIdentity(record.path)
		if seen[key] {
			continue
		}
		seen[key] = true
		if state.isProtectedPath(record.path) {
			continue
		}
		if !state.pathAllowed(record.path) {
			if record.group != "untracked" {
				hidden++
			}
			continue
		}
		protectedEntry, recordIsProtected := protectedEntryForRecord(protectedEntries, record)
		if recordIsProtected {
			seenProtected[pathIdentity(protectedEntry.Path)] = true
		}
		if record.group == "conflicts" || strings.Contains(record.code, "MERGE") || strings.Contains(record.code, "INTEGRATE") {
			merge = true
		}
		if record.group == "conflicts" {
			ref, _ := state.refForPath(record.path)
			groups["conflicts"] = append(groups["conflicts"], sourcecontrol.Change{
				Path: record.path, OldPath: record.oldPath, Ref: ref, Status: statusLabel(record.code),
				StatusCode: record.code, Kind: record.kind, GroupID: "conflicts",
			})
			continue
		}
		if recordIsProtected {
			matched, matchErr := p.fileStateMatchesWorking(state, protectedEntry)
			if matchErr != nil {
				return sourcecontrol.StatusSnapshot{}, matchErr
			}
			if !matched {
				copy := record
				groups["working"] = append(groups["working"], p.laterChange(state, protectedEntry, &copy))
			}
			continue
		}
		ref, _ := state.refForPath(record.path)
		change := sourcecontrol.Change{
			Path: record.path, OldPath: record.oldPath, Ref: ref, Status: statusLabel(record.code),
			StatusCode: record.code, Kind: record.kind, GroupID: record.group,
		}
		groups[record.group] = append(groups[record.group], change)
	}
	for _, entry := range protectedEntries {
		groups[protectedGroupID] = append(groups[protectedGroupID], protectedChange(entry, state))
		if seenProtected[pathIdentity(entry.Path)] {
			continue
		}
		matched, matchErr := p.fileStateMatchesWorking(state, entry)
		if matchErr != nil {
			return sourcecontrol.StatusSnapshot{}, matchErr
		}
		if !matched {
			groups["working"] = append(groups["working"], p.laterChange(state, entry, nil))
		}
	}
	for id := range groups {
		sort.SliceStable(groups[id], func(i, j int) bool { return strings.ToLower(groups[id][i].Path) < strings.ToLower(groups[id][j].Path) })
	}
	totalPaths := make(map[string]bool)
	for _, changes := range groups {
		for _, change := range changes {
			totalPaths[pathIdentity(change.Path)] = true
		}
	}
	total := len(totalPaths)
	truncated := total > sourcecontrol.StatusLimit
	if truncated {
		remaining := sourcecontrol.StatusLimit
		for _, id := range []string{"conflicts", protectedGroupID, "working", "untracked"} {
			if len(groups[id]) > remaining {
				groups[id] = groups[id][:remaining]
			}
			remaining -= len(groups[id])
			if remaining < 0 {
				remaining = 0
			}
		}
	}
	workingActions := []string{"discard", "protect", "untrack"}
	untrackedActions := []string{"protect", "track", "discard"}
	protectedActions := []string{}
	if manifest == nil {
		workingActions = append(workingActions, "commit_selected")
	} else {
		// The unprotect action also serves as the provider-neutral signal that
		// a checkpoint exists. Unlike the rendered change count, it is not lost
		// if an unusually large status is truncated before this group.
		protectedActions = append(protectedActions, "unprotect")
		if protectedDiagnostic == "" {
			protectedActions = append(protectedActions, "commit_protected")
		} else {
			workingActions = nil
			untrackedActions = nil
		}
	}
	conflictActions := []string{"discard"}
	if protectedDiagnostic != "" {
		conflictActions = nil
	}
	return sourcecontrol.StatusSnapshot{
		WorkspaceID: state.workspaceID, RepositoryID: sourcecontrol.RepositoryID(state.workspaceID, ID, state.root), ProviderID: ID,
		Revision: state.revision.Load(), Branch: info.Branch, Head: info.Checkout,
		Groups: []sourcecontrol.ChangeGroup{
			{ID: "conflicts", Label: "Merge Changes", Role: "conflicts", Changes: groups["conflicts"], Actions: conflictActions},
			{ID: protectedGroupID, Label: "Protected Changes", Role: "included", Changes: groups[protectedGroupID], Actions: protectedActions, Diagnostic: protectedDiagnostic},
			{ID: "working", Label: "Changes", Role: "working", Changes: groups["working"], Actions: workingActions},
			{ID: "untracked", Label: "Untracked Files", Role: "untracked", Changes: groups["untracked"], Actions: untrackedActions},
		},
		HiddenChangeCount: hidden, Truncated: truncated, TotalChangeCount: total,
		State: sourcecontrol.RepositoryState{MergeInProgress: merge},
	}, nil
}

func parseClassifiedChanges(output string) []statusRecord {
	result := []statusRecord{}
	for _, raw := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		code := strings.ToUpper(strings.TrimSuffix(fields[0], ":"))
		pathValue := strings.TrimSpace(line[len(fields[0]):])
		record := statusRecord{code: code, path: filepathClean(pathValue), group: "working", kind: "modified"}
		switch code {
		case "CONFLICT", "CONFLICT_1", "CONFLICT_2":
			record.group, record.kind = "conflicts", "conflict"
		case "EXTRA":
			record.group, record.kind = "untracked", "untracked"
		case "ADDED", "ADDED_BY_MERGE", "ADDED_BY_INTEGRATE":
			record.kind = "added"
		case "DELETED", "MISSING":
			record.kind = "deleted"
		case "RENAMED":
			record.kind = "renamed"
		case "UPDATED_BY_MERGE", "UPDATED_BY_INTEGRATE", "UPDATED_BY_CHERRY_PICK", "UPDATED_BY_BACKOUT":
			record.kind = "modified"
		}
		// Fossil 2.27 classifies a renamed file whose content also changed as
		// EDITED and prints "old  ->  new". Treat the arrow as the rename
		// signal independently of the leading classification token.
		if record.group != "conflicts" && record.group != "untracked" {
			if endpoints := fossilRenameSeparator.Split(pathValue, 2); len(endpoints) == 2 {
				record.code, record.kind = "RENAMED", "renamed"
				record.oldPath = filepathClean(endpoints[0])
				record.path = filepathClean(endpoints[1])
			}
		}
		if record.path != "" {
			result = append(result, record)
		}
	}
	return result
}

func filepathClean(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	value = strings.TrimPrefix(value, "./")
	if clean, err := cleanPath(value); err == nil {
		return clean
	}
	return ""
}

func statusLabel(code string) string {
	labels := map[string]string{
		"EDITED": "Modified", "ADDED": "Added", "DELETED": "Deleted", "MISSING": "Missing",
		"RENAMED": "Renamed", "CONFLICT": "Conflict", "EXTRA": "Untracked",
		"UPDATED_BY_MERGE": "Updated by merge", "ADDED_BY_MERGE": "Added by merge",
		"UPDATED_BY_INTEGRATE": "Updated by integrate", "ADDED_BY_INTEGRATE": "Added by integrate",
	}
	if label := labels[code]; label != "" {
		return label
	}
	return strings.ToLower(strings.ReplaceAll(code, "_", " "))
}
