package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

// PureProxy v2：只做透明转发。
//   /api/p/<b64url(origin)>/<rest>  →  <origin>/<rest>
//
// 不重写 HTML、不注入 base、不拦自引用，页面以为自己在 origin 根路径下运行；
// Service Worker 把"干净路径"的请求送到这里，由本层 1:1 交给上游。
// 另外兜住 WebSocket：SW 拦不到 ws://（浏览器不把 WS 交给 fetch），
// 这类请求以裸路径打到网关根，用最近一次代理响应种下的 nwg_up cookie 接回来。
type PureProxy struct {
	rp  *httputil.ReverseProxy
	jar *CookieJar
}

type pureTargetKey struct{}

// redirectRT 在服务端把同源 3xx 跟完，再把最终响应交给浏览器。
//
// 为什么必须跟：Service Worker 的 fetch 只转发它自己那一次请求，上游 302/307
// 跳到的路径（绿联 / → /desktop/?os=ugospro）会由浏览器重新发起，而这条新请求
// 没有"属于哪个上游"的上下文，落回网关根 → 变成我们自己的首页（就是"套娃"）。
// 跟完之后浏览器只看到 200，什么都不用重发。每一跳的 Set-Cookie 都收进 jar，
// 跨源的 Location 不跟，交回前端。
type redirectRT struct {
	base http.RoundTripper
	jar  *CookieJar
}

func isRedirectCode(c int) bool {
	switch c {
	case 301, 302, 303, 307, 308:
		return true
	}
	return false
}

func (t *redirectRT) RoundTrip(req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error
	for hop := 0; hop < 6; hop++ {
		if c := t.jar.Get(cookieKey(req.URL.Host)); c != "" {
			req.Header.Set("Cookie", c)
		}
		resp, err = t.base.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		if sc := resp.Header.Values("Set-Cookie"); len(sc) > 0 {
			t.jar.Set(cookieKey(req.URL.Host), sc)
			resp.Header.Del("Set-Cookie")
		}
		if !isRedirectCode(resp.StatusCode) {
			return resp, nil
		}
		loc := resp.Header.Get("Location")
		if loc == "" {
			return resp, nil
		}
		u, perr := req.URL.Parse(loc)
		if perr != nil {
			return resp, nil
		}
		// 同 endpoint 的跳转不在这里跟：那是"站点入口 → 具体页"（绿联 / → /desktop/），
		// 跟了会让页面里的相对路径按旧根解析而全 404，交给 X-NWG-Redirect 处理。
		// 这里只跟同主机名、端口/协议变了的跳转（172.30.2.231 → 172.30.2.231:9999），
		// 否则 Location 指到裸内网地址，浏览器直接跳出代理、SW 里还会拿到
		// status 0 的 opaque-redirect 而报 RangeError。
		if !strings.EqualFold(u.Hostname(), req.URL.Hostname()) || strings.EqualFold(u.Host, req.URL.Host) {
			return resp, nil
		}
		_ = resp.Body.Close()
		next := req.Clone(req.Context())
		next.URL = u
		next.Host = u.Host
		next.Header = req.Header.Clone()
		next.Body = req.Body
		if resp.StatusCode == 303 || ((resp.StatusCode == 301 || resp.StatusCode == 302) && req.Method == http.MethodPost) {
			next.Method = http.MethodGet
			next.Body = nil
			next.Header.Del("Content-Type")
			next.Header.Del("Content-Length")
		}
		req = next
	}
	return resp, nil
}
type pureSegKey struct{}
type pureDeskKey struct{}

// 上游的"请使用 PC 浏览器"提示是页面自己读 navigator.userAgent 判的，只改
// 服务端请求头 UA 对它无效；客户端改 navigator 又会被页面 CSP 拦。
// compatPath 是绿联桌面 <head> 里引的那个"浏览器版本拦截"脚本。它会读
// navigator.userAgent 判断是否手机端，然后跳 check-mobile.html 显示
// "请使用电脑访问"。客户端改 navigator 会被页面 CSP 拦，所以在代理层直接
// 把这份脚本换成空实现：只保留 ban / blockMobile 两个空函数，页面照常往下跑。
//
// 只在"访问电脑版"时替换，移动端模式仍走上游原逻辑。
const compatPath = "browser-compatible/browser-compatible.js"

const compatStubJS = `window.browserCompatible={ban:function(){},blockMobile:function(){}};`

const upCookie = "nwg_up"

