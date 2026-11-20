#!/usr/bin/env bash
# 从源码构建并启动服务（macOS / Linux）。
#   ./start.sh          构建并启动（首次运行会引导创建管理员）
#   ./start.sh build    只构建可执行文件 bin/file_linker
# 其余参数原样传给服务，例如 ./start.sh -addr :8081
set -euo pipefail
cd "$(dirname "$0")"

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
