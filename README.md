# 录不停（RecNonstop）

一个**只用普通 App 权限**实现的长时间连续录音工具。

> 定位：打开就录，锁屏不断，通话自动让路。默认不联网、不上传；开启电脑分析后只走局域网到自己的电脑。

---

## 一、它解决什么问题

手机自带录音有三个硬伤：

1. **锁屏后被系统杀**，录一会儿就断
2. **来电直接中断**，挂了不会自己续上
3. **一录就是一整段**，中途崩溃整个文件全废

这个 App 针对这三点：

| 问题 | 做法 |
| --- | --- |
| 后台被杀 | 前台服务 + 唤醒锁 + 精确闹钟看门狗 |
| 来电中断 | 轮询 `AudioManager` 模式，自动暂停 / 自动续录 |
| 崩溃丢数据 | 分段录音，默认 3 分钟一段 |

---

## 二、能做什么

### 录音

- 前台服务常驻，通知栏显示实时状态
- 息屏、切后台、锁屏都继续录
- 分段录音，每段可设 1 / 3 / 5 / 10 分钟
- 录音质量四档可选（最高 / 高 / 标准 / 省空间），默认省空间（16kHz / 32kbps），改档后下一段自动生效
- 通话（来电、去电、微信语音）自动暂停，挂断自动续录
- 通话空档写入 `gaps.jsonl`，记录起止时间戳
- 按天数自动清理旧文件（可设 1 / 3 / 7 / 30 天或不清理）
- **先写 `.part`，关闭成功后才 rename 成 `.aac`**：不会上传正在写的段，崩溃后还能恢复孤儿段
- **健康心跳**：记录上次成功切段、今日段数/体积/空档、最后一次错误
- **下拉快捷开关**：不用打开 App 就能开始/停止
- **播放 / 分享 / 删除**：点文件列表里的任意一段即可
- **诊断日志**：`logs/rec.log` 轮转，App 内一键导出

### 电脑分析（可选）

- 局域网 UDP 发现：手机扫到电脑，点一下，电脑页面点「允许」即连接（不需要蓝牙配对、不用跳系统设置），token 只存 hash
- 自动上传已关闭的段；Wi-Fi 恢复后自动补传
- 上传带幂等键，PC 重启/重复上传不会产生重复任务
- PC 端 ASR + 说话人分离 + LLM 摘要，分析失败也保留转写
- **每日自动日报**：电脑按设定时间汇总当天录音，生成后自动推送到手机（App 内「日报」页 + 通知）；手机按日期翻看历史，离线多天后重连自动补齐
- PC 端本机时间线：`http://127.0.0.1:18082/`（只监听本机）

