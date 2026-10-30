#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
REPO_DIR="$(cd .. && pwd)"
OUT_DIR="${RECNONSTOP_BIN_DIR:-}"
if [ -z "$OUT_DIR" ]; then
  echo "错误：必须设置 RECNONSTOP_BIN_DIR（仓库外路径）。拒绝构建，避免把二进制写进仓库。" >&2
  exit 1
fi
TARGET="$(realpath -m "$OUT_DIR")"
case "$TARGET/" in
  "$REPO_DIR"/*)
    echo "错误：RECNONSTOP_BIN_DIR 指向仓库内路径（$TARGET）。拒绝构建。" >&2
    exit 1
    ;;
esac
mkdir -p "$OUT_DIR"
CGO_ENABLED=0 go build -trimpath -o "$OUT_DIR/pc-server" ./cmd/pc-server
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -o "$OUT_DIR/pc-server.exe" ./cmd/pc-server
ls -lh "$OUT_DIR/pc-server" "$OUT_DIR/pc-server.exe"
