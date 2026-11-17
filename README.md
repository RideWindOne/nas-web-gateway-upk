# 内网浏览器（com.ridewind.naswebgateway）

装在绿联 NAS 上的原生 UPK 应用。用手机通过 UGREENlink / DDNS 在外网打开 NAS 所在局域网里的设备后台——路由器 LuCI、飞牛 fnOS、绿联云桌面（9999）、摄像头等等。

页面在自己浏览器里渲染，不起远端桌面、不转视频流；代理由 NAS 上的 Go 服务完成，地址改写、SPA 修复、登录态持久化都在这一层处理。

## 组成

| 路径 | 作用 |
|---|---|
| `backend/` | Go 服务：`/api/p/<seg>/` 反向代理、HTML/CSS/JS 地址改写、Service Worker 配套、收藏/历史/应用卡片持久化 |
| `rootfs_common/www/` | 前端：地址栏 + 标签栏 + 菜单、多标签并排、虚拟鼠标、缩放、横屏、沉浸查看 |
| `rootfs_common/www/sw.js`、`boot.html` | Service Worker 透明转发层与引导页（解决 SPA 首帧路由错乱） |
| `project.yaml` | UPK 打包声明（`app_id`、端口 21010、`proxy_path /api`、`open_type inner`） |
| `rootfs_arm64/app/`、`rootfs_amd64/app/` | 各架构的 `app_serv` 二进制（构建产物，不入库） |

## 构建

```bash
# 后端（在任意 Linux + Go 环境交叉编译）
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -buildvcs=false \
  -o rootfs_arm64/app/app_serv ./backend
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -buildvcs=false \
  -o rootfs_amd64/app/app_serv ./backend

# 打包（ugcli 在 UGOS 打包工具链里）
/Users/wind/ugcli/ugcli pack --arch arm64 --build 50
/Users/wind/ugcli/ugcli pack --arch amd64 --build 50
```

产物在 `build_dir/pkgs/upk/`，命名形如
`arm64_nasync_com.ridewind.naswebgateway_0.1.0.0050.upk`。

## 安装

在 UGOS 应用中心添加三方商店源后直接安装（源地址）：

```
https://raw.githubusercontent.com/RideWindOne/kmodloader-upk/main/apps.json
```

商店按 `upk_version` 判断更新，装好后在桌面或 App 里点开即用，端口 21010。

数据落在 `/volume1/@appstore/com.ridewind.naswebgateway/shared/persist/`（`apps.json`、`cookies.json`、`history.json`），跨升级保留。

## 关键设计

- **`start_cmd` 直接指向静态二进制**：unit 带 `TemporaryFileSystem=/:ro`，沙箱里没有 `/bin/sh`，任何 `#!/bin/sh` 脚本都会 203/EXEC。
- **`proxy_path` 必须是真实前缀（`/api`）**：写成 `/` 会生成 `location ~ ^//`，什么都匹配不到；静态页面由网关直发，代理挂 `/api`。
- **Service Worker 转发层**：飞牛的 SPA 会走 `location.href = '/login?redirect_uri=...'`、`import('/assets/xx.js')` 这类绕过注入 hook 的路径，靠 SW 把干净路径透明转成 `/api/p/<seg>/...` 才稳。
- **seg 携带 UA 意图**：base64 里以 `d|` 为前缀表示「按电脑 UA 请求」，页面里所有改写出的子资源地址都继承同一个 seg，所以单个网页应用勾选「访问电脑版」后整站一致。
