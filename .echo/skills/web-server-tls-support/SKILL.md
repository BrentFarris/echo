---
name: web-server-tls-support
description: Self-signed TLS support for Echo's web server to enable Web Speech API on mobile browsers via HTTPS.
triggers:
    - TLS support
    - self-signed certificate
    - web server HTTPS
    - mobile Web Speech API
    - secure context
    - ServeTLS
    - echo-cert.pem
---

## Self-Signed TLS for Web Server

### Overview

Echo's web server supports optional self-signed TLS to provide a secure context required by mobile Chromium browsers for the Web Speech API. Tailscale HTTP URLs (`http://100.x.x.x:port`) are not secure contexts, so speech recognition fails silently on mobile.

### Key Files

- `internal/services/web_access.go` — `WebAccessSettings.EnableTLS`, `WebAccessStatus.EnableTLS`, `GenerateSelfSignedCert()`, `WebAccessConfigDir()`
- `internal/webserver/server.go` — Conditional `ServeTLS`/`Serve` in `ApplyWebAccessSettings`, `https://` scheme in `publicURLs()`
- `frontend/src/app/settings/index.ts` — "Enable HTTPS" checkbox toggle in web access settings
- `frontend/wailsjs/go/models.ts` — Generated TypeScript types with `enableTLS` field

### How It Works

1. User enables "Enable HTTPS" checkbox in Settings → Web Access
2. On first TLS enable, `GenerateSelfSignedCert()` creates ECDSA P-256 self-signed cert/key at `<configDir>/Echo/echo-cert.pem` and `echo-key.pem`
3. Cert is valid for 1 year, covers `localhost` DNS name
4. Server uses `server.ServeTLS(listener, certPath, keyPath)` instead of `server.Serve(listener)`
5. `publicURLs()` returns `https://` URLs when TLS enabled
6. Frontend uses relative URLs (`/api/rpc/...`) so scheme is inherited from page — no code change needed in web.ts

### Cert Persistence

Certs persist to the user config directory (same location as `state.json`). Once generated, they are reused across app restarts. Users must accept the certificate warning once on mobile browsers.

### Frontend Notes

- The settings UI has an "Enable HTTPS (required for mobile voice input)" checkbox
- Changing `enableTLS` triggers immediate save like the `enabled` toggle
- Relative URLs in web.ts mean no scheme-aware code needed — browser inherits from page origin

### Testing

```powershell
go test ./...
cd frontend; npm run build
```

### Pitfalls

- The Wails TypeScript bindings (`frontend/wailsjs/go/models.ts`) must be updated when Go struct fields change. `wails generate` in this version doesn't auto-regenerate — update manually.
- Cert and key files are created on-demand at first TLS enable, not at startup
- Error handling: if cert generation fails, the server sets `LastError` and doesn't start
