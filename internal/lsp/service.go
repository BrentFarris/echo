package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brent/echo/internal/lspconfig"
	"github.com/brent/echo/internal/sandbox"
	"github.com/brent/echo/internal/workspaces"
	"github.com/google/uuid"
)

var (
	ErrProfileNotFound = errors.New("language server profile was not found")
	ErrProfileInUse    = errors.New("language server profile is used by a workspace")
	ErrLeaseDenied     = errors.New("another browser owns the language server document")
	ErrLeaseRequired   = errors.New("this browser does not own the language server document")
)

type workspaceResolver interface {
	Get(string) (workspaces.Workspace, bool, error)
	List() ([]workspaces.Workspace, error)
	ActiveID() (string, error)
	SetLanguageServerConfig(string, lspconfig.WorkspaceConfig) (workspaces.Workspace, error)
}

type Service struct {
	profiles   *lspconfig.Store
	workspaces workspaceResolver

	mu            sync.Mutex
	activated     map[string]bool
	runtimes      map[string]*serverRuntime
	clients       map[string]*Client
	leases        map[string]*documentLease
	lastRequester map[string]string
	requestSeq    atomic.Uint64
	sandbox       *sandbox.Manager
}

func (s *Service) SetSandbox(manager *sandbox.Manager) {
	s.mu.Lock()
	s.sandbox = manager
	s.mu.Unlock()
}

func (s *Service) sandboxManager(workspaceID string) *sandbox.Manager {
	s.mu.Lock()
	manager := s.sandbox
	s.mu.Unlock()
	if manager != nil && manager.IsEnabled(workspaceID) {
		return manager
	}
	return nil
}

type Client struct {
	ID          string
	WorkspaceID string
	service     *Service
	send        func(any)

	mu        sync.Mutex
	documents map[string]Document
	pending   map[string]chan serverRequestResponse
	closed    bool
}

type Document struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

type documentLease struct {
	clientID string
	version  int
	language string
	uri      string
}

type serverRequestResponse struct {
	result json.RawMessage
	err    *RPCError
}

func NewService(profiles *lspconfig.Store, workspaceManager workspaceResolver) *Service {
	return &Service{
		profiles: profiles, workspaces: workspaceManager,
		activated: make(map[string]bool), runtimes: make(map[string]*serverRuntime),
		clients: make(map[string]*Client), leases: make(map[string]*documentLease),
		lastRequester: make(map[string]string),
	}
}

func (s *Service) StartActiveWorkspace() {
	id, err := s.workspaces.ActiveID()
	if err == nil && strings.TrimSpace(id) != "" {
		_ = s.Activate(id)
	}
}

func (s *Service) Profiles() ([]lspconfig.Profile, error) {
	return s.profiles.Load()
}

func (s *Service) AddProfile(profile lspconfig.Profile) (lspconfig.Profile, error) {
	created, err := s.profiles.Add(profile)
	if err == nil {
		s.ReconcileActivated()
	}
	return created, err
}

func (s *Service) AddTemplate(templateID string) (lspconfig.Profile, error) {
	profile, ok := lspconfig.TemplateByID(templateID)
	if !ok {
		return lspconfig.Profile{}, fmt.Errorf("language server template %q was not found", templateID)
	}
	profiles, err := s.profiles.Load()
	if err != nil {
		return lspconfig.Profile{}, err
	}
	baseID := profile.ID
	used := map[string]bool{}
	for _, existing := range profiles {
		used[existing.ID] = true
	}
	for suffix := 2; used[profile.ID]; suffix++ {
		profile.ID = fmt.Sprintf("%s-%d", baseID, suffix)
		profile.Name = fmt.Sprintf("%s %d", profile.Name, suffix)
	}
	return s.AddProfile(profile)
}

