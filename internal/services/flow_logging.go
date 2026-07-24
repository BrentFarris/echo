package services

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/brent/echo/internal/llm"
)

const developmentLogDisplayPath = ".echo/echo.log"

type DevelopmentLogStatus struct {
	Enabled bool   `json:"enabled"`
	Path    string `json:"path"`
}

func (s *SystemService) LoadDevelopmentLogStatus() DevelopmentLogStatus {
	status := DevelopmentLogStatus{Path: developmentLogDisplayPath}
	if s == nil || s.flowLog == nil {
		return status
	}
	status.Enabled = s.flowLog.Enabled()
	if status.Enabled {
		if path := s.flowLog.Path(); path != "" {
			status.Path = path
			return status
		}
	}
	if path, err := s.activeWorkspaceDevelopmentLogPath(false); err == nil {
		status.Path = path
	}
	return status
}

func (s *SystemService) SetDevelopmentLoggingEnabled(enabled bool) (DevelopmentLogStatus, error) {
	if s == nil || s.flowLog == nil {
		return DevelopmentLogStatus{Path: developmentLogDisplayPath}, fmt.Errorf("development logging is unavailable")
	}
	if enabled == s.flowLog.Enabled() {
		return s.LoadDevelopmentLogStatus(), nil
	}
	var err error
	if enabled {
		path, pathErr := s.activeWorkspaceDevelopmentLogPath(true)
		if pathErr != nil {
			return s.LoadDevelopmentLogStatus(), fmt.Errorf("resolve development log path: %w", pathErr)
		}
		err = s.flowLog.Enable(path)
	} else {
		err = s.flowLog.Disable()
	}
	if err != nil {
		return s.LoadDevelopmentLogStatus(), fmt.Errorf("update development logging: %w", err)
	}
	return s.LoadDevelopmentLogStatus(), nil
}

func (s *SystemService) activeWorkspaceDevelopmentLogPath(createCache bool) (string, error) {
	s.mu.Lock()
	workspaceID := strings.TrimSpace(s.state.ActiveWorkspaceID)
	workspace := s.resolveWorkspaceLocked(workspaceID)
	var folder WorkspaceFolder
	if len(workspace.Folders) > 0 {
		folder = workspace.Folders[0]
	}
	s.mu.Unlock()

	if workspaceID == "" || workspace.ID == "" {
		return "", fmt.Errorf("an active workspace is required")
	}
	if len(workspace.Folders) == 0 {
		return "", fmt.Errorf("the active workspace has no configured folders")
	}
	if folder.Missing || strings.TrimSpace(folder.Path) == "" {
		return "", fmt.Errorf("the first workspace folder is unavailable")
	}
	root, err := workspaceFolderAbsolutePath(folder)
	if err != nil {
		return "", fmt.Errorf("resolve first workspace folder: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("the first workspace folder is unavailable: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("the first workspace folder is not a directory")
	}

	cacheRoot := filepath.Join(root, workspaceCacheDirName)
	if createCache {
		if err := ensureWorkspaceCacheDirectory(cacheRoot, root); err != nil {
			return "", err
		}
	} else if cacheInfo, cacheErr := os.Lstat(cacheRoot); cacheErr == nil {
		if cacheInfo.Mode()&os.ModeSymlink != 0 || !cacheInfo.IsDir() {
			return "", fmt.Errorf("workspace .echo path must be a directory and not a symlink")
		}
	} else if !os.IsNotExist(cacheErr) {
		return "", fmt.Errorf("stat workspace .echo directory: %w", cacheErr)
	}

	path := filepath.Join(cacheRoot, "echo.log")
	if logInfo, logErr := os.Lstat(path); logErr == nil {
		if logInfo.Mode()&os.ModeSymlink != 0 || !logInfo.Mode().IsRegular() {
			return "", fmt.Errorf("workspace development log must be a regular file and not a symlink")
		}
	} else if !os.IsNotExist(logErr) {
		return "", fmt.Errorf("stat workspace development log: %w", logErr)
	}
	return path, nil
}

func (s *SystemService) newLLMClient(settings llm.Settings, options ...llm.ClientOption) (*llm.Client, error) {
	options = append(options, llm.WithFlowLogger(s.flowLog))
	return llm.NewClient(settings, options...)
}

func (s *SystemService) logAIEvent(level slog.Level, event string, attrs ...slog.Attr) {
	if s != nil && s.flowLog != nil {
		s.flowLog.Log(level, event, attrs...)
	}
}
