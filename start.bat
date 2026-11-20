@echo off
rem 从源码构建并启动服务（Windows），双击运行即可。
rem   start.bat          构建并启动（首次运行会引导创建管理员）
rem   start.bat build    只构建可执行文件 bin\file_linker.exe
rem 缺少 Go / Node.js / pnpm 时会询问是否自动安装（Go 和 Node.js 通过 winget 安装）。
chcp 65001 >nul
setlocal
cd /d "%~dp0"

call :need go "Go" GoLang.Go "https://go.dev/dl/" || goto fail
call :need node "Node.js" OpenJS.NodeJS.LTS "https://nodejs.org/" || goto fail
call :need_pnpm || goto fail

echo ==^> 构建前端
pushd web
call pnpm install --frozen-lockfile || (popd & goto fail)
call pnpm build || (popd & goto fail)
popd

echo ==^> 构建可执行文件
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
rem :need 命令 显示名 winget包ID 下载地址
rem 命令不存在时询问是否用 winget 自动安装；选 N 或无法安装时提示手动安装方法。
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

rem pnpm 通过 Node.js 自带的 npm 安装
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

rem 安装程序只修改注册表中的 PATH，当前窗口看不到；从注册表重新读取，免得用户重开窗口
:refresh_path
for /f "usebackq delims=" %%p in (`powershell -NoProfile -Command "[Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [Environment]::GetEnvironmentVariable('Path','User')"`) do set "PATH=%%p"
rem npm 全局命令目录，新装的 Node.js 有时还没写入 PATH
set "PATH=%PATH%;%APPDATA%\npm"
exit /b 0
