package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/brent/echo/internal/server"
)

func main() {
	port := flag.Int("port", 3740, "port to listen on")
	webDir := flag.String("web", "web", "directory containing the SPA frontend assets")
	flag.Parse()

	addr := fmt.Sprintf(":%d", *port)
	srv := server.New(addr, *webDir)

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
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