func (s *Service) UpdateProfile(id string, profile lspconfig.Profile) (lspconfig.Profile, error) {
	registered, err := s.workspaces.List()
	if err != nil {
		return lspconfig.Profile{}, err
	}
	updated, err := s.profiles.UpdateChecked(id, profile, func(profiles []lspconfig.Profile) error {
		for _, workspace := range registered {
			if err := workspace.LanguageServers.Validate(profiles); err != nil {
				return fmt.Errorf("workspace %q would have invalid language-server configuration: %w", workspace.Name, err)
			}
		}
		return nil
	})
	if err == nil {
		s.ReconcileActivated()
	}
	return updated, err
}

func (s *Service) DeleteProfile(id string) error {
	workspaces, err := s.workspaces.List()
	if err != nil {
		return err
	}
	for _, workspace := range workspaces {
		for _, enabled := range workspace.LanguageServers.EnabledProfileIDs {
			if enabled == id {
				return fmt.Errorf("%w: %s", ErrProfileInUse, workspace.Name)
			}
		}
		if _, referenced := workspace.LanguageServers.Overrides[id]; referenced {
			return fmt.Errorf("%w: %s", ErrProfileInUse, workspace.Name)
		}
	}
	if err := s.profiles.Delete(id); err != nil {
		return err
	}
	s.ReconcileActivated()
	return nil
}

func (s *Service) WorkspaceConfig(workspaceID string) (lspconfig.WorkspaceConfig, []lspconfig.Profile, []Status, error) {
	workspace, ok, err := s.workspaces.Get(workspaceID)
	if err != nil {
		return lspconfig.WorkspaceConfig{}, nil, nil, err
	}
	if !ok {
		return lspconfig.WorkspaceConfig{}, nil, nil, fmt.Errorf("workspace %q was not found", workspaceID)
	}
	profiles, err := s.profiles.Load()
	if err != nil {
		return lspconfig.WorkspaceConfig{}, nil, nil, err
	}
	config := workspace.LanguageServers.Normalized()
	effective, err := lspconfig.EffectiveProfiles(config, profiles)
	if err != nil {
		return config, nil, nil, err
	}
	return config, effective, s.Statuses(workspaceID, effective), nil
}

func (s *Service) SetWorkspaceConfig(workspaceID string, config lspconfig.WorkspaceConfig) (lspconfig.WorkspaceConfig, []lspconfig.Profile, []Status, error) {
	profiles, err := s.profiles.Load()
	if err != nil {
		return lspconfig.WorkspaceConfig{}, nil, nil, err
	}
	config = config.Normalized()
	if err := config.Validate(profiles); err != nil {
		return lspconfig.WorkspaceConfig{}, nil, nil, err
	}
	if _, err := s.workspaces.SetLanguageServerConfig(workspaceID, config); err != nil {
		return lspconfig.WorkspaceConfig{}, nil, nil, err
	}
	s.mu.Lock()
	active := s.activated[workspaceID]
	s.mu.Unlock()
	if active {
		_ = s.reconcile(workspaceID, false)
	}
	saved, effective, statuses, err := s.WorkspaceConfig(workspaceID)
	if err == nil {
		s.broadcastConfiguration(workspaceID, saved, effective, statuses)
	}
	return saved, effective, statuses, err
}

func (s *Service) Activate(workspaceID string) error {
	s.mu.Lock()
	s.activated[workspaceID] = true
	s.mu.Unlock()
	if err := s.reconcile(workspaceID, false); err != nil {
		return err
	}
	s.notifyConfiguration(workspaceID)
	return nil
}

