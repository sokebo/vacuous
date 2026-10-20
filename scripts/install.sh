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

REPO="sokebo/mavis-masterpiece"
BINARY="vacuous"

info() { printf '\033[0;36m==>\033[0m %s\n' "$1"; }
warn() { printf '\033[0;33m警告:\033[0m %s\n' "$1" >&2; }
die()  { printf '\033[0;31m错误:\033[0m %s\n' "$1" >&2; exit 1; }

need() {
  command -v "$1" >/dev/null 2>&1 || die "找不到 $1，请先安装它"
}

detect_platform() {
  local os arch
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  case "$os" in
    darwin) os="darwin" ;;
    linux)  os="linux" ;;
    *) die "不支持的操作系统: $os" ;;
  esac

  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64)  arch="x86_64" ;;
    arm64|aarch64) arch="arm64" ;;
    *) die "不支持的架构: $arch" ;;
  esac

  printf '%s_%s' "$os" "$arch"
}

install_via_go() {
  need go
  info "用 go install 安装 $REPO/cmd/$BINARY ..."
  go install "github.com/$REPO/cmd/$BINARY@latest"

  local gobin
  gobin="$(go env GOBIN)"
  if [ -z "$gobin" ]; then
    gobin="$(go env GOPATH)/bin"
  fi

  if ! command -v "$BINARY" >/dev/null 2>&1; then
    warn "装好了，但 $gobin 不在 PATH 里。加这一行到你的 shell 配置："
    warn "    export PATH=\"\$PATH:$gobin\""
  else
    info "装好了: $(command -v "$BINARY")"
  fi
}

install_via_release() {
  need curl
  need tar

  local platform version url tmp
  platform="$(detect_platform)"
  version="${VERSION:-latest}"

  if [ "$version" = "latest" ]; then
    url="https://github.com/$REPO/releases/latest/download/vacuous_${platform}.tar.gz"
  else
    url="https://github.com/$REPO/releases/download/${version}/vacuous_${platform}.tar.gz"
  fi

  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  info "下载 $url ..."
  curl -fsSL "$url" -o "$tmp/vacuous.tar.gz" \
    || die "下载失败。检查网络，或者自己用 go install 装。"

  tar -xzf "$tmp/vacuous.tar.gz" -C "$tmp"

  local dest
  dest="${DESTDIR:-/usr/local/bin}"
  if [ ! -w "$dest" ]; then
    dest="$HOME/.local/bin"
    mkdir -p "$dest"
    warn "没有 /usr/local/bin 的写权限，装到 $dest"
  fi

  install -m 0755 "$tmp/$BINARY" "$dest/$BINARY"
  info "装好了: $dest/$BINARY"
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
    *) die "用法: $0 [go|release|auto]" ;;
  esac

  printf '\n'
  info "试试: cd 你的项目 && $BINARY --mode check"
}

main "$@"
