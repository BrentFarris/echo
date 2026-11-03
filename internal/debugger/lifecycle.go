package debugger

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/brent/echo/internal/debugconfig"
	"github.com/google/uuid"
	"time"
)

func (s *Service) Start(ctx context.Context, id string, request StartRequest) (Snapshot, error) {
	if _, err := s.workspace(id); err != nil {
		return Snapshot{}, err
	}
	config, err := s.options.Configurations(id)
	if err != nil {
		return Snapshot{}, err
	}
	if (request.ConfigurationID == "") == (request.CompoundID == "") {
		return Snapshot{}, fmt.Errorf("exactly one configuration or compound is required")
	}
	var entries []debugconfig.Configuration
	var compound debugconfig.Compound
	if request.ConfigurationID != "" {
		entry, ok := config.Configuration(request.ConfigurationID)
		if !ok {
			return Snapshot{}, fmt.Errorf("configuration no longer exists; reload launch.json")
		}
		entries = append(entries, entry)
	} else {
		var ok bool
		compound, ok = config.Compound(request.CompoundID)
		if !ok {
			return Snapshot{}, fmt.Errorf("compound no longer exists")
		}
		if compound.Error != "" {
			return Snapshot{}, fmt.Errorf("%s", compound.Error)
		}
		for _, key := range compound.ConfigurationIDs {
			entry, ok := config.Configuration(key)
			if !ok {
				return Snapshot{}, fmt.Errorf("compound configuration %q was not found", key)
			}
			entries = append(entries, entry)
		}
	}
	if len(entries) == 0 {
		return Snapshot{}, fmt.Errorf("compound contains no configurations")
	}
	prepared := []*session{}
	dispose := func() {
		for _, current := range prepared {
			current.cancel()
			if current.launch != nil && current.launch.Cleanup != nil {
				current.launch.Cleanup()
			}
			current.release()
		}
	}
	// Preparation validates every member without starting a process.
	for _, entry := range entries {
		current, e := s.prepareSession(ctx, id, entry, request)
		if e != nil {
			dispose()
			return Snapshot{}, e
		}
		prepared = append(prepared, current)
	}
	s.mu.Lock()
	active := 0
	for _, runtime := range s.runtimes {
		for _, current := range runtime.sessions {
			if !isTerminalStatus(current.status) {
				active++
			}
		}
	}
	if s.closed || active+len(prepared) > 32 {
		s.mu.Unlock()
		dispose()
		return Snapshot{}, fmt.Errorf("debugger is shutting down or the session limit was reached; stop an unused session")
	}
	runtime := s.runtimeLocked(id)
	if len(runtime.sessions) > 64 {
		for key, current := range runtime.sessions {
			if isTerminalStatus(current.status) {
				select {
				case <-current.cleaned:
					delete(runtime.sessions, key)
				default:
				}
			}
		}
		for key, group := range runtime.groups {
			retained := false
			for _, id := range group.sessionIDs {
				if runtime.sessions[id] != nil {
					retained = true
				}
			}
			if !retained {
				delete(runtime.groups, key)
			}
		}
	}
	groupID := ""
	if request.CompoundID != "" {
		groupID = uuid.NewString()
		runtime.groups[groupID] = &sessionGroup{id: groupID, name: compound.Name, compoundID: compound.ID, stopAll: compound.StopAll}
	}
	for _, current := range prepared {
		s.lifetimes.Add(1)
		current.groupID = groupID
		runtime.sessions[current.id] = current
		if groupID != "" {
			runtime.groups[groupID].sessionIDs = append(runtime.groups[groupID].sessionIDs, current.id)
		}
	}
	s.mu.Unlock()
	for _, current := range prepared {
		context.AfterFunc(current.ctx, func() { _ = s.Stop(id, current.id, nil) })
		s.publishSession(id, current.id, "session_started", nil, "")
		go s.runSession(current)
	}
	// Startup supervision belongs to the host, so losing the HTTP connection
	// does not terminate a launch. A failed compound still rolls back fully.
	startup := make(chan error, 1)
	go func() {
		for _, current := range prepared {
			<-current.ready
			s.mu.Lock()
			failure, status := current.err, current.status
			s.mu.Unlock()
			if status == StatusFailed {
				for _, v := range prepared {
					_ = s.Stop(id, v.id, nil)
				}
				startup <- fmt.Errorf("%s: %s", current.configuration.Name, failure)
				return
			}
		}
		startup <- nil
	}()
	select {
	case err := <-startup:
		if err != nil {
			return Snapshot{}, err
		}
		return s.Snapshot(id)
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	}
}

