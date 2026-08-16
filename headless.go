package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/brent/echo/internal/services"
	"github.com/brent/echo/internal/webserver"
)

// headlessFlagName is parsed locally in runHeadless so the desktop path never
// depends on it. Both `go run . -headless` and the built binary accept it.
const headlessFlagName = "headless"

// hasHeadlessFlag reports whether the process was started with -headless (or
// --headless) anywhere in its arguments, so main can branch before Wails is
// involved. The desktop path never parses these flags itself.
func hasHeadlessFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-"+headlessFlagName || arg == "--"+headlessFlagName {
			return true
		}
	}
	return false
}

// prepareHeadlessWebAccessSettings forces web access on for headless runs and
// applies optional CLI overrides. It never persists anything: the returned
// settings are only handed to the web server, so desktop behavior is unchanged.
func prepareHeadlessWebAccessSettings(current services.WebAccessSettings, port int, bindHost string) services.WebAccessSettings {
	settings := current
	settings.Enabled = true
	if host := strings.TrimSpace(bindHost); host != "" {
		settings.BindHost = host
	}
	if strings.TrimSpace(settings.BindHost) == "" {
		settings.BindHost = "0.0.0.0"
	}
	if port > 0 && port <= 65535 {
		settings.Port = port
	}
	return settings
}

// runHeadless starts the web access server without a Wails window and blocks
// until the process receives an interrupt signal. It returns the exit code.
func runHeadless() int {
	flags := flag.NewFlagSet("echo", flag.ExitOnError)
	port := flags.Int("port", 0, "Web access port for headless mode (defaults to the saved setting or 3740).")
	bindHost := flags.String("bind", "", "Web access bind host for headless mode (defaults to the saved setting or 0.0.0.0).")
	_ = flags.Parse(os.Args[1:])

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runHeadlessWithInterrupt(ctx, *port, *bindHost, services.NewSystemService)
}

// runHeadlessWithInterrupt is the testable core of headless mode. It starts the
// web access server, prints the remote URLs, and waits for interrupt to be done
// before shutting everything down. newSystem is injectable so tests can use an
// isolated state store instead of the real user config dir.
func runHeadlessWithInterrupt(interrupt context.Context, port int, bindHost string, newSystem func() *services.SystemService) int {
	system := newSystem()
	webAccess := webserver.New(system, assets)
	services.SetWebAccessController(system, webAccess)

	savedSettings := system.LoadState().WebAccess
	settings := prepareHeadlessWebAccessSettings(savedSettings, port, bindHost)

	// Force-enable web access in memory so token checks pass even when the
	// saved settings have it disabled. This is runtime-only: nothing is
	// persisted, so desktop behavior is unchanged.
	system.ApplyWebAccessSettingsRuntime(settings)

	status, err := webAccess.ApplyWebAccessSettings(settings)
	if err != nil {
		fmt.Fprintf(os.Stderr, "echo headless: %v\n", err)
		return 1
	}
	if !status.Running || status.LastError != "" {
		fmt.Fprintf(os.Stderr, "echo headless: web access server is not running: %s\n", status.LastError)
		return 1
	}

	printHeadlessStatus(status)

	<-interrupt.Done()

	fmt.Println("\necho headless: shutting down...")
	// Restore the saved settings in memory before shutdown so no runtime-only
	// change can leak into a later state.json write.
	system.ApplyWebAccessSettingsRuntime(savedSettings)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := webAccess.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(os.Stderr, "echo headless: shutdown web access: %v\n", err)
	}
	system.Shutdown()
	return 0
}

// printHeadlessStatus reports the listening address and the tokenized URLs a
// remote browser can open. The LAN URLs already embed the access token.
func printHeadlessStatus(status services.WebAccessStatus) {
	fmt.Println("echo headless: running without the desktop window.")
	fmt.Printf("echo headless: web access listening on %s:%d\n", status.BindHost, status.Port)

	urls := status.LANURLs
	if len(urls) == 0 && status.PrimaryURL != "" {
		urls = []string{status.PrimaryURL}
	}
	if len(urls) > 0 {
		fmt.Println("Open one of these URLs in a remote browser:")
		for _, u := range urls {
			fmt.Printf("  %s\n", u)
		}
	} else {
		scheme := "http"
		if status.EnableTLS {
			scheme = "https"
		}
		fmt.Printf("  %s://localhost:%d/#token=%s\n", scheme, status.Port, url.QueryEscape(status.AccessToken))
	}

	if status.EnableTLS && strings.ToLower(status.BindHost) != "localhost" {
		fmt.Println("Note: the self-signed TLS certificate only covers 'localhost', so remote browsers will show a certificate warning.")
	}

	fmt.Println("Press Ctrl+C to stop.")
}
