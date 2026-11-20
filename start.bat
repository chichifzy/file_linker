@echo off
chcp 65001 >nul
rem Build and start the service on Windows. Double-click this file to run.
rem   start.bat          build and start (asks to create an admin account on first run)
rem   start.bat build    build only, output bin\file_linker.exe
rem Go / Node.js / pnpm are installed through winget when missing.
rem ffmpeg is optional: only the video "convert encoding" feature needs it.
rem NOTE: keep every rem line ASCII. Chinese comments before chcp take effect
rem are decoded with the ANSI code page and get mis-split into commands.
setlocal
cd /d "%~dp0"

call :need go "Go" GoLang.Go "https://go.dev/dl/" || goto fail
call :need node "Node.js" OpenJS.NodeJS.LTS "https://nodejs.org/" || goto fail
call :need_pnpm || goto fail
call :optional_ffmpeg

echo ==^> 构建前端
pushd web
call pnpm install --frozen-lockfile || (popd & goto fail)
call pnpm build || (popd & goto fail)
popd

echo ==^> 构建可执行文件
rem Fallback to a domestic Go module mirror when using the default proxy.
for /f "delims=" %%p in ('go env GOPROXY') do set "CUR_GOPROXY=%%p"
if /i "%CUR_GOPROXY%"=="https://proxy.golang.org,direct" set "GOPROXY=https://goproxy.cn,direct"
if /i "%CUR_GOPROXY%"=="https://proxy.golang.org,direct" set "GOSUMDB=sum.golang.google.cn"
set CGO_ENABLED=0
go build -trimpath -ldflags="-s -w" -o bin\file_linker.exe . || goto fail

if /i "%~1"=="build" (
  echo 构建完成：bin\file_linker.exe
  echo 可以把它复制到任意位置运行，数据库默认是当前目录下的 data.db。
  pause
  exit /b 0
)

bin\file_linker.exe has-admin
if not errorlevel 1 goto run
echo.
echo ==^> 首次运行，请创建管理员账号
:create
set "uid="
set "nickname="
set /p "uid=管理员用户 ID（3-20 位英文字母或数字）: "
set /p "nickname=管理员用户名: "
bin\file_linker.exe create-admin "%uid%" "%nickname%"
if errorlevel 1 goto create

:run
bin\file_linker.exe %*
pause
exit /b 0

:fail
echo.
echo 启动失败，请查看上面的错误信息。
pause
exit /b 1

rem ---------------------------------------------------------------------------
rem :need command display-name winget-id download-url
rem Ask before installing via winget; if declined or unavailable, print manual steps.
:need
where %1 >nul 2>nul && exit /b 0
echo.
echo 未找到 %~2。
where winget >nul 2>nul || (
  echo 这台电脑没有 winget，无法自动安装。请手动安装 %~2：%~4
  echo 安装完成后重新双击 start.bat。
  exit /b 1
)
choice /c YN /m "是否自动安装 %~2"
if errorlevel 2 (
  echo 请手动安装 %~2：%~4
  echo 安装完成后重新双击 start.bat。
  exit /b 1
)
echo ==^> 正在安装 %~2，如果弹出“是否允许更改”的窗口，请点“是”
winget install --exact --id %3 --silent --accept-package-agreements --accept-source-agreements
call :refresh_path
where %1 >nul 2>nul && (echo %~2 安装完成。& exit /b 0)
echo %~2 安装失败或尚未生效。如果上面显示已安装，请关闭本窗口后重新双击 start.bat；否则请手动安装：%~4
exit /b 1

rem ffmpeg is only needed by the "convert encoding" feature (AV1/HEVC -> H.264).
rem Missing it does not block browsing or downloading, so never fail here.
:optional_ffmpeg
where ffmpeg >nul 2>nul && exit /b 0
echo.
where winget >nul 2>nul || (
  echo 未找到 ffmpeg，视频「转换编码」功能不可用。需要时手动安装：https://ffmpeg.org/download.html
  exit /b 0
)
choice /c YN /m "未找到 ffmpeg（视频转换编码需要），是否自动安装"
if errorlevel 2 (
  echo 已跳过，视频「转换编码」功能不可用。
  exit /b 0
)
echo ==^> 正在安装 ffmpeg
winget install --exact --id Gyan.FFmpeg --silent --accept-package-agreements --accept-source-agreements
call :refresh_path
where ffmpeg >nul 2>nul && (echo ffmpeg 安装完成。& exit /b 0)
echo ffmpeg 安装失败或还没生效。可以手动安装后重新双击 start.bat，不影响其他功能。
exit /b 0

rem pnpm is installed through the npm bundled with Node.js
:need_pnpm
where pnpm >nul 2>nul && exit /b 0
echo.
echo 未找到 pnpm。
choice /c YN /m "是否自动安装 pnpm"
if errorlevel 2 (
  echo 请在命令提示符中运行 npm install -g pnpm 安装，然后重新双击 start.bat。
  exit /b 1
)
echo ==^> 正在安装 pnpm
call npm install -g pnpm
call :refresh_path
where pnpm >nul 2>nul && (echo pnpm 安装完成。& exit /b 0)
echo pnpm 安装失败或尚未生效。请关闭本窗口后重新双击 start.bat，或手动运行 npm install -g pnpm。
exit /b 1

rem Installers only update the registry PATH; reload it so this window sees new tools.
:refresh_path
for /f "usebackq delims=" %%p in (`powershell -NoProfile -Command "[Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [Environment]::GetEnvironmentVariable('Path','User')"`) do set "PATH=%%p"
rem Global npm bin dir; a fresh Node.js install may not have it in PATH yet.
set "PATH=%PATH%;%APPDATA%\npm"
exit /b 0
