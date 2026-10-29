package launch

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
)

func TestOpenSuppressedForDevelopmentRestart(t *testing.T) {
	t.Setenv(suppressEnv, "1")
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	Open("http://localhost:3740/")
	if !strings.Contains(output.String(), "skipped (ECHO_NO_OPEN is set)") {
		t.Fatal(output.String())
	}
}

func TestWindowsLaunchesInstalledShortcutAndFallsBackOnFailure(t *testing.T) {
	originalGOOS := goos
	goos = "windows"
	defer func() { goos = originalGOOS }()
	shortcut := `C:\Echo's Apps\Echo.lnk`
	shortcutCommand := "powershell -NoProfile -NonInteractive -Command Start-Process -ErrorAction Stop -FilePath 'C:\\Echo''s Apps\\Echo.lnk'"
	for _, fail := range []bool{false, true} {
		runner := &fakeRunner{responses: map[string]response{
			`reg query HKCU\Software\Classes\echo\shell\open\command /ve`:         {err: errors.New("not installed")},
			`reg query HKLM\Software\Classes\echo\shell\open\command /ve`:         {err: errors.New("not installed")},
			"powershell -NoProfile -NonInteractive -Command " + pwaShortcutScript: {output: shortcut},
		}}
		want := shortcut
		if fail {
			runner.responses[shortcutCommand] = response{err: errors.New("stale shortcut")}
			want = BrowserURL(4123)
		}
		target, err := open(runner, BrowserURL(4123))
		if err != nil || target != want {
			t.Fatalf("open = %q, %v; want %q", target, err, want)
		}
		if !strings.Contains(strings.Join(runner.commands, "\n"), shortcutCommand) {
			t.Fatalf("did not safely launch the installed shortcut: %v", runner.commands)
		}
	}
}

func TestBrowserFallbackPreservesConfiguredOrigin(t *testing.T) {
	originalGOOS := goos
	defer func() { goos = originalGOOS }()
	for _, platform := range []string{"windows", "linux", "darwin"} {
		goos = platform
		serverURL := "https://localhost:4872/"
		runner := &fakeRunner{}
		target, err := open(runner, serverURL)
		if err != nil || target != serverURL || !strings.Contains(runner.commands[len(runner.commands)-1], serverURL) {
			t.Fatalf("%s open = %q, %v; commands = %v", platform, target, err, runner.commands)
		}
		for _, command := range runner.commands {
			if strings.Contains(command, "echo://") {
				t.Fatal("HTTPS origin was downgraded through the legacy protocol", command)
			}
		}
	}
}

func TestLinuxFallsBackWhenInstalledHandlerFails(t *testing.T) {
	originalGOOS := goos
	goos = "linux"
	defer func() { goos = originalGOOS }()
	runner := &fakeRunner{responses: map[string]response{
		"xdg-mime query default x-scheme-handler/echo": {output: "echo.desktop"},
		"xdg-open echo://open?port=4123":               {err: errors.New("stale handler")},
	}}
	target, err := open(runner, BrowserURL(4123))
	if err != nil || target != BrowserURL(4123) {
		t.Fatalf("open = %q, %v", target, err)
	}
}

// fakeRunner records commands and answers them from a script.
type fakeRunner struct {
	commands  []string
	responses map[string]response
}

type response struct {
	output string
	err    error
}

func (f *fakeRunner) Run(name string, args ...string) (string, error) {
	joined := name + " " + strings.Join(args, " ")
	f.commands = append(f.commands, joined)
	if response, ok := f.responses[joined]; ok {
		return response.output, response.err
	}
	return "", nil
}

func TestResolveWindowsPrefersSchemeHandler(t *testing.T) {
	originalGOOS := goos
	goos = "windows"
	defer func() { goos = originalGOOS }()

	runner := &fakeRunner{responses: map[string]response{
		`reg query HKCU\Software\Classes\echo\shell\open\command /ve`: {output: "(Default)    REG_SZ    ..."},
	}}
	if got := resolveWindows(runner, BrowserURL(3740), AppURL(3740)); got != "echo://open?port=3740" {
		t.Fatalf("resolveWindows = %q, want the echo:// deep link", got)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("shortcut search should not run when the handler exists, got %v", runner.commands)
	}
}

