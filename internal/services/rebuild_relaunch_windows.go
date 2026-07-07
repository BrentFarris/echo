//go:build windows

package services

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func launchDetachedRebuild(scriptPath string, workspaceDir string) error {
	baseName := strings.TrimSuffix(scriptPath, ".ps1")
	logFile := baseName + ".log"
	batPath := baseName + ".bat"

	const batTemplate = `@echo off
chcp 65001 >nul 2>&1
cd /d "__WS_DIR__" || (echo [%Date% %Time%] FAIL: cd failed >> "__LOG_FILE__" & exit /b 1)

echo [%Date% %Time%] === Echo rebuild started === >> "__LOG_FILE__"
echo [%Date% %Time%] Workspace: __WS_DIR__ >> "__LOG_FILE__"

echo [%Date% %Time%] Waiting for Echo to shut down... >> "__LOG_FILE__"
timeout /t 5 /nobreak >nul

echo [%Date% %Time%] Killing echo.exe... >> "__LOG_FILE__"
taskkill /F /IM echo.exe >nul 2>&1
timeout /t 2 /nobreak >nul

echo [%Date% %Time%] Running wails build... >> "__LOG_FILE__"
wails build >> "__LOG_FILE__" 2>&1
set BUILD_RC=%errorlevel%

if %BUILD_RC% neq 0 (
    echo [%Date% %Time%] BUILD FAILED with exit code %BUILD_RC% >> "__LOG_FILE__"
    exit /b %BUILD_RC%
)

echo [%Date% %Time%] Build succeeded. Launching echo.exe... >> "__LOG_FILE__"
start "" "__WS_DIR__\build\bin\echo.exe"
echo [%Date% %Time%] Echo launched successfully. >> "__LOG_FILE__"

del "%~f0"
`

	batContent := strings.ReplaceAll(batTemplate, "__WS_DIR__", workspaceDir)
	batContent = strings.ReplaceAll(batContent, "__LOG_FILE__", logFile)

	if err := os.WriteFile(batPath, []byte(batContent), 0644); err != nil {
		return fmt.Errorf("write bat: %w", err)
	}

	cmd := exec.Command("cmd.exe", "/c", "start", "", batPath)
	return cmd.Run()
}