电脑分析的模型、输出预算和共享推理并发见 [PC 配置说明](pc/README.md#配置)。4B 量化模型是 8 GB 显存目标的候选方案，具体质量和目标显卡资源占用仍需验证。

### 设置（全部可手动改；分段时长、录音质量在下一段生效）

| 设置项 | 说明 | 默认 |
| --- | --- | --- |
| 打开软件自动录音 | 进入 App 就开始录 | 关 |
| 自定义界面亮度 | 只影响本 App 窗口，1%~100% | 开，1% |
| 后台被杀自动拉起 | 精确闹钟每 60 秒检查一次 | 开 |
| 系统息屏时间 | 跳转系统设置页 | 1 分钟 |
| 分段时长 | 1 / 3 / 5 / 10 分钟 | 3 分钟 |
| 录音质量 | 最高 / 高 / 标准 / 省空间 | 省空间（16kHz / 32kbps，约 14MB/小时） |
| 录音保留 | 不清理 / 1 / 3 / 7 / 30 天 | 7 天 |

### 调试接口（仅 shell 可用）

```bash
adb shell am start -n <包名>/.MainActivity --es cmd start
adb shell am start -n <包名>/.MainActivity --es cmd stop
```

---

## 三、做不到什么（重要）

这部分是**系统限制**，不是实现问题。

| 需求 | 为什么做不到 |
| --- | --- |
| **录到通话双方声音** | 通话音频归系统所有，需要 `CAPTURE_AUDIO_OUTPUT`（系统签名权限），普通 App 永远拿不到 |
| **主动锁屏** | 需要设备管理员或无障碍服务，属于特殊权限，本项目不使用 |
| **修改系统亮度 / 息屏时间** | 需要 `WRITE_SETTINGS`，只能改自己窗口、跳转系统设置引导用户 |
| **后台弹窗** | 需要 `SYSTEM_ALERT_WINDOW` |
| **绕过厂商杀后台** | 只能引导用户加白名单，无法强制 |
| **Android 15 开机自动录音** | 系统禁止 `BOOT_COMPLETED` 启动麦克风类型前台服务 |

### 通话场景的实际表现

```
录音 00:00 - 12:30   ✅
[空档 12:30 - 13:05  通话中，未录制]   ← 双方声音都没有
录音 13:05 - 25:40   ✅
```

通话那段时间是**真空档**，只能记录时间点，补不回来。

---

## 四、权限清单

App 只申请普通权限；网络权限仅用于“可选的电脑分析”，默认关闭上传时不会发出任何请求。

| 权限 | 用途 |
| --- | --- |
| `RECORD_AUDIO` | 录音（运行时申请） |
| `FOREGROUND_SERVICE` | 前台服务 |
| `FOREGROUND_SERVICE_MICROPHONE` | 前台服务声明麦克风类型（Android 14+） |
| `POST_NOTIFICATIONS` | 常驻通知（Android 13+ 运行时申请） |
| `WAKE_LOCK` | 熄屏后保持 CPU |
| `RECEIVE_BOOT_COMPLETED` | 开机自启 |
| `SCHEDULE_EXACT_ALARM` | 看门狗定时检查 |
| `REQUEST_IGNORE_BATTERY_OPTIMIZATIONS` | 引导用户加入电池白名单 |
| `INTERNET` / `ACCESS_NETWORK_STATE` | 仅用于向**局域网内自己的电脑**上传（默认关闭） |

**默认不上传**：`自动上传到电脑分析` 关闭时，App 只写本地文件；开启后也只连你确认过的那台电脑，不经过任何云端。

---

## 五、录音规格

| 项目 | 值 |
| --- | --- |
| 编码 | AAC-LC |
| 容器 | **AAC-ADTS（`.aac`）** |
| 质量档位 | 省空间 16kHz / 32kbps（默认）<br>标准 32kHz / 64kbps<br>高 44.1kHz / 96kbps<br>最高 48kHz / 128kbps |
| 声道 | 单声道 |
| 体积 | 约 14.4 / 28.8 / 43.2 / 57.6 MB/小时（对应上面四档） |
| 默认分段 | 3 分钟（省空间档约 0.7 MB） |
| 切段空档 | 100~150 毫秒 |

### 为什么用 ADTS 而不是 MP4

MP4 的 `moov` 头在 `stop()` 时才写入。进程被杀、更新安装、系统强停，都会让**正在录的那一整段文件打不开**。

ADTS 每帧自带帧头，文件被截断仍可正常播放，最多丢最后一帧。实测（当时默认还是 16kHz / 32kbps）：

```
force-stop 打断录音后拉出的残缺文件
  codec_name=aac / 16kHz / 单声道
  duration=38.24 秒
  mean_volume=-34.0 dB
```

分段仍然是 3 分钟，但现在已经不是为了"减少损失"，而是控制单文件体积、方便管理。

---

## 六、文件位置

```
/sdcard/Android/data/<包名>/files/rec/
    rec_20261026_114544_123.aac
    rec_20261026_114844_456.aac
    rec_20261026_114844_456.aac.part   ← 正在写；stop 成功后 rename 成 .aac
    gaps.jsonl                         ← 通话空档 / 低电平记录
```

文件名带毫秒，避免同一秒内重开段撞名。`.part` 不会进入上传队列；进程被杀后，下次启动会校验 ADTS 头，能读就恢复成 `.aac`。

`gaps.jsonl` 事件类型：

| type | 含义 |
| --- | --- |
| `gap_start` / `gap_end` | 通话暂停起止 |
| `low_level_start` / `low_level_end` | 音量异常（可能被占用）起止 |
| `pause_timeout` | 暂停超过 2 小时，强制尝试恢复 |

`gaps.jsonl` 格式：

```json
{"type":"gap_start","ts":1761439200000}
{"type":"gap_end","ts":1761439320000}
```

---

## 七、代码结构

```
app/src/main/java/com/recnonstop/app/
├── MainActivity.java       界面、权限、设置、状态、播放/分享、诊断导出
├── RecorderService.java    前台服务、录音主循环、通话检测、分段、孤儿段恢复
├── Settings.java           设置项读写
├── WatchdogReceiver.java   精确闹钟看门狗 + 心跳巡检
├── BootReceiver.java       开机自启（Android 15 被拦截时提醒）
├── ControlReceiver.java    adb 控制通道（DUMP 权限保护）
├── RecState.java           进程内共享状态
├── UploadStore.java        上传状态/重试/摘要（SQLite）
├── Uploader.java           上传队列、幂等键、结果轮询、网络恢复补传
├── Health.java             健康心跳（health.json）
├── Adts.java               ADTS 头校验 / 采样率解析
├── RecFileProvider.java    零依赖文件共享（播放/分享/日志）
├── RecorderTileService.java 下拉快捷开关
├── Diag.java               本地日志轮转
├── Notifications.java      通知渠道、告警、日报推送
├── DailyReports.java       日报拉取 / 本地缓存 / 已读状态
├── Discovery.java          UDP 局域网发现（电脑 / 手机身份）
├── JoinClient.java         申请连接并等待电脑确认
├── Presence.java           回应电脑扫描（手机端身份）
└── PairingClient.java      旧版 6 位码换 token（兼容）
```

### 关键实现点

**录音主循环**

```java
while (running) {
    if (inCall()) { closeSegment(); openGap(); sleep(1000); continue; }
    if (!openSegment(dir)) { sleep(2000); continue; }
    closeGap();
    while (running && !inCall() && 段未超时) { sleep(500); }
    closeSegment();
}
```

异常时**只重建 MediaRecorder，不退出循环**——这是不失效的关键。

**通话检测**

```java
AudioManager am = getSystemService(AudioManager.class);
return am.getMode() == MODE_IN_CALL
    || am.getMode() == MODE_IN_COMMUNICATION
    || am.getMode() == MODE_CALL_SCREENING;
```

`getMode()` 不需要任何权限，500ms 轮询一次。

**看门狗**

`AlarmManager.setExactAndAllowWhileIdle` 每 60 秒一次。精确闹钟是 Android 14 允许后台启动前台服务的合法豁免路径之一。

实际限制：**Doze 下系统把这类闹钟限制到约 9 分钟一次**，没有精确闹钟权限回退到 inexact 时可能更久；部分厂商还会直接清掉闹钟。所以看门狗是 **best-effort**，不是保证。App 现在用 `health.json` 心跳 + 告警把失败暴露出来：打开 App 能看到“上次成功录音：X 分钟前”，而不是只显示“已停止”。

**健康心跳 / 静默停录**

- 每次成功切段写 `last_segment_at`、今日段数/体积
- 每次 gap / 低电平写 `gaps_today`
- 看门狗检查 30 分钟没有新段且不在通话暂停中 → 尝试重启并告警
- `startForeground` 失败不再每分钟硬拉：权限问题停 + 通知，后台限制退避 15 分钟
- 空间不足不再退出服务：暂停录音，腾出空间后自动继续

**窗口亮度**

```java
WindowManager.LayoutParams lp = getWindow().getAttributes();
lp.screenBrightness = pct / 100f;   // 0.01 ~ 1.0
getWindow().setAttributes(lp);
```

只作用于本 App 窗口，不需要权限，退出即失效。

---

## 八、电脑分析（可选）

手机端只是“采集”。转写、说话人分离、摘要由 PC 端负责，详见 [`pc/README.md`](pc/README.md)。

```bash
# 1. 启动 ASR worker（首次会加载模型）
RECNONSTOP_PYTHON=/path/to/venv/bin/python pc/scripts/run-worker.sh

# 2. 启动 Go 服务
RECNONSTOP_CONFIG=/path/to/config.json pc/scripts/run-server.sh

# 3. 手机端：打开「发现电脑」→ 选中电脑 → 电脑页面右下角点「允许」
#    连接信息会记住，之后自动上传；旧的 6 位码方式仍可用：pc/scripts/pair.sh
```

> `run-worker.sh` / `run-server.sh` 不传路径参数会直接拒绝启动，避免把数据/环境落进仓库。

> 仓库只带 `pc/configs/config.example.json`；首次运行先复制成 `config.json` 再改，或设置 `RECNONSTOP_CONFIG` 指向仓库外的私密配置。

连接后打开 **`http://127.0.0.1:18082/`** 就是本机时间线：搜索转写/摘要、播放音频、看逐句说话人，也可以点「扫描附近手机」让某台手机立即同步。

PC 端现在有：

- 手机和电脑互相发现：手机点电脑直接连；电脑点手机「连接」，手机上弹确认；已连接的手机可直接「同步」
- 首次连接只在电脑页面点一次「允许」，之后自动同步
- 任务落盘 + 重启恢复（不再 404 / 永久 `running`）
- 幂等键去重（重复上传不会重复分析）
- 有界任务池 + 队列背压（429）
- 上传大小/扩展名/音频头校验
- 音频 7 天、任务 30 天自动清理（`config.json` 可改）
- 分析失败保留转写，结果照常可查
- 本机时间线/搜索页（只监听 127.0.0.1）

### 每日自动日报

电脑默认每天 **18:00**（电脑本地时间）生成一份日报：汇总当天已分析的录音，交给本机 LLM 整理成 Markdown，生成完成后等手机下一次拉指令时自动推送。手机端在「设置 → 电脑分析」里可以改开关和生成时间，改动会写回电脑的 `report_settings.json`，不需要改 `config.json`。

- 中午 12 点前生成：总结前一天
- 中午 12 点后生成：总结当天 00:00 到生成时刻
- 电脑在生成时间关机时，下次启动补最近一次；手机离线时下次连上补推最新一份
- 手机端按日期列出所有已接收日报；连上电脑或打开「日报」页时自动补齐缺失的历史，只对最新一份发通知
- 日报正文只保存在电脑（`storage.dir/reports/`）和手机内部存储（`files/daily_reports/`），不上传云端
- 生成时如果还有分段在排队/分析，会等它们完成（最多 2 小时），避免日报只写一半
- 模型不可用时回退为分段摘要，日报仍然会推送

Windows：`RECNONSTOP_BIN_DIR=/path/to/private/bin pc/scripts/build.sh` 会交叉编译出 `pc-server.exe`；设置 `RECNONSTOP_CONFIG` 和 `RECNONSTOP_BIN_DIR` 后运行 `pc/scripts/run-server.bat`。worker 建议用 WSL 跑。

---

## 九、构建

环境已就绪：

```
JDK 17         /usr/lib/jvm/java-17-openjdk-amd64
Android SDK    /opt/android-sdk  (platform 35, build-tools 35.0.0)
Gradle 8.9     /opt/gradle/gradle-8.9
```

```bash
RECNONSTOP_BUILD_DIR=/path/outside/repo/android \
GRADLE_PROJECT_CACHE_DIR=/path/outside/repo/gradle-cache \
./build.sh
# 产物：$RECNONSTOP_BUILD_DIR/app/outputs/apk/debug/app-debug.apk
```

> **必须传这两个路径**：不传或指向仓库内时，`build.sh` 会拒绝执行，避免 `app/build`、`.gradle` 落进仓库。

首次编译需下载 AGP 依赖（约 330MB，4~5 分钟），之后增量编译约 1 分钟。

| 环境变量 | 是否必填 | 作用 |
| --- | --- | --- |
| `RECNONSTOP_BUILD_DIR` | 必填 | Gradle 产物根目录（`app/`、`micgrab/` 子目录） |
| `GRADLE_PROJECT_CACHE_DIR` | 必填 | 替代仓库内 `.gradle/` 的项目缓存 |
| `JAVA_HOME` | 可选 | JDK 路径 |
| `ANDROID_SDK_ROOT` / `ANDROID_HOME` | 可选 | Android SDK 路径 |
| `GRADLE_BIN` | 可选 | gradle 可执行文件 |

设置 `ANDROID_SDK_ROOT` 后不再需要仓库内的 `local.properties`。

## 十、安装与调试

```bash
adb install -r "$RECNONSTOP_BUILD_DIR/app/outputs/apk/debug/app-debug.apk"
adb shell pm grant <包名> android.permission.RECORD_AUDIO

# 启动 / 停止
adb shell am start -n <包名>/.MainActivity --es cmd start
adb shell am start -n <包名>/.MainActivity --es cmd stop

# 看日志
adb logcat -s RecNonstop:*

# 看文件
adb shell ls -l /sdcard/Android/data/<包名>/files/rec/
```

手机侧通过 `adb` 连接，具体连接方式按你的设备环境配置。

### MIUI 安装注意事项（实测踩坑）

在小米 / Redmi 上用 `adb install` 会连续遇到三道拦截，**`adb install` 会直接报 `INSTALL_FAILED_USER_RESTRICTED`**：

| 拦截 | 表现 | 处理 |
| --- | --- | --- |
| USB 安装确认 | 手机弹出「USB安装提示」，9 秒倒计时 | 倒计时内点「继续安装」，否则视为拒绝 |
| 未授权来源 | 弹出「是否允许 Shell 安装应用」 | 点「允许」（可勾「记住我的选择」） |
| ICP 备案提示 | 「未查询到此应用的 ICP 备案信息」 | 点「继续安装」 |

一旦某次倒计时超时，MIUI 会记住「拒绝」，之后 `adb install` 全部失败，需要改用本机安装：

```bash
adb push app-debug.apk /sdcard/Download/lubuting.apk
adb shell am start -a android.intent.action.INSTALL_PACKAGE \
  -d file:///sdcard/Download/lubuting.apk
# 然后在手机上点：允许 → 继续安装 → 继续安装
```

**关键：操作期间屏幕必须解锁并保持点亮**，否则弹窗无法显示，安装被自动取消。

---

## 十一、实测记录

Redmi 10X（Android 12 / MIUI）实测：

| 项目 | 结果 |
| --- | --- |
| 连续录音 | 23 分钟，8 段，零中断 |
| 每段时长 | 180.03 秒（误差 0.03 秒） |
| 切段空档 | 103 ~ 149 毫秒 |
| 息屏 30 秒 | 文件增长 142,869 字节 |
| 音频真实性 | 平均 -38.7 dB / 峰值 -4.9 dB |
| 自动锁屏 | 息屏时间到点后锁屏，录音继续 |

### 通话暂停 / 续录（微信视频通话实测）

```
17:11:21.954  segment closed rec_20261026_171021.m4a
17:11:21.960  call detected, recording paused      ← 6 毫秒内响应
17:20:03.954  segment opened rec_20261026_172003.m4a
17:20:03.959  call ended, recording resumed        ← 挂断即续录
```

`gaps.jsonl`：

```json
{"type":"gap_start","ts":1793005881959}
{"type":"gap_end","ts":1793006403958}
```

空档 522 秒，与通话时长一致。

### 被其他 App 抢麦克风（已知问题）

用测试工具 `micgrab` 持续占用麦克风，同一时段的录音音量对比：

| 时间段 | 平均音量 | 峰值 |
| --- | --- | --- |
| 占用前 | -34.2 dB | -16.4 dB |
| 被占用 | **-61.6 dB** | -24.0 dB |
| 占用结束 | -40.1 dB | -16.1 dB |

- 后启动的 App 抢到麦克风，先启动的只拿到残余音频
- 录不停不会崩溃、不会报错，文件继续增长，但内容基本不可用
- 停止占用后自动恢复

**结论：`AudioManager.getMode()` 只能识别通话，识别不了"被别的 App 抢麦"。**

### 低电平检测（0.4+）

`MediaRecorder.getMaxAmplitude()` 每 500ms 采一次峰值，换算成 dBFS：

| 条件 | 判定时间 |
| --- | --- |
| 采到纯数字零（amp = 0） | 10 秒 |
| 低于 -70 dB（接近静音） | 30 秒 |

触发后：

- 日志 `low level -90.3 dB (amp=0) for 10 s`
- `gaps.jsonl` 写入 `low_level_start`
- 状态栏显示「录音中（音量异常，可能未录到）」
- 通知栏追加「音量异常」
- 电平恢复后写 `low_level_end`，状态自动清除

阈值取 -70 dB 而不是 -50 dB，是为了避免安静房间被误判——实测正常环境音在 -34 ~ -42 dB，夜间安静环境可能到 -60 dB 左右。

---

## 十二、待办

- [x] 通话真实场景测试（微信视频通话 8 分 42 秒，暂停/续录均正常）
- [x] 低电平检测（-70 dB 持续 30 秒，或数字静音 10 秒）
- [x] 暂停超时保护（2 小时）
- [x] 存储不足保护（200 MB，改为暂停/空间恢复自动继续）
- [x] 录音列表播放、删除、分享
- [x] 录音自动上传/备份（局域网，可选）
- [x] 健康心跳 / 静默停录告警 / 诊断日志导出
- [x] PC 端任务恢复、幂等、队列背压、磁盘保留
- [x] PC 端本机时间线 / 搜索页
- [ ] 整夜 8 小时稳定性测试（`scripts/soak.sh`）
- [ ] MIUI 神隐模式下的存活测试
- [ ] Android 14 / 15 真机验证（开机自启、精确闹钟、麦克风 FGS）
- [x] 每日自动日报（电脑定时生成、自动推送手机）
- [ ] 分析粒度 session 化（3 分钟采集 → 30~60 分钟合并转写）
- [ ] 跨段说话人一致性（CAM++ 声纹库）
- [ ] 可选：录音文件加密存储（Keystore）
- [ ] 可选：局域网 TLS + 证书 pinning

---

## 十三、测试工具

`micgrab/` 是一个独立的小 App（`com.recnonstop.micgrab`），用于验证**别的 App 抢麦克风**时录不停的表现。

- 常驻前台服务占用麦克风
- 实时显示电平（dB）
- 可切换"通话模式"模拟 VoIP 抢占

```bash
RECNONSTOP_BUILD_DIR=/path/outside/repo/android \
GRADLE_PROJECT_CACHE_DIR=/path/outside/repo/gradle-cache \
/opt/gradle/gradle-8.9/bin/gradle :micgrab:assembleDebug \
  --project-cache-dir "$GRADLE_PROJECT_CACHE_DIR" \
  "-PrecnonstopBuildDir=$RECNONSTOP_BUILD_DIR"

adb install "$RECNONSTOP_BUILD_DIR/micgrab/outputs/apk/debug/micgrab-debug.apk"
```

说明：在 MIUI 上 `setMode(MODE_IN_COMMUNICATION)` 会被系统忽略（`Requested mode = MODE_NORMAL`），所以"通话模式"开关在这台机器上无效，只能验证麦克风占用。

---

## 十四、合规提醒

- 录音属于敏感个人信息，**未经对方同意录制对话可能违法**（《民法典》隐私权、《个人信息保护法》）
- 上架应用商店需提供隐私政策、权限说明、录音中显著提示
- 不建议默认开启「自动录音」，应由用户主动打开
- 本 App 默认不联网、不上传；开启「自动上传到电脑分析」后只连你确认过的那台局域网电脑，不经过云端
