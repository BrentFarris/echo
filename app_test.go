package main

import "testing"

func TestNewAppInitializesServices(t *testing.T) {
	app := NewApp()

	if app == nil {
		t.Fatal("expected app")
	}
	if app.System == nil {
		t.Fatal("expected system service")
	}
}
