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
| POST | `/api/v1/admin/reports/generate` | 立即生成一份日报，可指定历史日期（仅本机） |

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
| `pipeline.workers` | 并发录音任务数，8GB 显存目标建议 1 |
| `pipeline.inference_concurrency` | 本进程 ASR 与文本服务共享的推理并发，默认 1；每次响应读取结束后释放，健康查询不受阻 |
| `pipeline.queue` | 队列长度，满了返回 429 让手机退避 |
| `pipeline.job_timeout_seconds` | 单个任务总超时，默认 1800 |
| `merge.max_minutes` | 单次合并的音频总时长上限，默认 120 |
| `merge.max_jobs` | 单次最多合并段数，默认 100 |
| `merge.ffmpeg` / `merge.ffprobe` | 留空用 PATH 里的 ffmpeg/ffprobe |
| `worker.url` | Python worker 地址，默认 `http://127.0.0.1:18090` |
| `lmstudio.model` | 文本服务实际提供的模型 ID；示例 `qwen3.5-4b-q4k` 是服务别名，须与 `/v1/models` 一致，不会自动下载或切换模型 |
| `lmstudio.max_tokens` | 单次输出上限，示例 4096；开启思考时也包含思考 token，须为输入与输出共同预留上下文 |
| `lmstudio.reasoning_effort` | 示例 `none`；仅在后端及模型支持时关闭思考，不能仅凭参数值认定已关闭 |
| `lmstudio.json_mode` | 分段分析和长材料日报提炼先尝试严格 `json_schema`；不支持时在既有两次尝试内回退提示词 JSON，并保留本地校验 |
| `daily_report.enabled` | 是否开启每日日报，默认 `true` |
| `daily_report.generate_at` | 生成时间，`HH:MM`，电脑本地时间，默认 `18:00` |
| `daily_report.title` | 日报标题后缀，默认「录音日报」 |
| `daily_report.keep_days` | 日报保留天数，默认 30；`-1` 不清理 |
| `pairing.window_minutes` | 6 位配对码窗口时长（旧方式，新流程不需要） |

## 行为说明

- **幂等**：同一个 `X-Idempotency-Key` 重复上传返回已有 job，不会重复分析。
- **重启恢复**：`running` 任务重新入队（最多重跑 2 次），不会永远卡住。
- **失败保留转写**：ASR 成功、LLM 失败时，任务仍以 `status=done` 保存转写；摘要留空，分析失败原因写入服务日志。
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
- 自动与手动日报的总生成时限均为 1 小时；每次模型请求仍使用独立的 `lmstudio.timeout_seconds`（默认 300 秒），请求取消或更早的调用方截止时间仍生效。
- 日报归并某层未缩短时，对该层原始有界材料块最多定向重试一轮；重试不使用膨胀输出作为证据，仍不收敛则明确失败，不静默截断材料。
- 长材料分块提炼为每个程序编号的原发言单元填写分类，不能省略中间或末尾编号；只压缩无实质信息的背景，具体信息、转述、观点和方法均保留。严格校验完整编号集合、类别、字段和最多 320 字的背景概括，按原顺序恢复保留项的原文与已有时间、来源。兼容旧 `actual_lines` 响应，但禁止与新格式混用。schema 不受支持时只回退提示词 JSON，总尝试最多两次；非法响应明确失败。分类不证明内容真实性。
- 模型请求失败、归并仍不收敛、输出超限或总时限耗尽时，日报正文与摘要提示汇总未完成，保留分段材料并继续生成和推送；不把汇总未完成一概判断为模型离线。
- 有原始转写的录音只向日报模型提供完整原文，不附旧摘要及旧待办、风险候选；缺少原文时才携带明确标注的自动分析参考。回退正文和详情同样优先原文、排除旧推断，仅在缺少原文时展示需核实的自动分析候选；原记录保持不变。
- 报告读取在锁内取得完整快照，详情数据复制后返回，避免并发更新竞态或共享详情缓冲区相互影响。
- 日报提示词要求只压缩无具体事实的背景和重复措辞，保留不同事实、决定、数字、时间、责任人、期限以及否定、条件和风险依据；实际内容仍需结合原文核对。
- 段数与时长由任务数据计算；仍有录音排队时，正文和摘要明确显示尚未纳入的数量。来源未确认的背景、引用或抒情内容不推断为录音者本人的经历或心理风险。
- 分段分析把发言来源 `speaker` 与任务负责人 `owner` 分开；来源仅接受原转写标识，不能把正文提到的人名当成新增说话人。正常前提和禁止事项保存在 `constraints`，与有依据的风险分开，合并时保留各段提取结果。
- 日期校验同时核对年份和完整年月日，不以报告年份补全其他事项的月日；声音提示只关联重叠的原发言时段，不能作为新增事实的依据。
- 无法确认身份的说话人编号只在当前录音内有效，不按相同编号跨段合并人物。时间线和待办保留对应录音时间，便于回看依据。
- 手动生成：`curl -X POST http://127.0.0.1:18082/api/v1/admin/reports/generate`。
- 重新生成指定日期：`curl -X POST http://127.0.0.1:18082/api/v1/admin/reports/generate -H 'Content-Type: application/json' -d '{"date":"2001-02-03"}'`（日期为合成示例）。按服务器本地时区选取该日 00:00 至次日 00:00 之前的录音；指定今天则截止请求时刻。每次新建一份日报，保留原版本，不修改定时设置。未来日期、非法日期、显式空日期或非法 JSON 返回 `400`；请求体最多 1024 字节。空请求体或 `{}` 沿用原手动生成区间。接口仍仅允许本机访问，生成与排队等待遵守请求取消及原有总时限。

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
- `/health` 的 `ready=true` 表示 worker 启动预热时已成功访问 llama-server；不代表持续探测其可达性
- Qwen3-ASR 自带标点，支持中文/粤语/英语等
- 说话人目前统一为 `spk_0`；需要区分说话人要另配 diarization
- worker 将长音频分成最多 30 秒的块，优先在低音量位置切割；模型输出达到长度限制时自动缩短重试，避免静默丢失后半段
- 返回的时间戳对应识别音频块，并非逐字或逐句精确对齐；说话人分离仍需单独配置
- 精确数字静音不会送入识别模型；微弱语音仍保留识别

