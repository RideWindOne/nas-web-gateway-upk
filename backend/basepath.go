package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// uiBase 是本应用对外暴露时的路径前缀。
//
// 绿联里应用挂在根路径上，uiBase == ""；飞牛桌面走统一网关（Unix socket + gatewayPrefix），
// 浏览器看到的是 https://<飞牛>/app/naswebgateway/…，于是所有对外生成的根绝对路径
// （页面资源、/api/p/ 前缀、注入脚本、boot.html 跳转）都必须带上这一层，
// 否则会被飞牛当成它自己的接口而 404。
var uiBase string

// withBase 给内部路径补上 uiBase；已经是绝对外链或已带前缀的原样返回。
func withBase(p string) string {
	if uiBase == "" || p == "" || !strings.HasPrefix(p, "/") {
		return p
	}
	if strings.HasPrefix(p, uiBase+"/") || p == uiBase {
		return p
	}
	return uiBase + p
}

// stripBase 把统一网关转发进来时的前缀去掉，让 mux 仍按根路径路由。
// 同时把 base 写进 window，前端据此拼 /api/p/ 等根绝对路径。
func stripBase(h http.Handler) http.Handler {
	if uiBase == "" {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasPrefix(p, uiBase+"/"):
			r.URL.Path = strings.TrimPrefix(p, uiBase)
		case p == uiBase || p == uiBase+"/":
			r.URL.Path = "/"
		}
		h.ServeHTTP(w, r)
	})
}

// serveUI 返回首页时把根绝对路径补上 uiBase，并注入 window.__NWG_BASE__。
// 只有首页需要改写：其余静态资源都是被首页引用的具体路径，补过前缀后自然命中。
func serveUI(w http.ResponseWriter, r *http.Request, wwwDir string) bool {
	if uiBase == "" {
		return false
	}
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		return false
	}
	return writeIndex(w, wwwDir)
}

// writeIndex 输出改写过根绝对路径的首页，不做路径判断：
// 桌面（绿联 /app/<name>/desktop/?os=ugospro、飞牛同类追加段）会在应用地址后面
// 挂上自己的路径后缀，这些后缀不属于任何静态文件，也不该 404——回首页即可。
func writeIndex(w http.ResponseWriter, wwwDir string) bool {
	b, err := os.ReadFile(filepath.Join(wwwDir, "index.html"))
	if err != nil {
		return false
	}
	b = bytes.ReplaceAll(b, []byte(`href="/`), []byte(`href="`+uiBase+`/`))
	b = bytes.ReplaceAll(b, []byte(`src="/`), []byte(`src="`+uiBase+`/`))
	// 老版本在这里无条件塞 window.__NWG_NO_SW__=true，等于把 Service Worker 整个关掉：
	// 于是 iframe 只能落到 /api/p/<seg>/ 的带前缀地址上，SPA 路由匹配不上 → 自己的 404；
	// 页面里的绝对跳转又会绕出前缀 → 被当成"上一层的会话"恢复 → 套娃。
	// v2 的接管完全依赖 SW（只剩一个明文端口），这一行必须去掉。
	inject := []byte(`<base href="` + uiBase + `/"><script>window.__NWG_BASE__="` + uiBase + `";</script>`)
	if i := bytes.Index(b, []byte("<head>")); i >= 0 {
		i += len("<head>")
		b = append(b[:i], append(inject, b[i:]...)...)
	} else {
		b = append(inject, b...)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
	return true
}

// isStaticFile 判断请求路径是否命中 www 目录下的真实文件（用于区分“要资源”还是“被追加了路径段”）。
func isStaticFile(wwwDir, urlPath string) bool {
	if urlPath == "" || strings.Contains(urlPath, "..") {
		return false
	}
	fi, err := os.Stat(filepath.Join(wwwDir, filepath.FromSlash(strings.TrimPrefix(urlPath, "/"))))
	return err == nil && !fi.IsDir()
}

// looksLikeAsset 判断路径最后一段是否带扩展名（.js/.css/.png…）。
// 带扩展名的一律按资源处理，取不到就 404，不能回首页冒充成功。
func looksLikeAsset(urlPath string) bool {
	base := urlPath
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	return strings.Contains(base, ".")
}