func NewPureProxy(jar *CookieJar, insecure bool) *PureProxy {
	base := NewProxy(jar, insecure) // 复用同一套 Transport（TLS 跳过校验、长连接）
	p := &PureProxy{jar: jar}

	rp := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			raw := strings.TrimPrefix(r.URL.Path, "/api/p/")
			seg, rest := raw, ""
			if i := strings.Index(raw, "/"); i >= 0 {
				seg, rest = raw[:i], raw[i+1:]
			}
			origin, err := decodeOrigin(seg)
			if err != nil {
				return
			}
			u, err := url.Parse(origin)
			if err != nil {
				return
			}
			r.URL.Scheme = u.Scheme
			r.URL.Host = u.Host
			r.URL.Path = "/" + rest
			r.URL.RawPath = ""
			if u.Path != "" && u.Path != "/" {
				r.URL.Path = strings.TrimSuffix(u.Path, "/") + r.URL.Path
			}
			r.Host = u.Host
			// 浏览器给我们的 Origin/Referer 指向网关自己（如 http://172.30.2.246:18092），
			// 而上游看到的 Host 已经被换成它自己。ws-scrcpy、Lucky、飞牛这类面板会做
			// Origin==Host 的同源校验，不改写的话每个 POST（包括 /api/auth/login）
			// 都会被 403 "cross-origin request rejected"，前端就显示成"密码错误"。
			fixForwardSiteHeaders(r, u, u.Host)
			r.Header.Set("X-Forwarded-Host", r.Host)
			r.Header.Set("X-Forwarded-Proto", u.Scheme)
			if ip := r.RemoteAddr; ip != "" {
				if prev := r.Header.Get("X-Forwarded-For"); prev != "" {
					r.Header.Set("X-Forwarded-For", prev+", "+ip)
				} else {
					r.Header.Set("X-Forwarded-For", ip)
				}
			}
			// UA 由前端决定：seg 带 "d|" 或浏览器侧 nwg_ua=desktop 就是电脑版，
			// 否则按移动端身份访问。这里必须尊重开关，不能再无条件钉死桌面 UA。
			desk := isDesktopSeg(seg) || uaMode(r)
			applyUA(r.Header, desk)
			if jarCookie := p.jar.Get(cookieKey(u.Host)); jarCookie != "" {
				r.Header.Set("Cookie", mergeCookie(r.Header.Get("Cookie"), jarCookie))
			}
			ctx := context.WithValue(r.Context(), pureTargetKey{}, u.Host)
			ctx = context.WithValue(ctx, pureSegKey{}, seg)
			ctx = context.WithValue(ctx, pureDeskKey{}, desk)
			*r = *r.WithContext(ctx)
		},
		ModifyResponse: func(resp *http.Response) error {
			host, _ := resp.Request.Context().Value(pureTargetKey{}).(string)
			seg, _ := resp.Request.Context().Value(pureSegKey{}).(string)
			desk, _ := resp.Request.Context().Value(pureDeskKey{}).(bool)
			// 电脑版模式：把"请使用电脑访问"的拦截脚本换成空实现
			if desk && resp.StatusCode < 300 && resp.Request.URL != nil &&
				strings.HasSuffix(resp.Request.URL.Path, compatPath) {
				_ = resp.Body.Close()
				nb := []byte(compatStubJS)
				resp.Body = io.NopCloser(bytes.NewReader(nb))
				resp.ContentLength = int64(len(nb))
				resp.Header.Set("Content-Length", strconv.Itoa(len(nb)))
				resp.Header.Set("Content-Type", "application/javascript; charset=utf-8")
				resp.Header.Del("Content-Encoding")
			}
			if host != "" && len(resp.Header.Values("Set-Cookie")) > 0 {
				p.jar.Set(cookieKey(host), resp.Header.Values("Set-Cookie"))
			}
			// 记住"这个浏览器最近在代理哪个上游"，给 WebSocket 兜底用。
			if seg != "" && resp.Header.Get("Location") == "" {
				resp.Header.Add("Set-Cookie", upCookie+"="+seg+"; Path=/; Max-Age=1800; SameSite=Lax")
			}
			// 同源 3xx 不在这里跟：跟完页面的地址仍是 "/"，上游那些相对路径
			// （绿联桌面 "./static/..."）就会按根解析而全部打错位置（白屏）。
			// 改成 200 + X-NWG-Redirect，让 Service Worker 回一条"干净地址"的 302，
			// 由浏览器自己带着正确路径重新导航，SW 再按新路径转发。
			if isRedirectCode(resp.StatusCode) && resp.Request.URL != nil {
				if loc := resp.Header.Get("Location"); loc != "" {
					if lu, err := url.Parse(loc); err == nil {
						abs := lu
						if !lu.IsAbs() {
							abs = resp.Request.URL.ResolveReference(lu)
						}
						if strings.EqualFold(abs.Host, resp.Request.URL.Host) {
							pth := abs.Path
							if pth == "" {
								pth = "/"
							}
							if abs.RawQuery != "" {
								pth += "?" + abs.RawQuery
							}
							resp.Header.Set("X-NWG-Redirect", pth)
							resp.Header.Del("Location")
							_ = resp.Body.Close()
							resp.Body = http.NoBody
							resp.StatusCode = http.StatusOK
							resp.ContentLength = 0
							resp.Header.Set("Content-Length", "0")
							resp.Header.Del("Content-Type")
							return nil
						}
					}
				}
			}
			// 跨源 3xx 仍把绝对 Location 改回同源相对路径，避免直连客户端够不着的内网机器。
			if loc := resp.Header.Get("Location"); loc != "" && resp.Request.URL != nil {
				if lu, err := url.Parse(loc); err == nil && lu.IsAbs() &&
					strings.EqualFold(lu.Host, resp.Request.URL.Host) {
					out := lu.Path
					if out == "" {
						out = "/"
					}
					if lu.RawQuery != "" {
						out += "?" + lu.RawQuery
					}
					if lu.Fragment != "" {
						out += "#" + lu.Fragment
					}
					resp.Header.Set("Location", out)
				}
			}
			return nil
		},
		Transport: &redirectRT{base: base.tr, jar: jar},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("[v2] proxy %s -> %v", r.URL.Path, err)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("上游不可达：" + err.Error()))
		},
	}
	p.rp = rp
	return p
}

