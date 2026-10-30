#!/usr/bin/env bash
# 连续录音 soak 测试：启动 App，按分钟巡检段数/心跳，发现静默停录就失败。
# 用法：./scripts/soak.sh 30        # 跑 30 分钟
set -euo pipefail

PKG="${PKG:-com.recnonstop.app}"
MINUTES="${1:-30}"
DIR="/sdcard/Android/data/${PKG}/files/rec"

if ! adb get-state >/dev/null 2>&1; then
  echo "没有 adb 设备" >&2
  exit 1
fi

echo "== 启动录音 =="
adb shell am start -n "${PKG}/.MainActivity" --es cmd start >/dev/null
sleep 5

last_count=0
stall=0
fail=0
start_ts=$(date +%s)

for i in $(seq 1 "$MINUTES"); do
  sleep 60
  count=$(adb shell "ls ${DIR}/rec_*.aac 2>/dev/null | wc -l" | tr -d '\r')
  health=$(adb shell "run-as ${PKG} cat files/health.json 2>/dev/null" | tr -d '\r' || true)
  last_seg=$(printf '%s' "$health" | python3 -c 'import sys,json
try:
  print(json.load(sys.stdin).get("last_segment_at",0))
except Exception:
  print(0)' 2>/dev/null || echo 0)
  now=$(date +%s)
  age=$(( now - last_seg / 1000 )) 2>/dev/null || age=0
  [ "$last_seg" = "0" ] && age=-1

  echo "[$i/${MINUTES}] segments=${count} last_segment_age=${age}s"

  if [ "$count" -le "$last_count" ]; then
    stall=$((stall + 1))
  else
    stall=0
  fi
  last_count="$count"

  # 3 分钟分段：连续 3 次巡检（3 分钟）没有新段，且心跳超过 8 分钟，判失败。
  if [ "$stall" -ge 3 ] && { [ "$age" -lt 0 ] || [ "$age" -gt 480 ]; }; then
    echo "!! 连续 ${stall} 次没有新段，心跳 age=${age}s，判定静默停录" >&2
    fail=1
    break
  fi

  # 超过 15 分钟的 .part 说明切段卡死
  old_part=$(adb shell "find ${DIR} -name '*.part' -mmin +15 2>/dev/null | wc -l" | tr -d '\r')
  if [ "${old_part:-0}" -gt 0 ]; then
    echo "!! 存在超过 15 分钟的 .part 文件（${old_part} 个），切段可能卡死" >&2
    fail=1
    break
  fi
done

echo "== 结果 =="
adb shell "ls -l ${DIR} | tail -5"
echo "开始时间：$(date -d @${start_ts} '+%F %T')  结束时间：$(date '+%F %T')"

if [ "$fail" -ne 0 ]; then
  echo "== 失败，导出最近日志 =="
  adb logcat -d -s RecNonstop:* RecNonstopUpload:* | tail -80
  exit 1
fi

echo "OK：连续 ${MINUTES} 分钟没有发现静默停录"
