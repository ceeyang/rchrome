#!/bin/sh
# rchrome installer (macOS / Linux). https://github.com/ceeyang/rchrome
#
#   curl -fsSL https://raw.githubusercontent.com/ceeyang/rchrome/main/scripts/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/ceeyang/rchrome/main/scripts/install.sh | sh -s -- serve
#
# Env: RCHROME_VERSION=v1.2.3 (default latest), RCHROME_INSTALL_DIR=/some/bin
set -eu

REPO="ceeyang/rchrome"
VERSION="${RCHROME_VERSION:-latest}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  darwin|linux) ;;
  *) echo "✗ 不支持的系统 $os（Windows 请用 install.ps1）/ unsupported OS" >&2; exit 1 ;;
esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "✗ 不支持的架构 $arch / unsupported arch" >&2; exit 1 ;;
esac

file="rchrome_${os}_${arch}.tar.gz"
if [ "$VERSION" = "latest" ]; then
  base="https://github.com/$REPO/releases/latest/download"
else
  base="https://github.com/$REPO/releases/download/$VERSION"
fi

fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$2" "$1"
  else
    echo "✗ 需要 curl 或 wget / need curl or wget" >&2; exit 1
  fi
}

tmp=$(mktemp -d 2>/dev/null || mktemp -d -t rchrome)
trap 'rm -rf "$tmp"' EXIT

echo "↓ $base/$file" >&2
fetch "$base/$file" "$tmp/$file"
fetch "$base/checksums.txt" "$tmp/checksums.txt"

want=$(grep " $file\$" "$tmp/checksums.txt" | awk '{print $1}')
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$file" | awk '{print $1}')
else
  got=$(shasum -a 256 "$tmp/$file" | awk '{print $1}')
fi
if [ -z "$want" ] || [ "$want" != "$got" ]; then
  echo "✗ 校验失败 / checksum mismatch" >&2; exit 1
fi
tar -xzf "$tmp/$file" -C "$tmp"

dir="${RCHROME_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    dir=/usr/local/bin
  elif [ -d /opt/homebrew/bin ] && [ -w /opt/homebrew/bin ]; then
    dir=/opt/homebrew/bin
  else
    dir="$HOME/.local/bin"
  fi
fi
mkdir -p "$dir"
# Replace atomically so a running rchrome keeps its old inode.
cp "$tmp/rchrome" "$dir/rchrome.new"
chmod 755 "$dir/rchrome.new"
mv -f "$dir/rchrome.new" "$dir/rchrome"
if [ "$os" = darwin ]; then
  xattr -d com.apple.quarantine "$dir/rchrome" 2>/dev/null || true
fi

ver=$("$dir/rchrome" version 2>/dev/null | awk '{print $1}')
echo "rchrome ${ver:-?}  $os/$arch  → $dir/rchrome   ✓ 校验通过"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "提示：$dir 不在 PATH 里。加到 shell 配置：  export PATH=\"$dir:\$PATH\"" >&2 ;;
esac

if [ $# -gt 0 ]; then
  echo "" >&2
  exec "$dir/rchrome" "$@"
fi
