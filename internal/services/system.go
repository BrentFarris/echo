package services

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"


	"github.com/brent/echo/internal/llm"
	"github.com/google/uuid"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const workspaceIconRoutePrefix = "/workspace-icons/"

type AppInfo struct {
	Name      string `json:"name"`
	Phase     string `json:"phase"`
	AccentHex string `json:"accentHex"`
}

type WorkspaceFolder struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Path      string `json:"path"`
	UseAgents bool   `json:"useAgents"`
	Missing   bool   `json:"missing"`
	Error     string `json:"error,omitempty"`
}

func (f *WorkspaceFolder) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID        string `json:"id"`
		Label     string `json:"label"`
		Path      string `json:"path"`
		UseAgents bool   `json:"useAgents"`
		Missing   bool   `json:"missing"`
		Error     string `json:"error"`
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*f = WorkspaceFolder{
		ID:        raw.ID,
		Label:     raw.Label,
		Path:      raw.Path,
		UseAgents: raw.UseAgents,
		Missing:   raw.Missing,
		Error:     raw.Error,
	}
	if _, ok := keys["useAgents"]; !ok {
		f.UseAgents = true
	}
	return nil
}

type Workspace struct {
	ID              string            `json:"id"`
	Folders         []WorkspaceFolder `json:"folders"`
	DisplayName     string            `json:"displayName"`
	DefaultPlanMode bool              `json:"defaultPlanMode"`
	Letter          string            `json:"letter,omitempty"`
	IconPath        string            `json:"iconPath,omitempty"`
	IconURL         string            `json:"iconUrl,omitempty"`
	Active          bool              `json:"active"`
	Missing         bool              `json:"missing"`
	Error           string            `json:"error,omitempty"`
}

func (w *Workspace) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID              string            `json:"id"`
		Folders         []WorkspaceFolder `json:"folders"`
		FolderPath      string            `json:"folderPath"`
		DisplayName     string            `json:"displayName"`
		DefaultPlanMode bool              `json:"defaultPlanMode"`
		Letter          string            `json:"letter"`
		IconPath        string            `json:"iconPath"`
		IconURL         string            `json:"iconUrl"`
		LegacyPath      string            `json:"path"`
		LegacyName      string            `json:"name"`
		Active          bool              `json:"active"`
		Missing         bool              `json:"missing"`
		Error           string            `json:"error"`
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*w = Workspace{
		ID:              raw.ID,
		Folders:         raw.Folders,
		DisplayName:     raw.DisplayName,
		DefaultPlanMode: raw.DefaultPlanMode,
		Letter:          normalizeWorkspaceLetter(raw.Letter),
		IconPath:        raw.IconPath,
		IconURL:         raw.IconURL,
		Active:          raw.Active,
		Missing:         raw.Missing,
		Error:           raw.Error,
	}
	if _, ok := keys["defaultPlanMode"]; !ok {
		w.DefaultPlanMode = true
	}
	legacyPath := raw.FolderPath
	if legacyPath == "" {
		legacyPath = raw.LegacyPath
	}
	if len(w.Folders) == 0 && strings.TrimSpace(legacyPath) != "" {
		w.Folders = []WorkspaceFolder{workspaceFolderFromPath(legacyPath, nil)}
	}
	if w.DisplayName == "" {
		w.DisplayName = raw.LegacyName
	}
	return nil
}

type DashboardWidgetJSON struct {
	ID    string `json:"id"`
	View  string `json:"view"`
	Title string `json:"title"`
	Size  string `json:"size"`
	Order int    `json:"order"`
}

type WorkspaceActivitySummary struct {
	WorkspaceID        string `json:"workspaceId"`
	IsChatBusy         bool   `json:"isChatBusy"`
	IsKanbanRunning    bool   `json:"isKanbanRunning"`
	ActiveAgentCount   int    `json:"activeAgentCount"`
	LastMessageSnippet string `json:"lastMessageSnippet,omitempty"`
}

type SavedCommand struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Command string `json:"command"`
	Order   int    `json:"order"`
}

type AppState struct {
	Settings          llm.Settings                 `json:"settings"`
	WebAccess         WebAccessSettings            `json:"webAccess"`
	Workspaces        []Workspace                  `json:"workspaces"`
	ActiveWorkspaceID string                       `json:"activeWorkspaceId"`
	HeartbeatConfigs  map[string]HeartbeatConfig   `json:"heartbeatConfigs,omitempty"`
	LivenessConfigs   map[string]LivenessConfig    `json:"livenessConfigs,omitempty"`
	WatchdogConfigs   map[string]WatchdogConfig    `json:"watchdogConfigs,omitempty"`
	DashboardLayouts  map[string][]DashboardWidgetJSON `json:"dashboardLayouts,omitempty"`
	SavedCommands     map[string][]SavedCommand        `json:"savedCommands,omitempty"`
	KanbanCards       []KanbanCard                 `json:"-"`
}

type SystemService struct {
	info                    AppInfo
	ctx                     context.Context
	storePath               string
	mu                      sync.Mutex
	autosaveMu              sync.Mutex
	taskMu                  sync.Mutex
	state                   AppState
	persistedChatSessions   map[string]persistedChatSession
	chatMu                  sync.Mutex
	chatSessions            map[string]*chatSessionState
	chatStreams             map[string]context.CancelFunc
	chatSeq                 uint64
	kanbanRuns              map[string]context.CancelFunc
	kanbanAgents            map[string]*kanbanAgentRun
	kanbanAgentSeq          uint64
	kanbanDetailViews       map[string]string
	shellCommandRuns        map[string]context.CancelFunc // runID -> cancel func
	shellCommandSeq         uint64
	heartbeats              map[string]*heartbeatHandle // workspaceID -\u003e running heartbeat
	watchdogs               map[string]*watchdogHandle  // workspaceID -> running watchdog
	fileChangeMu            sync.Mutex
	fileChangeSeq           uint64
	fileChanges             map[string][]trackedFileChange
	workspaceToolLocks      map[string]*sync.Mutex
	lspMu                   sync.Mutex
	lspClients              map[string]*lspClient
	lspWarmups              map[string]struct{}
	workspaceContextBuilder workspaceContextBuildFunc
	webAccessController     WebAccessController
	eventMu                 sync.Mutex
	eventSeq                uint64
	eventSubscribers        map[uint64]chan RuntimeEvent
	kanbanEventSink         func(KanbanEvent)
	fileChangesEventSink    func(FileChangesEvent)
	inlineCodeEventSink     func(InlineCodePromptEvent)
	tokenBudget             *TokenBudgetService
}

