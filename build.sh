#!/bin/bash
# 交叉编译 + 组装 rootfs + 调 ugcli 打包
# 用法: ./build.sh [build号]
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
BUILD_NO="${1:-1}"
UGCLI="${UGCLI:-/Users/wind/ugcli/ugcli}"
APP_ID="com.ridewind.naswebgateway"
VERSION="0.1.0"
ARCHES="arm64 amd64"

cd "$HERE"

echo "== 编译 =="
for a in $ARCHES; do
  mkdir -p "rootfs_$a/app"
  echo "  -> $a"
  CGO_ENABLED=0 GOOS=linux GOARCH="$a" \
    go build -trimpath -ldflags="-s -w" -buildvcs=false \
    -o "rootfs_$a/app/app_serv" ./backend
  cp start.sh "rootfs_$a/app/start.sh"
  chmod 755 "rootfs_$a/app/app_serv" "rootfs_$a/app/start.sh"
done

echo "== 公共部分 =="
mkdir -p rootfs_common/app
cp start.sh rootfs_common/app/start.sh
chmod 755 rootfs_common/app/start.sh

echo "== 校验 =="
"$UGCLI" check --path .

echo "== 打包 =="
rm -rf build_dir
"$UGCLI" pack --arch arm64 --build "$BUILD_NO"
"$UGCLI" pack --arch amd64 --build "$BUILD_NO"

echo "== 产物 =="
ls -lh build_dir/pkgs/upk/*.upk 2>/dev/null || ls -lh "$HERE"/build_dir/pkgs/upk/ 2>/dev/null
