#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
cd "$SCRIPT_DIR/.."
if [ -z "${RECNONSTOP_CONFIG:-}" ]; then
  echo "错误：必须设置 RECNONSTOP_CONFIG（仓库外配置文件）。拒绝启动，避免把数据写进仓库。" >&2
  exit 1
fi
CONFIG="$RECNONSTOP_CONFIG"
CONFIG_TARGET="$(realpath -m "$CONFIG")"
case "$CONFIG_TARGET/" in
  "$REPO_DIR"/*)
    echo "错误：RECNONSTOP_CONFIG 指向仓库内路径（$CONFIG_TARGET）。拒绝启动。" >&2
    exit 1
    ;;
esac
if [ ! -f "$CONFIG" ]; then
  echo "配置文件不存在：$CONFIG" >&2
  exit 1
fi
python3 - "$CONFIG" "$REPO_DIR" <<'PY'
import json, os, sys

cfg_path, repo = sys.argv[1], os.path.realpath(sys.argv[2])
with open(cfg_path, encoding="utf-8") as f:
    cfg = json.load(f)

def check(label, value):
    if not value:
        return
    p = value if os.path.isabs(value) else os.path.join(os.getcwd(), value)
    p = os.path.realpath(p)
    if p == repo or p.startswith(repo + os.sep):
        print(f"错误：{label} 指向仓库内路径（{p}）。拒绝启动。", file=sys.stderr)
        sys.exit(1)

check("storage.dir", cfg.get("storage", {}).get("dir") or "./data")
check("pairing.devices_file", cfg.get("pairing", {}).get("devices_file") or "")
PY
if [ -n "${RECNONSTOP_SERVER_BIN:-}" ]; then
  if [ ! -x "$RECNONSTOP_SERVER_BIN" ]; then
    echo "RECNONSTOP_SERVER_BIN 不存在或不可执行：$RECNONSTOP_SERVER_BIN" >&2
    exit 1
  fi
  exec "$RECNONSTOP_SERVER_BIN" -config "$CONFIG"
fi
exec go run ./cmd/pc-server -config "$CONFIG"