func NewSystemService() *SystemService {
	storePath, err := defaultStorePath()
	if err != nil {
		storePath = filepath.Join(".", ".tmp", "echo-state.json")
	}
	return NewSystemServiceWithStorePath(storePath)
}

func NewSystemServiceWithStorePath(storePath string) *SystemService {
	service := &SystemService{
		info: AppInfo{
			Name:      "Echo",
			Phase:     "release-readiness",
			AccentHex: "#8f1d2c",
		},
		storePath:             storePath,
		state:                 defaultAppState(),
		persistedChatSessions: make(map[string]persistedChatSession),
		chatSessions:          make(map[string]*chatSessionState),
		chatStreams:           make(map[string]context.CancelFunc),
		kanbanRuns:            make(map[string]context.CancelFunc),
		kanbanAgents:          make(map[string]*kanbanAgentRun),
		kanbanDetailViews:     make(map[string]string),
		shellCommandRuns:      make(map[string]context.CancelFunc),
		heartbeats:            make(map[string]*heartbeatHandle),
		watchdogs:             make(map[string]*watchdogHandle),
		fileChanges:           make(map[string][]trackedFileChange),
		workspaceToolLocks:    make(map[string]*sync.Mutex),
		lspClients:            make(map[string]*lspClient),
		lspWarmups:            make(map[string]struct{}),
		eventSubscribers:      make(map[uint64]chan RuntimeEvent),
		tokenBudget:           newTokenBudgetService(),
	}
	_ = service.load()
	return service
}

func (s *SystemService) AppInfo() AppInfo {
	return s.info
}

func SetSystemServiceContext(service *SystemService, ctx context.Context) {
	service.ctx = ctx
}

func (s *SystemService) LoadState() AppState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refreshWorkspaceStatusesLocked() {
		_ = s.saveLocked()
	}
	state := cloneState(s.state)
	s.warmActiveWorkspaceLSPClients(state)
	return state
}

// GetWorkspaceActivitySummaries returns activity status for all workspaces.
func (s *SystemService) GetWorkspaceActivitySummaries() []WorkspaceActivitySummary {
	s.mu.Lock()
	workspaceIDs := make([]string, 0, len(s.state.Workspaces))
	for _, w := range s.state.Workspaces {
		workspaceIDs = append(workspaceIDs, w.ID)
	}
	s.mu.Unlock()

	summaries := make([]WorkspaceActivitySummary, 0, len(workspaceIDs))

	for _, wsID := range workspaceIDs {
		var summary WorkspaceActivitySummary
		summary.WorkspaceID = wsID

		// Chat busy check
		s.chatMu.Lock()
		_, summary.IsChatBusy = s.chatStreams[wsID]
		_, summary.IsKanbanRunning = s.kanbanRuns[wsID]
		summary.ActiveAgentCount = 0
		for agentKey := range s.kanbanAgents {
			if strings.HasPrefix(agentKey, wsID+":") {
				summary.ActiveAgentCount++
			}
		}

		// Last message snippet from chat session
		if session, ok := s.chatSessions[wsID]; ok {
			for i := len(session.Messages) - 1; i >= 0; i-- {
				msg := session.Messages[i]
				if msg.Role == "assistant" && msg.Content != "" {
					snippet := msg.Content
					if len(snippet) > 60 {
						snippet = snippet[:60] + "…"
					}
					summary.LastMessageSnippet = strings.TrimSpace(strings.ReplaceAll(snippet, "\n", " "))
					break
				}
			}
		}
		s.chatMu.Unlock()

		summaries = append(summaries, summary)
	}

	return summaries
}

func (s *SystemService) GetDashboardLayouts() map[string][]DashboardWidgetJSON {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string][]DashboardWidgetJSON, len(s.state.DashboardLayouts))
	for k, v := range s.state.DashboardLayouts {
		result[k] = append([]DashboardWidgetJSON{}, v...)
	}
	return result
}

