#!/bin/sh
# UGOS 把运行用户的 CWD 设成 appdata 目录，所以这里全部用绝对路径。
set -eu

if [ -n "${UGAPP_INSTALL_DIR:-}" ]; then
  INSTALL="$UGAPP_INSTALL_DIR"
else
  # 兜底：从脚本自身位置往上推两级（app/start.sh -> 安装根目录）
  INSTALL="$(cd "$(dirname "$0")/.." && pwd)"
fi

DATA="${UGAPP_DATA_DIR:-$INSTALL/data}"

exec "$INSTALL/app/app_serv" \
  --port="${PORT:-21010}" \
  --www="$INSTALL/www" \
  --data="$DATA" \
  --insecure=true
