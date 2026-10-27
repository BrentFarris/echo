#!/bin/sh
# Native Linux/macOS build. No startup service is installed by building Echo.
set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
test_suite=0; native=0; browser=0; release=0; output=echo
while [ "$#" -gt 0 ]; do
  case "$1" in
    --test) test_suite=1;;
    --native-tests) native=1;;
    --browser-tests) browser=1;;
    --release) release=1; test_suite=1; native=1; browser=1;;
    --output) shift; output=${1:?--output requires a path};;
    *) printf 'Unknown build argument: %s\n' "$1" >&2; exit 2;;
  esac
  shift
done
export GOCACHE="$PWD/.cache/go-build"
GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -trimpath -o internal/plugins/guest/builtin.wasm ./internal/plugins/guest
(cd web; npm ci --ignore-scripts --prefer-offline; npm run build; if [ "$test_suite" = 1 ]; then npm test; fi)
if [ "$native" = 1 ]; then export ECHO2_NATIVE_TESTS=1 ECHO2_REQUIRE_TOOLCHAINS=1; fi
if [ "$test_suite" = 1 ] || [ "$native" = 1 ]; then go test ./... -count=1 -timeout=5m; go vet ./...; fi
revision=$(git rev-parse --verify HEAD 2>/dev/null || printf unknown)
source_changes=$(git status --porcelain --untracked-files=normal 2>/dev/null) || source_changes=unknown
if [ -n "$source_changes" ]; then revision="$revision-modified"; fi
go build -trimpath -ldflags "-X github.com/brent/echo/internal/buildinfo.Revision=$revision" -o "$output" .
if [ "$browser" = 1 ]; then
  go build -o mock-model ./cmd/mock-model
  go build -o browser-fixture ./cmd/browser-fixture
  (cd web; npx playwright install chromium; npm run test:e2e)
fi
if [ "$release" = 1 ]; then
  if [ "$output" != echo ]; then printf '%s\n' '--release requires the default output path' >&2; exit 2; fi
  node scripts/notices.mjs
  platform=$(go env GOOS); architecture=$(go env GOARCH)
  archive="echo-$platform-$architecture.tar.gz"
  tar -czf "$archive" echo README.md ARCHITECTURE.md ACCEPTANCE.md LICENSE THIRD-PARTY-NOTICES.txt scripts/startup.sh
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$archive"; else shasum -a 256 "$archive"; fi
fi
