#!/bin/sh
# 生成 deb 安装包：/opt/naswebgateway/{nwg,www} + systemd 单元 + /etc/default 配置
# 用法：在仓库根目录执行 packaging/build-deb.sh [版本号]
# 依赖：bin/nwg-amd64、bin/nwg-arm64、www/ 需先存在（由 cross build 产出）
set -e
ROOT=$(cd "$(dirname "$0")/.." && pwd)
VER=${1:-0.1.0.$(date +%Y%m%d)}
OUT=$ROOT/dist-deb
mkdir -p "$OUT"

for ARCH in arm64 amd64; do
  R=$(mktemp -d)
  trap 'rm -rf "$R"' EXIT
  mkdir -p "$R/DEBIAN" "$R/opt/naswebgateway" "$R/etc/default" \
           "$R/lib/systemd/system" "$R/usr/share/doc/naswebgateway"
  install -m 0755 "$ROOT/bin/nwg-$ARCH" "$R/opt/naswebgateway/nwg"
  cp -a "$ROOT/www" "$R/opt/naswebgateway/www"
  find "$R/opt/naswebgateway/www" -type f -exec chmod 0644 {} +

  cat > "$R/lib/systemd/system/naswebgateway.service" <<'UNIT'
[Unit]
Description=NAS Web Gateway (内网浏览器)
After=network-online.target
Wants=network-online.target

[Service]
User=root
EnvironmentFile=-/etc/default/naswebgateway
Environment=NWG_WWW=/opt/naswebgateway/www
Environment=NWG_DATA=/var/lib/naswebgateway
ExecStart=/opt/naswebgateway/nwg
Restart=always
RestartSec=3
WorkingDirectory=/var/lib/naswebgateway

[Install]
WantedBy=multi-user.target
UNIT

  cat > "$R/etc/default/naswebgateway" <<'ENVFILE'
# nas-web-gateway 运行参数
NWG_HTTP_PORT=18092
NWG_AUTH=on
NWG_INSECURE=on
#NWG_BASE=
ENVFILE

  cat > "$R/DEBIAN/control" <<CTRL
Package: naswebgateway
Version: $VER
Section: net
Priority: optional
Architecture: $ARCH
Maintainer: Ride_Wind <ridewindone@gmail.com>
Depends: libc6
Installed-Size: $(du -sk "$R/opt" | cut -f1)
Description: NAS Web Gateway (内网浏览器)
 局域网 Web 网关：把 NAS / 路由器 / 容器应用统一收进一个入口，
 支持路径前缀反代、Service Worker 接管与 WebSocket 转发。
 数据目录 /var/lib/naswebgateway，端口与鉴权见 /etc/default/naswebgateway。
CTRL

  cat > "$R/DEBIAN/postinst" <<'PI'
#!/bin/sh
set -e
mkdir -p /var/lib/naswebgateway
[ -f /var/lib/naswebgateway/apps.json ] || echo '[]' > /var/lib/naswebgateway/apps.json
systemctl daemon-reload || true
systemctl enable naswebgateway >/dev/null 2>&1 || true
systemctl restart naswebgateway || true
exit 0
PI
  cat > "$R/DEBIAN/prerm" <<'PR'
#!/bin/sh
set -e
systemctl stop naswebgateway >/dev/null 2>&1 || true
systemctl disable naswebgateway >/dev/null 2>&1 || true
exit 0
PR
  cat > "$R/DEBIAN/postrm" <<'PO'
#!/bin/sh
set -e
systemctl daemon-reload >/dev/null 2>&1 || true
exit 0
PO
  chmod 0755 "$R/DEBIAN/postinst" "$R/DEBIAN/prerm" "$R/DEBIAN/postrm"

  dpkg-deb --root-owner-group --build "$R" "$OUT/naswebgateway_${VER}_${ARCH}.deb" >/dev/null
  echo "built $OUT/naswebgateway_${VER}_${ARCH}.deb"
done
