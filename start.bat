@echo off
rem 从源码构建并启动服务（Windows），双击运行即可。
rem   start.bat          构建并启动（首次运行会引导创建管理员）
rem   start.bat build    只构建可执行文件 bin\file_linker.exe
chcp 65001 >nul
setlocal
cd /d "%~dp0"

where go >nul 2>nul || (echo 未找到 go。请安装 Go：https://go.dev/dl/ & goto fail)
where node >nul 2>nul || (echo 未找到 node。请安装 Node.js 20 或更高版本：https://nodejs.org/ & goto fail)
where pnpm >nul 2>nul || (echo 未找到 pnpm。请运行 npm install -g pnpm 安装 & goto fail)

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
