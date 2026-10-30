# warehouse-audio PC 服务

APK 通过局域网把音频传到 PC，PC 负责转写、说话人分离和分析。

> 默认数据不出局域网。PC 端页面只监听 `127.0.0.1`，不要直接把 18082 暴露到公网。

## 架构

```
APK
  │  POST /api/v1/jobs  (multipart + X-Idempotency-Key)
  ▼
Go 服务 :18082
  ├─ 任务落盘 data/jobs/*.json（原子写、重启恢复）
  ├─ 有界任务池 (workers / queue)
  ├─ 调用 Python worker :18090  (ASR + VAD + CAM++)
  ├─ 调用 LM Studio / 远程 API   (摘要 JSON)
  ├─ 每日日报：定时汇总 → LLM → 等手机拉取
  ├─ 本机时间线 http://127.0.0.1:18082/
  └─ 音频/任务/日报保留清理
```

## 启动

```bash
# 1) ASR worker（首次加载模型，/health 里 ready=true 才算就绪）
RECNONSTOP_PYTHON=/path/to/private/venv/bin/python pc/scripts/run-worker.sh
# CUDA: ASR_DEVICE=cuda ...
# 标点: ASR_PUNC=1 ...

# 2) Go 服务
RECNONSTOP_CONFIG=/path/to/private/config.json pc/scripts/run-server.sh
# 或首次配对时直接开窗口：
# RECNONSTOP_SERVER_BIN=/path/to/private/bin/pc-server \
#   $RECNONSTOP_SERVER_BIN -config /path/to/private/config.json -pair
```

打开 `http://127.0.0.1:18082/` 查看时间线。

### 本地环境/产物必须放仓库外

| 环境变量 | 是否必填 | 作用 |
| --- | --- | --- |
| `RECNONSTOP_CONFIG` | 启动必填 | 真实配置文件 |
| `RECNONSTOP_PYTHON` | worker 必填 | 仓库外 venv 的 python |
| `RECNONSTOP_BIN_DIR` | 构建必填 | `pc/scripts/build.sh` 的输出目录 |
| `RECNONSTOP_SERVER_BIN` | 可选 | 预编译好的 `pc-server` 路径 |

不传这些路径时，脚本会直接报错退出，不会回退到仓库内默认目录。
`run-server.sh` 还会检查配置里的 `storage.dir` / `pairing.devices_file`；指向仓库内也会拒绝启动。

```bash
RECNONSTOP_BIN_DIR=/path/to/private/bin pc/scripts/build.sh
RECNONSTOP_PYTHON=/path/to/private/venv/bin/python pc/scripts/run-worker.sh
RECNONSTOP_SERVER_BIN=/path/to/private/bin/pc-server \
RECNONSTOP_CONFIG=/path/to/private/config.json \
  pc/scripts/run-server.sh
```

## 连接手机

手机端打开「发现电脑」，选中同一 Wi-Fi 下的电脑，电脑页面右下角会弹出
「有手机想连接」，点「允许」即可。不用输配对码，也不用跳去系统蓝牙设置。

允许后手机记住 token，之后自动上传/同步；电脑端「扫描附近手机」可以主动让某台手机立即同步。

反过来也可以：电脑端扫描到未连接的手机，点「连接」，手机 App 里会弹出确认（后台时先收到通知），
确认后完成连接。两边都是在自己界面里点，不用去系统设置。

> 自动发现走 UDP 18083；如果 Windows 防火墙拦了广播，手机端可以「手动输入电脑地址」，这时只需要放行 TCP 18082。

旧的 6 位码方式仍保留（旧版 App 或打不开页面时）：

```bash
pc/scripts/pair.sh
# 或
curl -X POST http://127.0.0.1:18082/api/v1/admin/pairing
```