func (s *Service) Restart(workspaceID, profileID string) error {
	s.mu.Lock()
	if !s.activated[workspaceID] {
		s.mu.Unlock()
		return fmt.Errorf("workspace language servers have not been activated")
	}
	key := runtimeKey(workspaceID, profileID)
	current := s.runtimes[key]
	delete(s.runtimes, key)
	s.mu.Unlock()
	if current == nil {
		return fmt.Errorf("%w: %s", ErrProfileNotFound, profileID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	current.stop(ctx)
	cancel()
	return s.reconcile(workspaceID, false)
}

func (s *Service) RefreshWorkspace(workspaceID string) {
	s.mu.Lock()
	active := s.activated[workspaceID]
	var stopped []*serverRuntime
	if active {
		for key, current := range s.runtimes {
			if current.workspace.ID == workspaceID {
				stopped = append(stopped, current)
				delete(s.runtimes, key)
			}
		}
	}
	s.mu.Unlock()
	stopRuntimes(stopped)
	if active {
		_ = s.reconcile(workspaceID, false)
		s.notifyConfiguration(workspaceID)
	}
}

// StopWorkspaceProcesses stops active runtimes without immediately
// reconciling them. Configuration transitions use it to avoid starting a
// process against the old execution target between stop and save.
func (s *Service) StopWorkspaceProcesses(workspaceID string) {
	s.mu.Lock()
	var stopped []*serverRuntime
	for key, current := range s.runtimes {
		if current.workspace.ID == workspaceID {
			stopped = append(stopped, current)
			delete(s.runtimes, key)
		}
	}
	s.mu.Unlock()
	stopRuntimes(stopped)
}

// DeactivateWorkspace stops active runtimes and forgets activation state for
// a workspace that is no longer registered.
func (s *Service) DeactivateWorkspace(workspaceID string) {
	s.mu.Lock()
	delete(s.activated, workspaceID)
	var stopped []*serverRuntime
	for key, current := range s.runtimes {
		if current.workspace.ID == workspaceID {
			stopped = append(stopped, current)
			delete(s.runtimes, key)
		}
	}
	s.mu.Unlock()
	stopRuntimes(stopped)
}

func (s *Service) ReconcileActivated() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.activated))
	for id := range s.activated {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		if s.reconcile(id, false) == nil {
			s.notifyConfiguration(id)
		}
	}
}

func (s *Service) reconcile(workspaceID string, force bool) error {
	workspace, ok, err := s.workspaces.Get(workspaceID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("workspace %q was not found", workspaceID)
	}
	profiles, err := s.profiles.Load()
	if err != nil {
		return err
	}
	effective, err := lspconfig.EffectiveProfiles(workspace.LanguageServers, profiles)
	if err != nil {
		return err
	}
	wanted := make(map[string]lspconfig.Profile, len(effective))
	for _, profile := range effective {
		wanted[profile.ID] = profile
	}

	var stopped []*serverRuntime
	var started []*serverRuntime
	sandboxManager := s.sandboxManager(workspaceID)
	s.mu.Lock()
	if !s.activated[workspaceID] {
		s.mu.Unlock()
		return nil
	}
	for key, current := range s.runtimes {
		if current.workspace.ID != workspaceID {
			continue
		}
		profile, keep := wanted[current.profile.ID]
		if !keep || force || runtimeFingerprint(current.profile, current.workspace) != runtimeFingerprint(profile, workspace) {
			stopped = append(stopped, current)
			delete(s.runtimes, key)
			continue
		}
		if !sameSettings(current.profile.Settings, profile.Settings) {
			current.updateSettings(profile.Settings)
		}
		current.updateName(profile.Name)
		delete(wanted, current.profile.ID)
	}
	for _, profile := range wanted {
		current := newServerRuntimeWithSandbox(s, workspace, profile, sandboxManager)
		s.runtimes[runtimeKey(workspaceID, profile.ID)] = current
		started = append(started, current)
	}
	s.mu.Unlock()
	stopRuntimes(stopped)
	for _, current := range started {
		current.start()
	}
	return nil
}