摘要模型只负责摘要分析，不参与转写校对。

### 8 GB 显存目标配置

以 RTX 3060、8 GB 显存为部署预算，不要求开发或测试机器使用相同显卡。ASR 保留上述 Q8 解码器与 bf16 音频编码器；文本分析可评估 Qwen3.5-4B Q4_K_M，既有 Qwen3-4B-Instruct-2507 Q4_K_M 也可作为对照。Qwen3.5 的 GGUF 如采用 Unsloth 等发布者版本，应记录发布者、固定 revision 和文件 SHA256；第三方量化不等于 Qwen 官方发布的 GGUF。纯文本分析不需要视觉 `mmproj`。

- ASR 上下文 4096，文本上下文 8192、单次输出上限 4096；实际输入 token 与输出预算合计须能放入窗口。服务分别限制 `--parallel 1`，PC pipeline 保持一个 worker。
- `pipeline.inference_concurrency` 保持 1，让新录音 ASR 与独立运行的日报逐个发起推理请求，避免两个服务同时计算；这不会卸载常驻模型，也不限制绕过 PC 的其他客户端。
- 文本服务使用 `--batch-size 256 --ubatch-size 128 --flash-attn on --cache-type-k q8_0 --cache-type-v q8_0`，须确认安装的 llama.cpp 版本与后端支持。
- 首轮可使用非思考模式对比；支持这些参数的 llama.cpp 可设置 `--jinja --reasoning-format auto --reasoning-budget 0 --chat-template-kwargs '{"enable_thinking":false}'`，并检查返回内容。请求级参数可能覆盖启动参数，不能只检查启动命令。
- 长转写和日报依靠分块、逐层汇总控制上下文；不为放入窗口而丢弃最后一段待办。
- 可选音频事件分析优先放在 CPU，给转写和摘要保留显存。

这些是候选配置，仍需按固定录音参考验证事实覆盖、责任归属、否定与条件。当前尚无 RTX 3060 8 GB 的完整显存峰值和吞吐实测；Linux CPU 或其他硬件上的运行结果不能替代目标显卡验收。部署时须验证两个模型同时驻留、最长输入推理以及系统占用下的显存峰值，不能只把权重文件大小相加。

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

worker 的合成音频回归测试（不加载模型、不需要真实录音）：

```bash
PYTHONDONTWRITEBYTECODE=1 "$RECNONSTOP_PYTHON" -m unittest discover -s pc/worker -p 'test_*.py'
```

覆盖：任务落盘/幂等/重启恢复/保留清理、配对持久化、JSON 漂移、上传头校验、分析失败保留转写、多段合并与来源偏移。
