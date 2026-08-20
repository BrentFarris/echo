#!/bin/sh

set -eu

platform=${1:-}
if [ "$#" -gt 0 ]; then
    shift
fi

case "$platform" in
    linux)
        go_os=linux
        node_os=linux
        ;;
    macos)
        go_os=darwin
        node_os=darwin
        ;;
    *)
        printf 'ERROR: expected platform "linux" or "macos".\n' >&2
        exit 1
        ;;
esac

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
tools_root="$repository_root/.echo-tools"
go_root="$tools_root/go"
node_root="$tools_root/node"
npm_cache="$tools_root/npm-cache"
go_cache="$tools_root/go-cache"
go_module_cache="$tools_root/go-mod-cache"
go_temp="$tools_root/go-tmp"
build_root="$repository_root/build/bin"
echo_binary="$build_root/echo"
install_temp=

cleanup() {
    if [ -n "$install_temp" ] && [ -d "$install_temp" ]; then
        rm -rf "$install_temp"
    fi
}
trap cleanup EXIT HUP INT TERM

step() {
    printf '\n==> %s\n' "$1"
}

fail() {
    printf '\nERROR: %s\n' "$1" >&2
    exit 1
}

version_at_least() {
    version_value=$(printf '%s' "$1" | sed 's/^[^0-9]*//')
    version_major=${version_value%%.*}
    version_remainder=${version_value#*.}
    version_minor=${version_remainder%%.*}

    case "$version_major" in
        ''|*[!0-9]*) return 1 ;;
    esac
    case "$version_minor" in
        ''|*[!0-9]*) return 1 ;;
    esac

    [ "$version_major" -gt "$2" ] || { [ "$version_major" -eq "$2" ] && [ "$version_minor" -ge "$3" ]; }
}

go_version() {
    command -v go >/dev/null 2>&1 || return 1
    go version 2>/dev/null | sed -n 's/.* go\([0-9][^ ]*\) .*/\1/p'
}

node_version() {
    command -v node >/dev/null 2>&1 || return 1
    node --version 2>/dev/null | sed 's/^v//'
}

have_required_go() {
    detected_go_version=$(go_version) || return 1
    version_at_least "$detected_go_version" 1 26
}

have_required_node() {
    detected_node_version=$(node_version) || return 1
    command -v npm >/dev/null 2>&1 || return 1
    version_at_least "$detected_node_version" 22 0
}

as_root() {
    if [ "$(id -u)" -eq 0 ]; then
        "$@"
    elif command -v sudo >/dev/null 2>&1; then
        sudo "$@"
    else
        fail "A download tool is missing, and sudo is unavailable to install one. Install curl and run this launcher again."
    fi
}

install_linux_base_tools() {
    step "Installing required download and archive utilities"
    if command -v apt-get >/dev/null 2>&1; then
        as_root apt-get update
        as_root apt-get install -y curl ca-certificates tar gzip coreutils
    elif command -v dnf >/dev/null 2>&1; then
        as_root dnf install -y curl ca-certificates tar gzip coreutils
    elif command -v yum >/dev/null 2>&1; then
        as_root yum install -y curl ca-certificates tar gzip coreutils
    elif command -v zypper >/dev/null 2>&1; then
        as_root zypper --non-interactive install curl ca-certificates tar gzip coreutils
    elif command -v pacman >/dev/null 2>&1; then
        as_root pacman -Sy --needed --noconfirm curl ca-certificates tar gzip coreutils
    elif command -v apk >/dev/null 2>&1; then
        as_root apk add curl ca-certificates tar gzip coreutils
    else
        fail "Required download/archive utilities are missing, and no supported package manager was found. Install curl and tar, then run this launcher again."
    fi
}

ensure_base_tools() {
    if { command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1; } && command -v tar >/dev/null 2>&1; then
        return
    fi
    if [ "$platform" = linux ]; then
        install_linux_base_tools
    else
        fail "Required download/archive utilities are missing. Install curl and tar, then run this launcher again."
    fi

    { command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1; } || fail "Neither curl nor wget is available after installation."
    command -v tar >/dev/null 2>&1 || fail "tar is unavailable after installation."
}

download_to() {
    download_url=$1
    download_destination=$2
    if command -v curl >/dev/null 2>&1; then
        curl -fL --retry 3 --connect-timeout 20 "$download_url" -o "$download_destination"
    else
        wget -O "$download_destination" "$download_url"
    fi
}

sha256_file() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    elif command -v openssl >/dev/null 2>&1; then
        openssl dgst -sha256 "$1" | awk '{print $NF}'
    else
        fail "No SHA-256 tool was found (tried sha256sum, shasum, and openssl)."
    fi
}

verify_sha256() {
    checksum_file=$1
    expected_checksum=$(printf '%s' "$2" | tr '[:upper:]' '[:lower:]')
    actual_checksum=$(sha256_file "$checksum_file" | tr '[:upper:]' '[:lower:]')
    [ "$actual_checksum" = "$expected_checksum" ] || fail "SHA-256 verification failed for $(basename "$checksum_file")."
}