func (s *Service) Statuses(workspaceID string, effective []lspconfig.Profile) []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Status, 0, len(effective))
	for _, profile := range effective {
		if current := s.runtimes[runtimeKey(workspaceID, profile.ID)]; current != nil {
			result = append(result, current.status())
		} else {
			result = append(result, Status{WorkspaceID: workspaceID, ProfileID: profile.ID, Name: profile.Name, State: "inactive", Sandbox: s.sandbox != nil && s.sandbox.IsEnabled(workspaceID)})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *Service) Attach(workspaceID string, send func(any)) (*Client, error) {
	if _, ok, err := s.workspaces.Get(workspaceID); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("workspace %q was not found", workspaceID)
	}
	client := &Client{
		ID: uuid.NewString(), WorkspaceID: workspaceID, service: s, send: send,
		documents: make(map[string]Document), pending: make(map[string]chan serverRequestResponse),
	}
	s.mu.Lock()
	s.clients[client.ID] = client
	s.mu.Unlock()
	config, effective, statuses, _ := s.WorkspaceConfig(workspaceID)
	client.send(map[string]any{
		"type": "lsp_ready", "workspaceId": workspaceID, "clientId": client.ID,
		"config": config, "profiles": effective, "statuses": statuses,
	})
	return client, nil
}

func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	documents := make(map[string]Document, len(c.documents))
	for key, document := range c.documents {
		documents[key] = document
	}
	pending := c.pending
	c.pending = make(map[string]chan serverRequestResponse)
	c.mu.Unlock()
	for key, document := range documents {
		profileID, _, _ := strings.Cut(key, "\x00")
		_ = c.service.closeDocument(c, profileID, document.URI)
	}
	for _, response := range pending {
		response <- serverRequestResponse{err: &RPCError{Code: -32603, Message: "browser disconnected"}}
	}
	c.service.mu.Lock()
	delete(c.service.clients, c.ID)
	for key, id := range c.service.lastRequester {
		if id == c.ID {
			delete(c.service.lastRequester, key)
		}
	}
	c.service.mu.Unlock()
}

func (s *Service) ClaimDocument(client *Client, profileID string, document Document, takeOver bool) error {
	current, err := s.runningRuntime(client.WorkspaceID, profileID)
	if err != nil {
		return err
	}
	if err := validateDocument(current, document); err != nil {
		return err
	}
	key := leaseKey(client.WorkspaceID, profileID, document.URI)
	s.mu.Lock()
	lease := s.leases[key]
	if lease != nil && lease.clientID != client.ID && !takeOver {
		owner := s.clients[lease.clientID]
		s.mu.Unlock()
		client.send(map[string]any{"type": "lsp_lease_denied", "profileId": profileID, "uri": document.URI, "ownerClientId": lease.clientID})
		if owner == nil {
			return ErrLeaseDenied
		}
		return ErrLeaseDenied
	}
	previousOwner := ""
	wasOpen := lease != nil
	if lease != nil {
		previousOwner = lease.clientID
	}
	s.leases[key] = &documentLease{clientID: client.ID, version: document.Version, language: document.LanguageID, uri: document.URI}
	s.mu.Unlock()

	if previousOwner != "" && previousOwner != client.ID {
		if previous := s.client(previousOwner); previous != nil {
			previous.send(map[string]any{"type": "lsp_lease_revoked", "profileId": profileID, "uri": document.URI, "ownerClientId": client.ID})
		}
	}
	client.mu.Lock()
	client.documents[profileID+"\x00"+document.URI] = document
	client.mu.Unlock()
	if wasOpen {
		// A takeover can move to a Monaco model whose version sequence is lower
		// than the previous browser's. Close and reopen so the server receives an
		// authoritative replacement snapshot with a fresh version sequence. The
		// same path also reconstructs synchronization after a server restart.
		_ = current.notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": document.URI}})
	}
	err = current.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{
		"uri": document.URI, "languageId": document.LanguageID, "version": document.Version, "text": document.Text,
	}})
	if err != nil {
		return err
	}
	client.send(map[string]any{"type": "lsp_lease_granted", "profileId": profileID, "uri": document.URI, "version": document.Version})
	return nil
}

