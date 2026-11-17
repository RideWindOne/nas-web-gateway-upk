package main

import (
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
)

// 属性重写只能覆盖 HTML 里写死的路径。SPA 面板（飞牛 fnOS 这类）的 JS 会在运行时
// 自己拼 "/assets/x.png"、"/license/v1/..." 这类根绝对路径，这些请求会直接打到网关
// 根、绕开 /api/p/ 前缀而 404（表现为图片破图、i18n 不翻译、提示连接已断开）。
// 所以在页面最前面注入一段拦截：fetch / XHR / sendBeacon / WebSocket / EventSource
// 以及运行时赋值的 src、href，统一把根绝对路径补成 "<前缀>/..."。
const injectScript = `(function(){var P="__NWG_PREFIX__";var O="__NWG_ORIGIN__";var DUAMODE=__NWG_UA_MODE__;
// 上游页面里跑的 JS 也会读 UA。绿联云这类后台一旦读到手机 UA 就直接摆出
// “请使用 PC 浏览器”的提示页，所以在这里把 navigator 的 UA 也伪装成桌面 Chrome。
var DUA="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36";
try{var _def=function(o,k,v){try{Object.defineProperty(o,k,{get:function(){return v;},configurable:true});}catch(e){}};
if(DUAMODE){
_def(Navigator.prototype,"userAgent",DUA);
_def(Navigator.prototype,"appVersion",DUA.replace("Mozilla/",""));
_def(Navigator.prototype,"platform","Win32");
_def(Navigator.prototype,"vendor","Google Inc.");
// maxTouchPoints / userAgentData 也必须一起伪装：绿联云桌面除了看 UA，
// 还会读 navigator.maxTouchPoints（iPad 判定）和 UA-CH（userAgentData.mobile），
// 手机 Chrome 上后者为 true，光改 userAgent 仍然会被判成手机而跳
// browser-compatible/check-mobile.html 那张“请使用 PC 浏览器”的拦截页。
_def(Navigator.prototype,"maxTouchPoints",0);
try{var _uad={brands:[{brand:"Chromium",version:"131"},{brand:"Google Chrome",version:"131"},{brand:"Not_A Brand",version:"24"}],mobile:false,platform:"Windows"};
_uad.getHighEntropyValues=function(){return Promise.resolve({architecture:"x86",bitness:"64",brands:_uad.brands,mobile:false,platform:"Windows",platformVersion:"10.0.0",uaFullVersion:"131.0.0.0",model:"",fullVersionList:_uad.brands});};
_uad.toJSON=function(){return{brands:_uad.brands,mobile:false,platform:"Windows"};};
_def(Navigator.prototype,"userAgentData",_uad);}catch(e){}
}
}catch(e){}
function fix(u){if(typeof u!=="string"||!u.length)return u;if(u.indexOf(O)===0){u=u.slice(O.length)||"/";}if(u.charAt(0)!=="/"||u.charAt(1)==="/")return u;if(u===P||u.indexOf(P+"/")===0)return u;return P+u;}
var of=window.fetch;if(of){window.fetch=function(i,o){try{if(typeof i==="string")i=fix(i);else if(i&&i.url){var n=fix(i.url);if(n!==i.url)i=new Request(n,i);}}catch(e){}return of.call(this,i,o);};}
var ox=XMLHttpRequest.prototype.open;XMLHttpRequest.prototype.open=function(m,u){try{arguments[1]=fix(u);}catch(e){}return ox.apply(this,arguments);};
if(navigator.sendBeacon){var ob=navigator.sendBeacon.bind(navigator);navigator.sendBeacon=function(u,d){return ob(fix(u),d);};}
function wrapCtor(n){var O=window[n];if(!O)return;var W=function(u,o){try{if(typeof u==="string"){var a=u;if(n==="WebSocket"&&u.indexOf("ws")!==0)a=(location.protocol==="https:"?"wss://":"ws://")+location.host+(u.charAt(0)==="/"?u:"/"+u);var x=new URL(a,location.href);if(x.pathname.indexOf(P)!==0){x.pathname=P+(x.pathname.charAt(0)==="/"?x.pathname:"/"+x.pathname);}u=(n==="WebSocket")?x.toString():x.pathname+x.search;}}catch(e){}return new O(u,o);};W.prototype=O.prototype;["CONNECTING","OPEN","CLOSING","CLOSED"].forEach(function(k){if(O[k]!==undefined)W[k]=O[k];});window[n]=W;}
wrapCtor("WebSocket");wrapCtor("EventSource");
// 把 src/href 上的“上游 origin 绝对地址”（绿联云桌面用它开应用 iframe）和
// 根绝对路径都改到代理前缀下，否则外网时这些子页面只能打到 172.30.x.x，白窗。
function fixAttr(e,t){var v=e&&e.getAttribute&&e.getAttribute(t);if(!v)return;
if(v.indexOf(O)===0){try{e.setAttribute(t,P+(v.slice(O.length)||"/"));}catch(x){}return;}
if(v.charAt(0)!=="/"||v.charAt(1)==="/"||v.indexOf(P)===0)return;try{e.setAttribute(t,P+v);}catch(x){}}
var mo=new MutationObserver(function(ms){for(var i=0;i<ms.length;i++){var m=ms[i];if(m.type!=="attributes"){continue;}var t=m.attributeName;if(t!=="src"&&t!=="href")continue;fixAttr(m.target,t);}});
try{mo.observe(document.documentElement,{subtree:true,attributes:true,attributeFilter:["src","href"]});}catch(e){}
try{var all=document.querySelectorAll("[src],[href]");for(var k=0;k<all.length;k++){fixAttr(all[k],all[k].getAttribute("src")?"src":"href");}}catch(e){}
function rel(u){try{var a=new URL(u,location.href);if(a.origin!==location.origin&&a.origin!==O)return null;if(a.pathname.indexOf("/api/")===0)return null;return a.pathname+a.search+a.hash;}catch(e){return null;}}
function nav(u){var r=rel(u);if(r===null)return false;location.href=r;return true;}
function tell(u){try{if(window.parent&&window.parent!==window){window.parent.postMessage({__nwg:"open",url:u},location.origin);return true;}}catch(e){}return false;}
var _open=window.open;window.open=function(u){try{if(typeof u==="string"&&u&&u!=="about:blank"){var r=rel(u);if(r!==null){if(tell(r))return window;if(nav(r))return window;}}}catch(e){}return _open.apply(window,arguments);};
try{document.addEventListener("click",function(ev){var n=ev.target;while(n&&n.nodeType===1&&n.tagName!=="A")n=n.parentNode;if(!n||n.tagName!=="A")return;var t=n.getAttribute("target");if(t!=="_blank"&&t!=="_new")return;var h=n.getAttribute("href");var r=rel(h);if(r===null)return;ev.preventDefault();ev.stopPropagation();if(!tell(r))nav(r);},true);}catch(e){}
})();`

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