func (s *Service) prepareSession(requestContext context.Context, id string, entry debugconfig.Configuration, request StartRequest) (*session, error) {
	if err := debugconfig.ValidateConfiguration(entry); err != nil {
		return nil, err
	}
	provider, err := s.options.Provider(id, entry.AdapterProfileID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	release := func() {}
	if s.options.Lease != nil {
		var e error
		owner := provider.Metadata().PluginID
		if owner == "" {
			owner = provider.Metadata().ID
		}
		ctx, release, e = s.options.Lease(ctx, id, owner)
		if e != nil {
			cancel()
			return nil, e
		}
	}
	current := &session{id: uuid.NewString(), workspaceID: id, configuration: entry, profile: provider.Metadata(), provider: provider, startRequest: request, status: StatusStarting, revision: 1, startedAt: time.Now().UTC(), ctx: ctx, cancel: cancel, release: release, done: make(chan struct{}), ready: make(chan struct{}), cleaned: make(chan struct{}), initialized: make(chan struct{}), capabilities: map[string]any{}, breakpointSources: map[string]bool{}, breakpointStatuses: map[string]BreakpointStatus{}, adapterBreakpointIDs: map[int]string{}}
	roots, err := s.fs.Roots(id)
	file := ""
	if err == nil && request.CurrentFile != nil {
		file, err = s.resolveSource(id, *request.CurrentFile)
	}
	stop := context.AfterFunc(requestContext, cancel)
	if err == nil {
		s.mu.Lock()
		run, stopTerminals := s.runInTerminal, s.stopTerminals
		s.mu.Unlock()
		var terminal func(context.Context, map[string]any) (map[string]any, error)
		if run != nil {
			terminal = func(ctx context.Context, args map[string]any) (map[string]any, error) {
				return run(ctx, id, current.id, args)
			}
		}
		var stopOwned func()
		if stopTerminals != nil {
			stopOwned = func() { stopTerminals(id, current.id) }
		}
		current.launch, err = provider.Prepare(ctx, LaunchContext{RunInTerminal: terminal, StopTerminals: stopOwned, SessionID: current.id, BotID: id, CurrentFile: file, Roots: roots, Inputs: request.Inputs, SelectedText: request.SelectedText, NoDebug: request.NoDebug}, entry)
	}
	stop()
	if err != nil {
		cancel()
		release()
		return nil, err
	}
	return current, nil
}

func (s *Service) RestartChecked(ctx context.Context, id, sessionID string, revision uint64, fresh ...StartRequest) (Snapshot, error) {
	s.mu.Lock()
	current, err := s.sessionLocked(id, sessionID)
	if err != nil {
		s.mu.Unlock()
		return Snapshot{}, err
	}
	request := current.startRequest
	if len(fresh) > 0 {
		request.Inputs = fresh[0].Inputs
		request.CurrentFile = fresh[0].CurrentFile
		request.SelectedText = fresh[0].SelectedText
	}
	request.ConfigurationID = current.configuration.ID
	request.CompoundID = ""
	s.mu.Unlock()
	if err = s.StopChecked(id, sessionID, revision, nil); err != nil {
		return Snapshot{}, err
	}
	return s.Start(ctx, id, request)
}
func (s *Service) StopGroupChecked(id, groupID string, expected map[string]uint64) error {
	s.mu.Lock()
	group := s.runtimeLocked(id).groups[groupID]
	if group == nil {
		s.mu.Unlock()
		return fmt.Errorf("debug group was not found")
	}
	ids := append([]string{}, group.sessionIDs...)
	for _, key := range ids {
		current := s.runtimeLocked(id).sessions[key]
		if revision := expected[key]; revision != 0 && current.revision != revision {
			s.mu.Unlock()
			return &RevisionError{Expected: revision, Actual: current.revision}
		}
	}
	s.mu.Unlock()
	for _, key := range ids {
		if err := s.Stop(id, key, nil); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) RestartGroup(ctx context.Context, id, groupID string, expected map[string]uint64, fresh ...StartRequest) (Snapshot, error) {
	s.mu.Lock()
	group := s.runtimeLocked(id).groups[groupID]
	if group == nil {
		s.mu.Unlock()
		return Snapshot{}, fmt.Errorf("debug group was not found")
	}
	request := StartRequest{CompoundID: group.compoundID}
	if len(group.sessionIDs) > 0 {
		request = s.runtimeLocked(id).sessions[group.sessionIDs[0]].startRequest
		request.CompoundID = group.compoundID
		request.ConfigurationID = ""
	}
	if len(fresh) > 0 {
		request.Inputs = fresh[0].Inputs
		request.CurrentFile = fresh[0].CurrentFile
		request.SelectedText = fresh[0].SelectedText
	}
	s.mu.Unlock()
	if err := s.StopGroupChecked(id, groupID, expected); err != nil {
		return Snapshot{}, err
	}
	return s.Start(ctx, id, request)
}
func (s *Service) StopWorkspace(id string) {
	s.mu.Lock()
	var ids []string
	if r := s.runtimes[id]; r != nil {
		for key, current := range r.sessions {
			if !isTerminalStatus(current.status) {
				ids = append(ids, key)
			}
		}
	}
	s.mu.Unlock()
	for _, key := range ids {
		_ = s.Stop(id, key, nil)
	}
}
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	ids := []string{}
	for id := range s.runtimes {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, id := range ids {
			s.StopWorkspace(id)
		}
		s.lifetimes.Wait()
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) handleReverseRequest(ctx context.Context, id, sessionID string, request dapEnvelope) (any, error) {
	if request.Command != "runInTerminal" {
		return nil, fmt.Errorf("unsupported adapter reverse request: %s", request.Command)
	}
	s.mu.Lock()
	run := s.runInTerminal
	current, err := s.sessionLocked(id, sessionID)
	if err != nil || isStoppingOrTerminal(current.status) {
		s.mu.Unlock()
		return nil, fmt.Errorf("debug session ended")
	}
	sessionContext := current.ctx
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(sessionContext, cancel)
	defer stop()
	if run == nil {
		return nil, fmt.Errorf("integrated debug terminals are unavailable")
	}
	var args map[string]any
	if err := json.Unmarshal(request.Arguments, &args); err != nil {
		return nil, err
	}
	if args["kind"] == "external" || args["argsCanBeInterpretedByShell"] == true {
		return nil, fmt.Errorf("debug terminals require integratedTerminal and direct arguments")
	}
	return run(ctx, id, sessionID, args)
}

func (s *Service) finishSession(id, sessionID, status, message string, minimum uint64) {
	s.mu.Lock()
	current, err := s.sessionLocked(id, sessionID)
	if err != nil || isTerminalStatus(current.status) {
		s.mu.Unlock()
		return
	}
	current.status = status
	current.err = message
	current.threadID = 0
	current.location = nil
	current.revision++
	current.stopGeneration++
	if current.revision < minimum {
		current.revision = minimum
	}
	ended := time.Now().UTC()
	current.endedAt = &ended
	connection, handle := current.conn, current.handle
	current.conn = nil
	current.handle = nil
	current.step = nil
	current.frames = nil
	stopTerminals := s.stopTerminals
	var siblings []string
	if group := s.runtimeLocked(id).groups[current.groupID]; group != nil && group.stopAll {
		for _, key := range group.sessionIDs {
			if key != sessionID {
				siblings = append(siblings, key)
			}
		}
	}
	s.mu.Unlock()
	current.cancel()
	if connection != nil {
		_ = connection.Close()
	}
	if handle != nil {
		handle.Stop()
	}
	if stopTerminals != nil {
		stopTerminals(id, sessionID)
	}
	// Startup may still be building when cancellation arrives. Cleanup waits for
	// it to relinquish the adapter so it cannot delete an in-use debug binary.
	go func() {
		defer s.lifetimes.Done()
		if current.cleaned != nil {
			defer close(current.cleaned)
		}
		<-current.done
		if current.launch != nil && current.launch.Cleanup != nil {
			current.launch.Cleanup()
		}
		if current.release != nil {
			current.release()
		}
	}()
	s.publishSession(id, sessionID, status, nil, message)
	for _, key := range siblings {
		go s.Stop(id, key, nil)
	}
}