func (s *Service) ChangeDocument(client *Client, profileID, uri string, version int, contentChanges json.RawMessage) error {
	current, err := s.requireLease(client, profileID, uri)
	if err != nil {
		return err
	}
	key := leaseKey(client.WorkspaceID, profileID, uri)
	s.mu.Lock()
	lease := s.leases[key]
	if lease == nil || lease.clientID != client.ID {
		s.mu.Unlock()
		return ErrLeaseRequired
	}
	if version <= lease.version {
		s.mu.Unlock()
		return fmt.Errorf("stale document version %d; current version is %d", version, lease.version)
	}
	lease.version = version
	s.mu.Unlock()
	var changes any
	if err := json.Unmarshal(contentChanges, &changes); err != nil {
		return fmt.Errorf("decode document changes: %w", err)
	}
	return current.notify("textDocument/didChange", map[string]any{
		"textDocument": map[string]any{"uri": uri, "version": version}, "contentChanges": changes,
	})
}

func (s *Service) SaveDocument(client *Client, profileID, uri, text string) error {
	current, err := s.requireLease(client, profileID, uri)
	if err != nil {
		return err
	}
	return current.notify("textDocument/didSave", map[string]any{
		"textDocument": map[string]any{"uri": uri}, "text": text,
	})
}

func (s *Service) CloseDocument(client *Client, profileID, uri string) error {
	return s.closeDocument(client, profileID, uri)
}

func (s *Service) closeDocument(client *Client, profileID, uri string) error {
	client.mu.Lock()
	delete(client.documents, profileID+"\x00"+uri)
	client.mu.Unlock()
	key := leaseKey(client.WorkspaceID, profileID, uri)
	s.mu.Lock()
	lease := s.leases[key]
	if lease == nil || lease.clientID != client.ID {
		s.mu.Unlock()
		return nil
	}
	delete(s.leases, key)
	s.mu.Unlock()
	current, err := s.runningRuntime(client.WorkspaceID, profileID)
	if err != nil {
		return nil
	}
	return current.notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": uri}})
}

