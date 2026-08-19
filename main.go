package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/brent/echo/internal/server"
)

//go:embed web/dist
var embeddedWeb embed.FS

func main() {
	port := flag.Int("port", 3740, "port to listen on")
	webDir := flag.String("web", "", "serve SPA assets from this directory instead of the embedded production build")
	dataPath := flag.String("data", "", "path to Echo's application-data JSON (defaults to the platform config directory)")
	resetAuth := flag.Bool("reset-auth", false, "clear the owner password and remembered sessions")
	flag.Parse()

	addr := fmt.Sprintf(":%d", *port)
	var srv *server.Server
	if *webDir != "" {
		srv = server.NewWithSettingsPath(addr, *webDir, *dataPath)
	} else {
		assets, err := fs.Sub(embeddedWeb, "web/dist")
		if err != nil {
			log.Fatalf("load embedded frontend: %v", err)
		}
		srv = server.NewWithAssets(addr, assets, *dataPath)
	}
	if *resetAuth {
		code, err := srv.ResetAuthentication()
		if err != nil {
			log.Fatalf("reset authentication: %v", err)
		}
		log.Printf("Echo authentication was reset. New setup code: %s", code)
	} else if code := srv.AuthenticationSetupCode(); code != "" {
		log.Printf("Echo authentication setup code: %s", code)
	}

	// Run the server in a goroutine so we can wait for signals.
	errCh := make(chan error, 1)
	go func() {
		log.Printf("Echo web server listening on http://localhost:%d", *port)
		errCh <- srv.ListenAndServe()
	}()

	// Wait for an interrupt or a fatal serve error.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	case sig := <-stop:
		log.Printf("received signal %v, shutting down", sig)
	case <-srv.RestartRequested():
		log.Printf("rebuilt Echo is ready; shutting down for relaunch")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