## API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/health` | 服务/队列/任务统计（无需 auth） |
| POST | `/api/v1/join` | 手机申请连接（同一局域网） |
| GET | `/api/v1/join/{id}` | 查询申请状态，允许后返回 token |
| GET | `/api/v1/commands` | 手机拉取指令：立即同步 + 待推送日报（Bearer token） |
| GET | `/api/v1/report-settings` | 日报开关和生成时间（Bearer token） |
| POST | `/api/v1/report-settings` | 修改日报开关/生成时间（Bearer token） |
| GET | `/api/v1/reports` | 日报历史元数据，用于手机补齐缺失的日报（Bearer token） |
| GET | `/api/v1/reports/latest` | 最新一份日报（Bearer token） |
| GET | `/api/v1/reports/{id}` | 日报正文（Bearer token） |
| POST | `/api/v1/reports/{id}/ack` | 标记这台设备已收到（Bearer token） |
| POST | `/api/v1/pair` | 6 位码换 token（旧方式） |
| POST | `/api/v1/jobs` | 上传音频（Bearer token；支持 `X-Idempotency-Key`） |
| POST | `/api/v1/jobs/merge` | 合并多个已完成分段（Bearer token，只能合并本设备的分段；`mode=transcript\|audio`） |
| GET | `/api/v1/jobs?limit=50&q=` | 任务列表/搜索 |
| GET | `/api/v1/jobs/{id}` | 任务状态 |
| GET | `/api/v1/jobs/{id}/result` | 转写 + 摘要（失败时也保留转写） |
| GET | `/api/v1/jobs/{id}/audio` | 播放音频（支持 Range） |
| DELETE | `/api/v1/jobs/{id}` | 删除任务和音频 |
| POST | `/api/v1/admin/merge` | 合并多个已完成分段（仅本机） |
| POST | `/api/v1/admin/pairing` | 打开配对窗口（仅本机） |
| GET | `/api/v1/admin/join` | 待确认的连接申请（仅本机） |
| POST | `/api/v1/admin/join/{id}/approve\|reject` | 允许 / 拒绝（仅本机） |
| GET | `/api/v1/admin/nearby` | 扫描附近手机（仅本机） |
| POST | `/api/v1/admin/sync` | 让某台手机立即同步（仅本机） |
| POST | `/api/v1/admin/invite` | 电脑主动邀请手机连接（仅本机） |
| GET | `/api/v1/admin/jobs?limit=&q=` | 本机时间线数据（仅本机，无需 token） |
| GET | `/api/v1/admin/reports?limit=` | 日报列表（仅本机） |
| POST | `/api/v1/admin/reports/generate` | 立即生成一份日报（仅本机） |

上传示例：

```bash
curl -H "Authorization: Bearer <token>" \
     -H "X-Idempotency-Key: device:filename:size:mtime" \
     -F "file=@test.aac" \
     -F "device=Pixel" \
     -F "device_id=<id>" \
     -F "recorded_at=1793000000000" \
     -F "segment_name=rec_20261026_120000_000.aac" \
     http://127.0.0.1:18082/api/v1/jobs
```

### 合并整段（跨段说话人对齐）

每段录音是独立任务，`spk_0` 在不同分段里可能是不同人。本机时间线页面勾选多段 → 「合并选中」，或在请求里指定 `mode`：

- `transcript`（默认）：复用各分段已有转写，只做时间偏移 + 整段摘要。不重新识别，保留每段更好的上下文，速度最快；说话人编号仍是各段自己的。
- `audio`：ffmpeg 拼接后整段重新跑 ASR + CAM++，全程说话人编号统一；长对话可能被 VAD 切得更碎，也更慢。

两种模式都会把合并音频存到 `data/merged/`，并记录 `sources` / `source_spans`，页面可以把整段转写映射回原始分段。