func (s *SystemService) SaveDashboardLayout(view string, widgets []DashboardWidgetJSON) error {
	view = strings.TrimSpace(view)
	if view == "" {
		return fmt.Errorf("dashboard view is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.DashboardLayouts == nil {
		s.state.DashboardLayouts = make(map[string][]DashboardWidgetJSON)
	}
	s.state.DashboardLayouts[view] = append([]DashboardWidgetJSON{}, widgets...)
	return s.saveLocked()
}

func (s *SystemService) GetSavedCommands(workspaceID string) []SavedCommand {
	if _, err := s.workspaceByID(workspaceID); err != nil {
		return []SavedCommand{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cmds := s.state.SavedCommands[workspaceID]
	result := make([]SavedCommand, len(cmds))
	copy(result, cmds)
	return result
}

func (s *SystemService) UpsertSavedCommand(workspaceID, id, name, command string, order int) error {
	if _, err := s.workspaceByID(workspaceID); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	command = strings.TrimSpace(command)
	if name == "" && command == "" {
		return fmt.Errorf("name and command cannot both be empty")
	}
	if id == "" {
		id = uuid.New().String()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.SavedCommands == nil {
		s.state.SavedCommands = make(map[string][]SavedCommand)
	}
	existing := s.state.SavedCommands[workspaceID]
	found := false
	for i, cmd := range existing {
		if cmd.ID == id {
			existing[i].Name = name
			existing[i].Command = command
			existing[i].Order = order
			found = true
			break
		}
	}
	if !found {
		existing = append(existing, SavedCommand{ID: id, Name: name, Command: command, Order: order})
	}
	s.state.SavedCommands[workspaceID] = existing
	return s.saveLocked()
}

func (s *SystemService) DeleteSavedCommand(workspaceID, id string) error {
	if _, err := s.workspaceByID(workspaceID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.state.SavedCommands[workspaceID]
	next := make([]SavedCommand, 0, len(existing))
	for _, cmd := range existing {
		if cmd.ID == id {
			continue
		}
		next = append(next, cmd)
	}
	s.state.SavedCommands[workspaceID] = next
	return s.saveLocked()
}

func (s *SystemService) SaveSettings(settings llm.Settings) (AppState, error) {
	settings = settings.Normalized()
	if err := settings.Validate(); err != nil {
		return AppState{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Settings = settings
	s.refreshWorkspaceStatusesLocked()
	if err := s.saveLocked(); err != nil {
		return AppState{}, err
	}
	return cloneState(s.state), nil
}

func (s *SystemService) AddWorkspace(path string) (AppState, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return AppState{}, fmt.Errorf("workspace path is required")
	}

	absolute, err := normalizedWorkspacePath(path)
	if err != nil {
		return AppState{}, fmt.Errorf("resolve workspace path: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return AppState{}, fmt.Errorf("workspace folder does not exist")
	}
	if !info.IsDir() {
		return AppState{}, fmt.Errorf("workspace must be a folder")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	workspace := workspaceFromPath(absolute)
	for _, existing := range s.state.Workspaces {
		if workspaceContainsFolderPath(existing, absolute) {
			s.state.ActiveWorkspaceID = existing.ID
			s.refreshWorkspaceStatusesLocked()
			if err := s.saveLocked(); err != nil {
				return AppState{}, err
			}
			state := cloneState(s.state)
			s.warmActiveWorkspaceLSPClients(state)
			return state, nil
		}
	}
	s.state.Workspaces = append(s.state.Workspaces, workspace)
	s.state.ActiveWorkspaceID = workspace.ID
	s.refreshWorkspaceStatusesLocked()
	if err := s.saveLocked(); err != nil {
		return AppState{}, err
	}
	state := cloneState(s.state)
	s.warmActiveWorkspaceLSPClients(state)
	return state, nil
}

func (s *SystemService) ChooseWorkspaceFolder() (AppState, error) {
	if s.ctx == nil {
		return AppState{}, fmt.Errorf("application is not ready to open a folder picker")
	}
	path, err := runtime.OpenDirectoryDialog(s.ctx, runtime.OpenDialogOptions{
		Title: "Add workspace",
	})
	if err != nil {
		return AppState{}, err
	}
	if path == "" {
		return s.LoadState(), nil
	}
	return s.AddWorkspace(path)
}

func (s *SystemService) ChooseWorkspaceFolderForWorkspace(workspaceID string) (AppState, error) {
	if s.ctx == nil {
		return AppState{}, fmt.Errorf("application is not ready to open a folder picker")
	}
	path, err := runtime.OpenDirectoryDialog(s.ctx, runtime.OpenDialogOptions{
		Title: "Add folder to workspace",
	})
	if err != nil {
		return AppState{}, err
	}
	if path == "" {
		return s.LoadState(), nil
	}
	return s.AddWorkspaceFolder(workspaceID, path)
}

func (s *SystemService) AddWorkspaceFolder(workspaceID string, path string) (AppState, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return AppState{}, fmt.Errorf("workspace id is required")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return AppState{}, fmt.Errorf("workspace folder path is required")
	}

	absolute, err := normalizedWorkspacePath(path)
	if err != nil {
		return AppState{}, fmt.Errorf("resolve workspace folder path: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return AppState{}, fmt.Errorf("workspace folder does not exist")
	}
	if !info.IsDir() {
		return AppState{}, fmt.Errorf("workspace folder must be a folder")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Workspaces {
		if s.state.Workspaces[i].ID != workspaceID {
			continue
		}
		if workspaceContainsFolderPath(s.state.Workspaces[i], absolute) {
			s.refreshWorkspaceStatusesLocked()
			if err := s.saveLocked(); err != nil {
				return AppState{}, err
			}
			state := cloneState(s.state)
			s.warmActiveWorkspaceLSPClients(state)
			return state, nil
		}
		used := workspaceFolderLabelSet(s.state.Workspaces[i].Folders)
		s.state.Workspaces[i].Folders = append(s.state.Workspaces[i].Folders, workspaceFolderFromPath(absolute, used))
		s.refreshWorkspaceStatusesLocked()
		if err := s.saveLocked(); err != nil {
			return AppState{}, err
		}
		state := cloneState(s.state)
		s.warmActiveWorkspaceLSPClients(state)
		return state, nil
	}
	return AppState{}, fmt.Errorf("workspace was not found")
}

func (s *SystemService) RemoveWorkspaceFolder(workspaceID string, folderID string) (AppState, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	folderID = strings.TrimSpace(folderID)
	if workspaceID == "" {
		return AppState{}, fmt.Errorf("workspace id is required")
	}
	if folderID == "" {
		return AppState{}, fmt.Errorf("workspace folder id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Workspaces {
		if s.state.Workspaces[i].ID != workspaceID {
			continue
		}
		next := s.state.Workspaces[i].Folders[:0]
		removed := false
		for _, folder := range s.state.Workspaces[i].Folders {
			if folder.ID == folderID {
				removed = true
				continue
			}
			next = append(next, folder)
		}
		if !removed {
			return AppState{}, fmt.Errorf("workspace folder was not found")
		}
		s.state.Workspaces[i].Folders = next
		s.refreshWorkspaceStatusesLocked()
		if err := s.saveLocked(); err != nil {
			return AppState{}, err
		}
		s.dropWorkspaceChangeReview(workspaceID)
		s.closeWorkspaceLSPClients(workspaceID)
		state := cloneState(s.state)
		s.warmActiveWorkspaceLSPClients(state)
		return state, nil
	}
	return AppState{}, fmt.Errorf("workspace was not found")
}

func (s *SystemService) SetWorkspaceFolderUseAgents(workspaceID string, folderID string, useAgents bool) (AppState, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	folderID = strings.TrimSpace(folderID)
	if workspaceID == "" {
		return AppState{}, fmt.Errorf("workspace id is required")
	}
	if folderID == "" {
		return AppState{}, fmt.Errorf("workspace folder id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Workspaces {
		if s.state.Workspaces[i].ID != workspaceID {
			continue
		}
		for j := range s.state.Workspaces[i].Folders {
			if s.state.Workspaces[i].Folders[j].ID != folderID {
				continue
			}
			s.state.Workspaces[i].Folders[j].UseAgents = useAgents
			s.refreshWorkspaceStatusesLocked()
			if err := s.saveLocked(); err != nil {
				return AppState{}, err
			}
			return cloneState(s.state), nil
		}
		return AppState{}, fmt.Errorf("workspace folder was not found")
	}
	return AppState{}, fmt.Errorf("workspace was not found")
}

func (s *SystemService) SetWorkspaceDefaultPlanMode(workspaceID string, enabled bool) (AppState, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return AppState{}, fmt.Errorf("workspace id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Workspaces {
		if s.state.Workspaces[i].ID == workspaceID {
			s.state.Workspaces[i].DefaultPlanMode = enabled
			s.refreshWorkspaceStatusesLocked()
			if err := s.saveLocked(); err != nil {
				return AppState{}, err
			}
			return cloneState(s.state), nil
		}
	}
	return AppState{}, fmt.Errorf("workspace was not found")
}

func (s *SystemService) SetActiveWorkspace(id string) (AppState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		s.state.ActiveWorkspaceID = ""
		s.refreshWorkspaceStatusesLocked()
		if err := s.saveLocked(); err != nil {
			return AppState{}, err
		}
		state := cloneState(s.state)
		s.warmActiveWorkspaceLSPClients(state)
		return state, nil
	}
	for _, workspace := range s.state.Workspaces {
		if workspace.ID == id {
			s.state.ActiveWorkspaceID = id
			s.refreshWorkspaceStatusesLocked()
			if err := s.saveLocked(); err != nil {
				return AppState{}, err
			}
			state := cloneState(s.state)
			s.warmActiveWorkspaceLSPClients(state)
			return state, nil
		}
	}
	return AppState{}, fmt.Errorf("workspace was not found")
}

func (s *SystemService) ReorderWorkspaces(ids []string) (AppState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(ids) != len(s.state.Workspaces) {
		return AppState{}, fmt.Errorf("workspace order must include every workspace")
	}
	byID := make(map[string]Workspace, len(s.state.Workspaces))
	for _, workspace := range s.state.Workspaces {
		byID[workspace.ID] = workspace
	}
	seen := make(map[string]struct{}, len(ids))
	ordered := make([]Workspace, 0, len(ids))
	for _, rawID := range ids {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return AppState{}, fmt.Errorf("workspace order contains an empty id")
		}
		if _, ok := seen[id]; ok {
			return AppState{}, fmt.Errorf("workspace order contains a duplicate id")
		}
		workspace, ok := byID[id]
		if !ok {
			return AppState{}, fmt.Errorf("workspace was not found")
		}
		seen[id] = struct{}{}
		ordered = append(ordered, workspace)
	}

	s.state.Workspaces = ordered
	s.refreshWorkspaceStatusesLocked()
	if err := s.saveLocked(); err != nil {
		return AppState{}, err
	}
	state := cloneState(s.state)
	s.warmActiveWorkspaceLSPClients(state)
	return state, nil
}

func (s *SystemService) SetWorkspaceLetter(id string, letter string) (AppState, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return AppState{}, fmt.Errorf("workspace id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Workspaces {
		if s.state.Workspaces[i].ID == id {
			s.state.Workspaces[i].Letter = normalizeWorkspaceLetter(letter)
			s.refreshWorkspaceStatusesLocked()
			if err := s.saveLocked(); err != nil {
				return AppState{}, err
			}
			return cloneState(s.state), nil
		}
	}
	return AppState{}, fmt.Errorf("workspace was not found")
}

func (s *SystemService) ChooseWorkspaceIcon(id string) (AppState, error) {
	if s.ctx == nil {
		return AppState{}, fmt.Errorf("application is not ready to open a file picker")
	}
	path, err := runtime.OpenFileDialog(s.ctx, runtime.OpenDialogOptions{
		Title: "Choose workspace icon",
		Filters: []runtime.FileFilter{
			{
				DisplayName: "Images",
				Pattern:     "*.png;*.jpg;*.jpeg;*.gif;*.webp;*.bmp;*.ico",
			},
		},
	})
	if err != nil {
		return AppState{}, err
	}
	if path == "" {
		return s.LoadState(), nil
	}
	return s.setWorkspaceIconFromPath(id, path)
}

func (s *SystemService) SetWorkspaceIconFromPath(id string, path string) (AppState, error) {
	return s.setWorkspaceIconFromPath(id, path)
}

func (s *SystemService) SetWorkspaceIconFromUpload(id string, input WorkspaceIconInput) (AppState, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return AppState{}, fmt.Errorf("workspace id is required")
	}
	mediaType, data, err := parseChatImageDataURL(input.DataURL)
	if err != nil {
		return AppState{}, err
	}
	if input.Bytes > 0 && input.Bytes != int64(len(data)) {
		return AppState{}, fmt.Errorf("workspace icon size does not match its data")
	}
	if len(data) > maxChatImageBytes {
		return AppState{}, fmt.Errorf("workspace icon is larger than the %d byte limit", maxChatImageBytes)
	}
	extension := chatImageExtension(mediaType)
	if extension == "" {
		return AppState{}, fmt.Errorf("workspace icon must be a PNG, JPG, GIF, or WebP image")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Workspaces {
		if s.state.Workspaces[i].ID != id {
			continue
		}
		destination := filepath.Join(s.workspaceIconDir(), id+extension)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return AppState{}, err
		}
		if err := os.WriteFile(destination, data, 0o600); err != nil {
			return AppState{}, err
		}
		removeOtherWorkspaceIconExtensions(s.workspaceIconDir(), id, extension)
		oldPath := s.state.Workspaces[i].IconPath
		s.state.Workspaces[i].IconPath = destination
		s.state.Workspaces[i].IconURL = workspaceIconURL(destination)
		s.refreshWorkspaceStatusesLocked()
		if err := s.saveLocked(); err != nil {
			return AppState{}, err
		}
		if !strings.EqualFold(oldPath, destination) {
			removeStoredWorkspaceIcon(oldPath)
		}
		return cloneState(s.state), nil
	}
	return AppState{}, fmt.Errorf("workspace was not found")
}

func (s *SystemService) ClearWorkspaceIcon(id string) (AppState, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return AppState{}, fmt.Errorf("workspace id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Workspaces {
		if s.state.Workspaces[i].ID == id {
			oldPath := s.state.Workspaces[i].IconPath
			s.state.Workspaces[i].IconPath = ""
			s.state.Workspaces[i].IconURL = ""
			s.refreshWorkspaceStatusesLocked()
			if err := s.saveLocked(); err != nil {
				return AppState{}, err
			}
			removeStoredWorkspaceIcon(oldPath)
			return cloneState(s.state), nil
		}
	}
	return AppState{}, fmt.Errorf("workspace was not found")
}

func (s *SystemService) setWorkspaceIconFromPath(id string, sourcePath string) (AppState, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return AppState{}, fmt.Errorf("workspace id is required")
	}
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return AppState{}, fmt.Errorf("workspace icon path is required")
	}

	sourcePath, err := filepath.Abs(sourcePath)
	if err != nil {
		return AppState{}, fmt.Errorf("resolve workspace icon: %w", err)
	}
	extension, err := validateWorkspaceIconFile(sourcePath)
	if err != nil {
		return AppState{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Workspaces {
		if s.state.Workspaces[i].ID != id {
			continue
		}
		destination := filepath.Join(s.workspaceIconDir(), id+extension)
		if err := copyWorkspaceIcon(sourcePath, destination); err != nil {
			return AppState{}, err
		}
		removeOtherWorkspaceIconExtensions(s.workspaceIconDir(), id, extension)
		oldPath := s.state.Workspaces[i].IconPath
		s.state.Workspaces[i].IconPath = destination
		s.state.Workspaces[i].IconURL = workspaceIconURL(destination)
		s.refreshWorkspaceStatusesLocked()
		if err := s.saveLocked(); err != nil {
			return AppState{}, err
		}
		if !strings.EqualFold(oldPath, destination) {
			removeStoredWorkspaceIcon(oldPath)
		}
		return cloneState(s.state), nil
	}
	return AppState{}, fmt.Errorf("workspace was not found")
}

func (s *SystemService) DeleteWorkspace(id string) (AppState, error) {
	if strings.TrimSpace(id) == "" {
		return AppState{}, fmt.Errorf("workspace id is required")
	}

	s.mu.Lock()
	next := s.state.Workspaces[:0]
	deleted := false
	var deletedIconPath string
	for _, workspace := range s.state.Workspaces {
		if workspace.ID == id {
			deleted = true
			deletedIconPath = workspace.IconPath
			continue
		}
		next = append(next, workspace)
	}
	if !deleted {
		s.mu.Unlock()
		return AppState{}, fmt.Errorf("workspace was not found")
	}

	s.state.Workspaces = next
	s.state.KanbanCards = cardsWithoutWorkspace(s.state.KanbanCards, id)
	delete(s.persistedChatSessions, id)
	delete(s.tokenBudget.budgets, id)
	if s.state.ActiveWorkspaceID == id {
		s.state.ActiveWorkspaceID = ""
		if len(s.state.Workspaces) > 0 {
			s.state.ActiveWorkspaceID = s.state.Workspaces[0].ID
		}
	}
	s.refreshWorkspaceStatusesLocked()
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return AppState{}, err
	}
	state := cloneState(s.state)
	s.mu.Unlock()

	s.dropChatSession(id)
	s.chatMu.Lock()
	delete(s.kanbanDetailViews, id)
	s.chatMu.Unlock()
	s.dropWorkspaceChangeReview(id)
	s.closeWorkspaceLSPClients(id)
	removeStoredWorkspaceIcon(deletedIconPath)
	s.warmActiveWorkspaceLSPClients(state)
	return state, nil
}

func (s *SystemService) OpenWorkspaceExplorer(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("workspace id is required")
	}

	workspace, err := s.workspaceByID(id)
	if err != nil {
		return err
	}
	folderPath, ok := firstAvailableWorkspaceFolderPath(workspace)
	if !ok {
		return fmt.Errorf("workspace has no available folders")
	}

	info, err := os.Stat(folderPath)
	if err != nil {
		return fmt.Errorf("workspace folder does not exist: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace path is not a folder")
	}

	var cmd *exec.Cmd
	switch goruntime.GOOS {
	case "windows":
		cmd = exec.Command("explorer.exe", folderPath)
	case "darwin":
		cmd = exec.Command("open", folderPath)
	default:
		cmd = exec.Command("xdg-open", folderPath)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to open workspace explorer: %w", err)
	}

	// Don't Wait() — we don't want to block the caller waiting for the
	// external process to finish. The OS handles cleanup.
	return nil
}

func (s *SystemService) OpenWorkspacePathExplorer(id string, path string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("workspace id is required")
	}

	workspace, err := s.workspaceByID(id)
	if err != nil {
		return err
	}
	if strings.TrimSpace(path) == "" || strings.TrimSpace(path) == "." {
		folderPath, ok := firstAvailableWorkspaceFolderPath(workspace)
		if !ok {
			return fmt.Errorf("workspace has no available folders")
		}
		path = workspaceFolderByPath(workspace, folderPath).Label
	}
	resolved, err := resolveWorkspaceServicePath(workspace, path)
	if err != nil {
		return err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("workspace path does not exist: %w", err)
	}

	target := resolved
	selectFile := false
	if !info.IsDir() {
		target = filepath.Dir(resolved)
		selectFile = true
	}

	var cmd *exec.Cmd
	switch goruntime.GOOS {
	case "windows":
		if selectFile {
			cmd = exec.Command("explorer.exe", "/select,"+resolved)
		} else {
			cmd = exec.Command("explorer.exe", target)
		}
	case "darwin":
		if selectFile {
			cmd = exec.Command("open", "-R", resolved)
		} else {
			cmd = exec.Command("open", target)
		}
	default:
		cmd = exec.Command("xdg-open", target)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to open workspace path in explorer: %w", err)
	}
	return nil
}

func (s *SystemService) workspaceByID(id string) (Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, workspace := range s.state.Workspaces {
		if workspace.ID == id {
			return workspace, nil
		}
	}
	return Workspace{}, fmt.Errorf("workspace was not found")
}

func (s *SystemService) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.storePath)
	if err != nil {
		if os.IsNotExist(err) {
			return s.saveLocked()
		}
		return err
	}

	stored := storedAppStateFrom(defaultAppState())
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}
	state := stored.appState()

	// Read legacy agentModes from state file for migration to disk storage.
	var storedRaw storedAppStateRaw
	_ = json.Unmarshal(data, &storedRaw)

	legacyKanbanCards := cloneKanbanCards(stored.KanbanCards)
	legacyChatSessions := clonePersistedChatSessions(stored.ChatSessions)
	hadLegacyWorkspaceState := len(legacyKanbanCards) > 0 || len(legacyChatSessions) > 0
	legacyThinkingDisabled := stateFileLegacyThinkingDisabled(data) && !stateFileHasSettingKey(data, "thinkingTokenBudget")
	legacyLLMEndpoints := !stateFileHasSettingKey(data, "endpoints")
	legacyEndpointSelection := !stateFileHasSettingKey(data, "endpointSelection")

	// Migrate legacy comfyuiDefaultWorkflow → separate txt2img/img2img workflow fields.
	if stateFileHasSettingKey(data, "comfyuiDefaultWorkflow") {
		var oldSettings struct {
			ComfyuiDefaultWorkflow string `json:"comfyuiDefaultWorkflow"`
		}
		if err := json.Unmarshal(stateFileSettingsRaw(data), &oldSettings); err == nil {
			oldValue := strings.TrimSpace(oldSettings.ComfyuiDefaultWorkflow)
			if oldValue != "" && state.Settings.ComfyuiTxt2imgWorkflow == "" && state.Settings.ComfyuiImg2imgWorkflow == "" {
				state.Settings.ComfyuiTxt2imgWorkflow = oldValue
				state.Settings.ComfyuiImg2imgWorkflow = oldValue
			}
		}
	}

	state.Settings = state.Settings.Normalized()
	missingLLMEndpoint := state.Settings.Endpoint == ""
	missingLLMModel := state.Settings.Model == ""
	missingWebAccessToken := strings.TrimSpace(state.WebAccess.AccessToken) == ""
	migratedWebAccessPort := false
	state.WebAccess, migratedWebAccessPort = migrateWebAccessDefaultPort(state.WebAccess)
	state.WebAccess = normalizeWebAccessSettings(state.WebAccess, "")
	if legacyThinkingDisabled {
		state.Settings.ThinkingTokenBudget = 0
	}
	if state.Settings.Endpoint == "" {
		state.Settings.Endpoint = llm.DefaultEndpoint
	}
	if state.Settings.Model == "" {
		state.Settings.Model = llm.DefaultModel
	}
	state.Settings = state.Settings.Normalized()
	normalizeLoadedWorkspaces(&state)
	state.KanbanCards = []KanbanCard{}
	loadedChatSessions := make(map[string]persistedChatSession)
	workspacesToMigrate := make(map[string]bool)
	for _, workspace := range state.Workspaces {
		autosave, found, autosaveErr := readWorkspaceAutosave(workspace)
		if autosaveErr == nil && found {
			for _, card := range autosave.KanbanCards {
				card.WorkspaceID = workspace.ID
				state.KanbanCards = append(state.KanbanCards, cloneKanbanCard(card))
			}
			if autosave.ChatSession != nil {
				session := *autosave.ChatSession
				session.WorkspaceID = workspace.ID
				session.Messages = cloneChatMessages(session.Messages)
				session.History = cloneLLMMessages(session.History)
				loadedChatSessions[workspace.ID] = session
			}
			continue
		}
		for _, card := range legacyKanbanCards {
			if card.WorkspaceID == workspace.ID {
				state.KanbanCards = append(state.KanbanCards, cloneKanbanCard(card))
				workspacesToMigrate[workspace.ID] = true
			}
		}
		if session, ok := legacyChatSessions[workspace.ID]; ok {
			session.WorkspaceID = workspace.ID
			loadedChatSessions[workspace.ID] = session
			workspacesToMigrate[workspace.ID] = true
		}
	}
	interruptedKanban := normalizeInterruptedKanbanCards(state.KanbanCards)
	if !workspaceExists(state.Workspaces, state.ActiveWorkspaceID) {
		state.ActiveWorkspaceID = ""
		for _, workspace := range state.Workspaces {
			if workspace.Active {
				state.ActiveWorkspaceID = workspace.ID
				break
			}
		}
		if state.ActiveWorkspaceID == "" && len(state.Workspaces) > 0 {
			state.ActiveWorkspaceID = state.Workspaces[0].ID
		}
	}
	s.state = state
	// Restore token budgets from persisted state.
	if stored.TokenBudgets != nil {
		for wid, b := range stored.TokenBudgets {
			s.tokenBudget.budgets[wid] = b
		}
	}
	// Migrate legacy global agent modes to workspace disk storage.
	if len(storedRaw.AgentModes) > 0 {
		s.migrateGlobalAgentModesToDisk(storedRaw.AgentModes)
	}
	s.persistedChatSessions = loadedChatSessions
	interruptedChat := s.restoreChatSessionsLocked()
	changed := s.refreshWorkspaceStatusesLocked()
	for _, workspace := range s.state.Workspaces {
		if !workspacesToMigrate[workspace.ID] {
			continue
		}
		var chat *persistedChatSession
		if persisted, ok := s.persistedChatSessions[workspace.ID]; ok {
			snapshot := persisted
			chat = &snapshot
		}
		cards := make([]KanbanCard, 0)
		for _, card := range s.state.KanbanCards {
			if card.WorkspaceID == workspace.ID {
				cards = append(cards, cloneKanbanCard(card))
			}
		}
		if err := writeWorkspaceAutosave(workspace, workspaceAutosave{
			Version:     workspaceAutosaveVersion,
			ChatSession: chat,
			KanbanCards: cards,
		}); err != nil {
			return err
		}
	}
	if changed || interruptedKanban || interruptedChat || hadLegacyWorkspaceState || legacyThinkingDisabled || legacyLLMEndpoints || legacyEndpointSelection || missingLLMEndpoint || missingLLMModel || missingWebAccessToken || migratedWebAccessPort {
		return s.saveLocked()
	}
	return nil
}

func (s *SystemService) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.storePath), 0o755); err != nil {
		return err
	}
	stored := storedAppStateFrom(s.state)
	stored.TokenBudgets = s.tokenBudget.budgets
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	if existing, err := os.ReadFile(s.storePath); err == nil && bytes.Equal(existing, data) {
		return nil
	}
	return os.WriteFile(s.storePath, data, 0o600)
}

