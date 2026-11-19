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

// 属性重写只能覆盖 HTML 里写死的路径。SPA 面板（飞牛 fnOS 这类）的 JS 会在运行时
// 自己拼 "/assets/x.png"、"/license/v1/..." 这类根绝对路径，这些请求会直接打到网关
// 根、绕开 /api/p/ 前缀而 404（表现为图片破图、i18n 不翻译、提示连接已断开）。
// 所以在页面最前面注入一段拦截：fetch / XHR / sendBeacon / WebSocket / EventSource
// 以及运行时赋值的 src、href，统一把根绝对路径补成 "<前缀>/..."。
const injectScript = `(function(){var P="__NWG_PREFIX__";var O="__NWG_ORIGIN__";var DUAMODE=__NWG_UA_MODE__;var B="__NWG_BASE__";
var DUA="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36";
try{var _def=function(o,k,v){try{Object.defineProperty(o,k,{get:function(){return v;},configurable:true});}catch(e){}};
if(DUAMODE){
_def(Navigator.prototype,"userAgent",DUA);
_def(Navigator.prototype,"appVersion",DUA.replace("Mozilla/",""));
_def(Navigator.prototype,"platform","Win32");
_def(Navigator.prototype,"vendor","Google Inc.");
_def(Navigator.prototype,"maxTouchPoints",0);
try{var _uad={brands:[{brand:"Chromium",version:"131"},{brand:"Google Chrome",version:"131"},{brand:"Not_A Brand",version:"24"}],mobile:false,platform:"Windows"};
_uad.getHighEntropyValues=function(){return Promise.resolve({architecture:"x86",bitness:"64",brands:_uad.brands,mobile:false,platform:"Windows",platformVersion:"10.0.0",uaFullVersion:"131.0.0.0",model:"",fullVersionList:_uad.brands});};
_uad.toJSON=function(){return{brands:_uad.brands,mobile:false,platform:"Windows"};};
_def(Navigator.prototype,"userAgentData",_uad);}catch(e){}
}
}catch(e){}
var _base=null,_bhost="";try{_base=new URL(O);_bhost=_base.hostname;}catch(e){}
function isLan(h){return /^(10\.|127\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|169\.254\.)/.test(h||"");}
function enc(s){try{return btoa(s).replace(/\+/g,"-").replace(/\//g,"_").replace(/=+$/,"");}catch(e){return "";}}
function segFor(o){return "/api/p/"+enc((DUAMODE?"d|":"")+o);}
function fix(u){
if(typeof u!=="string"||!u.length)return u;
if(u.charAt(0)==="#"||u.indexOf("data:")===0||u.indexOf("blob:")===0||u.indexOf("javascript:")===0)return u;
if(/^https?:\/\//i.test(u)){
try{var x=new URL(u);
// 飞牛这类桌面会把应用的相对地址（/app/wsscrcpy）按"当前页面的 origin"补成绝对地址，
// 而这个 origin 是网关自己（127.0.0.1:21110）。不处理就会自己代理自己，一路打回
// 网关 → connection refused → 白窗。这里把它换算回当前页面的上游路径。
if(location.origin&&x.origin===location.origin){if(x.pathname.indexOf(P)===0)return u;return P+x.pathname+x.search+x.hash;}
if(_base&&x.host===_base.host){u=x.pathname+x.search+x.hash;}
else if(_bhost&&x.hostname===_bhost){return segFor(x.protocol+"//"+x.host)+x.pathname+x.search+x.hash;}
else if(isLan(x.hostname)){return segFor(x.protocol+"//"+x.host)+x.pathname+x.search+x.hash;}
else return u;
}catch(e){return u;}
}
if(u.indexOf(O)===0){u=u.slice(O.length)||"/";}
if(u.charAt(0)==="."||(u.charAt(0)!=="/"&&!/^[a-z][a-z0-9+.-]*:/i.test(u))){try{var r=new URL(u,"http://nwg/"+String(B||"").replace(/^\/+/,""));if(r.pathname.indexOf("/api/p/")===0)return r.pathname+r.search+r.hash;return P+r.pathname+r.search+r.hash;}catch(e){return u;}}
if(u.charAt(0)!=="/"||u.charAt(1)==="/")return u;
if(u===P||u.indexOf(P+"/")===0)return u;
return P+u;}
var of=window.fetch;if(of){window.fetch=function(i,o){try{if(typeof i==="string")i=fix(i);else if(i&&i.url){var n=fix(i.url);if(n!==i.url)i=new Request(n,i);}}catch(e){}return of.call(this,i,o);};}
// 桌面/SPA 会用 history.pushState 把地址改回根绝对路径（如 /desktop/?os=ugospro），
// 一旦绕开 /api/p/ 前缀，后续所有请求就打不到设备上，表现就是白屏、一直加载。
(function(){try{["pushState","replaceState"].forEach(function(m){var o=history[m];if(!o)return;history[m]=function(st,t,u){try{if(typeof u==="string"&&u){
// 带前缀的先剥掉再补：飞牛登录后会把 /api/p/<seg>/ 塞给前端路由，URL 变了却
// 没有真正导航，SW 的导航拦截插不上手，SPA 读到前缀就渲染自己的 404 页；
// 而绿联桌面反过来要靠这个前缀推导接口地址，所以剥完必须原样补回去。
var tail=null;
if(u.indexOf("/api/p/")>=0){u=u.replace(/\/api\/p\/[A-Za-z0-9_-]+/g,"");tail=u||"/";}
// 只有“前缀后面什么都没有”才是飞牛那次泄漏（它把 redirect_uri 记成了带前缀的
// 地址）；其余情况说明页面自己就在用带前缀的路径，绿联桌面还要靠它推导接口
// 地址，不能剥。所以前者换成干净路径，后者原样补前缀。
u=tail!==null?tail:fix(u);
}}catch(e){}return o.call(history,st,t,u);};});}catch(e){}})();
var ox=XMLHttpRequest.prototype.open;XMLHttpRequest.prototype.open=function(m,u){try{arguments[1]=fix(u);}catch(e){}return ox.apply(this,arguments);};
if(navigator.sendBeacon){var ob=navigator.sendBeacon.bind(navigator);navigator.sendBeacon=function(u,d){return ob(fix(u),d);};}
function wrapCtor(n){var O=window[n];if(!O)return;var W=function(u,o){try{if(typeof u==="string"){var a=u;if(n==="WebSocket"&&u.indexOf("ws")!==0)a=(location.protocol==="https:"?"wss://":"ws://")+location.host+(u.charAt(0)==="/"?u:"/"+u);var x=new URL(a,location.href);if(x.pathname.indexOf(P)!==0){x.pathname=P+(x.pathname.charAt(0)==="/"?x.pathname:"/"+x.pathname);}u=(n==="WebSocket")?x.toString():x.pathname+x.search;}}catch(e){}return new O(u,o);};W.prototype=O.prototype;["CONNECTING","OPEN","CLOSING","CLOSED"].forEach(function(k){if(O[k]!==undefined)W[k]=O[k];});window[n]=W;}
wrapCtor("WebSocket");wrapCtor("EventSource");
function fixAttr(e,t){var v=e&&e.getAttribute&&e.getAttribute(t);if(!v)return;
if(v.indexOf(P)===0)return;
if(/^(http|https):\/\//i.test(v)||v.charAt(0)==="."||(v.charAt(0)==="/"&&v.charAt(1)!=="/")){var nv=fix(v);if(nv!==v){try{e.setAttribute(t,nv);}catch(x){}}}}
try{var _ifsd=Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype,"src");
if(_ifsd&&_ifsd.set){Object.defineProperty(HTMLIFrameElement.prototype,"src",{configurable:true,get:_ifsd.get,set:function(v){try{v=fix(v);}catch(e){}return _ifsd.set.call(this,v);}});}}catch(e){}
// 脚本/图片/样式这些元素的 src、href 走的是"反射 URL 属性"，只改 src 属性名不够：
// 老式设备面板（IPC 之类）会用 s.src="../web-static/lib/x.js" 动态加载，
// 浏览器按 <base> 解析时 ../ 会跨出 /api/p/<seg>/ 变成 /api/p/x.js → 400 → 白屏。
try{(function(){var ps=[["HTMLScriptElement","src"],["HTMLImageElement","src"],["HTMLVideoElement","src"],["HTMLAudioElement","src"],["HTMLSourceElement","src"],["HTMLLinkElement","href"],["HTMLAnchorElement","href"]];
for(var pi=0;pi<ps.length;pi++){(function(C,A){var P2=window[C];if(!P2)return;var d=Object.getOwnPropertyDescriptor(P2.prototype,A);if(!d||!d.set)return;try{Object.defineProperty(P2.prototype,A,{configurable:true,get:d.get,set:function(v){try{v=fix(v);}catch(e){}return d.set.call(this,v);}});}catch(e){}})(ps[pi][0],ps[pi][1]);}})();}catch(e){}
// document.write 拼接的 HTML 完全不经过 DOM 属性，只能按文本替换：../ 一律收回前缀根。
try{(function(){var dw=Document.prototype.write,dl=Document.prototype.writeln;
var R=function(s){try{return typeof s==="string"?s.replace(/((?:href|src)\s*=\s*["'])(?:\.\.\/)+([^"']*)/gi,function(m,a,c){return a+P+"/"+c;}):s;}catch(e){return s;}};
Document.prototype.write=function(){var as=[];for(var i=0;i<arguments.length;i++)as.push(R(arguments[i]));return dw.apply(this,as);};
Document.prototype.writeln=function(){var as=[];for(var i=0;i<arguments.length;i++)as.push(R(arguments[i]));return dl.apply(this,as);};})();}catch(e){}
try{var _sa=Element.prototype.setAttribute;Element.prototype.setAttribute=function(n,v){try{if((n==="src"||n==="href")&&typeof v==="string"&&v.indexOf(P)!==0){var nv=fix(v);if(nv!==v)v=nv;}}catch(e){}return _sa.call(this,n,v);};}catch(e){}
var mo=new MutationObserver(function(ms){for(var i=0;i<ms.length;i++){var m=ms[i];if(m.type!=="attributes"){continue;}var t=m.attributeName;if(t!=="src"&&t!=="href")continue;fixAttr(m.target,t);}});
try{mo.observe(document.documentElement,{subtree:true,attributes:true,attributeFilter:["src","href"]});}catch(e){}
try{var all=document.querySelectorAll("[src],[href]");for(var k=0;k<all.length;k++){fixAttr(all[k],all[k].getAttribute("src")?"src":"href");}}catch(e){}
function rel(u){try{var a=new URL(u,location.href);if(a.pathname.indexOf("/api/")===0)return null;
if(a.origin!==location.origin&&a.origin!==O){if(!((_bhost&&a.hostname===_bhost)||isLan(a.hostname)))return null;return fix(a.href);}
return a.pathname+a.search+a.hash;}catch(e){return null;}}
function nav(u){var r=rel(u);if(r===null)return false;location.href=r;return true;}
function tell(u){try{if(window.parent&&window.parent!==window){window.parent.postMessage({__nwg:"open",url:u},location.origin);return true;}}catch(e){}return false;}
try{window.__nwgFix=fix;}catch(e){}
var _open=window.open;window.open=function(u){try{if(typeof u==="string"&&u&&u!=="about:blank"){
// 先按"上游绝对地址"归一：相对路径（如飞牛第三方应用的 /app/wscrcpy）、
// 根绝对路径、同主机异端口、局域网绝对地址，全部落到代理前缀上。
// 关键点：新窗口如果拿到的是裸 /app/wscrcpy，会以网关自身的域名去解析，
// 直接 404 白屏——所以必须先把上游 origin 补回去再交给标签页。
var abs=u;if(u.charAt(0)==="/"&&u.charAt(1)!=="/")abs=O+u;else if(!/^[a-z]+:/i.test(u))abs=O+"/"+u;
var f=fix(abs);
if(f!==abs){
// 先按原生 window.open 开：绿联的应用中心这类程序要靠 window.opener 跟打开它的
// 桌面做握手（提权取密码就发生在这条握手里），换成我们自己的标签页会没有 opener，
// 表现就是一直转圈的白窗。原生弹窗被拦（返回 null）时才退回标签页方案。
try{if(f.indexOf("/appmgr/")>=0){var w2=_open.call(window,f);if(w2){return w2;}}}catch(e){}
try{if(window.parent&&window.parent!==window){window.parent.postMessage({__nwg:"open",url:f,abs:abs},location.origin);return window;}}catch(e){}location.href=f;return window;}
var r=rel(u);if(r!==null){if(tell(r))return window;if(nav(r))return window;}}}catch(e){}return _open.apply(window,arguments);};
try{(function(){var send=function(k,m,st){try{navigator.sendBeacon("//"+location.host+"/api/clientlog",JSON.stringify({k:k,m:String(m).slice(0,300),s:String(st||"").slice(0,1200),u:location.href.slice(0,200)}));}catch(e){}};
window.addEventListener("error",function(e){send("error",e.message||"",(e.error&&e.error.stack)||((e.filename||"")+":"+(e.lineno||0)));},true);
window.addEventListener("unhandledrejection",function(e){var r=e.reason;send("reject",(r&&r.message)||String(r),(r&&r.stack)||"");});})();}catch(e){}
try{document.addEventListener("click",function(ev){var n=ev.target;while(n&&n.nodeType===1&&n.tagName!=="A")n=n.parentNode;if(!n||n.tagName!=="A")return;var t=n.getAttribute("target");if(t!=="_blank"&&t!=="_new")return;var h=n.getAttribute("href");var r=rel(h);if(r===null)return;ev.preventDefault();ev.stopPropagation();if(!tell(r))nav(r);},true);}catch(e){}
// 没有 Service Worker 时（手机端开了 VPN 之类，SW 注册/接管会失败）代理只能靠
// 前缀路径硬扛：页面里所有指向"上游自己"的请求都必须被拽回 P 前缀，否则
// 飞牛这类 SPA 会直接打上游 origin，https 外壳下就是混合内容被拦、或设备侧 404。
try{(function(){if(window.top===window)return;var c=null;try{c=navigator.serviceWorker&&navigator.serviceWorker.controller;}catch(e){}
if(c)return;
var OHOST="";try{OHOST=new URL(O).host;}catch(e){return;}
// 没有 SW 时靠前缀硬扛，但 SPA 的路由读的是 location.pathname：前缀在，
// 飞牛这类前端一条路由都匹配不上，直接渲染自己的 404（"回到系统桌面"）。
// 这里把浏览器里显示的路径换回干净路径（<base> 已经定住了相对地址基准，
// 请求改写又把所有指向上游的请求拽回前缀），路由就能正常匹配。
var PP=P.replace(/\/$/,"");
try{if(location.pathname.indexOf(PP)===0){var tail=location.pathname.slice(PP.length)||"/";if(tail.charAt(0)!=="/")tail="/"+tail;history.replaceState(history.state,"",tail+location.search+location.hash);}}catch(e){}
// 之后页面自己再改地址（router push、表单提交、location 跳转）也要留在前缀里，
// 否则一旦真的发生文档级跳转就会落到网关根的 nginx 404。
function keep(u){var s="";try{s=String(u);}catch(e){return u;}
try{var a=new URL(s,location.href);if(a.origin!==location.origin)return u;
if(a.pathname.indexOf(PP)===0)return u;
return PP+a.pathname+a.search+a.hash;}catch(e){return u;}}
try{var ps=history.pushState;history.pushState=function(st,t,u){if(u!==undefined&&u!==null){try{arguments[2]=keep(u);}catch(e){}}return ps.apply(history,arguments);};}catch(e){}
try{var rs=history.replaceState;history.replaceState=function(st,t,u){if(u!==undefined&&u!==null){try{arguments[2]=keep(u);}catch(e){}}return rs.apply(history,arguments);};}catch(e){}
try{var la=location.assign;location.assign=function(u){return la.call(location,keep(u));};}catch(e){}
try{var lr=location.replace;location.replace=function(u){return lr.call(location,keep(u));};}catch(e){}
try{document.addEventListener("click",function(ev){var n=ev.target;while(n&&n.nodeType===1&&n.tagName!=="A")n=n.parentNode;if(!n||n.tagName!=="A")return;var t=n.getAttribute("target");if(t==="_blank"||t==="_new")return;var h=n.getAttribute("href");if(!h||h.charAt(0)==="#"||/^(javascript|mailto|tel):/i.test(h))return;var r=null;try{r=rel(h);}catch(e){}if(r===null)return;ev.preventDefault();ev.stopPropagation();try{location.href=r;}catch(e){}},true);}catch(e){}
function wrap(u){var s="";try{s=typeof u==="string"?u:(u&&u.url)||String(u);}catch(e){return u;}
try{var a=new URL(s,location.href);if(a.host!==OHOST)return u;
if(a.pathname.indexOf(P.replace(/\/$/,""))===0)return u;
var t=P.replace(/\/$/,"")+a.pathname+a.search+a.hash;
if(typeof u==="string")return location.origin+t;
try{return new URL(location.origin+t);}catch(e){return u;}}catch(e){return u;}}
function wrapWS(u){var s="";try{s=String(u);}catch(e){return u;}
try{var a=new URL(s,location.href);if(a.host!==OHOST)return u;
var t=(location.protocol==="https:"?"wss://":"ws://")+location.host+P.replace(/\/$/,"")+a.pathname+a.search;
return t;}catch(e){return u;}}
try{var f=window.fetch;if(f)window.fetch=function(i,o){try{i=wrap(i);}catch(e){}return f.call(window,i,o);};}catch(e){}
try{var xo=XMLHttpRequest.prototype.open;XMLHttpRequest.prototype.open=function(m,u){try{var w=wrap(u);if(w!==u){arguments[1]=w;}}catch(e){}return xo.apply(this,arguments);};}catch(e){}
try{var sb=navigator.sendBeacon;if(sb)navigator.sendBeacon=function(u,d){try{u=wrap(u);}catch(e){}return sb.call(navigator,u,d);};}catch(e){}
try{var ES=window.EventSource;if(ES)window.EventSource=function(u,c){return new ES(wrap(u),c);};}catch(e){}
try{var W=window.WebSocket;if(W)window.WebSocket=function(u,p){return new W(wrapWS(u),p);};}catch(e){}
})();}catch(e){}
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

// injectJS 生成注入脚本的完整源码，由 /api/inject.js 单独下发。
//go:embed assets/inject094.tpl.js
var inject094Tpl string

//go:embed assets/patch_setter.js
var inject094Setter string

//go:embed assets/patch_rel.txt
var inject094Rel string

//go:embed assets/patch_0155.js
var inject0155 string

// injectJS 由 /api/inject.js 下发。
// 基线取 0094 的注入脚本原文（那一版飞牛登录后能停干净路径、QWRT 也能进），
// 只叠加 IPC 白屏那几段补丁，新加的行为一律不带。
func injectJS(prefix, origin, baseDir string, desktop bool) string {
	uaMode := "0"
	if desktop {
		uaMode = "1"
	}
	if baseDir == "" {
		baseDir = "/"
	}
	code := inject094Tpl
	code = strings.ReplaceAll(code, "__NWG_PREFIX__", prefix)
	code = strings.ReplaceAll(code, "__NWG_ORIGIN__", origin)
	code = strings.ReplaceAll(code, "__NWG_UA_MODE__", uaMode)
	code = strings.ReplaceAll(code, "__NWG_BASE__", baseDir)

	// IPC 修复 1：../ 与 "./" 按上游文档目录折算，别跨出 /api/p/<seg>/ 变成 400。
	code = strings.Replace(code,
		`if(u.indexOf(O)===0){u=u.slice(O.length)||"/";}`,
		`if(u.indexOf(O)===0){u=u.slice(O.length)||"/";}`+"\n"+inject094Rel, 1)

	// IPC 修复 2：脚本/图片/样式走"反射 URL 属性"，只改 src 属性名不够；
	// 顺带补 document.write/writeln 的文本改写。
	code = strings.Replace(code,
		`try{var _sa=Element.prototype.setAttribute;`,
		inject094Setter+"\ntry{var _sa=Element.prototype.setAttribute;", 1)

	// 0155：Worker / 表单 / 同标签链接这三类出口在运行时改写，
	// 不再依赖改上游数据（数据一律原样透传）。
	code = strings.Replace(code,
		`try{window.__nwgFix=fix;}`,
		inject0155+"\n"+`try{window.__nwgFix=fix;}`, 1)

	// IPC 修复 3：fixAttr 也要认 "." 开头的相对路径。
	code = strings.Replace(code,
		`if(/^(http|https):\/\//i.test(v)||(v.charAt(0)==="/"&&v.charAt(1)!=="/"))`,
		`if(/^(http|https):\/\//i.test(v)||v.charAt(0)==="."||(v.charAt(0)==="/"&&v.charAt(1)!=="/"))`, 1)

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
