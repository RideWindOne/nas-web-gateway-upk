package main

import (
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
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

// 绿联桌面给第三方应用套的那层链接包装前缀，见下方 404 兜底的说明。
const redirectWrapper = "ugreen/v1/desktop/redirect/"

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
		// 只管“等响应头”，不等 body（视频/下载/SSE 都是头先到）
		ResponseHeaderTimeout: 30 * time.Second,
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

// isAssetPath 过滤掉静态资源，只留页面级请求进日志。
func isAssetPath(p string) bool {
	lp := strings.ToLower(p)
	if strings.Contains(lp, "/assets/") || strings.Contains(lp, "/static/") {
		return true
	}
	for _, ext := range []string{".js", ".css", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".woff", ".woff2", ".ttf", ".map"} {
		if strings.HasSuffix(lp, ext) {
			return true
		}
	}
	return false
}

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

	// 记住"这个客户端最近在代理哪个上游"。没有 Service Worker 时（手机开 VPN 很常见，
	// SW 注册/接管会失败）页面里会冒出干净路径（登录后的 /login、/desktop、/p…），
	// 这些请求由浏览器直接发到网关根，网关不知道它属于哪个上游 → 落到本应用首页，
	// 或者被当成静态文件 404。有了这个 cookie，根路径上的文档请求就能被拽回
	// /api/p/<seg>/…，登录后跳 404、切标签跳首页都由这一层兜住。

	// 前缀去重：飞牛登录会把 redirect_uri 原样跳回来，而它当初记下的就是带前缀的
	// 地址，于是出现 /api/p/<seg>/api/p/<seg>/xxx，上游必然 404（表现为登录后
	// 落到“这里暂时没有装入页面”）。这里把重复的、且指向同一上游的前缀剥掉。
	for strings.HasPrefix(rest, "api/p/") {
		inner := strings.TrimPrefix(rest, "api/p/")
		nxt, after := inner, ""
		if i := strings.Index(inner, "/"); i >= 0 {
			nxt, after = inner[:i], inner[i+1:]
		}
		if o2, e2 := decodeOrigin(nxt); e2 == nil && o2 == origin {
			rest = after
			continue
		}
		break
	}

	// 我们自己的资源被页面（或注入脚本的路径归一）误补了前缀时，会落到
	// /api/p/<seg>/api/inject.js 这种地址上，再被当成上游路径转发 → 上游 404
	// → 注入脚本没加载 → 页面里所有根绝对路径都不补前缀 → 应用直接白屏。
	// 这类路径永远属于网关自身，直接踢回本机地址。
	if rest == "api/inject.js" || rest == "sw.js" || rest == "boot.html" {
		target := "/" + rest
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
		return
	}

	// 绿联桌面给第三方应用套的链接包装段 /ugreen/v1/desktop/redirect?url=<应用地址>。
	// 应用自己的 JS 会按 location.pathname 拼接口，包装段就顺着漏进请求路径
	// （.../redirect/api/xxx 或 .../redirect/api/webterminal/connect/xxx），而上游应用
	// 根本没有这层路由。这里在入口统一剥掉一次，HTTP 与 WebSocket 都不再各自兜底。
	if strings.HasPrefix(rest, redirectWrapper) {
		rest = strings.TrimPrefix(strings.TrimPrefix(rest, redirectWrapper), "/")
		if !isAssetPath(rest) {
			log.Printf("[nwg] unwrap -> /%s", rest)
		}
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
	// 查询串里的代理前缀同样要剥掉：上游前端会把 redirect_uri 之类的参数原样
	// 记下来，登录/跳转时再跳回我们自己加的 /api/p/<seg>/，那一跳落在 SPA 上
	// 就匹配不到路由，直接渲染它自己的 404 页。
	if q := sanitizeQuery(r.URL.RawQuery, marker); q != "" {
		target += "?" + q
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

	// 排查用：第三方应用窗口白屏时，从日志能直接看出它到底请求了哪个上游地址。
	// 只记录非静态资源，避免刷屏。
	if !isAssetPath(rest) {
		log.Printf("[nwg] %s %s%s dest=%q", r.Method, host, rest, r.Header.Get("Sec-Fetch-Dest"))
	}

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
	fixForwardSiteHeaders(req, u, host)

	if ck := p.jar.Get(host); ck != "" {
		req.Header.Set("Cookie", ck)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		log.Printf("[nwg] !! upstream %s%s: %v", host, rest, err)
		writeProxyError(w, prefix, err)
		return
	}

	defer resp.Body.Close()

	p.jar.Set(host, resp.Header.Values("Set-Cookie"))

	copyHeader(w.Header(), resp.Header)
	for _, h := range hopHeaders {
		w.Header().Del(h)
	}
	// 排查白屏用：把上游返回的状态码也记下来，只记非静态资源。
	if !isAssetPath(rest) || resp.StatusCode >= 400 {
		log.Printf("[nwg] <- %d %s%s", resp.StatusCode, host, rest)
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

	// JSON：把接口里返回的局域网绝对地址改写成代理前缀。
	// 飞牛 fnOS 的应用列表/应用令牌接口会直接给出 `http://172.30.2.126:16601/?token=…`，
	// 桌面点开第三方应用时这个地址被交给新窗口，手机根本连不到 → 白窗。
	// 改写成 `/api/p/<seg>/…` 后，无论窗口是内嵌 iframe 还是原生新窗口，
	// 都会落回网关自己的域名，走代理出去。
	if isJSONContentType(resp.Header.Get("Content-Type")) {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		body = gunzipBytes(body, resp.Header.Get("Content-Encoding"))
		if strings.Contains(rest, "app/eula") || strings.Contains(rest, "app/states") {
			prev := string(body)
			if len(prev) > 600 {
				prev = prev[:600]
			}
			log.Printf("[nwg] dbg %s%s hdr=%v body=%s", host, rest, dbgHeaders(r), prev)
		}
		body = rewriteLanURLs(body, marker)
		w.Header().Del("Content-Encoding")
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}

	// 其它非 HTML：原始字节流透传，一个字节都不改
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// lanOriginRe 匹配局域网绝对地址里的 scheme+host[:port] 部分。
var lanOriginRe = regexp.MustCompile(`(https?)://((?:10\.|127\.|192\.168\.|169\.254\.|172\.(?:1[6-9]|2[0-9]|3[01])\.)[0-9a-zA-Z.\-]*(?::[0-9]+)?)`)

// rewriteLanURLs 把 LAN 绝对地址换成 /api/p/<b64url(origin)>，保留其后的路径和查询串。
func rewriteLanURLs(body []byte, marker string) []byte {
	if !bytes.Contains(body, []byte("http")) {
		return body
	}
	return lanOriginRe.ReplaceAllFunc(body, func(m []byte) []byte {
		sm := lanOriginRe.FindSubmatch(m)
		if len(sm) < 3 {
			return m
		}
		origin := string(sm[1]) + "://" + string(sm[2])
		seg := base64.RawURLEncoding.EncodeToString([]byte(origin))
		return []byte(marker + seg)
	})
}

var proxyPrefixRe = regexp.MustCompile(`(?:/api/p/|%2Fapi%2Fp%2F)[A-Za-z0-9_-]+(?:/|%2F)?`)

// sanitizeQuery 把查询串里泄漏的代理前缀抹掉，其余参数原样保留。
// 只处理含 marker 的值，不动别的参数，避免误伤上游业务字段。
func sanitizeQuery(raw, marker string) string {
	if raw == "" || !strings.Contains(raw, "api") {
		return raw
	}
	parts := strings.Split(raw, "&")
	out := make([]string, 0, len(parts))
	for _, kv := range parts {
		if kv == "" {
			continue
		}
		k := kv
		v := ""
		if i := strings.Index(kv, "="); i >= 0 {
			k, v = kv[:i], kv[i+1:]
		}
		if strings.Contains(v, "api/p") || strings.Contains(strings.ToLower(v), "%2fapi%2fp%2f") {
			if dv, err := url.QueryUnescape(v); err == nil {
				dv = proxyPrefixRe.ReplaceAllString(dv, "")
				v = url.QueryEscape(dv)
			} else {
				v = proxyPrefixRe.ReplaceAllString(v, "")
			}
		}
		out = append(out, k+"="+v)
	}
	return strings.Join(out, "&")
}

// isJSONContentType 判断是否 JSON（跳过 SSE 等流式类型）。
// fixForwardSiteHeaders 把浏览器的 Origin / Referer 换回上游自己的站点。
// Lucky 这类面板会校验 Origin（不是同源 POST 直接 403），飞牛的部分接口也看 Referer；
// 而浏览器发给网关的 Origin 是我们的地址（如 http://172.30.2.231:9999），所以要改回去。
func fixForwardSiteHeaders(req *http.Request, upstream *url.URL, host string) {
	if upstream == nil {
		return
	}
	sch := upstream.Scheme
	if sch == "" {
		sch = "http"
	}
	self := sch + "://" + host

	if o := req.Header.Get("Origin"); o != "" {
		if ou, err := url.Parse(o); err == nil && !strings.EqualFold(ou.Host, host) {
			req.Header.Set("Origin", self)
		}
	}
	if ref := req.Header.Get("Referer"); ref != "" {
		if ru, err := url.Parse(ref); err == nil && !strings.EqualFold(ru.Host, host) {
			p := ru.EscapedPath()
			if p == "" {
				p = "/"
			}
			if ru.RawQuery != "" {
				p += "?" + ru.RawQuery
			}
			req.Header.Set("Referer", self+p)
		}
	}
}

func isJSONContentType(ct string) bool {
	lc := strings.ToLower(ct)
	return strings.Contains(lc, "application/json") || strings.Contains(lc, "+json")
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
	if ou, errU := url.Parse(origin); errU == nil {
		fixForwardSiteHeaders(req, ou, host)
	}
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
	fixForwardSiteHeaders(req, u, host)
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
			log.Printf("[nwg] !! ws upstream %s: %v", host, err)
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

// dbgHeaders 挑出和鉴权相关的请求头，用来判断应用中心到底把令牌放在哪。
func dbgHeaders(r *http.Request) map[string]string {
	out := map[string]string{}
	for k, v := range r.Header {
		lk := strings.ToLower(k)
		if lk == "cookie" || strings.Contains(lk, "token") || strings.Contains(lk, "auth") || strings.Contains(lk, "appmgr") {
			if len(v) > 0 {
				val := v[0]
				if len(val) > 80 {
					val = val[:80] + "…"
				}
				out[k] = val
			}
		}
	}
	return out
}