func TestResolveWindowsFallsBackToInstalledPWAShortcut(t *testing.T) {
	originalGOOS := goos
	goos = "windows"
	defer func() { goos = originalGOOS }()

	runner := &fakeRunner{responses: map[string]response{
		`reg query HKCU\Software\Classes\echo\shell\open\command /ve`: {err: errors.New("unable to find the specified registry key")},
		`reg query HKLM\Software\Classes\echo\shell\open\command /ve`: {err: errors.New("unable to find the specified registry key")},
		"powershell -NoProfile -NonInteractive -Command " + pwaShortcutScript: {
			output: "C:\\Users\\brent\\AppData\\Roaming\\Microsoft\\Windows\\Start Menu\\Programs\\Brave Apps\\Echo.lnk\r\n",
		},
	}}
	got := resolveWindows(runner, BrowserURL(3740), AppURL(3740))
	if got != `C:\Users\brent\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Brave Apps\Echo.lnk` {
		t.Fatalf("resolveWindows = %q, want the installed PWA shortcut", got)
	}
}

func TestResolveWindowsFallsBackToBrowserWithoutPWA(t *testing.T) {
	originalGOOS := goos
	goos = "windows"
	defer func() { goos = originalGOOS }()

	runner := &fakeRunner{responses: map[string]response{
		`reg query HKCU\Software\Classes\echo\shell\open\command /ve`:         {err: errors.New("unable to find the specified registry key")},
		`reg query HKLM\Software\Classes\echo\shell\open\command /ve`:         {err: errors.New("unable to find the specified registry key")},
		"powershell -NoProfile -NonInteractive -Command " + pwaShortcutScript: {output: ""},
	}}
	if got := resolveWindows(runner, BrowserURL(3740), AppURL(3740)); got != "http://localhost:3740/" {
		t.Fatalf("resolveWindows = %q, want the browser URL", got)
	}
}

func TestFindPWAShortcutPrefersExactNameMatch(t *testing.T) {
	runner := &fakeRunner{responses: map[string]response{
		"powershell -NoProfile -NonInteractive -Command " + pwaShortcutScript: {
			output: "C:\\Start\\EchoSomething.lnk\r\nC:\\Start\\Echo.lnk\r\n",
		},
	}}
	if got := findPWAShortcut(runner); got != `C:\Start\Echo.lnk` {
		t.Fatalf("findPWAShortcut = %q, want the exact Echo.lnk", got)
	}
}

func TestFindPWAShortcutAcceptsPartialNameMatch(t *testing.T) {
	runner := &fakeRunner{responses: map[string]response{
		"powershell -NoProfile -NonInteractive -Command " + pwaShortcutScript: {
			output: "C:\\Start\\Echo Dev.lnk\r\n",
		},
	}}
	if got := findPWAShortcut(runner); got != `C:\Start\Echo Dev.lnk` {
		t.Fatalf("findPWAShortcut = %q, want the partial match", got)
	}
}

func TestFindPWAShortcutEmptyWhenNothingMatches(t *testing.T) {
	runner := &fakeRunner{responses: map[string]response{
		"powershell -NoProfile -NonInteractive -Command " + pwaShortcutScript: {
			output: "C:\\Start\\Unrelated.lnk\r\n",
		},
	}}
	if got := findPWAShortcut(runner); got != "" {
		t.Fatalf("findPWAShortcut = %q, want empty", got)
	}
}

