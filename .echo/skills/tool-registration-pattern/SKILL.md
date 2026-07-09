---
name: tool-registration-pattern
description: 'How to register new agent tools in Echo: provider interface, tool file, registry wiring, adapter pattern for mismatched signatures, and mutation classification.'
triggers:
    - new tool
    - tool registration
    - agent tool
    - ExecutionContext
    - provider interface
    - mutatingToolNames
    - LLMSchema
    - adapter pattern
    - kanban tools
    - task tools
---

## Tool Registration Pattern

Every agent tool in Echo follows a 5-step pattern:

### 1. Provider interface in `internal/tools/types.go`

Define an interface that decouples the tool from SystemService:

```go
type MyProvider interface {
    DoThing(ctx context.Context, request MyRequest) (MyResponse, error)
}
```

Add a field of this interface type to the `ExecutionContext` struct.

### 2. Tool file in `internal/tools/xxx.go`

Create a file with an `init()` that calls `Register(ToolFunc{...})`:

```go
func init() {
    Register(ToolFunc{
        Meta: Metadata{
            Name:        "my_tool",
            Description: "What it does.",
            Parameters: Schema{...}, // JSON Schema
        },
        Run: myToolHandler,
    })
}
```

The handler checks for nil provider, decodes arguments via `DecodeToolArguments`, and calls the provider:

```go
func myToolHandler(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
    if err := ctx.context().Err(); err != nil { return nil, err }
    var request MyRequest
    if err := DecodeToolArguments(arguments, &request); err != nil {
        return nil, SafeError{Code: "invalid_arguments", Message: "..."}
    }
    if ctx.MyProvider == nil {
        return nil, SafeError{Code: "unavailable", Message: "..."}
    }
    return ctx.MyProvider.DoThing(ctx.context(), request)
}
```

### 3. Mutating tool classification in `internal/tools/registry.go`

If the tool changes project files or system state, add it to `mutatingToolNames`. Read-only tools go in `readOnlyToolNames`. Tools not in either map are still callable but won't appear in filtered schemas.

### 4. Wire provider in `internal/services/file_changes.go`

In the `tools.ExecutionContext{}` construction inside `executeTrackedToolCall` (~line 150), add the provider field:

```go
MyProvider: s,
```

The `s` is `*SystemService`. It must satisfy the provider interface.

### 5. Adapter pattern (when signatures differ)

When existing SystemService methods don't match the tool interface (e.g., return `services.KanbanBoard` instead of `tools.KanbanBoard`, or lack `context.Context`), use a thin adapter struct rather than changing SystemService method signatures:

```go
// internal/services/kanban_adapter.go
type kanbanManagerAdapter struct {
    service *SystemService
}

func (a *kanbanManagerAdapter) MoveKanbanCard(ctx context.Context, workspaceID string, cardID string, lane string) (tools.KanbanBoard, error) {
    board, err := a.service.MoveKanbanCard(workspaceID, cardID, lane)
    return convertKanbanBoard(board), err
}
```

Wire the adapter in `file_changes.go`:

```go
KanbanManager: &kanbanManagerAdapter{service: s},
```

This avoids polluting SystemService with context-aware signatures that the UI doesn't need.

### Frontend synchronization

After adding tools, update `availableToolNames` in `frontend/src/app/settings/index.ts` (alphabetical order) so they appear in the per-tool permission selector for agent modes.

### Verification

- Run `go test ./...` — fake providers in tests must implement all interface methods
- Tool appears in `LLMSchema()` output
- Run `cd frontend; npm run build` to confirm TypeScript compiles
