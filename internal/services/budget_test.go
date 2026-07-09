package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTokenBudgetSetCheckRecordReset(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))

	// Add a workspace.
	root := t.TempDir()
	ws := workspaceFromPath(root)
	service.mu.Lock()
	service.state.Workspaces = append(service.state.Workspaces, ws)
	service.state.ActiveWorkspaceID = ws.ID
	if err := service.saveLocked(); err != nil {
		service.mu.Unlock()
		t.Fatal(err)
	}
	service.mu.Unlock()

	// Set budget.
	if err := service.SetTokenBudget(ws.ID, 1000); err != nil {
		t.Fatalf("SetTokenBudget: %v", err)
	}

	// Check within budget.
	allowed, remaining, err := service.CheckTokenBudget(ws.ID)
	if err != nil {
		t.Fatalf("CheckTokenBudget: %v", err)
	}
	if !allowed {
		t.Fatal("expected workspace to be allowed")
	}
	if remaining != 1000 {
		t.Fatalf("expected remaining 1000, got %d", remaining)
	}

	// Record usage.
	used, err := service.RecordTokenUsage(ws.ID, 400)
	if err != nil {
		t.Fatalf("RecordTokenUsage: %v", err)
	}
	if used != 400 {
		t.Fatalf("expected used 400, got %d", used)
	}

	// Check after usage.
	allowed, remaining, err = service.CheckTokenBudget(ws.ID)
	if err != nil {
		t.Fatalf("CheckTokenBudget: %v", err)
	}
	if !allowed || remaining != 600 {
		t.Fatalf("expected allowed=true remaining=600, got allowed=%v remaining=%d", allowed, remaining)
	}

	// Record enough to exceed.
	_, err = service.RecordTokenUsage(ws.ID, 700)
	if err != nil {
		t.Fatalf("RecordTokenUsage: %v", err)
	}

	// Check should now be paused.
	allowed, _, err = service.CheckTokenBudget(ws.ID)
	if err != nil {
		t.Fatalf("CheckTokenBudget: %v", err)
	}
	if allowed {
		t.Fatal("expected workspace to be paused after exceeding budget")
	}

	// Reset.
	if err := service.ResetTokenBudget(ws.ID); err != nil {
		t.Fatalf("ResetTokenBudget: %v", err)
	}

	allowed, remaining, err = service.CheckTokenBudget(ws.ID)
	if err != nil {
		t.Fatalf("CheckTokenBudget: %v", err)
	}
	if !allowed || remaining != 1000 {
		t.Fatalf("expected allowed=true remaining=1000 after reset, got allowed=%v remaining=%d", allowed, remaining)
	}
}

func TestTokenBudgetPersistAndRestore(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "state.json")

	// Create service, add workspace, set budget.
	service1 := NewSystemServiceWithStorePath(storePath)
	root := t.TempDir()
	ws := workspaceFromPath(root)
	service1.mu.Lock()
	service1.state.Workspaces = append(service1.state.Workspaces, ws)
	service1.state.ActiveWorkspaceID = ws.ID
	if err := service1.saveLocked(); err != nil {
		service1.mu.Unlock()
		t.Fatal(err)
	}
	service1.mu.Unlock()

	if err := service1.SetTokenBudget(ws.ID, 500); err != nil {
		t.Fatalf("SetTokenBudget: %v", err)
	}
	if _, err := service1.RecordTokenUsage(ws.ID, 200); err != nil {
		t.Fatalf("RecordTokenUsage: %v", err)
	}

	// Simulate restart by creating a new service with the same store path.
	service2 := NewSystemServiceWithStorePath(storePath)

	budget, err := service2.GetTokenBudget(ws.ID)
	if err != nil {
		t.Fatalf("GetTokenBudget: %v", err)
	}
	if budget.Limit != 500 {
		t.Fatalf("expected limit 500, got %d", budget.Limit)
	}
	if budget.Used != 200 {
		t.Fatalf("expected used 200, got %d", budget.Used)
	}

	// Verify raw state.json contains tokenBudgets.
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["tokenBudgets"]; !ok {
		t.Fatal("state.json missing tokenBudgets key")
	}
}

func TestTokenBudgetNoLimitAllowsFreely(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	root := t.TempDir()
	ws := workspaceFromPath(root)
	service.mu.Lock()
	service.state.Workspaces = append(service.state.Workspaces, ws)
	service.state.ActiveWorkspaceID = ws.ID
	if err := service.saveLocked(); err != nil {
		service.mu.Unlock()
		t.Fatal(err)
	}
	service.mu.Unlock()

	// No budget set — should allow.
	allowed, _, err := service.CheckTokenBudget(ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected allowed when no budget set")
	}

	// Record with no budget — should not error.
	used, err := service.RecordTokenUsage(ws.ID, 9999)
	if err != nil {
		t.Fatal(err)
	}
	if used != 0 {
		t.Fatalf("expected used 0 when no budget, got %d", used)
	}
}

func TestTokenBudgetUnlimited(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	root := t.TempDir()
	ws := workspaceFromPath(root)
	service.mu.Lock()
	service.state.Workspaces = append(service.state.Workspaces, ws)
	service.state.ActiveWorkspaceID = ws.ID
	if err := service.saveLocked(); err != nil {
		service.mu.Unlock()
		t.Fatal(err)
	}
	service.mu.Unlock()

	// Set limit to 0 (unlimited).
	if err := service.SetTokenBudget(ws.ID, 0); err != nil {
		t.Fatal(err)
	}

	allowed, _, err := service.CheckTokenBudget(ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("expected allowed when limit is 0 (unlimited)")
	}
}

func TestTokenBudgetValidation(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))

	// Empty workspace ID.
	if err := service.SetTokenBudget("", 100); err == nil {
		t.Fatal("expected error for empty workspace ID")
	}
	if _, _, err := service.CheckTokenBudget(""); err == nil {
		t.Fatal("expected error for empty workspace ID")
	}
	if _, err := service.RecordTokenUsage("", 10); err == nil {
		t.Fatal("expected error for empty workspace ID")
	}
	if err := service.ResetTokenBudget(""); err == nil {
		t.Fatal("expected error for empty workspace ID")
	}

	// Negative limit.
	if err := service.SetTokenBudget("fake", -1); err == nil {
		t.Fatal("expected error for negative limit")
	}

	// Negative tokens.
	if _, err := service.RecordTokenUsage("fake", -5); err == nil {
		t.Fatal("expected error for negative token count")
	}

	// Non-existent workspace.
	if err := service.SetTokenBudget("nonexistent", 100); err == nil {
		t.Fatal("expected error for nonexistent workspace")
	}
}