```bash
# 默认：转写合并（推荐）
curl -X POST http://127.0.0.1:18082/api/v1/jobs/merge \
  -H "Authorization: Bearer <token>" \
  -H 'Content-Type: application/json' \
  -d '{"job_ids":["j_1","j_2","j_3"],"title":"10-26 下午会议"}'

# 需要跨段统一说话人时
curl -X POST http://127.0.0.1:18082/api/v1/jobs/merge \
  -H "Authorization: Bearer <token>" \
  -H 'Content-Type: application/json' \
  -d '{"job_ids":["j_1","j_2","j_3"],"mode":"audio"}'
```

本机页面用 `/api/v1/admin/merge`（仅 127.0.0.1），只是 UI 的便利入口，不是唯一入口。
带 token 调用时只能合并该设备自己上传的分段，合并别人的任务会返回 403。
页面上的「统一说话人（重新识别）」勾选框对应 `mode=audio`，不勾选就是默认的转写合并。

要求：至少 2 段、都已完成、音频还在；不能合并已经合并过的任务。
默认上限 2 小时 / 100 段，由 `merge.max_minutes`、`merge.max_jobs` 控制。

合并依赖 `ffmpeg` / `ffprobe`。Windows 下如果不在 PATH，把 `merge.ffmpeg`、`merge.ffprobe` 写成绝对路径。

## 配置

仓库只提供 `configs/config.example.json`，真实的 `configs/config.json` 已被 `.gitignore` 排除。

```bash
# 首次使用：复制到仓库外再改
cp pc/configs/config.example.json /path/outside/repo/config.json
# 编辑 LM Studio 地址、模型名
# 并把 storage.dir / pairing.devices_file 改成仓库外绝对路径

RECNONSTOP_CONFIG=/path/outside/repo/config.json pc/scripts/run-server.sh
```

真实配置不要放回仓库内；`configs/config.json` 已被 `.gitignore` 排除，但本地约定是放到仓库外。

字段说明（`configs/config.json`）：

| 字段 | 说明 |
| --- | --- |
| `server.name` | 局域网/时间线里显示的电脑名，留空自动生成（如“电脑 3856”） |
| `server.max_upload_mb` | 单文件上限，默认 512 |
| `storage.audio_keep_days` | 音频保留天数，默认 7；`-1` 不清理 |
| `storage.job_keep_days` | 任务 JSON 保留天数，默认 30；`-1` 不清理 |
| `pipeline.mode` | `local` / `remote` |
| `pipeline.workers` | 并发分析数，CPU 建议 1，GPU 可 2 |
| `pipeline.queue` | 队列长度，满了返回 429 让手机退避 |
| `pipeline.job_timeout_seconds` | 单个任务总超时，默认 1800 |
| `merge.max_minutes` | 单次合并的音频总时长上限，默认 120 |
| `merge.max_jobs` | 单次最多合并段数，默认 100 |
| `merge.ffmpeg` / `merge.ffprobe` | 留空用 PATH 里的 ffmpeg/ffprobe |
| `worker.url` | Python worker 地址，默认 `http://127.0.0.1:18090` |
| `lmstudio.json_mode` | 先尝试 `response_format=json_object`，服务端不支持会自动回退 |
| `daily_report.enabled` | 是否开启每日日报，默认 `true` |
| `daily_report.generate_at` | 生成时间，`HH:MM`，电脑本地时间，默认 `18:00` |
| `daily_report.title` | 日报标题后缀，默认「录音日报」 |
| `daily_report.keep_days` | 日报保留天数，默认 30；`-1` 不清理 |
| `pairing.window_minutes` | 6 位配对码窗口时长（旧方式，新流程不需要） |

## 行为说明

- **幂等**：同一个 `X-Idempotency-Key` 重复上传返回已有 job，不会重复分析。
- **重启恢复**：`running` 任务重新入队（最多重跑 2 次），不会永远卡住。
- **失败保留转写**：ASR 成功、LLM 失败时，`status=failed` 但 `/result` 仍返回 segments 和错误原因。
- **背压**：队列满返回 429；手机端按 30s/60s/.../30min 退避重试。
- **磁盘保留**：音频、任务和日报分别按天数清理，避免 24/7 把 PC 盘写满。
- **本机页面**：只在 `127.0.0.1` 可访问；从手机访问会 403。

