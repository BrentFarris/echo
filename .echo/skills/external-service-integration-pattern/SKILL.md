---
name: external-service-integration-pattern
description: How to add a new external service integration (settings, client, tool, frontend UI) following Echo patterns. Includes Jira-specific auth details (Basic auth email:token for Jira Cloud, Bearer fallback, 3-arg constructor) and verified GET endpoint for search/jql.
triggers:
    - new external service
---

## Adding New Tool Integration in Echo

### Pattern for read-only tools with external service configuration

1. **Backend service client** (`internal/<service>/client.go`):
   - `New()` validates URL, strips trailing slash, returns `*Client`
   - Core method (e.g. `Search()`) takes `context.Context`, uses appropriate auth
   - Auto-pagination if needed; cap pages to prevent runaway requests

2. **Types** (`internal/<service>/types.go`):
   - Response types matching the API response shape
   - Environment variable constants declared here (e.g. `jiraEnvAuth = "ATLASSIAN_AUTH_TOKEN"`)
   - ADF→text conversion if the service uses rich document format
   - Output minimization: strip `self`, `avatarUrls`, `accountId`, expand fields to save context tokens

3. **Settings** (`internal/llm/settings.go`):
   - Add config fields to `Settings` struct with JSON tags
   - Normalize (trim whitespace) in `Sanitize()` method
   - Validate non-empty URL in `Validate()` using `validateHTTPURL()`

4. **ExecutionContext** (`internal/tools/types.go`):
   - Add config fields alongside existing similar services (e.g. after `SearxngURL`)

5. **Wire ExecutionContext construction sites**:
   - Search for the last added field name to find all sites that build `ExecutionContext`:
     - `internal/services/chat_research.go` — research agent tool calls
     - `internal/services/file_changes.go` — inline code change context
   - Pass settings fields: `settings.NewField,`

6. **Tool** (`internal/tools/<name>.go`):
   ```go
   func init() { tools.Register(ToolFunc{Meta: Metadata{...}, Run: myHandler}) }
   ```
   - Check context cancellation first: `ctx.context().Err()`
   - Parse args with `DecodeToolArguments(arguments, &args)`
   - Return `SafeError{Code: "not_configured", Message: "..."}` when service not configured
   - Return `SafeError{Code: "invalid_arguments", Message: "..."}` for bad args

7. **Registry** (`internal/tools/registry.go`):
   - Add to `readOnlyToolNames` for plan-mode availability (non-mutating tools)
   - Update test count expectations if there's an exact-count assertion in `registry_test.go`

8. **Frontend settings** (`frontend/src/app/settings/index.ts`):
   - Section already exists in `settingsSections` nav array (don't duplicate)
   - Add `<section>` fields in `renderSettingsOverlay()` HTML template using existing field patterns
   - Every setting the backend needs MUST have a matching frontend input — missing UI fields silently break the feature

9. **Tests**: Use `httptest.NewServer` with mock responses; test pagination, auth failure (401), empty query, cancellation, and unconfigured error

### Jira-specific notes (`jira_read` tool)
- **File location**: `internal/tools/jira_read.go`, client at `internal/jira/`
- **Auth mode: Basic auth (email:token)** for Jira Cloud. Constructor is `jira.New(baseURL, username, apiToken)` — exactly 3 arguments.
  - When `username` (email) is provided, uses `req.SetBasicAuth(username, apiToken)` (Atlassian API token pattern)
  - When `username` is empty, falls back to `Authorization: Bearer {apiToken}` for backward compatibility with env var only
- **Env var fallback**: When `apiToken` argument is empty, falls back to `ATLASSIAN_AUTH_TOKEN` environment variable (matches Jane's pattern). Constant declared in `internal/jira/types.go` as `jiraEnvAuth`.
- **Settings fields**: `JiraHost` (base URL), `JiraUsername` (email/account), and `JiraAPIToken` (API token) on `llm.Settings`. All three are wired through `ExecutionContext.JiraHost`, `ExecutionContext.JiraUsername`, `ExecutionContext.JiraAPIToken`.
- **Frontend inputs**: Three fields in Settings → Jira section: `jiraHost` (URL), `jiraUsername` (email), `jiraApiToken` (password). All three must be present or auth fails.
- **Endpoint**: `GET /rest/api/3/search/jql?jql=...&fields=...&startAt=...&maxResults=...` with query parameters. **NOT POST** — Atlassian Gateway API rejects POST JSON bodies (returns 400).

### Key pitfalls
- **ExecutionContext construction happens in multiple files** — search for the last added field name to find all sites. Missing a site means the tool has no config at runtime.
- **Frontend UI field omission is silent**: If a settings field exists on the Go struct but has no `<input>` in `renderSettingsOverlay()`, the user can't configure it and the feature appears broken. Always verify both sides.
- Tests must mock GET requests that read from `r.URL.Query()`, NOT POST requests that decode JSON body
- Don't hardcode org-specific URLs in Echo source code — all config comes from settings
- Adding a constant referenced by client.go but declared in types.go requires both files to be updated together, or the build fails
