#!/bin/sh
# Cross-compile every supported target into dist/ with checksums.
#   sh scripts/build-release.sh v1.0.0
set -eu
ver="${1:?usage: build-release.sh vX.Y.Z}"
commit=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
out=dist
rm -rf "$out"; mkdir -p "$out"
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}; arch=${target#*/}
  bin=rchrome; [ "$os" = windows ] && bin=rchrome.exe
  echo "building $os/$arch" >&2
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -X main.version=$ver -X main.commit=$commit" \
    -o "$out/$bin" ./cmd/rchrome
  if [ "$os" = windows ]; then
    (cd "$out" && zip -q "rchrome_${os}_${arch}.zip" "$bin" && rm "$bin")
  else
    (cd "$out" && tar -czf "rchrome_${os}_${arch}.tar.gz" "$bin" && rm "$bin")
  fi
done
cd "$out"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum *.tar.gz *.zip > checksums.txt
else
  shasum -a 256 *.tar.gz *.zip > checksums.txt
fi
cat checksums.txt
