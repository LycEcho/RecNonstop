#!/usr/bin/env bash
set -euo pipefail

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
export JAVA_HOME="${JAVA_HOME:-/usr/lib/jvm/java-17-openjdk-amd64}"
export ANDROID_SDK_ROOT="${ANDROID_SDK_ROOT:-${ANDROID_HOME:-/opt/android-sdk}}"
export ANDROID_HOME="$ANDROID_SDK_ROOT"

require_outside_repo() {
  local name="$1" value="$2" target
  if [ -z "$value" ]; then
    echo "错误：必须设置 $name（仓库外路径）。拒绝构建，避免产物写进仓库。" >&2
    exit 1
  fi
  target="$(realpath -m "$value")"
  case "$target/" in
    "$BASE_DIR"/*)
      echo "错误：$name 指向仓库内路径（$target）。拒绝构建。" >&2
      exit 1
      ;;
  esac
}

require_outside_repo "RECNONSTOP_BUILD_DIR" "${RECNONSTOP_BUILD_DIR:-}"
require_outside_repo "GRADLE_PROJECT_CACHE_DIR" "${GRADLE_PROJECT_CACHE_DIR:-}"

GRADLE_BIN="${GRADLE_BIN:-/opt/gradle/gradle-8.9/bin/gradle}"

cd "$BASE_DIR"
"$GRADLE_BIN" :app:assembleDebug \
  --project-cache-dir "$GRADLE_PROJECT_CACHE_DIR" \
  "-PrecnonstopBuildDir=$RECNONSTOP_BUILD_DIR" "$@"

APK="$RECNONSTOP_BUILD_DIR/app/outputs/apk/debug/app-debug.apk"
echo
echo "APK: $APK"
ls -lh "$APK"