func (s *Service) Call(ctx context.Context, client *Client, requestID, profileID, method string, params json.RawMessage) (json.RawMessage, error) {
	current, err := s.runningRuntime(client.WorkspaceID, profileID)
	if err != nil {
		return nil, err
	}
	if uri := documentURI(params); uri != "" {
		if _, err := s.requireLease(client, profileID, uri); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	s.lastRequester[runtimeKey(client.WorkspaceID, profileID)] = client.ID
	s.mu.Unlock()
	return current.call(ctx, method, params)
}

func (s *Service) RespondServerRequest(client *Client, id string, result json.RawMessage, rpcErr *RPCError) error {
	client.mu.Lock()
	pending := client.pending[id]
	delete(client.pending, id)
	client.mu.Unlock()
	if pending == nil {
		return fmt.Errorf("server request %q was not found", id)
	}
	pending <- serverRequestResponse{result: result, err: rpcErr}
	return nil
}

func (s *Service) forwardServerRequest(ctx context.Context, current *serverRuntime, method string, params json.RawMessage) (any, error) {
	key := runtimeKey(current.workspace.ID, current.profile.ID)
	s.mu.Lock()
	clientID := s.lastRequester[key]
	client := s.clients[clientID]
	if client == nil {
		prefix := key + "\x00"
		for leaseKey, lease := range s.leases {
			if strings.HasPrefix(leaseKey, prefix) {
				client = s.clients[lease.clientID]
				if client != nil {
					break
				}
			}
		}
	}
	s.mu.Unlock()
	if client == nil {
		return nil, errors.New("no active editor can apply the workspace edit")
	}
	id := fmt.Sprintf("server-%d", s.requestSeq.Add(1))
	response := make(chan serverRequestResponse, 1)
	client.mu.Lock()
	if client.closed {
		client.mu.Unlock()
		return nil, errors.New("active editor disconnected")
	}
	client.pending[id] = response
	client.mu.Unlock()
	client.send(map[string]any{
		"type": "lsp_server_request", "id": id, "profileId": current.profile.ID,
		"method": method, "params": params,
	})
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case answer := <-response:
		if answer.err != nil {
			return nil, answer.err
		}
		if len(answer.result) == 0 {
			return nil, nil
		}
		var result any
		if err := json.Unmarshal(answer.result, &result); err != nil {
			return nil, err
		}
		return result, nil
	case <-timer.C:
		return nil, errors.New("editor did not answer the language server request")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Service) runtimeStatusChanged(current *serverRuntime) {
	status := current.status()
	s.broadcast(current.workspace.ID, map[string]any{"type": "lsp_status", "status": status})
}

func (s *Service) runtimeNotification(current *serverRuntime, method string, params json.RawMessage) {
	if method == "textDocument/publishDiagnostics" {
		uri := documentURI(params)
		if uri == "" {
			return
		}
		key := leaseKey(current.workspace.ID, current.profile.ID, uri)
		s.mu.Lock()
		lease := s.leases[key]
		leased := lease != nil
		var client *Client
		ownerURI := ""
		if lease != nil {
			client = s.clients[lease.clientID]
			ownerURI = lease.uri
		}
		s.mu.Unlock()
		if leased {
			if client == nil {
				return
			}
			// Language servers may normalize an opened file URI before publishing
			// diagnostics (notably gopls on Windows changes Monaco's
			// file:///c%3A/... to file:///C:/...). Send the owning browser the URI
			// it used to open the model so Monaco can resolve the marker target.
			if ownerURI != "" && ownerURI != uri {
				params = replaceDocumentURI(params, ownerURI)
			}
			client.send(map[string]any{
				"type": "lsp_notification", "profileId": current.profile.ID, "method": method, "params": params,
			})
			return
		}
		path, err := filePath(uri)
		if err != nil || !pathWithinWorkspace(current.workspace, path) {
			return
		}
		s.broadcast(current.workspace.ID, map[string]any{
			"type": "lsp_notification", "profileId": current.profile.ID, "method": method, "params": params,
		})
		return
	}
	s.broadcast(current.workspace.ID, map[string]any{
		"type": "lsp_notification", "profileId": current.profile.ID, "method": method, "params": params,
	})
}

func (s *Service) broadcast(workspaceID string, message any) {
	s.mu.Lock()
	clients := make([]*Client, 0)
	for _, client := range s.clients {
		if client.WorkspaceID == workspaceID {
			clients = append(clients, client)
		}
	}
	s.mu.Unlock()
	for _, client := range clients {
		client.send(message)
	}
}

func (s *Service) notifyConfiguration(workspaceID string) {
	config, profiles, statuses, err := s.WorkspaceConfig(workspaceID)
	if err == nil {
		s.broadcastConfiguration(workspaceID, config, profiles, statuses)
	}
}

func (s *Service) broadcastConfiguration(workspaceID string, config lspconfig.WorkspaceConfig, profiles []lspconfig.Profile, statuses []Status) {
	s.broadcast(workspaceID, map[string]any{
		"type": "lsp_configuration", "workspaceId": workspaceID,
		"config": config, "profiles": profiles, "statuses": statuses,
	})
}

func (s *Service) runningRuntime(workspaceID, profileID string) (*serverRuntime, error) {
	s.mu.Lock()
	current := s.runtimes[runtimeKey(workspaceID, profileID)]
	s.mu.Unlock()
	if current == nil {
		return nil, fmt.Errorf("%w: %s", ErrProfileNotFound, profileID)
	}
	status := current.status()
	if status.State != "running" {
		return nil, fmt.Errorf("language server %q is %s", status.Name, status.State)
	}
	return current, nil
}

func (s *Service) requireLease(client *Client, profileID, uri string) (*serverRuntime, error) {
	current, err := s.runningRuntime(client.WorkspaceID, profileID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	lease := s.leases[leaseKey(client.WorkspaceID, profileID, uri)]
	s.mu.Unlock()
	if lease == nil || lease.clientID != client.ID {
		return nil, ErrLeaseRequired
	}
	return current, nil
}

func (s *Service) client(id string) *Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clients[id]
}

func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	runtimes := make([]*serverRuntime, 0, len(s.runtimes))
	for _, current := range s.runtimes {
		runtimes = append(runtimes, current)
	}
	s.runtimes = make(map[string]*serverRuntime)
	clients := make([]*Client, 0, len(s.clients))
	for _, client := range s.clients {
		clients = append(clients, client)
	}
	s.mu.Unlock()
	for _, client := range clients {
		client.Close()
	}
	for _, current := range runtimes {
		current.stop(ctx)
	}
	return ctx.Err()
}

