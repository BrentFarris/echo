package services

import (
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/brent/echo/internal/llm"
)

const developmentLogDisplayPath = "./echo/echo.log"

type DevelopmentLogStatus struct {
	Enabled bool   `json:"enabled"`
	Path    string `json:"path"`
}

func (s *SystemService) LoadDevelopmentLogStatus() DevelopmentLogStatus {
	return DevelopmentLogStatus{
		Enabled: s != nil && s.flowLog != nil && s.flowLog.Enabled(),
		Path:    developmentLogDisplayPath,
	}
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
		err = s.flowLog.Enable(filepath.Join(".", "echo", "echo.log"))
	} else {
		err = s.flowLog.Disable()
	}
	if err != nil {
		return s.LoadDevelopmentLogStatus(), fmt.Errorf("update development logging: %w", err)
	}
	return s.LoadDevelopmentLogStatus(), nil
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