func (p *PureProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/__p/") {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/api/p/" + strings.TrimPrefix(r.URL.Path, "/__p/")
		p.rp.ServeHTTP(w, r2)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/p/") {
		http.NotFound(w, r)
		return
	}
	p.rp.ServeHTTP(w, r)
}

// ServeFallback 兜住 Service Worker 拦不到的请求（主要是 WebSocket 升级）：
// 路径已经是上游自己的真实路径（/websocket、/api/...），只缺"发给哪台"，
// 从前一次代理响应种下的 nwg_up cookie 里取。
// ServeFallbackAny 兜住"裸路径"的一切漏网请求：SW 没拿到（客户端未受控、SW 重启丢映射、
// 预加载绕过、上游用绝对路径的静态资源与接口）时，请求会直接打到网关根。
// 只要本浏览器种过 nwg_up（前一次代理响应），就按原路径转给那台上游，
// 而不是回我们自己的 404 或首页——否则表现出来就是"绿联桌面白屏"。
func (p *PureProxy) ServeFallbackAny(w http.ResponseWriter, r *http.Request) bool {
	ck, err := r.Cookie(upCookie)
	if err != nil || ck.Value == "" {
		return false
	}
	if r.URL.Path == "" || r.URL.Path == "/" {
		return false // 首页归本应用，绝不转给上游
	}
	for _, pre := range []string{"/css/", "/js/", "/sw.js", "/boot.html", "/api/p/", "/favicon.ico"} {
		if strings.HasPrefix(r.URL.Path, pre) {
			return false
		}
	}
	r2 := r.Clone(r.Context())
	orig := r2.URL.Path
	r2.URL.Path = "/api/p/" + ck.Value + orig
	p.rp.ServeHTTP(w, r2)
	return true
}

func (p *PureProxy) ServeFallback(w http.ResponseWriter, r *http.Request) bool {
	if !strings.Contains(strings.ToLower(r.Header.Get("Upgrade")), "websocket") {
		return false
	}
	ck, err := r.Cookie(upCookie)
	if err != nil || ck.Value == "" {
		return false
	}
	// 根路径 "/" 上的 WS 只有带上游应用特征（?action=…）才转发给上游：
	// 被代理的 SPA（ws-scrcpy-web 等）在洁净路径下用 location.pathname 拼
	// ws://gateway/?action=xxx；而飞牛桌面/网关自身也会在 "/" 附近开 WS，
	// 一律放行会被误送回上游，表现为桌面应用全部 404。这里只放行带 action 的。
	if (r.URL.Path == "" || r.URL.Path == "/") && r.URL.Query().Get("action") == "" {
		return false // 归本应用/桌面自己处理
	}
	for _, pre := range []string{"/css/", "/js/", "/sw.js", "/boot.html", "/api/p/", "/favicon.ico"} {
		if strings.HasPrefix(r.URL.Path, pre) {
			return false
		}
	}
	r2 := r.Clone(r.Context())
	orig := r2.URL.Path
	if orig == "" {
		orig = "/"
	}
	r2.URL.Path = "/api/p/" + ck.Value + orig
	p.rp.ServeHTTP(w, r2)
	return true
}
