package main

import (
	_ "embed"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"net/url"
	"regexp"
	"strings"
)

var (
	reBase     = regexp.MustCompile(`(?i)<base[^>]*>`)
	reHead     = regexp.MustCompile(`(?i)<head[^>]*>`)
	reAttr     = regexp.MustCompile(`(?i)(href|src|action|background|poster|data-src)\s*=\s*(["'])/([^/"'])`)
	reAttrRoot = regexp.MustCompile(`(?i)(href|src|action)\s*=\s*(["'])/(["'])`)
	reCSSURL   = regexp.MustCompile(`(?i)url\(\s*(["']?)/([^/"'])`)
	// 以 "../" 开头的相对引用（很多老式设备 Web（IPC、老路由器）大量这么写）。
	// 文档会被加上 <base href="前缀+目录">，此时 "../x" 会跨出前缀解析成
	// /api/p/x 而 404；必须按“上游文档目录”先算成根路径再补前缀。
	reAttrDot  = regexp.MustCompile(`(?i)(href|src|action|background|poster|data-src)\s*=\s*(["'])((?:\.\./)+)([^"']*)`)
	reCSSDot   = regexp.MustCompile(`(?i)url\(\s*(["']?)((?:\.\./)+)([^"')]*)`)
	// 正文里 URL 编码过的代理前缀（%2Fapi%2Fp%2F<seg>%2F，大小写混排也算）。
	reEncPrefix = regexp.MustCompile(`(?i)%2Fapi%2Fp%2F[A-Za-z0-9_-]+(?:%2F)?`)
)

// rewriteHTML 做三件事，页面结构与正文都不动：
//  1. 注入 <base href="前缀/">，让相对路径自动落到代理下
//  2. 把根绝对路径（/cgi-bin/xxx 这类）补上代理前缀
//  3. 注入运行期路径拦截脚本，兜住 JS 里拼出来的根绝对路径
// bindScript 让被代理的页面在装载后告诉 Service Worker "这个标签属于哪个上游"。
// SW 收到后按 clientId 记住，之后页面跳到干净的 /login 这类根路径时，
// 也能凭 clientId 把请求转回正确的上游。
//
// 顺带做地址净化：只要 SW 已经在控制这个标签，就把它从带前缀的路径
// （/api/p/<seg>/xxx）换到干净路径（/xxx）。否则 SPA 的前端路由读到的是
// "/api/p/<seg>/"，一条路由都匹配不上，直接渲染自己的 404 页——飞牛登录后
// 跳回 redirect_uri 落到前缀上就是这个现象。
func bindScript(prefix string) string {
	return `(function(){try{if(!navigator.serviceWorker)return;var P="` + prefix + `";` +
		`var go=function(){var c=navigator.serviceWorker.controller;if(!c)return;` +
		`try{c.postMessage({__nwg:"bind",prefix:P});}catch(e){}` +
		`if(location.pathname.indexOf(P)===0){` +
		`var tail=location.pathname.slice(P.length);` +
		`var clean=(tail.charAt(0)==="/"?"":"/")+tail+location.search;` +
		`location.replace(clean);}else{` +
		`var q=location.search;if(q.indexOf("__nwg=")>=0){` +
		`var qs=q.replace(/[?&]__nwg=[^&]*/,"").replace(/^&/,"?");` +
		`try{history.replaceState(null,"",location.pathname+qs+location.hash);}catch(e){}}}};` +
		`if(navigator.serviceWorker.controller){go();}` +
		`else{navigator.serviceWorker.ready.then(go).catch(function(){});}` +
		`navigator.serviceWorker.addEventListener("controllerchange",go);` +
		`}catch(e){}})();`
}

// injectJS 生成注入脚本的完整源码，由 /api/inject.js 单独下发。
//go:embed assets/inject.tpl.js
var injectTpl string

// injectJS 由 /api/inject.js 下发：单一模板 + 占位符替换，不再定点插补丁。
// 地址规则只在模板的 resolve() 里定义一次，出口只在 INSTALLERS 表里安装一次。
func injectJS(prefix, origin, baseDir string, desktop bool) string {
	uaMode := "0"
	if desktop {
		uaMode = "1"
	}
	if baseDir == "" {
		baseDir = "/"
	}
	code := injectTpl
	code = strings.ReplaceAll(code, "__NWG_PREFIX__", prefix)
	code = strings.ReplaceAll(code, "__NWG_ORIGIN__", origin)
	code = strings.ReplaceAll(code, "__NWG_UA_MODE__", uaMode)
	code = strings.ReplaceAll(code, "__NWG_BASE__", baseDir)
	return code + "\n" + bindScript(prefix)
}

