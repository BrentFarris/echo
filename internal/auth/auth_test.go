package auth

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brent/echo/internal/appdata"
)

func TestSetupLoginAndRememberedSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "echo.json")
	manager, err := New(appdata.NewStore(path))
	if err != nil {
		t.Fatal(err)
	}
	code := manager.SetupCode()
	if code == "" {
		t.Fatal("expected setup code")
	}
	token, first, err := manager.Setup(code, "correct horse battery", DeviceInfo{Name: "Desktop"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if token == "" || first.Device != "Desktop" || manager.SetupCode() != "" {
		t.Fatalf("unexpected first session: %+v", first)
	}
	if _, ok, err := manager.Authenticate(token); err != nil || !ok {
		t.Fatalf("authenticate first session: ok=%v err=%v", ok, err)
	}
	secondToken, _, err := manager.Login("correct horse battery", DeviceInfo{Name: "Laptop"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	sessions, err := manager.Sessions(token)
	if err != nil || len(sessions) != 2 {
		t.Fatalf("sessions: %+v err=%v", sessions, err)
	}
	if err := manager.Logout(secondToken); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := manager.Authenticate(secondToken); ok {
		t.Fatal("logged out session still authenticates")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(token)) || bytes.Contains(data, []byte(secondToken)) || bytes.Contains(data, []byte("correct horse battery")) {
		t.Fatal("raw credentials were persisted")
	}
}

func TestPasswordChangeRevokesOtherDevicesAndRollsExpiry(t *testing.T) {
	manager, err := New(appdata.NewStore(filepath.Join(t.TempDir(), "echo.json")))
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return clock }
	firstToken, first, err := manager.Setup(manager.SetupCode(), "correct horse battery", DeviceInfo{Name: "Desktop"})
	if err != nil {
		t.Fatal(err)
	}
	secondToken, _, err := manager.Login("correct horse battery", DeviceInfo{Name: "Laptop"})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2 * time.Hour)
	refreshed, ok, err := manager.Authenticate(firstToken)
	if err != nil || !ok {
		t.Fatalf("refresh session: ok=%v err=%v", ok, err)
	}
	if !refreshed.ExpiresAt.Equal(clock.Add(sessionLifetime)) || refreshed.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("expiry did not roll: before=%v after=%v", first.ExpiresAt, refreshed.ExpiresAt)
	}
	if err := manager.ChangePassword("correct horse battery", "an even better password", firstToken); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := manager.Authenticate(secondToken); ok {
		t.Fatal("password change did not revoke the other device")
	}
	if _, _, err := manager.Login("correct horse battery", DeviceInfo{}); err != ErrInvalidCredentials {
		t.Fatalf("old password still works: %v", err)
	}
	if _, _, err := manager.Login("an even better password", DeviceInfo{}); err != nil {
		t.Fatalf("new password login: %v", err)
	}
}

func TestPasswordValidationAndSetupCodeIsSingleUse(t *testing.T) {
	manager, err := New(appdata.NewStore(filepath.Join(t.TempDir(), "echo.json")))
	if err != nil {
		t.Fatal(err)
	}
	code := manager.SetupCode()
	if _, _, err := manager.Setup(code, "short", DeviceInfo{}); err == nil {
		t.Fatal("expected short password rejection")
	}
	if _, _, err := manager.Setup("wrong", "correct horse battery", DeviceInfo{}); err != ErrInvalidSetupCode {
		t.Fatalf("expected invalid setup code, got %v", err)
	}
	if _, _, err := manager.Setup(code, "correct horse battery", DeviceInfo{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Setup(code, "another correct password", DeviceInfo{}); err != ErrAlreadyConfigured {
		t.Fatalf("expected already configured, got %v", err)
	}
}
