package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
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
	port := flag.Int("port", 21010, "HTTP listen port, must match the port declared in project.yaml")
	wwwDir := flag.String("www", "", "static dir (defaults to $UGAPP_INSTALL_DIR/www)")
	dataDir := flag.String("data", "", "data dir (defaults to $UGAPP_DATA_DIR)")
	insecure := flag.Bool("insecure", true, "skip TLS verification for LAN devices with self-signed certs")
	flag.Parse()

	// UGOS 把进程 CWD 设成 appdata 目录，所以所有路径都必须从环境变量推导成绝对路径。
	// 注意：unit 里有 TemporaryFileSystem=/:ro，只有 data/cache/log/shared 被 BindPaths
	// 放行，所以数据只能落在 UGAPP_DATA_DIR 之内，不能自选 /volume1 其他目录。
	installDir := os.Getenv("UGAPP_INSTALL_DIR")

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
		if installDir != "" {
			*wwwDir = filepath.Join(installDir, "www")
		} else {
			*wwwDir = "./rootfs_common/www"
		}
	}
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("create data dir %s: %v", *dataDir, err)
	}

	store := NewStore(*dataDir)
	jar := NewCookieJar(filepath.Join(*dataDir, "cookies.json"))
	proxy := NewProxy(jar, *insecure)

	mux := http.NewServeMux()

	// 网关把 proxy_path(/api) 之下的请求转发到这里；/api/history 等精确路由
	// 优先于 /api/p/ 前缀，两者不冲突。
	// /__p/ 保留给本机直连调试（不经过网关时用）。
	mux.HandleFunc("/api/p/", proxy.ServeHTTP)
	mux.HandleFunc("/__p/", proxy.ServeHTTP)
	mux.HandleFunc("/api/relay", proxy.RelayHandler)
	mux.HandleFunc("/api/history", store.HistoryHandler())
	mux.HandleFunc("/api/fav", store.FavHandler())
	mux.HandleFunc("/api/apps", store.AppsHandler())
	mux.HandleFunc("/api/info", infoHandler)
	mux.HandleFunc("/api/health", healthHandler)

	mux.Handle("/", http.FileServer(http.Dir(*wwwDir)))

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", *port),
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
	}
	log.Printf("nas-web-gateway :%d data=%s www=%s insecure=%v", *port, *dataDir, *wwwDir, *insecure)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write([]byte(`{"ok":true}`))
}

func infoHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	fmt.Fprintf(w, `{"ok":true,"host":%q,"prefix":%q}`, r.Host, "/gw")
}
