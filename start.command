#!/usr/bin/env bash
# macOS 上双击即可在终端窗口中构建并启动服务。
cd "$(dirname "$0")"
./start.sh
status=$?
if [ $status -ne 0 ]; then
  echo
  read -rp "按回车键关闭窗口…"
fi
exit $status
