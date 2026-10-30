#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
cd "$REPO_DIR"
if [ -z "${RECNONSTOP_PYTHON:-}" ]; then
  echo "错误：必须设置 RECNONSTOP_PYTHON（仓库外虚拟环境的 python）。拒绝启动，避免在仓库内使用本地环境。" >&2
  exit 1
fi
PYTHON_BIN="$RECNONSTOP_PYTHON"
VENV_ROOT="$(cd "$(dirname "$PYTHON_BIN")/.." 2>/dev/null && pwd || true)"
if [ -n "$VENV_ROOT" ]; then
  case "$VENV_ROOT/" in
    "$REPO_DIR"/*)
      echo "错误：RECNONSTOP_PYTHON 指向仓库内环境（$VENV_ROOT）。拒绝启动。" >&2
      exit 1
      ;;
  esac
fi
if [ ! -x "$PYTHON_BIN" ]; then
  echo "找不到 Python 环境：$PYTHON_BIN" >&2
  exit 1
fi
exec "$PYTHON_BIN" pc/worker/worker.py