func rewriteHTML(body []byte, prefix, baseDir, origin string, desktop bool) []byte {
	body = reBase.ReplaceAll(body, nil)
	body = reAttr.ReplaceAll(body, []byte("${1}=${2}"+prefix+"/${3}"))
	body = reAttrRoot.ReplaceAll(body, []byte("${1}=${2}"+prefix+"/${3}"))
	body = reCSSURL.ReplaceAll(body, []byte("url(${1}"+prefix+"/${2}"))
	// "../" 必须放在最后：它的输出本身就带前缀，若先跑，后面那条
	// “/xxx 补前缀”的规则会把前缀再加一遍，变成 /api/p/<seg>/api/p/<seg>/xxx。
	body = reAttrDot.ReplaceAllFunc(body, func(m []byte) []byte {
		sub := reAttrDot.FindSubmatch(m)
		if len(sub) < 5 {
			return m
		}
		return []byte(string(sub[1]) + "=" + string(sub[2]) + prefix + collapseDotDot(baseDir, string(sub[3]), string(sub[4])))
	})
	body = reCSSDot.ReplaceAllFunc(body, func(m []byte) []byte {
		sub := reCSSDot.FindSubmatch(m)
		if len(sub) < 4 {
			return m
		}
		return []byte("url(" + string(sub[1]) + prefix + collapseDotDot(baseDir, string(sub[2]), string(sub[3])))
	})

	// base 必须指向“文档所在目录”而不是上游根：
	// 飞牛 /p、/login 这类根路径应用目录就是 "/"，两者等价；
	// 但 /app/EasyTier-Web/ 这种子目录应用用 ./assets/index.js 相对引用，
	// 指到根会解析成 /assets/index.js → 404 → 白屏（标题照旧正确）。
	baseHref := prefix + "/"
	if baseDir != "" && baseDir != "/" {
		baseHref += strings.TrimPrefix(baseDir, "/")
	}

	uaMode := "0"
	if desktop {
		uaMode = "1"
	}
	// 只用外部脚本：内联 <script> 的源码在某些页面（Unraid 这类把
	// 头部内容当文本渲染的模板）会被当成正文显示出来，页面上就会冒出一大段代码。
	// 现在页面里只有一个 src，脚本体由 /api/inject.js 单独下发。
	inj := []byte(`<base href="` + baseHref + `">` +
		`<script src="/api/inject.js?p=` + url.QueryEscape(prefix) + `&ua=` + uaMode +
		`&o=` + url.QueryEscape(origin) + `&d=` + url.QueryEscape(baseDir) + `"></script>`)
	// 先清洗上游正文里的前缀字面量，再插注入脚本：注入脚本自己的 src/base
	// 里本来就带着前缀，顺序反了会把它们也抹成 "/"，整页资源全 404。
	body = stripPrefixInBody(body, prefix)
	if loc := reHead.FindIndex(body); loc != nil {
		out := make([]byte, 0, len(body)+len(inj))
		out = append(out, body[:loc[1]]...)
		out = append(out, inj...)
		out = append(out, body[loc[1]:]...)
		return out
	}
	return append(inj, body...)
}

// stripPrefixInBody 把正文里“写死”的代理前缀抹掉。
//
// 为什么必须做：飞牛这类 SPA 会把 redirect_uri（有时还有其它回跳参数）**预先生成**
// 写进 HTML / 内嵌 JSON，而不是运行时读 location.search。我们注入脚本能清洗
// location.search，却管不到正文里的字面量，于是登出/超时后它照着
// /api/p/<seg>/ 跳回来 → 前端路由匹配不上 → 它自己的 404 页（"飞牛打不开"）。
// 这里同时处理三种写法：裸路径、URL 编码（%2Fapi%2Fp%2F...）、斜杠再编码。
func stripPrefixInBody(body []byte, prefix string) []byte {
	if prefix == "" || !bytes.Contains(body, []byte("api")) {
		return body
	}
	seg := prefix[strings.LastIndex(prefix, "/")+1:]
	if seg == "" {
		return body
	}
	// 只处理 **URL 编码** 形态（%2Fapi%2Fp%2F<seg>%2F）。裸路径形态绝对不能碰：
	// 正文里的 /luci-static/... 这类根相对地址是 reAttr 刚补好前缀的，
	// 再抹掉前缀它们就会解析到应用根 → 图标、样式全 404（"QWRT 图标全碎"）。
	repls := [][2]string{
		{strings.ReplaceAll(prefix, "/", "%2F") + "%2F", "/"},
		{strings.ReplaceAll(prefix, "/", "%2f") + "%2f", "/"},
	}
	for _, r := range repls {
		body = bytes.ReplaceAll(body, []byte(r[0]), []byte(r[1]))
	}
	// 大小写混排的编码形式（%2fapi%2fp%2f...%2F）用一次正则兜住，
	// 否则同一串里大小写不一致就漏。
	body = reEncPrefix.ReplaceAll(body, []byte("/"))
	return body
}

