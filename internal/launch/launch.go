// Package launch opens the Echo web app once the server is listening.
//
// Existing installations may register an echo: protocol handler. Windows
// installations without that handler are found through their Start Menu
// shortcuts. The default browser at the server URL is the final fallback.
//
// Open never fails the process: every error is logged and startup continues.
package launch

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// suppressEnv disables auto-open for headless or embedded use.
const suppressEnv = "ECHO_NO_OPEN"

// commandRunner executes external commands and returns their combined output.
// It exists so detection and launch behavior can be tested with a fake.
type commandRunner interface {
	Run(name string, args ...string) (string, error)
}

type execRunner struct{}

func (execRunner) Run(name string, args ...string) (string, error) {
	// exec.Command is intentionally not used with a context: opening the app
	// must not be cancelled mid-flight, and every opener here returns quickly.
	command := exec.Command(name, args...)
	configureCommand(command)
	output, err := command.CombinedOutput()
	return string(output), err
}

// AppURL returns the echo:// deep link that opens the installed Echo PWA and
// points it at the server running on port.
func AppURL(port int) string {
	return fmt.Sprintf("echo://open?port=%d", port)
}

// BrowserURL returns the plain HTTP URL for the server on port.
func BrowserURL(port int) string {
	return fmt.Sprintf("http://localhost:%d/", port)
}

// Open opens the installed Echo PWA or the default browser. The caller must
// bind the server listener first. Problems are logged without stopping Echo.
func Open(serverURL string) {
	if os.Getenv(suppressEnv) == "1" {
		log.Printf("echo launch: skipped (%s is set)", suppressEnv)
		return
	}
	runner := execRunner{}
	opened, err := open(runner, serverURL)
	if err != nil {
		log.Printf("echo launch: failed to open %s: %v", opened, err)
		return
	}
	log.Printf("echo launch: opened %s", opened)
}

// goos is the platform selector used by open and hasEchoHandler. It is a
// variable so tests can exercise every platform branch.
var goos = runtime.GOOS

// open performs the platform-specific launch and returns the target it
// attempted last (for logging). macOS relies on try-and-fall-back because
// `open` exits non-zero when no application claims the scheme; Windows and
// Linux detect the handler first because their openers either pop an OS
// dialog (Windows) or silently misbehave (some xdg-open
// implementations) when the scheme is unregistered.
func open(runner commandRunner, serverURL string) (string, error) {
	origin, err := url.Parse(serverURL)
	if err != nil || origin.Hostname() != "localhost" || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Port() == "" {
		return serverURL, fmt.Errorf("invalid local Echo URL: %q", serverURL)
	}
	// The legacy protocol only carries an HTTP port. HTTPS must keep its origin.
	appURL := ""
	if origin.Scheme == "http" {
		appURL = "echo://open?port=" + origin.Port()
	}
	run := func(name string, args ...string) error {
		_, err := runner.Run(name, args...)
		return err
	}
	switch goos {
	case "darwin":
		if appURL != "" {
			if err := run("open", appURL); err == nil {
				return appURL, nil
			}
		}
		return serverURL, run("open", serverURL)
	case "windows":
		decision := resolveWindows(runner, serverURL, appURL)
		launch := func(target string) error {
			// Quote as a PowerShell literal; paths may contain spaces or shell metacharacters.
			return run("powershell", "-NoProfile", "-NonInteractive", "-Command", "Start-Process -ErrorAction Stop -FilePath '"+strings.ReplaceAll(target, "'", "''")+"'")
		}
		if err := launch(decision); err == nil {
			return decision, nil
		} else if decision == serverURL {
			return decision, err
		}
		return serverURL, launch(serverURL)
	default:
		if appURL != "" && hasEchoHandler(runner) {
			if err := run("xdg-open", appURL); err == nil {
				return appURL, nil
			}
		}
		return serverURL, run("xdg-open", serverURL)
	}
}

// resolveWindows decides what to open on Windows, in order of preference:
// an existing echo: scheme handler, the
// installed PWA's Start Menu shortcut, then the browser.
func resolveWindows(runner commandRunner, serverURL, appURL string) string {
	if appURL != "" && hasEchoHandler(runner) {
		return appURL
	}
	// A PWA installed before the manifest declared protocol_handlers (or by
	// a browser that does not register them) has no echo: handler, but its
	// Start Menu shortcut still launches the standalone app window.
	if shortcut := findPWAShortcut(runner); shortcut != "" {
		return shortcut
	}
	return serverURL
}

// pwaShortcutScript prints the full paths of Start Menu shortcuts whose
// target is a browser app proxy with an --app-id, i.e. installed PWAs.
const pwaShortcutScript = `$shell = New-Object -ComObject WScript.Shell
$roots = @("$env:APPDATA\Microsoft\Windows\Start Menu\Programs", "$env:ProgramData\Microsoft\Windows\Start Menu\Programs")
foreach ($root in $roots) {
  Get-ChildItem -LiteralPath $root -Recurse -Filter *.lnk -ErrorAction SilentlyContinue | ForEach-Object {
    $shortcut = $shell.CreateShortcut($_.FullName)
    if ($shortcut.TargetPath -match 'proxy\.exe$' -and $shortcut.Arguments -match '--app-id=') { Write-Output $_.FullName }
  }
}`

// findPWAShortcut returns the Start Menu shortcut of an installed PWA whose
// name matches Echo, preferring an exact "Echo.lnk". Empty when none exists.
func findPWAShortcut(runner commandRunner) string {
	output, err := runner.Run("powershell", "-NoProfile", "-NonInteractive", "-Command", pwaShortcutScript)
	if err != nil {
		return ""
	}
	var partial string
	for _, line := range strings.Split(output, "\n") {
		path := strings.TrimSpace(line)
		if path == "" {
			continue
		}
		// Accept Windows paths even when these tests run on Unix.
		name := strings.TrimSuffix(strings.ToLower(filepath.Base(strings.ReplaceAll(path, `\`, "/"))), ".lnk")
		if name == "echo" {
			return path
		}
		if partial == "" && strings.Contains(name, "echo") {
			partial = path
		}
	}
	return partial
}

// hasEchoHandler reports whether the operating system has a registered
// handler for the echo: scheme, which the installed PWA provides.
func hasEchoHandler(runner commandRunner) bool {
	switch goos {
	case "darwin":
		// No cheap dry-run exists for LaunchServices; the caller uses
		// try-and-fall-back instead of consulting this.
		return true
	case "windows":
		// Chrome and Edge register installed-PWA protocol handlers under
		// HKCU (per-user installs) or HKLM (machine-wide) via their proxy
		// launcher. reg exits non-zero when the key is absent.
		for _, key := range []string{
			`HKCU\Software\Classes\echo\shell\open\command`,
			`HKLM\Software\Classes\echo\shell\open\command`,
		} {
			if _, err := runner.Run("reg", "query", key, "/ve"); err == nil {
				return true
			}
		}
		return false
	default:
		// xdg-mime prints the default handler for the scheme's pseudo-MIME
		// type; empty output means nothing claims it.
		output, err := runner.Run("xdg-mime", "query", "default", "x-scheme-handler/echo")
		if err != nil {
			return false
		}
		return strings.TrimSpace(output) != ""
	}
}