### 日报

- 默认每天 `18:00`（电脑本地时间）生成；12 点前生成总结前一天，12 点后生成总结当天 00:00 到生成时刻。
- 手机端「设置 → 电脑分析」改的时间会写入 `storage.dir/report_settings.json`，优先于 `config.json`；这是电脑上的全局设置，所有已连接手机看到的是同一个时间。
- 生成时如果窗口内还有排队/分析中的分段，会先等它们完成（最多 2 小时），避免日报只写一半。
- 生成完成后不会主动连手机；手机每 30 秒拉 `/api/v1/commands`，看到待推送日报后下载、通知、回 ack。
- 手机/电脑离线时保留未推送状态；下次上线会补齐缺失的日报正文，但通知只发最新一份，不会一次弹出十几条历史通知。
- 手机端「日报」页也会按日期列出本地缓存，进入页面时自动向电脑补齐缺失的历史，离线多天也不会漏掉中间几天。
- 电脑在生成时间关机，下次启动会补最近一次（例如 18:00 关机、第二天早上启动，补昨天 18:00 的日报）。
- LLM 不可用时回退为分段摘要，日报仍然生成并推送。
- 手动生成：`curl -X POST http://127.0.0.1:18082/api/v1/admin/reports/generate`。

## 模型

ASR 只使用 **Qwen3-ASR-1.7B**（llama.cpp GGUF）：

| 文件 | 用途 |
| --- | --- |
| `Qwen3-ASR-1.7B-Q8_0.gguf` | 文本解码器 |
| `mmproj-Qwen3-ASR-1.7B-bf16.gguf` | 音频编码器 |

启动 llama.cpp 的 OpenAI 兼容服务：

```bash
llama-server \
  -m /path/to/Qwen3-ASR-1.7B-Q8_0.gguf \
  --mmproj /path/to/mmproj-Qwen3-ASR-1.7B-bf16.gguf \
  -t 8 -c 4096 --host 127.0.0.1 --port 18091
```

再启动 worker：

```bash
QWEN3_ASR_URL=http://127.0.0.1:18091 pc/scripts/run-worker.sh
```

- worker 只用 ffmpeg 解码音频，然后调用 llama-server，不在本地加载模型
- `/health` 的 `ready=true` 表示 worker 能访问 llama-server
- Qwen3-ASR 自带标点，支持中文/粤语/英语等
- 说话人目前统一为 `spk_0`；需要区分说话人要另配 diarization
- 3 分钟以内建议 `-c 4096`；更长音频要加大 context 或先分段

摘要模型只负责摘要分析，不参与转写校对。

## Windows

```bash
# 在 Linux/WSL 里交叉编译
RECNONSTOP_BIN_DIR=/path/to/private/bin pc/scripts/build.sh
# 产物：$RECNONSTOP_BIN_DIR/pc-server.exe
```

Windows 运行 `pc/scripts/run-server.bat` 前必须设置 `RECNONSTOP_CONFIG`，以及 `RECNONSTOP_BIN_DIR` 或 `RECNONSTOP_SERVER_BIN`；否则脚本直接拒绝启动。加设备双击 `pc/scripts/pair.bat`。worker 建议在 WSL 里跑，或单独装 Python 环境。

「合并整段」需要 `ffmpeg` / `ffprobe`；没装的话在 `merge.ffmpeg` / `merge.ffprobe` 里写绝对路径。

## 测试

```bash
cd pc
go test ./...
```

覆盖：任务落盘/幂等/重启恢复/保留清理、配对持久化、JSON 漂移、上传头校验、分析失败保留转写、多段合并与来源偏移。