func (s *SystemService) workspaceIconDir() string {
	return filepath.Join(filepath.Dir(s.storePath), "icons")
}

func (s *SystemService) WorkspaceIconHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, workspaceIconRoutePrefix)
		if name == "" || name != filepath.Base(name) {
			http.NotFound(w, r)
			return
		}
		path := filepath.Join(s.workspaceIconDir(), name)
		if extension, err := validateWorkspaceIconFile(path); err != nil || extension == "" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, path)
	})
}

func (s *SystemService) WorkspaceIconMiddleware(next http.Handler) http.Handler {
	iconHandler := s.WorkspaceIconHandler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, workspaceIconRoutePrefix) {
			iconHandler.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func defaultAppState() AppState {
	return AppState{
		Settings:         llm.DefaultSettings(),
		WebAccess:        defaultWebAccessSettings(),
		Workspaces:       []Workspace{},
		SavedCommands:    make(map[string][]SavedCommand),
		HeartbeatConfigs: make(map[string]HeartbeatConfig),
		WatchdogConfigs:  make(map[string]WatchdogConfig),
		KanbanCards:      []KanbanCard{},
	}
}

func defaultStorePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "Echo", "state.json"), nil
}

func stateFileHasKey(data []byte, key string) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	_, ok := raw[key]
	return ok
}