func validateDocument(current *serverRuntime, document Document) error {
	if strings.TrimSpace(document.URI) == "" || strings.TrimSpace(document.LanguageID) == "" {
		return errors.New("document URI and language id are required")
	}
	path, err := filePath(document.URI)
	if err != nil || !pathWithinWorkspace(current.workspace, path) {
		return errors.New("document is outside the workspace")
	}
	name := strings.ToLower(filepath.Base(path))
	matched := false
	for _, selector := range current.profile.Selectors {
		if selector.LanguageID != document.LanguageID {
			continue
		}
		for _, filename := range selector.Filenames {
			if strings.EqualFold(filename, name) {
				matched = true
				break
			}
		}
		for _, extension := range selector.Extensions {
			if strings.HasSuffix(name, strings.ToLower(extension)) {
				matched = true
				break
			}
		}
		if matched {
			break
		}
	}
	if !matched {
		return fmt.Errorf("language server %q does not match %q", current.profile.Name, filepath.Base(path))
	}
	return nil
}

func documentURI(params json.RawMessage) string {
	var payload struct {
		URI          string `json:"uri"`
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	if json.Unmarshal(params, &payload) != nil {
		return ""
	}
	if payload.TextDocument.URI != "" {
		return payload.TextDocument.URI
	}
	return payload.URI
}

func replaceDocumentURI(params json.RawMessage, uri string) json.RawMessage {
	var payload map[string]json.RawMessage
	if json.Unmarshal(params, &payload) != nil {
		return params
	}
	replacement, err := json.Marshal(uri)
	if err != nil {
		return params
	}
	payload["uri"] = replacement
	updated, err := json.Marshal(payload)
	if err != nil {
		return params
	}
	return updated
}

func filePath(uri string) (string, error) {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "file" {
		return "", errors.New("URI is not a file URI")
	}
	path := filepath.FromSlash(parsed.Path)
	if runtime.GOOS == "windows" && len(path) >= 3 && path[0] == filepath.Separator && path[2] == ':' {
		path = path[1:]
	}
	return filepath.Clean(path), nil
}

func pathWithinWorkspace(workspace workspaces.Workspace, candidate string) bool {
	roots := append([]string(nil), workspace.Folders...)
	if len(roots) == 0 {
		roots = []string{workspace.MainPath}
	}
	for _, root := range roots {
		relative, err := filepath.Rel(root, candidate)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return true
		}
	}
	return false
}

func runtimeKey(workspaceID, profileID string) string { return workspaceID + "\x00" + profileID }
func leaseKey(workspaceID, profileID, uri string) string {
	return runtimeKey(workspaceID, profileID) + "\x00" + documentURIKey(uri)
}

func documentURIKey(uri string) string {
	path, err := filePath(uri)
	if err != nil {
		return uri
	}
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return "file\x00" + filepath.ToSlash(filepath.Clean(path))
}

func stopRuntimes(runtimes []*serverRuntime) {
	for _, current := range runtimes {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		current.stop(ctx)
		cancel()
	}
}
