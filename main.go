package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/brent/echo/internal/server"
)

//go:embed web/dist
var frontend embed.FS

//go:embed internal/plugins/guest/builtin.wasm
var guest []byte

func main() {
	port := flag.Int("port", 3740, "HTTP port")
	data := flag.String("data", "", "Echo data directory (defaults to the existing user profile)")
	web := flag.String("web", "", "serve frontend assets from this directory")
	flag.Parse()
	if *data == "" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base, _ = os.UserConfigDir()
		}
		// Preserve the existing profile path when changing the app's display name.
		*data = filepath.Join(base, "Echo2")
	}
	var assets fs.FS
	var e error
	if *web != "" {
		assets = os.DirFS(*web)
	} else {
		assets, e = fs.Sub(frontend, "web/dist")
		if e != nil {
			log.Fatal(e)
		}
	}
	app, e := server.New(*data, assets, guest)
	if e != nil {
		log.Fatal(e)
	}
	defer app.Close()
	addr, cert, key, e := app.ListenAddress(*port)
	if e != nil {
		log.Fatal(e)
	}
	httpServer := &http.Server{Addr: addr, Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	done := make(chan error, 1)
	go func() {
		if cert != "" {
			done <- httpServer.ListenAndServeTLS(cert, key)
		} else {
			done <- httpServer.ListenAndServe()
		}
	}()
	scheme := "http"
	if cert != "" {
		scheme = "https"
	}
	_, actualPort, _ := net.SplitHostPort(addr)
	log.Printf("Echo: %s://localhost:%s (data: %s)", scheme, actualPort, *data)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case <-stop:
	case <-app.ShutdownRequested():
	case e := <-done:
		if e != http.ErrServerClosed {
			log.Print(e)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpServer.Shutdown(ctx)
}
