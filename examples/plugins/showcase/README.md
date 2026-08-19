# Echo Plugin Showcase

This developer-only reference package proves Echo Plugin API v1's full-page UI, host-rendered global/workspace settings, secret boundary, JSON-RPC backend, backend-to-UI event, and model-visible tool.

Build every artifact declared by the manifest before local validation. From this directory on Windows:

```powershell
go test -C backend-src ./...
& .\backend-src\build-all.ps1
```

On Linux or macOS, run `go test ./...` inside `backend-src`, then `bash backend-src/build-all.sh`. The backend is pure Go, so it cross-compiles without platform SDKs. Then ask Echo to validate and stage this directory, review the real host approval card, and enable it for a workspace.

The included workflow cross-compiles every declared target and assembles a source-free package artifact. Echo itself never runs that workflow, build scripts, or any repository lifecycle hook during installation.

The backend intentionally never returns the configured token. Its `secretConfigured` booleans demonstrate that secrets arrive only at the invocation boundary, while the iframe receives only non-secret configuration.