func rewriteHTML(body []byte, prefix, baseDir, origin string, desktop bool) []byte {	body = reBase.ReplaceAll(body, nil)
	body = reAttr.ReplaceAll(body, []byte("${1}=${2}"+prefix+"/${3}"))
	body = reAttrRoot.ReplaceAll(body, []byte("${1}=${2}"+prefix+"/${3}"))
	body = reCSSURL.ReplaceAll(body, []byte("url(${1}"+prefix+"/${2}"))

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
	inj := []byte(`<base href="` + baseHref + `">` +
		`<script>` + strings.Replace(strings.Replace(strings.Replace(injectScript,
		"__NWG_PREFIX__", prefix, 1), "__NWG_ORIGIN__", origin, 1), "__NWG_UA_MODE__", uaMode, 1) + `</script>` +
		`<script>` + bindScript(prefix) + `</script>`)
	if loc := reHead.FindIndex(body); loc != nil {
		out := make([]byte, 0, len(body)+len(inj))
		out = append(out, body[:loc[1]]...)
		out = append(out, inj...)
		out = append(out, body[loc[1]:]...)
		return out
	}
	return append(inj, body...)
}

func rewriteLocation(loc, origin, prefix, marker string) string {
	if loc == "" {
		return loc
	}
	if strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") {
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
		rest += "?" + u.RawQuery
	}

	o, errO := url.Parse(origin)
	if errO == nil && u.Scheme == o.Scheme && u.Host == o.Host {
		return prefix + rest
	}

	scheme := u.Scheme
	if scheme == "" {
		scheme = "http"
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
