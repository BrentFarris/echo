package tools

import (
	"testing"
)

func TestToolScopeCheckerAllowAll(t *testing.T) {
	checker := NewToolScopeChecker(nil)
	if !checker.Allowed("any_tool", "") {
		t.Fatal("empty checker should allow all tools")
	}
	if !checker.Allowed("any_tool", "src/main.go") {
		t.Fatal("empty checker should allow all paths")
	}
	if !checker.HasTool("anything") {
		t.Fatal("empty checker should report HasTool true")
	}
}

func TestToolScopeCheckerNil(t *testing.T) {
	var c *ToolScopeChecker
	if !c.Allowed("any_tool", "src/main.go") {
		t.Fatal("nil checker should allow all")
	}
	if !c.HasTool("anything") {
		t.Fatal("nil checker should report HasTool true")
	}
}

func TestToolScopeCheckerToolNotAllowed(t *testing.T) {
	checker := NewToolScopeChecker([]ToolPermission{
		{Name: "filesystem_read_text"},
	})
	if checker.Allowed("filesystem_delete_file", "") {
		t.Fatal("should reject disallowed tool")
	}
	if checker.HasTool("filesystem_delete_file") {
		t.Fatal("HasTool should be false for disallowed tool")
	}
}

func TestToolScopeCheckerAllowedTool(t *testing.T) {
	checker := NewToolScopeChecker([]ToolPermission{
		{Name: "filesystem_read_text"},
	})
	if !checker.Allowed("filesystem_read_text", "") {
		t.Fatal("should allow listed tool")
	}
	if !checker.HasTool("filesystem_read_text") {
		t.Fatal("HasTool should be true for allowed tool")
	}
}

func TestToolScopeCheckerPathConstraint(t *testing.T) {
	checker := NewToolScopeChecker([]ToolPermission{
		{Name: "filesystem_read_text", Paths: []string{"src/**"}},
	})

	// Allowed tool, allowed path.
	if !checker.Allowed("filesystem_read_text", "src/main.go") {
		t.Fatal("should allow tool with matching path")
	}

	// Allowed tool, disallowed path.
	if checker.Allowed("filesystem_read_text", "secrets/key.pem") {
		t.Fatal("should reject allowed tool on disallowed path")
	}

	// Allowed tool, no path — always passes.
	if !checker.Allowed("filesystem_read_text", "") {
		t.Fatal("empty path should pass when tool is allowed")
	}
}

func TestToolScopeCheckerMultipleTools(t *testing.T) {
	checker := NewToolScopeChecker([]ToolPermission{
		{Name: "filesystem_read_text"},
		{Name: "filesystem_search_text"},
	})

	for _, name := range []string{"filesystem_read_text", "filesystem_search_text"} {
		if !checker.Allowed(name, "") {
			t.Errorf("should allow %s", name)
		}
	}
	if checker.Allowed("filesystem_delete_file", "") {
		t.Fatal("should reject unlisted tool")
	}
}
