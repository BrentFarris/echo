// Package launch opens the Echo web app once the server is listening.
//
// When the Echo PWA is installed it registers an OS-level handler for the
// echo: URL scheme (declared via protocol_handlers in the web app manifest).
// Launching echo:// then opens the standalone installed app. When no handler
// exists — the PWA was installed before the manifest declared the handler,
// or the browser does not register them — the installed PWA's Start Menu
// shortcut is launched directly (Windows), and the default browser at the
// server URL is the final fallback.
//
// Open never fails the process: every error is logged and startup continues.
package launch

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// suppressEnv disables auto-open for headless or embedded use.
const suppressEnv = "ECHO_NO_OPEN"

// healthTimeout bounds how long Open waits for the server to answer before
// giving up. The PWA (or browser tab) is only launched once the origin can
// actually serve it.
const healthTimeout = 10 * time.Second

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

// Open waits for the server on 127.0.0.1:port to become healthy, then opens
// the installed Echo PWA via the echo: scheme, falling back to launching the
// installed PWA shortcut directly, and finally to the default browser. It is
// safe to call in a goroutine and never returns an error; problems are
// logged.
func Open(port int) {
	if os.Getenv(suppressEnv) == "1" {
		log.Printf("echo launch: skipped (%s is set)", suppressEnv)
		return
	}
	if err := waitUntilHealthy(port, healthTimeout); err != nil {
		log.Printf("echo launch: server did not become healthy, not opening an app window: %v", err)
		return
	}
	runner := execRunner{}
	opened, err := open(runner, port)
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
// dialog (Windows `start`) or silently misbehave (some xdg-open
// implementations) when the scheme is unregistered.
func open(runner commandRunner, port int) (string, error) {
	run := func(name string, args ...string) error {
		_, err := runner.Run(name, args...)
		return err
	}
	switch goos {
	case "darwin":
		if err := run("open", AppURL(port)); err == nil {
			return AppURL(port), nil
		}
		return BrowserURL(port), run("open", BrowserURL(port))
	case "windows":
		decision := resolveWindows(runner, port)
		return decision, run("cmd", "/c", "start", "", decision)
	default:
		if hasEchoHandler(runner) {
			return AppURL(port), run("xdg-open", AppURL(port))
		}
		return BrowserURL(port), run("xdg-open", BrowserURL(port))
	}
}

// resolveWindows decides what to open on Windows, in order of preference:
// the echo: scheme handler (registered by an up-to-date PWA install), the
// installed PWA's Start Menu shortcut, then the browser.
func resolveWindows(runner commandRunner, port int) string {
	if hasEchoHandler(runner) {
		return AppURL(port)
	}
	// A PWA installed before the manifest declared protocol_handlers (or by
	// a browser that does not register them) has no echo: handler, but its
	// Start Menu shortcut still launches the standalone app window.
	if shortcut := findPWAShortcut(runner); shortcut != "" {
		return shortcut
	}
	return BrowserURL(port)
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
		name := strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".lnk")
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

// waitUntilHealthy polls the server's health endpoint until it answers or
// the timeout elapses.
func waitUntilHealthy(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://127.0.0.1:%d/api/health", port)
	client := &http.Client{Timeout: time.Second}
	for {
		response, err := client.Get(url)
		if err == nil {
			// Any HTTP response means the listener is up and serving; the
			// body is irrelevant here.
			_ = response.Body.Close()
			if response.StatusCode != 0 {
				return nil
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("health check at %s: %w", url, err)
			}
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
}
