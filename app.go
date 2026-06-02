package main

import (
	"context"

	"github.com/brent/echo/internal/services"
)

// App owns the Wails application lifecycle and exposes backend services.
type App struct {
	ctx    context.Context
	System *services.SystemService
}

func NewApp() *App {
	return &App{
		System: services.NewSystemService(),
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	services.SetSystemServiceContext(a.System, ctx)
}

func (a *App) shutdown(ctx context.Context) {
	if a.System != nil {
		a.System.Shutdown()
	}
	a.ctx = nil
}
