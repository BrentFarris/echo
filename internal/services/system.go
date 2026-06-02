package services

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/brent/echo/internal/llm"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type AppInfo struct {
	Name      string `json:"name"`
	Phase     string `json:"phase"`
	AccentHex string `json:"accentHex"`
}

type Workspace struct {
	ID          string `json:"id"`
	FolderPath  string `json:"folderPath"`
	DisplayName string `json:"displayName"`
	Letter      string `json:"letter,omitempty"`
	Active      bool   `json:"active"`
	Missing     bool   `json:"missing"`
	Error       string `json:"error,omitempty"`
}

func (w *Workspace) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID          string `json:"id"`
		FolderPath  string `json:"folderPath"`
		DisplayName string `json:"displayName"`
		Letter      string `json:"letter"`
		LegacyPath  string `json:"path"`
		LegacyName  string `json:"name"`
		Active      bool   `json:"active"`
		Missing     bool   `json:"missing"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*w = Workspace{
		ID:          raw.ID,
		FolderPath:  raw.FolderPath,
		DisplayName: raw.DisplayName,
		Letter:      normalizeWorkspaceLetter(raw.Letter),
		Active:      raw.Active,
		Missing:     raw.Missing,
		Error:       raw.Error,
	}
	if w.FolderPath == "" {
		w.FolderPath = raw.LegacyPath
	}
	if w.DisplayName == "" {
		w.DisplayName = raw.LegacyName
	}
	return nil
}

type AppState struct {
	Settings          llm.Settings `json:"settings"`
	Workspaces        []Workspace  `json:"workspaces"`
	ActiveWorkspaceID string       `json:"activeWorkspaceId"`
	KanbanCards       []KanbanCard `json:"-"`
}

type SystemService struct {
	info                 AppInfo
	ctx                  context.Context
	storePath            string
	mu                   sync.Mutex
	state                AppState
	chatMu               sync.Mutex
	chatSessions         map[string]*chatSessionState
	chatStreams          map[string]context.CancelFunc
	chatSeq              uint64
	kanbanRuns           map[string]context.CancelFunc
	kanbanAgents         map[string]*kanbanAgentRun
	kanbanAgentSeq       uint64
	kanbanDetailViews    map[string]string
	fileChangeMu         sync.Mutex
	fileChangeSeq        uint64
	fileChanges          map[string][]trackedFileChange
	workspaceToolLocks   map[string]*sync.Mutex
	kanbanEventSink      func(KanbanEvent)
	fileChangesEventSink func(FileChangesEvent)
	inlineCodeEventSink  func(InlineCodePromptEvent)
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
		storePath:          storePath,
		state:              defaultAppState(),
		chatSessions:       make(map[string]*chatSessionState),
		chatStreams:        make(map[string]context.CancelFunc),
		kanbanRuns:         make(map[string]context.CancelFunc),
		kanbanAgents:       make(map[string]*kanbanAgentRun),
		kanbanDetailViews:  make(map[string]string),
		fileChanges:        make(map[string][]trackedFileChange),
		workspaceToolLocks: make(map[string]*sync.Mutex),
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
	return cloneState(s.state)
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
		if strings.EqualFold(existing.FolderPath, workspace.FolderPath) {
			s.state.ActiveWorkspaceID = existing.ID
			s.refreshWorkspaceStatusesLocked()
			if err := s.saveLocked(); err != nil {
				return AppState{}, err
			}
			return cloneState(s.state), nil
		}
	}
	s.state.Workspaces = append(s.state.Workspaces, workspace)
	s.state.ActiveWorkspaceID = workspace.ID
	s.refreshWorkspaceStatusesLocked()
	if err := s.saveLocked(); err != nil {
		return AppState{}, err
	}
	return cloneState(s.state), nil
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

func (s *SystemService) SetActiveWorkspace(id string) (AppState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		s.state.ActiveWorkspaceID = ""
		s.refreshWorkspaceStatusesLocked()
		if err := s.saveLocked(); err != nil {
			return AppState{}, err
		}
		return cloneState(s.state), nil
	}
	for _, workspace := range s.state.Workspaces {
		if workspace.ID == id {
			s.state.ActiveWorkspaceID = id
			s.refreshWorkspaceStatusesLocked()
			if err := s.saveLocked(); err != nil {
				return AppState{}, err
			}
			return cloneState(s.state), nil
		}
	}
	return AppState{}, fmt.Errorf("workspace was not found")
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

func (s *SystemService) DeleteWorkspace(id string) (AppState, error) {
	if strings.TrimSpace(id) == "" {
		return AppState{}, fmt.Errorf("workspace id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.state.Workspaces[:0]
	deleted := false
	for _, workspace := range s.state.Workspaces {
		if workspace.ID == id {
			deleted = true
			continue
		}
		next = append(next, workspace)
	}
	if !deleted {
		return AppState{}, fmt.Errorf("workspace was not found")
	}

	s.state.Workspaces = next
	s.state.KanbanCards = cardsWithoutWorkspace(s.state.KanbanCards, id)
	if s.state.ActiveWorkspaceID == id {
		s.state.ActiveWorkspaceID = ""
		if len(s.state.Workspaces) > 0 {
			s.state.ActiveWorkspaceID = s.state.Workspaces[0].ID
		}
	}
	s.refreshWorkspaceStatusesLocked()
	if err := s.saveLocked(); err != nil {
		return AppState{}, err
	}
	s.dropChatSession(id)
	s.chatMu.Lock()
	delete(s.kanbanDetailViews, id)
	s.chatMu.Unlock()
	s.dropWorkspaceChangeReview(id)
	return cloneState(s.state), nil
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

	state := defaultAppState()
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	legacyKanbanCards := stateFileHasKey(data, "kanbanCards")
	state.Settings = state.Settings.Normalized()
	if state.Settings.Endpoint == "" {
		state.Settings.Endpoint = llm.DefaultEndpoint
	}
	if state.Settings.Model == "" {
		state.Settings.Model = llm.DefaultModel
	}
	normalizeLoadedWorkspaces(&state)
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
	changed := s.refreshWorkspaceStatusesLocked()
	if changed || legacyKanbanCards {
		return s.saveLocked()
	}
	return nil
}

func (s *SystemService) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.storePath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.storePath, data, 0o600)
}

func defaultAppState() AppState {
	return AppState{
		Settings:    llm.DefaultSettings(),
		Workspaces:  []Workspace{},
		KanbanCards: []KanbanCard{},
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

func workspaceFromPath(path string) Workspace {
	clean := filepath.Clean(path)
	hash := sha1.Sum([]byte(strings.ToLower(clean)))
	name := filepath.Base(clean)
	if name == "." || name == string(filepath.Separator) {
		name = clean
	}
	return Workspace{
		ID:          hex.EncodeToString(hash[:8]),
		FolderPath:  clean,
		DisplayName: name,
	}
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
		if strings.TrimSpace(workspace.FolderPath) == "" {
			continue
		}
		workspace.FolderPath = filepath.Clean(workspace.FolderPath)
		if workspace.ID == "" {
			workspace.ID = workspaceFromPath(workspace.FolderPath).ID
		}
		if strings.TrimSpace(workspace.DisplayName) == "" {
			workspace.DisplayName = workspaceFromPath(workspace.FolderPath).DisplayName
		}
		workspace.Letter = normalizeWorkspaceLetter(workspace.Letter)
	}
}

func normalizeWorkspaceLetter(letter string) string {
	letter = strings.TrimSpace(letter)
	if letter == "" {
		return ""
	}
	for _, char := range letter {
		return strings.ToUpper(string(char))
	}
	return ""
}

func (s *SystemService) refreshWorkspaceStatusesLocked() bool {
	changed := false
	for i := range s.state.Workspaces {
		workspace := &s.state.Workspaces[i]
		active := workspace.ID == s.state.ActiveWorkspaceID
		missing := false
		statusError := ""

		if strings.TrimSpace(workspace.FolderPath) == "" {
			missing = true
			statusError = "Workspace folder path is missing."
		} else if info, err := os.Stat(workspace.FolderPath); err != nil {
			missing = true
			statusError = "Workspace folder was moved or deleted."
		} else if !info.IsDir() {
			missing = true
			statusError = "Workspace path is no longer a folder."
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
	state.Workspaces = append([]Workspace{}, state.Workspaces...)
	state.KanbanCards = cloneKanbanCards(state.KanbanCards)
	return state
}
