package main

import (
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// desktopUA：上游一律按桌面浏览器对待。绿联云(9999)、LuCI 这类后台会按 UA
// 直接拒绝移动端，而这一层本来就跑在 NAS 上、由桌面式界面承载。
const desktopUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// mobileUA：设置里关掉「电脑版」后按移动端身份访问上游。
// 前端通过 nwg_ua 这个本域 cookie 传递开关（"desktop" 默认 / "mobile" 关闭）。
const mobileUA = "Mozilla/5.0 (Linux; Android 13; SM-S918B) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36"

// applyUA 统一改写出站请求的 UA 与 UA-CH（Client Hints）。
// 只换 User-Agent 不够：手机 Chrome 会带 sec-ch-ua-mobile: ?1，上游站点读到
// 这个就直接把请求判成手机，绿联云桌面因此仍会跳到 check-mobile 拦截页。
func applyUA(h http.Header, desktop bool) {
	if desktop {
		h.Set("User-Agent", desktopUA)
		h.Set("Sec-CH-UA-Mobile", "?0")
		h.Set("Sec-CH-UA-Platform", `"Windows"`)
		h.Del("Sec-CH-UA-Model")
		h.Del("Sec-CH-UA-Platform-Version")
		return
	}
	h.Set("User-Agent", mobileUA)
}

func uaMode(r *http.Request) bool {
	if r.Header.Get("X-Nwg-Force-UA") == "desktop" {
		return true
	}
	ck, err := r.Cookie("nwg_ua")
	return err == nil && ck.Value == "desktop"
}

func uaFor(r *http.Request) string {
	if uaMode(r) {
		return desktopUA
	}
	return mobileUA
}

// uaModeFromHeader 给 ReverseProxy 的 Director 用：那里只剩克隆出来的请求头。
func uaModeFromHeader(h string) bool {
	for _, part := range strings.Split(h, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 && kv[0] == "nwg_ua" && kv[1] == "desktop" {
			return true
		}
	}
	return false
}

type Proxy struct {
	jar    *CookieJar
	client *http.Client
	tr     *http.Transport // WS 升级要用同一套 Transport
}

var hopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func NewProxy(jar *CookieJar, insecure bool) *Proxy {
	tr := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: insecure},
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true, // 自己控制压缩，才能改写 HTML
		ForceAttemptHTTP2:     false,
	}
	return &Proxy{
		jar: jar,
		tr:  tr,
		client: &http.Client{
			Transport: tr,
			Timeout:   0, // 视频与固件下载需要长连接
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse // 交给浏览器，Location 由我们重写
			},
		},
	}
}

