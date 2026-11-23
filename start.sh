#!/usr/bin/env bash
# 从源码构建并启动服务（macOS / Linux）。
#   ./start.sh          构建并启动（已有实例在跑时复用它）
#   ./start.sh build    只构建可执行文件 bin/file_linker
# 其余参数原样传给服务，例如 ./start.sh -addr :8081
set -euo pipefail
cd "$(dirname "$0")"

# 已有实例在运行时直接复用它：浏览器打开真实地址，这个窗口按 Ctrl+C 停止那个服务。
# 只在无参数的双击/直接运行时检查，显式传参（-addr 等）保持原样。
if [ "$#" -eq 0 ] && [ -x ./bin/file_linker ]; then
  # 先静默探测：旧版本的可执行文件会把整段用法打到 stderr，不该显示给用户
  code=0
  ./bin/file_linker status >/dev/null 2>&1 || code=$?
  if [ "$code" -eq 0 ]; then
    # Ctrl+C 会同时发给脚本和前台进程，先让脚本忽略，等 status 收尾后再退出
    trap '' INT
    ./bin/file_linker status --attach --open
    trap - INT
    exit 0
  fi
  case "$code" in
    1 | 2) : ;; # 没有实例在跑；或 bin 里还是不认识 status 的旧版本，继续构建
    4)
      echo "已有实例在运行，但无法确认它的状态（可能正在启动）。" >&2
      echo "请稍后重试，或关闭之前启动的服务窗口后再试。" >&2
      exit 1
      ;;
    *)
      echo "状态检查失败（退出码 $code）。" >&2
      exit 1
      ;;
  esac
fi

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "未找到 $1。$2" >&2
    exit 1
  fi
}
need go "请安装 Go：https://go.dev/dl/"
need node "请安装 Node.js 20 或更高版本：https://nodejs.org/"
need pnpm "请安装 pnpm：npm install -g pnpm（或 corepack enable pnpm）"

echo "==> 构建前端"
(cd web && pnpm install --frozen-lockfile && pnpm build)

echo "==> 构建可执行文件"
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/file_linker .

if [ "${1:-}" = "build" ]; then
  echo "构建完成：bin/file_linker"
  echo "可以把它复制到任意位置运行，数据库默认是当前目录下的 data.db。"
  exit 0
fi

if ! ./bin/file_linker has-admin; then
  echo
  echo "==> 首次运行，请创建管理员账号"
  while true; do
    read -rp "管理员用户 ID（3-20 位英文字母或数字）: " uid
    read -rp "管理员用户名: " nickname
    ./bin/file_linker create-admin "$uid" "$nickname" && break
  done
fi

exec ./bin/file_linker "$@"