// collapseDotDot 把 "../" 前缀按上游文档目录（baseDir）折算成根路径。
// 例：baseDir="/"、dots="../"、rest="web-static/a.css" → "/web-static/a.css"
func collapseDotDot(baseDir, dots, rest string) string {
	var segs []string
	for _, s := range strings.Split(strings.Trim(baseDir, "/"), "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	for i := 0; i < strings.Count(dots, "../"); i++ {
		if len(segs) > 0 {
			segs = segs[:len(segs)-1]
		}
	}
	p := "/"
	if len(segs) > 0 {
		p = "/" + strings.Join(segs, "/") + "/"
	}
	return p + rest
}

// localizeRelayHost：把 UGREENlink 的远程域名（app-xxxx.ugapp.link:端口）换回本机地址。
// 绿联云桌面上的“打开应用”会给出 https://app-xxxx.ugapp.link:16601 这类远程地址，
// 手机/浏览器拿到它只能去公网连那个端口，必然打不开；而这个端口在 NAS 本机上就是
// 同一个端口，代理又是从 NAS 发出去的，所以改写成 127.0.0.1:端口 就能直接转通。
func localizeRelayHost(u *url.URL) *url.URL {
	if u == nil {
		return u
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasSuffix(host, ".ugapp.link") {
		return u
	}
	port := u.Port()
	if port == "" || port == "443" {
		return u
	}
	nu := *u
	nu.Scheme = "http"
	nu.Host = "127.0.0.1:" + port
	return &nu
}

func rewriteLocation(loc, origin, prefix, marker string) string {
	if loc == "" {
		return loc
	}
	if strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") {
		// 跳转地址里的查询串也要剥前缀：飞牛 nginx 会把 "/" 302 到
		// /login?redirect_uri=<当时的完整地址>，那个地址是我们带前缀的，
		// 原样带回去就变成 /login?redirect_uri=/api/p/<seg>/，登录页拿到
		// 这个值后登录成功照着跳 → 落回前缀 → SPA 自己的 404。
		if i := strings.Index(loc, "?"); i > 0 {
			if q := sanitizeQuery(loc[i+1:], marker); q != "" {
				loc = loc[:i] + "?" + q
			} else {
				loc = loc[:i]
			}
		}
		return prefix + loc
	}

	u, err := url.Parse(loc)
	if err != nil || u.Host == "" {
		return loc
	}

	rest := u.EscapedPath()
	if rest == "" {
		rest = "/"
	}
	if u.RawQuery != "" {
		if q := sanitizeQuery(u.RawQuery, marker); q != "" {
			rest += "?" + q
		}
	}

	o, errO := url.Parse(origin)
	if errO == nil && u.Scheme == o.Scheme && u.Host == o.Host {
		return prefix + rest
	}

	scheme := u.Scheme
	if scheme == "" {
		scheme = "http"
	}
	// 远程域名带非 443 端口时，先换回 127.0.0.1 的同端口地址再编码前缀。
	if lu := localizeRelayHost(u); lu != u {
		scheme = lu.Scheme
		u = lu
	}
	newPrefix := marker + base64.RawURLEncoding.EncodeToString([]byte(scheme+"://"+u.Host))
	return newPrefix + rest
}

func isHTMLContentType(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}

func gunzipBytes(body []byte, encoding string) []byte {
	if !strings.Contains(strings.ToLower(encoding), "gzip") || len(body) == 0 {
		return body
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return body
	}
	defer zr.Close()
	b, err := io.ReadAll(zr)
	if err != nil {
		return body
	}
	return b
}
