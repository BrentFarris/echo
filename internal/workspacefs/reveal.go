package workspacefs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func (s *Service) Reveal(workspaceID string, ref FileRef) error {
	_, resolved, _, err := s.resolveEntry(workspaceID, ref, true, false)
	if err != nil {
		return err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	name, arguments := revealCommand(resolved, info.IsDir())
	command := exec.Command(name, arguments...)
	if err := command.Start(); err != nil {
		return fmt.Errorf("open host file browser: %w", err)
	}
	if command.Process != nil {
		_ = command.Process.Release()
	}
	return nil
}

func revealCommand(resolved string, directory bool) (string, []string) {
	switch runtime.GOOS {
	case "windows":
		if directory {
			return "explorer.exe", []string{resolved}
		}
		return "explorer.exe", []string{"/select," + resolved}
	case "darwin":
		return "open", []string{"-R", resolved}
	default:
		target := resolved
		if !directory {
			target = filepath.Dir(resolved)
		}
		return "xdg-open", []string{target}
	}
}
