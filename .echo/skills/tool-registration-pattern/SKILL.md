---
name: tool-registration-pattern
description: 'How to register new agent tools in Echo: provider interface, tool file, registry wiring, and mutation classification.'
triggers:
    - new tool
    - tool registration
    - agent tool
    - ExecutionContext
    - provider interface
    - mutatingToolNames
    - LLMSchema
---

## Tool Registration Pattern

Every agent tool in Echo follows a 5-step pattern:

### 1. Provider interface in `internal/tools/types.go`

Define an interface that decouples the tool from SystemService:\n\n```go\ntype MyProvider interface {\n    DoThing(ctx context.Context, request MyRequest) (MyResponse, error)\n}\n```\n\nAdd a field of this interface type to the `ExecutionContext` struct.\n\n### 2. Tool file in `internal/tools/xxx.go`\n\nCreate a file with an `init()` that calls `Register(ToolFunc{...})`:\n\n```go\nfunc init() {\n    Register(ToolFunc{\n        Meta: Metadata{\n            Name:        \"my_tool\",\n            Description: \"What it does.\",\n            Parameters: Schema{...}, // JSON Schema\n        },\n        Run: myToolHandler,\n    })\n}\n```\n\nThe handler checks for nil provider, decodes arguments via `DecodeToolArguments`, and calls the provider:\n\n```go\nfunc myToolHandler(ctx ExecutionContext, arguments json.RawMessage) (any, error) {\n    if err := ctx.context().Err(); err != nil { return nil, err }\n    var request MyRequest\n    if err := DecodeToolArguments(arguments, &request); err != nil {\n        return nil, SafeError{Code: \"invalid_arguments\", Message: \"...\"}\n    }\n    if ctx.MyProvider == nil {\n        return nil, SafeError{Code: \"unavailable\", Message: \"...\"}\n    }\n    return ctx.MyProvider.DoThing(ctx.context(), request)\n}\n```\n\n### 3. Mutating tool classification in `internal/tools/registry.go`

If the tool changes project files or system state, add it to `mutatingToolNames`. Read-only tools go in `readOnlyToolNames`. Tools not in either map are still callable but won't appear in filtered schemas.\n\n### 4. Wire provider in `internal/services/file_changes.go`\n\nIn the `tools.ExecutionContext{}` construction inside `executeTrackedToolCall` (~line 150), add the provider field:\n\n```go\nMyProvider: s,\n```\n\nThe `s` is `*SystemService`. It must satisfy the provider interface.\n\n### 5. Adapter method on SystemService (if needed)\n\nIf the existing SystemService method signature doesn't match the interface (e.g., returns extra values), add a thin wrapper:\n\n```go\nfunc (s *SystemService) DoThingWithContext(ctx context.Context, request MyRequest) error {\n    _, err := s.ExistingDoThing(request)\n    return err\n}\n```\n\n### Verification\n\n- Add the tool name to `TestDefaultRegistryIncludesFilesystemTools` in `registry_test.go`\n- Run `go test ./...`\n- Tool appears in `LLMSchema()` output
