#!/usr/bin/env bash
# Install the vacuous binary.
#
# Two ways to install it, and this script picks the one that fits:
#
#   1. `go install` — the normal path for Go developers. It puts the binary
#      in $GOBIN (or $GOPATH/bin), which is already on your PATH.
#
#   2. A prebuilt release from GitHub — for everyone else. No Go toolchain
#      needed.
#
# Neither path needs anything but a tarball and a standard Unix shell.

set -euo pipefail

REPO="sokebo/vacuous"
BINARY="vacuous"

info() { printf '\033[0;36m==>\033[0m %s\n' "$1"; }
warn() { printf '\033[0;33mwarning:\033[0m %s\n' "$1" >&2; }
die()  { printf '\033[0;31merror:\033[0m %s\n' "$1" >&2; exit 1; }

need() {
  command -v "$1" >/dev/null 2>&1 || die "$1 not found — please install it first"
}

# sha256_of prints the SHA256 of a file.
#
# macOS ships shasum; Linux usually ships sha256sum. Either will do.
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    die "neither sha256sum nor shasum found — cannot verify the download"
  fi
}

detect_platform() {
  local os arch
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  case "$os" in
    darwin) os="darwin" ;;
    linux)  os="linux" ;;
    *) die "unsupported operating system: $os" ;;
  esac

  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64)  arch="x86_64" ;;
    arm64|aarch64) arch="arm64" ;;
    *) die "unsupported architecture: $arch" ;;
  esac

  printf '%s_%s' "$os" "$arch"
}

install_via_go() {
  need go
  info "Installing $REPO/cmd/$BINARY with go install ..."
  go install "github.com/$REPO/cmd/$BINARY@latest"

  local gobin
  gobin="$(go env GOBIN)"
  if [ -z "$gobin" ]; then
    gobin="$(go env GOPATH)/bin"
  fi

  if ! command -v "$BINARY" >/dev/null 2>&1; then
    warn "Installed, but $gobin is not on your PATH. Add this line to your shell config:"
    warn "    export PATH=\"\$PATH:$gobin\""
  else
    info "Installed: $(command -v "$BINARY")"
  fi
}

install_via_release() {
  need curl
  need tar

  local platform version url sums_url tmp
  platform="$(detect_platform)"
  version="${VERSION:-latest}"

  if [ "$version" = "latest" ]; then
    url="https://github.com/$REPO/releases/latest/download/vacuous_${platform}.tar.gz"
    sums_url="https://github.com/$REPO/releases/latest/download/vacuous-SHA256SUMS"
  else
    url="https://github.com/$REPO/releases/download/${version}/vacuous_${platform}.tar.gz"
    sums_url="https://github.com/$REPO/releases/download/${version}/vacuous-SHA256SUMS"
  fi

  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  info "Downloading $url ..."
  curl -fsSL "$url" -o "$tmp/vacuous.tar.gz" \
    || die "download failed. Check your network, or install it yourself with go install."

  # Verify the checksum BEFORE unpacking anything.
  #
  # A tarball that does not match its published checksum was either
  # corrupted in transit or is not what it claims to be, and installing it
  # either way is the wrong move. The release publishes
  # vacuous-SHA256SUMS next to the binaries; if that file cannot be
  # fetched, the install fails rather than proceeding unverified.
  curl -fsSL "$sums_url" -o "$tmp/SHA256SUMS" \
    || die "could not download the release checksums. Install it yourself with go install."

  local expected actual
  expected="$(awk -v f="vacuous_${platform}.tar.gz" '$2 == f {print $1}' "$tmp/SHA256SUMS")"
  [ -n "$expected" ] \
    || die "the release has no checksum for vacuous_${platform}.tar.gz. Install it yourself with go install."

  actual="$(sha256_of "$tmp/vacuous.tar.gz")"
  [ "$expected" = "$actual" ] \
    || die "checksum mismatch for vacuous_${platform}.tar.gz — refusing to install it."
  info "Checksum verified."

  tar -xzf "$tmp/vacuous.tar.gz" -C "$tmp"

  local dest
  dest="${DESTDIR:-/usr/local/bin}"
  if [ ! -w "$dest" ]; then
    dest="$HOME/.local/bin"
    mkdir -p "$dest"
    warn "No write access to /usr/local/bin — installing to $dest instead"
  fi

  install -m 0755 "$tmp/$BINARY" "$dest/$BINARY"
  info "Installed: $dest/$BINARY"
}

main() {
  case "${1:-auto}" in
    go)      install_via_go ;;
    release) install_via_release ;;
    auto)
      if command -v go >/dev/null 2>&1; then
        install_via_go
      else
        install_via_release
      fi
      ;;
    *) die "usage: $0 [go|release|auto]" ;;
  esac

  printf '\n'
  info "Try it: cd into your project && $BINARY --mode check"
}

main "$@"
