# 录不停 · 项目规则

## 仓库边界

- 本仓库是公开仓库：**仓库内现有内容默认都可以提交**。
- 私有/本机内容只放仓库外的私密目录，不要新增到本仓库：
  - 真实录音、转写、摘要、说话人声纹、通话记录
  - 设备 ID、配对码、token、token hash、`devices.json`
  - 真实内网 IP、主机名、SSID、MAC、个人绝对路径、个人设备型号/账号
  - 签名密钥、keystore、`.env`、API key、密码、cookie、数据库、日志、截图
- 本机构建/启动用仓库外的包装脚本；不要用公开脚本在仓库内生成 `app/build`、`micgrab/build`、`.gradle`、`pc/bin`、`.venv-asr`、`local.properties`。
- **路径参数必须显式传入**：`RECNONSTOP_BUILD_DIR`、`GRADLE_PROJECT_CACHE_DIR`、`RECNONSTOP_BIN_DIR`、`RECNONSTOP_CONFIG`、`RECNONSTOP_PYTHON` 缺失时，脚本必须报错退出，禁止回退到仓库内默认目录。
- 新增内容先判断能否开源；不确定就停下来问用户。
- **不要自动 commit/push**；只有用户明确说“提交”才提交。

## 详细规则

加载项目 skill `private-data` 获取完整清单、仓库外私密目录约定和提交前检查步骤。