func TestWindowsHandlerDetectionQueriesRegistry(t *testing.T) {
	originalGOOS := goos
	goos = "windows"
	defer func() { goos = originalGOOS }()

	runner := &fakeRunner{responses: map[string]response{
		`reg query HKCU\Software\Classes\echo\shell\open\command /ve`: {err: errors.New("unable to find the specified registry key")},
		`reg query HKLM\Software\Classes\echo\shell\open\command /ve`: {err: errors.New("unable to find the specified registry key")},
	}}
	if hasEchoHandler(runner) {
		t.Fatal("handler reported present with no registry key")
	}
	if len(runner.commands) != 2 {
		t.Fatalf("expected HKCU and HKLM probes, got %v", runner.commands)
	}
	if !strings.Contains(runner.commands[1], "HKLM") {
		t.Fatalf("second probe should check HKLM, got %v", runner.commands)
	}

	runner = &fakeRunner{responses: map[string]response{
		`reg query HKCU\Software\Classes\echo\shell\open\command /ve`: {
			output: "HKEY_CURRENT_USER\\Software\\Classes\\echo\\shell\\open\\command\r\n    (Default)    REG_SZ    \"C:\\...\\ChromeProxy.exe\" --protocol-handler ...",
		},
	}}
	if !hasEchoHandler(runner) {
		t.Fatal("handler not detected despite registered HKCU key")
	}
}

func TestLinuxHandlerDetectionQueriesXdgMime(t *testing.T) {
	originalGOOS := goos
	goos = "linux"
	defer func() { goos = originalGOOS }()

	runner := &fakeRunner{responses: map[string]response{
		"xdg-mime query default x-scheme-handler/echo": {output: "\n"},
	}}
	if hasEchoHandler(runner) {
		t.Fatal("handler reported present with empty xdg-mime output")
	}

	runner = &fakeRunner{responses: map[string]response{
		"xdg-mime query default x-scheme-handler/echo": {output: "chrome-echo-Default.desktop\n"},
	}}
	if !hasEchoHandler(runner) {
		t.Fatal("handler not detected despite xdg-mime default")
	}
}

func TestLinuxOpensBrowserWhenNoHandler(t *testing.T) {
	originalGOOS := goos
	goos = "linux"
	defer func() { goos = originalGOOS }()

	runner := &fakeRunner{responses: map[string]response{
		"xdg-mime query default x-scheme-handler/echo": {output: ""},
	}}
	if _, err := open(runner, BrowserURL(3740)); err != nil {
		t.Fatalf("open failed: %v", err)
	}
	last := runner.commands[len(runner.commands)-1]
	if !strings.HasPrefix(last, "xdg-open http://localhost:3740/") {
		t.Fatalf("expected browser fallback, got %q", last)
	}
}

func TestLinuxOpensPWAWhenHandlerExists(t *testing.T) {
	originalGOOS := goos
	goos = "linux"
	defer func() { goos = originalGOOS }()

	runner := &fakeRunner{responses: map[string]response{
		"xdg-mime query default x-scheme-handler/echo": {output: "chrome-echo-Default.desktop\n"},
	}}
	if _, err := open(runner, BrowserURL(3740)); err != nil {
		t.Fatalf("open failed: %v", err)
	}
	last := runner.commands[len(runner.commands)-1]
	if !strings.HasPrefix(last, "xdg-open echo://open?port=3740") {
		t.Fatalf("expected PWA launch, got %q", last)
	}
}

func TestDarwinFallsBackWhenOpenFails(t *testing.T) {
	originalGOOS := goos
	goos = "darwin"
	defer func() { goos = originalGOOS }()

	runner := &fakeRunner{responses: map[string]response{
		"open echo://open?port=3740": {err: errors.New("No application knows how to open URL echo://open?port=3740")},
	}}
	if _, err := open(runner, BrowserURL(3740)); err != nil {
		t.Fatalf("open should fall back to the browser, got error: %v", err)
	}
	last := runner.commands[len(runner.commands)-1]
	if !strings.HasPrefix(last, "open http://localhost:3740/") {
		t.Fatalf("expected browser fallback, got %q", last)
	}
}

func TestDarwinOpensPWAWhenOpenSucceeds(t *testing.T) {
	originalGOOS := goos
	goos = "darwin"
	defer func() { goos = originalGOOS }()

	runner := &fakeRunner{}
	if _, err := open(runner, BrowserURL(3740)); err != nil {
		t.Fatalf("open failed: %v", err)
	}
	if len(runner.commands) != 1 || !strings.HasPrefix(runner.commands[0], "open echo://open?port=3740") {
		t.Fatalf("expected a single PWA launch, got %v", runner.commands)
	}
}
