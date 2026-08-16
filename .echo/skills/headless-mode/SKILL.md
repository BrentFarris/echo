---
name: headless-mode
description: 'Headless mode implementation: -headless flag runs Echo without the Wails window, serving the web UI via internal/webserver with runtime-only forced web access and nil s.ctx safety.'
triggers:
    - headless
---

# Headless mode (web-only Echo, no desktop window)

## How it works
- `main.go` branches on `-headless` via `hasHeadlessFlag(os.Args[1:])` before any Wails code runs; the desktop `wails.Run` path is otherwise untouched. All headless logic lives in `headless.go` (package main).
- Flow: `runHeadless()` → parse flags (`-port`, `-bind`) → `runHeadlessWithInterrupt(ctx, port, bind, newSystem)`. The latter creates its own `SystemService` + `webserver.New(system, assets)` and calls `services.SetWebAccessController`.
- Web access is force-enabled **runtime-only** via `system.ApplyWebAccessSettingsRuntime(settings)` (in-memory; nothing persisted to state.json). Before shutdown it restores the saved settings in memory so a later state write can't leak the change.
- Headless never calls `services.SetSystemServiceContext`, so `s.ctx` stays nil. This is safe: every service event emitter does `emitRuntimeEvent(...)` (feeds SSE) + guarded `if s.ctx != nil { runtime.EventsEmit(...) }`; dialog/LSP/rebuild code paths check `s.ctx == nil` and degrade. Verified across all 38 `s.ctx` usages in internal/services.
- Prints tokenized LAN URLs from `status.LANURLs` (e.g. `http://<ip>:3740/#token=...`) so a remote browser can open directly.

## Pitfalls
- **Flag parsing**: `runHeadless()` parses `os.Args[1:]` which *includes* `-headless`; the FlagSet must register `-headless` as a no-op bool or it exits with "flag provided but not defined". The e2e test calls `runHeadlessWithInterrupt` directly and does NOT catch this — smoke-test the real binary (`go build -o x.exe .; ./x.exe -headless -port N`) to verify.
- **Token auth for RPC/SSE** (see `requestToken` in internal/webserver/server.go): accepts `Authorization: Bearer <t>`, `X-Echo-Access-Token: <t>`, or `?access_token=<t>` query param. The frontend stores the token from `#token=` URL hash in localStorage and sends it as a header.
- Embedded assets: headless serves the same `//go:embed all:frontend/dist` — rebuild the frontend (`cd frontend; npm run build`) before testing UI changes in headless mode.

## Tests
- `headless_test.go`: `TestPrepareHeadlessWebAccessSettings` (settings prep table test) + `TestRunHeadlessWithInterruptEndToEnd` (boots real server on ephemeral port via injected controller, verifies token-auth RPC 200 / no-token 401 / graceful shutdown / no state.json leak).
- Pre-existing failures unrelated to headless (fail on clean HEAD too): `TestDelveDAPBreakpointEvaluateAndStop` (needs dlv) and `TestDevelopmentLoggingIsTransientAndTruncatesOnEnable` in internal/services.

## Environment notes
- go.mod/go.sum were missing Wails v2.11.0 indirect entries (build failed with "missing go.sum entry"); fixed with `go mod tidy`.
- Known limitation: self-signed TLS cert only covers `localhost`, so remote HTTPS shows a cert warning; plain HTTP is the intended LAN flow.
