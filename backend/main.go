package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 沙箱里只有 BindPaths 放行的目录可写，且 shared 是可选挂载（可能不存在），
// 所以只能逐个试写，第一个写得进去的就是本版的数据落点。
func dirWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	probe := filepath.Join(dir, ".rw-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		return false
	}
	os.Remove(probe)
	return true
}

func main() {
	// 监听地址：默认所有网卡；给 127.0.0.1 就只允许本机进程访问，
	// 配合前面的 TLS 反代把后端藏到回环里。
	addr := flag.String("addr", "", "HTTP listen address (default all interfaces), e.g. 127.0.0.1")
	defPort := 21010
	if v := os.Getenv("NWG_HTTP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			defPort = n
		}
	} else if v := os.Getenv("NWG_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			defPort = n
		}
	}
	port := flag.Int("port", defPort, "HTTP listen port")
	// 需要登录：首启先设置用户名密码，之后所有请求校验 cookie。
	auth := flag.Bool("auth", os.Getenv("NWG_AUTH") != "off" && os.Getenv("NWG_AUTH") != "0", "require login; first run asks to create the admin account")
	wwwDir := flag.String("www", "", "static dir (defaults to $UGAPP_INSTALL_DIR/www)")
	dataDir := flag.String("data", "", "data dir (defaults to $UGAPP_DATA_DIR)")
	insecure := flag.Bool("insecure", os.Getenv("NWG_INSECURE") != "off", "skip TLS verification for LAN devices with self-signed certs")
	// 飞牛统一网关路径前缀（如 /app/naswebgateway）；绿联下留空。
	base := flag.String("base", "", "UI base path prefix (fnOS gateway), e.g. /app/naswebgateway")
	// 飞牛统一网关通过 Unix socket 转发；给了就额外监听这个 socket。
	sock := flag.String("sock", "", "also listen on this unix socket (fnOS gateway app.sock)")
	flag.Parse()

	uiBase = strings.TrimSuffix(*base, "/")
	if uiBase != "" && !strings.HasPrefix(uiBase, "/") {
		uiBase = "/" + uiBase
	}

	// UGOS 把进程 CWD 设成 appdata 目录，所以所有路径都必须从环境变量推导成绝对路径。
	// 注意：unit 里有 TemporaryFileSystem=/:ro，只有 data/cache/log/shared 被 BindPaths
	// 放行，所以数据只能落在 UGAPP_DATA_DIR 之内，不能自选 /volume1 其他目录。
	installDir := os.Getenv("UGAPP_INSTALL_DIR")

	if *dataDir == "" {
		if env := os.Getenv("NWG_DATA"); env != "" {
			*dataDir = env
		}
	}
	if *dataDir == "" {
		// 升级（uginstall -upk）会重建 data 与 cache，登录态/卡片/历史都会丢，
		// 只有 shared 在 unit 里是可选挂载、可能跨版本保留，所以优先落在这里。
		var cands []string
		if sh := os.Getenv("UGAPP_SHARED_DIR"); sh != "" {
			cands = append(cands, filepath.Join(sh, "persist"))
		}
		if d := os.Getenv("UGAPP_DATA_DIR"); d != "" {
			cands = append(cands, d)
		}
		if installDir != "" {
			cands = append(cands, filepath.Join(installDir, "data"))
		} else {
			cands = append(cands, "./data")
		}
		for _, c := range cands {
			if dirWritable(c) {
				*dataDir = c
				break
			}
		}
		if *dataDir == "" {
			*dataDir = cands[0]
		}
		log.Printf("data dir: %s", *dataDir)
	}
	if *wwwDir == "" {
		if env := os.Getenv("NWG_WWW"); env != "" {
			*wwwDir = env
		} else if installDir != "" {
			*wwwDir = filepath.Join(installDir, "www")
		} else {
			*wwwDir = "./rootfs_common/www"
		}
	}
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("create data dir %s: %v", *dataDir, err)
	}

	// 一次性迁移：如果已经切到 shared/persist（跨升级保留），而里面还是空的，
	// 就把旧版本留在 @appdata 里的卡片、历史、登录态搬过来，免得升级看上去“数据没了”。
	if d := os.Getenv("UGAPP_DATA_DIR"); d != "" && d != *dataDir && dirWritable(*dataDir) {
		for _, name := range []string{"apps.json", "cookies.json", "history.json", "fav.json"} {
			src := filepath.Join(d, name)
			dst := filepath.Join(*dataDir, name)
			if _, err := os.Stat(dst); err == nil {
				continue
			}
			b, err := os.ReadFile(src)
			if err != nil {
				continue
			}
			if err := os.WriteFile(dst, b, 0o644); err != nil {
				log.Printf("migrate %s: %v", name, err)
				continue
			}
			log.Printf("migrated %s from %s", name, d)
		}
	}

	store := NewStore(*dataDir)
	jar := NewCookieJar(filepath.Join(*dataDir, "cookies.json"))
	proxy := NewPureProxy(jar, *insecure)

	// 账号体系：只有 -auth 打开时才生效（飞牛/绿联的 fpk 部署保持原样，由 NAS 门户负责登录）。
	authSvc := NewAuth(*dataDir, *auth)

	mux := http.NewServeMux()

	mux.HandleFunc("/setup", authSvc.SetupHandler)
	mux.HandleFunc("/login", authSvc.LoginHandler)
	mux.HandleFunc("/logout", authSvc.LogoutHandler)

	// 网关把 proxy_path(/api) 之下的请求转发到这里；/api/history 等精确路由
	// 优先于 /api/p/ 前缀，两者不冲突。
	// /__p/ 保留给本机直连调试（不经过网关时用）。
	mux.HandleFunc("/api/p/", proxy.ServeHTTP)
	mux.HandleFunc("/__p/", proxy.ServeHTTP)
	// v2 不再提供 relay（旧版用于本机直连调试），前端未使用。
	mux.HandleFunc("/api/history", store.HistoryHandler())
	mux.HandleFunc("/api/fav", store.FavHandler())
	mux.HandleFunc("/api/apps", store.AppsHandler())
	mux.HandleFunc("/api/info", infoHandler)
	mux.HandleFunc("/api/health", healthHandler)
	// 被代理页面的注入脚本走外部 URL：页面里只有一个 <script src>，
	// 不会有内联源码被页面模板当成正文渲染出来。
	mux.HandleFunc("/api/inject.js", injectHandler)
	// 客户端报错回传：被代理的页面里出错时，前端把 message+stack POST 回来，
	// 日志里就能看到，避免只能靠“白屏”猜。
	mux.HandleFunc("/api/clientlog", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		b, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
		log.Printf("[nwg] client: %s", string(b))
		w.WriteHeader(http.StatusNoContent)
	})

	fs := http.FileServer(http.Dir(*wwwDir))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// WebSocket 升级请求：SW 拦不到 ws://，会以裸路径落到这里，
		// 交给代理按"最近一次上游"接回去（飞牛的实时状态就靠这条连接）。
		if proxy.ServeFallback(w, r) {
			return
		}
		if serveUI(w, r, *wwwDir) {
			return
		}
		// 桌面入口会在应用地址后面追加自己的路径段（绿联 …/app/<name>/desktop/?os=ugospro，
		// 飞牛同类）。这类路径不对应任何静态文件，按 SPA 兜底回首页，避免 404。
		// 到这里说明这条路径既不是我们的静态文件，也不是首页：可能是上游页面自己
		// 发的绝对路径请求（SW 漏接）。有 nwg_up 就按原路径转给那台上游。
		// 首页永远发我们自己的（独立部署时 uiBase 为空，serveUI 不管这块）。
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			if writeIndex(w, *wwwDir) {
				return
			}
		}
		if proxy.ServeFallbackAny(w, r) {
			return
		}
		if !looksLikeAsset(r.URL.Path) && !isStaticFile(*wwwDir, r.URL.Path) && writeIndex(w, *wwwDir) {
			return
		}
		fs.ServeHTTP(w, r)
	}))

	srv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", *addr, *port),
		Handler:           authSvc.Wrap(stripBase(mux)),
		ReadHeaderTimeout: 15 * time.Second,
	}
	log.Printf("nas-web-gateway %s:%d data=%s www=%s insecure=%v base=%q sock=%q auth=%v",
		*addr, *port, *dataDir, *wwwDir, *insecure, uiBase, *sock, *auth)
	if *sock != "" {
		_ = os.Remove(*sock)
		ul, err := net.Listen("unix", *sock)
		if err != nil {
			log.Fatal(err)
		}
		_ = os.Chmod(*sock, 0o666)
		log.Printf("unix socket listening: %s", *sock)
		go func() {
			if err := srv.Serve(ul); err != nil && err != http.ErrServerClosed {
				log.Printf("unix serve: %v", err)
			}
		}()
	}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func injectHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	p := r.URL.Query().Get("p")
	o := r.URL.Query().Get("o")
	if p == "" {
		http.Error(w, "missing p", http.StatusBadRequest)
		return
	}
	w.Write([]byte(injectJS(p, o, r.URL.Query().Get("d"), r.URL.Query().Get("ua") == "1")))
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write([]byte(`{"ok":true}`))
}

// lanIP 返回本机第一个私网 IPv4（跳过回环）。客户端拿它拼局域网地址：
// 绿联桌面里点应用给的是包装地址，里面常带 ugapp.link:端口 或 127.0.0.1:端口，
// 而 127.0.0.1 在手机/别的机器上指的是它自己。
func lanIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() {
			continue
		}
		v4 := ipn.IP.To4()
		if v4 == nil {
			continue
		}
		if v4[0] == 192 && v4[1] == 168 || v4[0] == 10 || (v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31) {
			return v4.String()
		}
	}
	return ""
}

func infoHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	fmt.Fprintf(w, `{"ok":true,"host":%q,"prefix":%q,"lanip":%q}`, r.Host, "/gw", lanIP())
}