install_go() {
    step "Installing a portable copy of Go 1.26 or newer"
    go_version_file="$install_temp/go-version.txt"
    download_to "https://go.dev/VERSION?m=text" "$go_version_file"
    go_tag=$(sed -n '1{s/\r$//;p;}' "$go_version_file")
    case "$go_tag" in
        go[0-9]*) ;;
        *) fail "go.dev returned an unexpected current version: $go_tag" ;;
    esac
    version_at_least "${go_tag#go}" 1 26 || fail "The current Go release (${go_tag#go}) is older than Echo's required Go 1.26."

    go_archive="$go_tag.$go_os-$go_arch.tar.gz"
    go_archive_path="$install_temp/$go_archive"
    go_checksum_path="$install_temp/$go_archive.sha256"
    download_to "https://go.dev/dl/$go_archive" "$go_archive_path"
    download_to "https://go.dev/dl/$go_archive.sha256" "$go_checksum_path"
    expected_go_checksum=$(tr -d '[:space:]' < "$go_checksum_path")
    verify_sha256 "$go_archive_path" "$expected_go_checksum"

    go_extract_root="$install_temp/go-extract"
    mkdir -p "$go_extract_root"
    tar -xzf "$go_archive_path" -C "$go_extract_root"
    [ -x "$go_extract_root/go/bin/go" ] || fail "The downloaded Go archive did not contain bin/go."

    mkdir -p "$tools_root"
    rm -rf "$go_root"
    mv "$go_extract_root/go" "$go_root"
}

install_node() {
    step "Installing a portable copy of Node.js 22 with npm"
    node_index_path="$install_temp/node-index.json"
    download_to "https://nodejs.org/dist/index.json" "$node_index_path"
    node_release=$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\(v22\.[0-9.]*\)".*/\1/p' "$node_index_path" | sed -n '1p')
    [ -n "$node_release" ] || fail "nodejs.org did not list a Node.js 22 release."

    node_archive="node-$node_release-$node_os-$node_arch.tar.gz"
    node_archive_path="$install_temp/$node_archive"
    node_checksums_path="$install_temp/SHASUMS256.txt"
    node_base_url="https://nodejs.org/dist/$node_release"
    download_to "$node_base_url/$node_archive" "$node_archive_path"
    download_to "$node_base_url/SHASUMS256.txt" "$node_checksums_path"
    expected_node_checksum=$(awk -v archive="$node_archive" '$2 == archive {print $1; exit}' "$node_checksums_path")
    [ -n "$expected_node_checksum" ] || fail "Could not find $node_archive in Node.js's checksum file."
    verify_sha256 "$node_archive_path" "$expected_node_checksum"

    node_extract_root="$install_temp/node-extract"
    mkdir -p "$node_extract_root"
    tar -xzf "$node_archive_path" -C "$node_extract_root"
    extracted_node="$node_extract_root/node-$node_release-$node_os-$node_arch"
    [ -x "$extracted_node/bin/node" ] || fail "The downloaded Node.js archive did not contain bin/node."

    mkdir -p "$tools_root"
    rm -rf "$node_root"
    mv "$extracted_node" "$node_root"
}

case "$(uname -m)" in
    x86_64|amd64)
        go_arch=amd64
        node_arch=x64
        ;;
    arm64|aarch64)
        go_arch=arm64
        node_arch=arm64
        ;;
    *)
        fail "Unsupported CPU architecture: $(uname -m). Echo's launcher supports x86-64 and ARM64."
        ;;
esac

ensure_base_tools
install_temp=$(mktemp -d "${TMPDIR:-/tmp}/echo-installer.XXXXXX")

# Prefer portable tools previously installed by this launcher, then fall back to PATH.
PATH="$go_root/bin:$node_root/bin:$PATH"
mkdir -p "$npm_cache" "$go_cache" "$go_module_cache" "$go_temp"
NPM_CONFIG_CACHE="$npm_cache"
GOCACHE="$go_cache"
GOMODCACHE="$go_module_cache"
GOTMPDIR="$go_temp"
export PATH NPM_CONFIG_CACHE GOCACHE GOMODCACHE GOTMPDIR

if ! have_required_go; then
    install_go
fi
have_required_go || fail "Go 1.26 or newer is unavailable after installation."
printf 'Using Go %s\n' "$detected_go_version"

if ! have_required_node; then
    install_node
fi
have_required_node || fail "Node.js 22 or newer with npm is unavailable after installation."
printf 'Using Node.js %s\n' "$detected_node_version"

step "Installing frontend packages"
(
    cd "$repository_root/web"
    npm ci
    step "Building the Echo web application"
    npm run build
)

step "Building the Echo server"
mkdir -p "$build_root"
(
    cd "$repository_root"
    go build -trimpath -o "$echo_binary" .
)

# Do not leave the download workspace behind while the long-running server owns
# this process via exec.
cleanup
install_temp=

if [ "${ECHO_INSTALL_ONLY:-}" = 1 ]; then
    step "Echo is ready at $echo_binary"
    exit 0
fi

step "Starting Echo at http://localhost:3740"
printf 'Leave this window open while you use Echo. Press Ctrl+C to stop the server.\n'
exec "$echo_binary" "$@"
