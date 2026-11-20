// 被代理页面的运行时注入脚本（单一版本，不再叠补丁）。
// 规则只有两处定义：
//   1) resolve(u)  —— 唯一地址归一函数，所有出口都只调它
//   2) INSTALLERS  —— 出口表，一个出口一行安装器
// 加新出口 = 表里加一行；改地址规则 = 只改 resolve()。
(function(){try{
var P="__NWG_PREFIX__",O="__NWG_ORIGIN__",DUAMODE=__NWG_UA_MODE__,B="__NWG_BASE__";
var DUA="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36";

// ---------- 1. UA 伪装（应用要求电脑版时） ----------
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

// ---------- 2. 唯一地址归一 ----------
// 绿联第三方应用只肯吐 *.ugapp.link:<端口> 这种公网中继地址，局域网/DDNS 下打不开，
// 先统一换成本机同端口，再往下走其余规则。
var RE_UG=/https?:\\?\/\\?\/[^"'\s]*?ugapp\.link:(\d{2,5})/gi;
function unrelay(u){try{if(typeof u==="string"&&u.length>10&&u.toLowerCase().indexOf("ugapp.link")>=0){u=u.replace(RE_UG,"http://127.0.0.1:$1").replace(/\\\//g,"/");}}catch(e){}return u;}
function resolve(u){
u=unrelay(u);
if(typeof u!=="string"||!u.length)return u;
if(u.charAt(0)==="#"||u.indexOf("data:")===0||u.indexOf("blob:")===0||u.indexOf("javascript:")===0)return u;
// (a) 绝对地址：桌面会把应用的相对地址按"当前页面 origin"补成绝对地址，而这个 origin
//     是网关自己，不换算就会自己代理自己 → 一路打回网关 → 白窗。
if(/^https?:\/\//i.test(u)){
try{var x=new URL(u);
if(location.origin&&x.origin===location.origin){if(x.pathname.indexOf(P)===0)return u;return P+x.pathname+x.search+x.hash;}
if(_base&&x.host===_base.host){u=x.pathname+x.search+x.hash;}
else if(_bhost&&x.hostname===_bhost){return segFor(x.protocol+"//"+x.host)+x.pathname+x.search+x.hash;}
else if(isLan(x.hostname)){return segFor(x.protocol+"//"+x.host)+x.pathname+x.search+x.hash;}
else return u;
}catch(e){return u;}
}
if(u.indexOf(O)===0){u=u.slice(O.length)||"/";}
// (b) 相对地址：按上游文档目录折算，别跨出前缀变成 /api/p/x → 400（IPC 白屏）。
if(u.charAt(0)==="."||(u.charAt(0)!=="/"&&!/^[a-z][a-z0-9+.-]*:/i.test(u))){
try{var r=new URL(u,"http://nwg/"+String(B||"").replace(/^\/+/,""));
if(r.pathname.indexOf("/api/p/")===0)return r.pathname+r.search+r.hash;
return P+r.pathname+r.search+r.hash;}catch(e){return u;}
}
if(u.charAt(0)!=="/"||u.charAt(1)==="/")return u;
if(u===P||u.indexOf(P+"/")===0)return u;
return P+u;}

// 导航目标：返回 null 表示"不归我们管，走浏览器默认行为"
function rel(u){try{var a=new URL(u,location.href);if(a.pathname.indexOf("/api/")===0)return null;
if(a.origin!==location.origin&&a.origin!==O){if(!((_bhost&&a.hostname===_bhost)||isLan(a.hostname)))return null;return resolve(a.href);}
return a.pathname+a.search+a.hash;}catch(e){return null;}}
function tell(u){try{if(window.parent&&window.parent!==window){window.parent.postMessage({__nwg:"open",url:u},location.origin);return true;}}catch(e){}return false;}
try{window.__nwgFix=resolve;}catch(e){}

// ---------- 3. 出口表 ----------
// 每个出口只做一件事：把地址交给 resolve()。顺序无关，互不覆盖。
function hookFetch(){
var of=window.fetch;if(!of)return;
window.fetch=function(i,o){try{if(typeof i==="string")i=resolve(i);else if(i&&i.url){var n=resolve(i.url);if(n!==i.url)i=new Request(n,i);}}catch(e){}return of.call(this,i,o);};}

function hookXHR(){
var ox=XMLHttpRequest.prototype.open;
XMLHttpRequest.prototype.open=function(m,u){try{arguments[1]=resolve(u);}catch(e){}return ox.apply(this,arguments);};}

function hookBeacon(){
if(!navigator.sendBeacon)return;
var ob=navigator.sendBeacon.bind(navigator);
navigator.sendBeacon=function(u,d){return ob(resolve(u),d);};}

// 长连接类：WebSocket 要带协议和网关 host，其余（EventSource/Worker）是路径型
function ctor(n,u2){
var O2=window[n];if(!O2)return;
var W=function(u,o){try{if(typeof u==="string")u=u2(u);}catch(e){}return new O2(u,o);};
W.prototype=O2.prototype;
["CONNECTING","OPEN","CLOSING","CLOSED"].forEach(function(k){if(O2[k]!==undefined)W[k]=O2[k];});
window[n]=W;}

function wsUrl(u){
var a=u;if(u.indexOf("ws")!==0)a=(location.protocol==="https:"?"wss://":"ws://")+location.host+(u.charAt(0)==="/"?u:"/"+u);
var x=new URL(a,location.href);
if(x.pathname.indexOf(P)!==0)x.pathname=P+(x.pathname.charAt(0)==="/"?x.pathname:"/"+x.pathname);
return x.toString();}

function hookWebSocket(){ctor("WebSocket",wsUrl);}
function hookEventSource(){ctor("EventSource",function(u){var x=new URL(resolve(u),location.href);
if(x.pathname.indexOf(P)!==0)x.pathname=P+(x.pathname.charAt(0)==="/"?x.pathname:"/"+x.pathname);
return x.pathname+x.search;});}
function hookWorker(){ctor("Worker",resolve);}
function hookSharedWorker(){ctor("SharedWorker",resolve);}

function hookImportScripts(){
try{if(typeof importScripts==="function"){var _is=importScripts;window.importScripts=function(){var a=[].slice.call(arguments);for(var i=0;i<a.length;i++){try{a[i]=resolve(a[i]);}catch(e){}}return _is.apply(this,a);};}}catch(e){}}

// 表单：action 指向别的设备/端口时直接提交就跳出代理
function hookForm(){
try{var _sub=HTMLFormElement.prototype.submit;
HTMLFormElement.prototype.submit=function(){try{var a=this.getAttribute("action");if(a){var nv=resolve(a);if(nv!==a)this.setAttribute("action",nv);}}catch(e){}return _sub.apply(this,arguments);};}catch(e){}
try{document.addEventListener("submit",function(ev){var f=ev.target;if(!f||f.tagName!=="FORM")return;try{var a=f.getAttribute("action");if(!a)return;var nv=resolve(a);if(nv!==a)f.setAttribute("action",nv);}catch(e){}},true);}catch(e){}}

// history：带前缀的先剥（飞牛会把前缀塞给前端路由）、无 SW 时才补回
function hookHistory(){
try{["pushState","replaceState"].forEach(function(m){var o=history[m];if(!o)return;
history[m]=function(st,t,u){try{if(typeof u==="string"&&u){
if(u.indexOf("/api/p/")>=0)u=u.replace(/\/api\/p\/[A-Za-z0-9_-]+/g,"")||"/";
else if(!(navigator.serviceWorker&&navigator.serviceWorker.controller))u=resolve(u);
}}catch(e){}return o.call(history,st,t,u);};});}catch(e){}}

// window.open：绿联应用中心要靠 opener 握手，能原生开就原生开
function hookOpen(){
var _open=window.open;
window.open=function(u){try{if(typeof u==="string"&&u&&u!=="about:blank"){
var abs=u;if(u.charAt(0)==="/"&&u.charAt(1)!=="/")abs=O+u;else if(!/^[a-z]+:/i.test(u))abs=O+"/"+u;
var f=resolve(abs);
if(f!==abs){
try{if(window.parent&&window.parent!==window){window.parent.postMessage({__nwg:"open",url:f,abs:abs},location.origin);return window;}}catch(e){}
location.href=f;return window;}
var r=rel(u);if(r!==null){if(tell(r))return window;location.href=r;return window;}}}catch(e){}return _open.apply(window,arguments);};}

// 链接点击：一个 handler 处理所有 target（原来三个 handler 合并）
function hookLinks(){
try{document.addEventListener("click",function(ev){
if(ev.defaultPrevented||ev.button!==0||ev.metaKey||ev.ctrlKey||ev.shiftKey||ev.altKey)return;
var n=ev.target;while(n&&n.nodeType===1&&n.tagName!=="A")n=n.parentNode;
if(!n||n.tagName!=="A")return;
var h=n.getAttribute("href");if(!h||h.charAt(0)==="#"||/^(javascript|mailto|tel):/i.test(h))return;
var t=n.getAttribute("target");
var r=rel(h);if(r===null)return;
ev.preventDefault();ev.stopPropagation();
if(t==="_blank"||t==="_new"){if(!tell(r))location.href=r;return;}
location.href=r;},true);}catch(e){}}

// 属性型出口：反射 URL 属性（script/img/video/audio/source/link/anchor）走 setter 才拦得住
function hookAttrs(){
var ps=[["HTMLScriptElement","src"],["HTMLImageElement","src"],["HTMLVideoElement","src"],["HTMLAudioElement","src"],["HTMLSourceElement","src"],["HTMLLinkElement","href"],["HTMLAnchorElement","href"],["HTMLIFrameElement","src"]];
function patch(C,A){var K=window[C];if(!K)return;var d=Object.getOwnPropertyDescriptor(K.prototype,A);if(!d||!d.set)return;
try{Object.defineProperty(K.prototype,A,{configurable:true,get:d.get,set:function(v){try{v=resolve(v);}catch(e){}return d.set.call(this,v);}});}catch(e){}}
for(var i=0;i<ps.length;i++)patch(ps[i][0],ps[i][1]);
try{var _sa=Element.prototype.setAttribute;Element.prototype.setAttribute=function(n,v){
try{if((n==="src"||n==="href")&&typeof v==="string"&&v.indexOf(P)!==0){var nv=resolve(v);if(nv!==v)v=nv;}}catch(e){}return _sa.call(this,n,v);};}catch(e){}}

// document.write 拼出来的 HTML 不经过 DOM 属性，只能按文本改：../ 收回前缀根
function hookDocWrite(){
try{var dw=Document.prototype.write,dl=Document.prototype.writeln;
var R=function(s){try{return typeof s==="string"?s.replace(/((?:href|src)\s*=\s*["'])(?:\.\.\/)+([^"']*)/gi,function(m,a,c){return a+P+"/"+c;}):s;}catch(e){return s;}};
Document.prototype.write=function(){var as=[];for(var i=0;i<arguments.length;i++)as.push(R(arguments[i]));return dw.apply(this,as);};
Document.prototype.writeln=function(){var as=[];for(var i=0;i<arguments.length;i++)as.push(R(arguments[i]));return dl.apply(this,as);};}catch(e){}}

// 兜底：MutationObserver + 首次扫描（覆盖绕过 setter 的写法）
function hookAttrScan(){
function one(e,t){var v=e&&e.getAttribute&&e.getAttribute(t);if(!v)return;if(v.indexOf(P)===0)return;
if(/^(http|https):\/\//i.test(v)||v.charAt(0)==="."||(v.charAt(0)==="/"&&v.charAt(1)!=="/")){var nv=resolve(v);if(nv!==v){try{e.setAttribute(t,nv);}catch(x){}}}}
try{var mo=new MutationObserver(function(ms){for(var i=0;i<ms.length;i++){var m=ms[i];if(m.type!=="attributes")continue;var t=m.attributeName;if(t!=="src"&&t!=="href")continue;one(m.target,t);}});mo.observe(document.documentElement,{subtree:true,attributes:true,attributeFilter:["src","href"]});}catch(e){}
try{var all=document.querySelectorAll("[src],[href]");for(var k=0;k<all.length;k++)one(all[k],all[k].getAttribute("src")?"src":"href");}catch(e){}}

var INSTALLERS=[
["fetch",hookFetch],["xhr",hookXHR],["beacon",hookBeacon],
["websocket",hookWebSocket],["eventsource",hookEventSource],
["worker",hookWorker],["sharedworker",hookSharedWorker],["importscripts",hookImportScripts],
["form",hookForm],["history",hookHistory],["open",hookOpen],["links",hookLinks],
["attrs",hookAttrs],["docwrite",hookDocWrite],["attrscan",hookAttrScan]
];
for(var ii=0;ii<INSTALLERS.length;ii++){try{INSTALLERS[ii][1]();}catch(e){}}
}catch(e){}})();
