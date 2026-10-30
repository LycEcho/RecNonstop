@echo off
cd /d "%~dp0\.."
set "SERVER="
if defined RECNONSTOP_BIN_DIR set "SERVER=%RECNONSTOP_BIN_DIR%\pc-server.exe"
if defined RECNONSTOP_SERVER_BIN set "SERVER=%RECNONSTOP_SERVER_BIN%"
if not defined SERVER (
  echo 错误：必须设置 RECNONSTOP_BIN_DIR 或 RECNONSTOP_SERVER_BIN（仓库外路径）。
  pause
  exit /b 1
)
if not exist "%SERVER%" (
  echo 找不到服务端二进制：%SERVER%
  pause
  exit /b 1
)
if not defined RECNONSTOP_CONFIG (
  echo 错误：必须设置 RECNONSTOP_CONFIG（仓库外配置文件）。
  pause
  exit /b 1
)
if not exist "%RECNONSTOP_CONFIG%" (
  echo 配置文件不存在：%RECNONSTOP_CONFIG%
  pause
  exit /b 1
)
"%SERVER%" -config "%RECNONSTOP_CONFIG%"
pause