func stateFileHasSettingKey(data []byte, key string) bool {
	var raw struct {
		Settings map[string]json.RawMessage `json:"settings"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	_, ok := raw.Settings[key]
	return ok
}

func stateFileSettingsRaw(data []byte) json.RawMessage {
	var raw struct {
		Settings json.RawMessage `json:"settings"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	return raw.Settings
}

func stateFileLegacyThinkingDisabled(data []byte) bool {
	var raw struct {
		Settings struct {
			EnableThinking *bool `json:"enableThinking"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	return raw.Settings.EnableThinking != nil && !*raw.Settings.EnableThinking
}

func workspaceFromPath(path string) Workspace {
	clean := filepath.Clean(path)
	hash := sha1.Sum([]byte(strings.ToLower(clean)))
	name := filepath.Base(clean)
	if name == "." || name == string(filepath.Separator) {
		name = clean
	}
	return Workspace{
		ID:              hex.EncodeToString(hash[:8]),
		Folders:         []WorkspaceFolder{workspaceFolderFromPath(clean, nil)},
		DisplayName:     name,
		DefaultPlanMode: true,
	}
}

func workspaceFolderFromPath(path string, usedLabels map[string]bool) WorkspaceFolder {
	clean := filepath.Clean(path)
	return WorkspaceFolder{
		ID:        workspaceFolderID(clean),
		Label:     uniqueWorkspaceFolderLabel(clean, usedLabels),
		Path:      clean,
		UseAgents: true,
	}
}

func workspaceFolderID(path string) string {
	clean := filepath.Clean(path)
	hash := sha1.Sum([]byte(strings.ToLower(clean)))
	return hex.EncodeToString(hash[:8])
}

func normalizedWorkspacePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func normalizeLoadedWorkspaces(state *AppState) {
	if state.Workspaces == nil {
		state.Workspaces = []Workspace{}
	}
	if state.KanbanCards == nil {
		state.KanbanCards = []KanbanCard{}
	}
	for i := range state.Workspaces {
		workspace := &state.Workspaces[i]
		if workspace.ID == "" {
			if len(workspace.Folders) > 0 {
				workspace.ID = workspaceFromPath(workspace.Folders[0].Path).ID
			} else {
				workspace.ID = workspaceIDFromName(workspace.DisplayName)
			}
		}
		if strings.TrimSpace(workspace.DisplayName) == "" {
			if len(workspace.Folders) > 0 {
				workspace.DisplayName = workspaceFromPath(workspace.Folders[0].Path).DisplayName
			} else {
				workspace.DisplayName = "Blank workspace"
			}
		}
		normalizeWorkspaceFolders(workspace)
		workspace.Letter = normalizeWorkspaceLetter(workspace.Letter)
		if workspace.IconPath != "" {
			workspace.IconURL = workspaceIconURL(workspace.IconPath)
		} else {
			workspace.IconURL = ""
		}
	}
}

func workspaceIDFromName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "blank workspace"
	}
	hash := sha1.Sum([]byte(strings.ToLower(name)))
	return hex.EncodeToString(hash[:8])
}

func normalizeWorkspaceFolders(workspace *Workspace) {
	if workspace.Folders == nil {
		workspace.Folders = []WorkspaceFolder{}
	}
	used := map[string]bool{}
	next := workspace.Folders[:0]
	for _, folder := range workspace.Folders {
		folder.Path = strings.TrimSpace(folder.Path)
		if folder.Path == "" {
			continue
		}
		folder.Path = filepath.Clean(folder.Path)
		if folder.ID == "" {
			folder.ID = workspaceFolderID(folder.Path)
		}
		folder.Label = uniqueWorkspaceFolderLabelWithPreferred(folder.Path, folder.Label, used)
		next = append(next, folder)
	}
	workspace.Folders = next
}

func workspaceFolderLabelSet(folders []WorkspaceFolder) map[string]bool {
	used := map[string]bool{}
	for _, folder := range folders {
		label := strings.ToLower(strings.TrimSpace(folder.Label))
		if label != "" {
			used[label] = true
		}
	}
	return used
}

func uniqueWorkspaceFolderLabel(path string, used map[string]bool) string {
	return uniqueWorkspaceFolderLabelWithPreferred(path, "", used)
}

func uniqueWorkspaceFolderLabelWithPreferred(path string, preferred string, used map[string]bool) string {
	if used == nil {
		used = map[string]bool{}
	}
	base := normalizeWorkspaceFolderLabel(preferred)
	if base == "" {
		base = normalizeWorkspaceFolderLabel(filepath.Base(filepath.Clean(path)))
	}
	if base == "" {
		base = "folder"
	}
	label := base
	for suffix := 2; used[strings.ToLower(label)]; suffix++ {
		label = fmt.Sprintf("%s-%d", base, suffix)
	}
	used[strings.ToLower(label)] = true
	return label
}

func normalizeWorkspaceFolderLabel(label string) string {
	label = strings.TrimSpace(strings.ToLower(label))
	var builder strings.Builder
	lastDash := false
	for _, r := range label {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if valid {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-_.")
}

func workspaceContainsFolderPath(workspace Workspace, path string) bool {
	clean := filepath.Clean(path)
	for _, folder := range workspace.Folders {
		if strings.EqualFold(filepath.Clean(folder.Path), clean) {
			return true
		}
	}
	return false
}

func firstAvailableWorkspaceFolderPath(workspace Workspace) (string, bool) {
	for _, folder := range workspace.Folders {
		if !folder.Missing && strings.TrimSpace(folder.Path) != "" {
			return folder.Path, true
		}
	}
	return "", false
}

func workspaceFolderByPath(workspace Workspace, path string) WorkspaceFolder {
	clean := filepath.Clean(path)
	for _, folder := range workspace.Folders {
		if strings.EqualFold(filepath.Clean(folder.Path), clean) {
			return folder
		}
	}
	return WorkspaceFolder{}
}

func normalizeWorkspaceLetter(letter string) string {
	letter = strings.TrimSpace(letter)
	if letter == "" {
		return ""
	}
	return strings.ToUpper(letter)
}

func validateWorkspaceIconFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("workspace icon does not exist")
	}
	if info.IsDir() {
		return "", fmt.Errorf("workspace icon must be an image file")
	}

	extension := strings.ToLower(filepath.Ext(path))
	switch extension {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".ico":
		return extension, nil
	default:
		return "", fmt.Errorf("workspace icon must be a PNG, JPG, GIF, WebP, BMP, or ICO image")
	}
}

func copyWorkspaceIcon(sourcePath string, destinationPath string) error {
	sourcePath = filepath.Clean(sourcePath)
	destinationPath = filepath.Clean(destinationPath)
	if strings.EqualFold(sourcePath, destinationPath) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()

	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer destination.Close()

	_, err = io.Copy(destination, source)
	return err
}

func removeOtherWorkspaceIconExtensions(iconDir string, workspaceID string, keepExtension string) {
	for _, extension := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".ico"} {
		if extension == keepExtension {
			continue
		}
		removeStoredWorkspaceIcon(filepath.Join(iconDir, workspaceID+extension))
	}
}

func removeStoredWorkspaceIcon(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	_ = os.Remove(path)
}

func workspaceIconURL(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	name := filepath.Base(filepath.Clean(path))
	if name == "." || name == string(filepath.Separator) {
		return ""
	}
	iconURL := workspaceIconRoutePrefix + url.PathEscape(name)
	if info, err := os.Stat(path); err == nil {
		iconURL += fmt.Sprintf("?v=%d", info.ModTime().UnixNano())
	}
	return iconURL
}

func (s *SystemService) refreshWorkspaceStatusesLocked() bool {
	changed := false
	for i := range s.state.Workspaces {
		workspace := &s.state.Workspaces[i]
		active := workspace.ID == s.state.ActiveWorkspaceID
		missing := false
		statusError := ""

		for j := range workspace.Folders {
			folder := &workspace.Folders[j]
			folderMissing := false
			folderError := ""
			if strings.TrimSpace(folder.Path) == "" {
				folderMissing = true
				folderError = "Workspace folder path is missing."
			} else if info, err := os.Stat(folder.Path); err != nil {
				folderMissing = true
				folderError = "Workspace folder was moved or deleted."
			} else if !info.IsDir() {
				folderMissing = true
				folderError = "Workspace path is no longer a folder."
			}
			if folder.Missing != folderMissing || folder.Error != folderError {
				changed = true
			}
			folder.Missing = folderMissing
			folder.Error = folderError
			if folderMissing {
				missing = true
			}
		}
		if missing {
			statusError = "One or more workspace folders are unavailable."
		}

		if workspace.Active != active || workspace.Missing != missing || workspace.Error != statusError {
			changed = true
		}
		workspace.Active = active
		workspace.Missing = missing
		workspace.Error = statusError
	}
	return changed
}

func workspaceExists(workspaces []Workspace, id string) bool {
	if id == "" {
		return false
	}
	for _, workspace := range workspaces {
		if workspace.ID == id {
			return true
		}
	}
	return false
}

func cloneState(state AppState) AppState {
	state.Settings = state.Settings.Clone()
	state.WebAccess = normalizeWebAccessSettings(state.WebAccess, "")
	state.Workspaces = append([]Workspace{}, state.Workspaces...)
	for i := range state.Workspaces {
		state.Workspaces[i].Folders = append([]WorkspaceFolder{}, state.Workspaces[i].Folders...)
	}
	if state.SavedCommands != nil {
		state.SavedCommands = make(map[string][]SavedCommand, len(state.SavedCommands))
		for k, v := range state.SavedCommands {
			state.SavedCommands[k] = append([]SavedCommand{}, v...)
		}
	}
	if state.HeartbeatConfigs != nil {
		state.HeartbeatConfigs = cloneHeartbeatConfigs(state.HeartbeatConfigs)
	}
	if state.WatchdogConfigs != nil {
		state.WatchdogConfigs = cloneWatchdogConfigs(state.WatchdogConfigs)
	}
	if state.LivenessConfigs != nil {
		state.LivenessConfigs = cloneLivenessConfigs(state.LivenessConfigs)
	}
	if state.DashboardLayouts != nil {
		state.DashboardLayouts = cloneDashboardLayouts(state.DashboardLayouts)
	}
	state.KanbanCards = cloneKanbanCards(state.KanbanCards)
	return state
}

func cloneHeartbeatConfigs(src map[string]HeartbeatConfig) map[string]HeartbeatConfig {
	dst := make(map[string]HeartbeatConfig, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneWatchdogConfigs(src map[string]WatchdogConfig) map[string]WatchdogConfig {
	dst := make(map[string]WatchdogConfig, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneLivenessConfigs(src map[string]LivenessConfig) map[string]LivenessConfig {
	dst := make(map[string]LivenessConfig, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneDashboardLayouts(src map[string][]DashboardWidgetJSON) map[string][]DashboardWidgetJSON {
	dst := make(map[string][]DashboardWidgetJSON, len(src))
	for k, v := range src {
		dst[k] = append([]DashboardWidgetJSON{}, v...)
	}
	return dst
}