// ServeHTTP 处理 /api/p/<b64origin>/<rest>
//
// 为什么要挂在 /api 下面：UGOS 网关把 project.yaml 的 proxy_path 原样翻译成
// nginx 的 `location ~ ^<proxy_path>/`（见 /etc/nginx/server.d/{appid}.conf），
// 只有落在 proxy_path 之内的请求才会被 proxy_pass 到后端 socket；
// 其余路径网关直接从 /ugreen/www/{appid} 静态发。
// proxy_path 声明为 "/" 时生成的是 `^//`，等于什么都匹配不到，所以必须
// 用一个真实前缀，把静态页面（/、/css、/js）和代理流量分开。
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	marker := ""
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/p/"):
		marker = "/api/p/"
	case strings.HasPrefix(r.URL.Path, "/__p/"): // 本机直连调试用
		marker = "/__p/"
	default:
		http.NotFound(w, r)
		return
	}

	raw := strings.TrimPrefix(r.URL.Path, marker)
	seg, rest := raw, ""
	if i := strings.Index(raw, "/"); i >= 0 {
		seg, rest = raw[:i], raw[i+1:]
	}

	origin, err := decodeOrigin(seg)
	if err != nil {
		http.Error(w, "bad target: "+err.Error(), http.StatusBadRequest)
		return
	}

	// seg 里带 "d|" 标记 = 这个应用要求电脑版 UA；用内部请求头传给下游，
	// 出站前会在 Director 里删掉，不会带到上游。
	if isDesktopSeg(seg) {
		r.Header.Set("X-Nwg-Force-UA", "desktop")
	}

	target := origin + "/"
	if rest != "" {
		target = origin + "/" + rest
	}
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	prefix := marker + seg

	// 文档所在目录，供 <base href> 使用（见 rewriteHTML 里的说明）。
	baseDir := "/"
	if rest != "" {
		if strings.HasSuffix(rest, "/") {
			baseDir = rest
		} else if i := strings.LastIndex(rest, "/"); i >= 0 {
			baseDir = rest[:i+1]
		}
	}

	u, err := url.Parse(origin)
	if err != nil {
		http.Error(w, "bad origin", http.StatusBadRequest)
		return
	}
	host := u.Host

	// WebSocket 与任何 Upgrade 请求走 hijack 双向转发。
	// 飞牛 fnOS 之类的面板靠 WS 维持会话，只搬 HTTP 会让页面一直提示"连接已断开"。
	if isWebSocket(r) {
		p.serveWS(w, r, target, host)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.ContentLength = r.ContentLength

	copyHeader(req.Header, r.Header)
	for _, h := range hopHeaders {
		req.Header.Del(h)
	}
	req.Header.Del("Host")
	req.Header.Del("Accept-Encoding")
	req.Header.Set("Accept-Encoding", "gzip")
	applyUA(req.Header, uaMode(r))
	// 让上游以为这是一次直连，绕开固件对 Host 的校验
	req.Host = host

	if ck := p.jar.Get(host); ck != "" {
		req.Header.Set("Cookie", ck)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		writeProxyError(w, prefix, err)
		return
	}
	defer resp.Body.Close()

	p.jar.Set(host, resp.Header.Values("Set-Cookie"))

	copyHeader(w.Header(), resp.Header)
	for _, h := range hopHeaders {
		w.Header().Del(h)
	}
	// 这些头会阻止页面被嵌进 iframe，或者把上游的安全策略强套到网关域名上
	w.Header().Del("Set-Cookie")
	w.Header().Del("X-Frame-Options")
	w.Header().Del("Frame-Options")
	w.Header().Del("Content-Security-Policy")
	w.Header().Del("Content-Security-Policy-Report-Only")
	w.Header().Del("Strict-Transport-Security")
	w.Header().Del("Public-Key-Pins")
	w.Header().Del("Content-Length")

	if loc := resp.Header.Get("Location"); loc != "" {
		w.Header().Set("Location", rewriteLocation(loc, origin, prefix, marker))
	}

	if isHTMLContentType(resp.Header.Get("Content-Type")) {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		body = gunzipBytes(body, resp.Header.Get("Content-Encoding"))

		status := resp.StatusCode
		// SPA fallback：飞牛 fnOS 这类前端路由（/desktop、/home…）服务端没配
		// try_files，直连也会 404。这里用根路径重取一次，等价于 nginx 的
		// `try_files $uri /index.html`，否则登录后一跳路由就掉进“404 未装入页面”。
		if status == http.StatusNotFound {
			if rootBody, ok := p.fetchRootHTML(r, origin, host); ok {
				body = rootBody
				status = http.StatusOK
				w.Header().Del("Content-Encoding")
			}
		}

		body = rewriteHTML(body, prefix, baseDir, origin, uaMode(r))
		w.Header().Del("Content-Encoding")

		if clientAcceptsGzip(r) && len(body) > 512 {
			w.Header().Set("Content-Encoding", "gzip")
			w.WriteHeader(status)
			gw := gzip.NewWriter(w)
			gw.Write(body)
			gw.Close()
			return
		}
		w.WriteHeader(status)
		w.Write(body)
		return
	}

	// 非 HTML：原始字节流透传，一个字节都不改
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

var errBadTarget = errors.New("target must start with http:// or https://")

// fetchRootHTML 用根路径重取一次 HTML，供 SPA fallback 使用。
// 直接沿用原请求的方法和头（除 hop 头），以保证登录态与语言偏好一致。
func (p *Proxy) fetchRootHTML(r *http.Request, origin, host string) ([]byte, bool) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, origin+"/", nil)
	if err != nil {
		return nil, false
	}
	copyHeader(req.Header, r.Header)
	for _, h := range hopHeaders {
		req.Header.Del(h)
	}
	req.Header.Del("Host")
	req.Header.Del("Accept-Encoding")
	req.Header.Set("Accept-Encoding", "gzip")
	applyUA(req.Header, uaMode(r))
	req.Host = host
	if ck := p.jar.Get(host); ck != "" {
		req.Header.Set("Cookie", ck)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if !isHTMLContentType(resp.Header.Get("Content-Type")) {
		return nil, false
	}
	p.jar.Set(host, resp.Header.Values("Set-Cookie"))

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	raw = gunzipBytes(raw, resp.Header.Get("Content-Encoding"))
	if len(raw) == 0 {
		return nil, false
	}
	return raw, true
}

// desktopSegPrefix 是 seg 里的“电脑版”标记：前端把 "d|" 拼在 origin 前面再 base64，
// 这样 /api/p/<seg>/... 这一整条前缀天然携带 UA 意图，页面里所有改写出来的地址
// 都会继续带标记，不需要额外传参就能让整个站点的子资源都走电脑 UA。
const desktopSegPrefix = "d|"

// isDesktopSeg 判断 seg 是否携带“电脑版”标记。
func isDesktopSeg(seg string) bool {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	return err == nil && bytes.HasPrefix(b, []byte(desktopSegPrefix))
}

func decodeOrigin(seg string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return "", err
	}
	s := strings.TrimPrefix(string(b), desktopSegPrefix)
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return "", errBadTarget
	}
	if !strings.Contains(s, "://") || len(s) > 512 {
		return "", errBadTarget
	}
	return s, nil
}

func isWebSocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// serveWS 转发 WebSocket 升级。ReverseProxy 拿到 101 后接管底层连接做双向拷贝，
// nginx 那边已经透传 Upgrade / Connection（proxy_http_version 1.1 + proxy_read_timeout 3600s）。
func (p *Proxy) serveWS(w http.ResponseWriter, r *http.Request, target, host string) {
	u, err := url.Parse(target)
	if err != nil {
		http.Error(w, "bad ws target", http.StatusBadRequest)
		return
	}

	origCookie := r.Header.Get("Cookie")
	forceDesk := r.Header.Get("X-Nwg-Force-UA") == "desktop"
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = u.Scheme
			req.URL.Host = u.Host
			req.URL.Path = u.Path
			req.URL.RawQuery = u.RawQuery
			// 上游按 Host 做校验，这里还原成目标自己的主机名
			req.Host = host
			req.Header.Del("Ugreen-App-Proxy-Target")
			req.Header.Del("Ugreen-App-Proxy-Type")
			req.Header.Del("X-Nwg-Force-UA")
			applyUA(req.Header, forceDesk || uaModeFromHeader(origCookie))
			if ck := p.jar.Get(host); ck != "" {
				req.Header.Set("Cookie", ck)
			}
		},
		Transport:     p.tr,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "ws upstream unreachable: "+err.Error(), http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func clientAcceptsGzip(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept-Encoding")), "gzip")
}

func writeProxyError(w http.ResponseWriter, prefix string, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	msg := strings.ReplaceAll(err.Error(), "`", "'")
	var buf bytes.Buffer
	buf.WriteString(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>无法连接</title>
<style>body{margin:0;height:100vh;display:flex;align-items:center;justify-content:center;
background:#0f1115;color:#e6e8eb;font-family:-apple-system,"PingFang SC",system-ui,sans-serif}
.box{max-width:560px;padding:32px;border:1px solid #2a2f38;border-radius:14px;background:#161a21}
h2{margin:0 0 12px;font-size:18px;font-weight:600}
p{margin:8px 0;color:#9aa4b2;font-size:13px;line-height:1.7}
code{background:#0b0e12;padding:2px 6px;border-radius:4px;color:#7fb3ff;font-size:12px;word-break:break-all}
.err{color:#6b7280;font-size:12px;margin-top:14px}</style></head><body><div class="box">
<h2>无法连接到目标设备</h2>
<p>请确认这台设备已开机，并且与 NAS 处在同一局域网。</p>
<p><code>` + prefix + `</code></p>
<p class="err">` + msg + `</p>
</div><script>(function(){try{var q=location.search;if(q.indexOf("__nwg=")<0)return;var s=q.replace(/[?&]__nwg=[^&]*/,"");history.replaceState(null,"",location.pathname+s+location.hash);}catch(e){}})();</script></body></html>`)
	w.Write(buf.Bytes())
}
