(function(){try{var P=(function(){var m=location.pathname.match(/\/api\/p\/([A-Za-z0-9_-]+)/);return m?m[0]:null;})();if(!P||!navigator.serviceWorker||!navigator.serviceWorker.controller)return;if(location.hash)return;var seg=P.slice(7),tail=location.pathname.slice(P.length)||"/";location.replace((tail.charAt(0)==="/"?tail:"/"+tail)+"?__nwg="+seg+location.hash);}catch(e){}})();
(function(){try{var q=location.search;if(!q)return;var s=q.replace(/[?&]__nwg=[^&]*/,"").replace(/^&/,"?");s=s.replace(/%2fapi%2fp%2f[A-Za-z0-9_-]+(%2f)?/gi,"").replace(/\/api\/p\/[A-Za-z0-9_-]+(\/)?/g,"");s=(function(q){if(!q)return "";var ps=q.replace(/^[?&]/,"").split("&"),kp=[];for(var i=0;i<ps.length;i++){var kv=ps[i];if(!kv)continue;var ei=kv.indexOf("=");var k=ei<0?kv:kv.slice(0,ei),v=ei<0?"":kv.slice(ei+1);if(v===""&&/^(redirect|redirect_uri|redirect_to|return_url|callback|continue|next)$/i.test(k))continue;kp.push(kv);}return kp.length?"?"+kp.join("&"):"";})(s);if(s!==q){history.replaceState(null,"",location.pathname+s+location.hash);}}catch(e){}})();
(function(){var P="__NWG_PREFIX__";var O="__NWG_ORIGIN__";var DUAMODE=__NWG_UA_MODE__;var B="__NWG_BASE__";
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
if(u.charAt(0)!=="/"||u.charAt(1)==="/")return u;
if(u===P||u.indexOf(P+"/")===0)return u;
return P+u;}
var of=window.fetch;if(of){window.fetch=function(i,o){try{if(typeof i==="string")i=fix(i);else if(i&&i.url){var n=fix(i.url);if(n!==i.url)i=new Request(n,i);}}catch(e){}return of.call(this,i,o);};}
// 桌面/SPA 会用 history.pushState 把地址改回根绝对路径（如 /desktop/?os=ugospro），
// 一旦绕开 /api/p/ 前缀，后续所有请求就打不到设备上，表现就是白屏、一直加载。
(function(){try{["pushState","replaceState"].forEach(function(m){var o=history[m];if(!o)return;history[m]=function(st,t,u){try{if(typeof u==="string"&&u){
// 带前缀的地址先剥掉：飞牛登录后会把 /api/p/<seg>/ 塞给前端路由，URL 变了却
// 没有真正导航，SW 的导航拦截插不上手，SPA 读到前缀就渲染自己的 404 页。
var had=u.indexOf("/api/p/")>=0;
if(had)u=u.replace(/\/api\/p\/[A-Za-z0-9_-]+/g,"")||"/";
// 没有 SW 接管时页面还离不开前缀，这时才补回代理前缀。
else if(!(navigator.serviceWorker&&navigator.serviceWorker.controller))u=fix(u);
}}catch(e){}return o.call(history,st,t,u);};});}catch(e){}})();
var ox=XMLHttpRequest.prototype.open;XMLHttpRequest.prototype.open=function(m,u){try{arguments[1]=fix(u);}catch(e){}return ox.apply(this,arguments);};
if(navigator.sendBeacon){var ob=navigator.sendBeacon.bind(navigator);navigator.sendBeacon=function(u,d){return ob(fix(u),d);};}
function wrapCtor(n){var O=window[n];if(!O)return;var W=function(u,o){try{if(typeof u==="string"){var a=u;if(n==="WebSocket"&&u.indexOf("ws")!==0)a=(location.protocol==="https:"?"wss://":"ws://")+location.host+(u.charAt(0)==="/"?u:"/"+u);var x=new URL(a,location.href);if(x.pathname.indexOf(P)!==0){x.pathname=P+(x.pathname.charAt(0)==="/"?x.pathname:"/"+x.pathname);}u=(n==="WebSocket")?x.toString():x.pathname+x.search;}}catch(e){}return new O(u,o);};W.prototype=O.prototype;["CONNECTING","OPEN","CLOSING","CLOSED"].forEach(function(k){if(O[k]!==undefined)W[k]=O[k];});window[n]=W;}
wrapCtor("WebSocket");wrapCtor("EventSource");
function fixAttr(e,t){var v=e&&e.getAttribute&&e.getAttribute(t);if(!v)return;
if(v.indexOf(P)===0)return;
if(/^(http|https):\/\//i.test(v)||(v.charAt(0)==="/"&&v.charAt(1)!=="/")){var nv=fix(v);if(nv!==v){try{e.setAttribute(t,nv);}catch(x){}}}}
try{var _ifsd=Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype,"src");
if(_ifsd&&_ifsd.set){Object.defineProperty(HTMLIFrameElement.prototype,"src",{configurable:true,get:_ifsd.get,set:function(v){try{v=fix(v);}catch(e){}return _ifsd.set.call(this,v);}});}}catch(e){}
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
if(f!==abs){try{if(window.parent&&window.parent!==window){window.parent.postMessage({__nwg:"open",url:f,abs:abs},location.origin);return window;}}catch(e){}location.href=f;return window;}
var r=rel(u);if(r!==null){if(tell(r))return window;if(nav(r))return window;}}}catch(e){}return _open.apply(window,arguments);};
try{document.addEventListener("click",function(ev){var n=ev.target;while(n&&n.nodeType===1&&n.tagName!=="A")n=n.parentNode;if(!n||n.tagName!=="A")return;var t=n.getAttribute("target");if(t!=="_blank"&&t!=="_new")return;var h=n.getAttribute("href");var r=rel(h);if(r===null)return;if(ev.defaultPrevented)return;ev.preventDefault();if(!tell(r))nav(r);},false);}catch(e){}
})();
(function(){try{if(!navigator.serviceWorker)return;var P="/api/p/ZHxodHRwOi8vMTcyLjMwLjIuMTI2OjU2NjY/";var go=function(){var c=navigator.serviceWorker.controller;if(!c)return;try{c.postMessage({__nwg:"bind",prefix:P});}catch(e){}if(location.pathname.indexOf(P)===0){var tail=location.pathname.slice(P.length);var clean=(tail.charAt(0)==="/"?"":"/")+tail+location.search+location.hash;location.replace(clean);}else{var q=location.search;if(q.indexOf("__nwg=")>=0){var qs=q.replace(/[?&]__nwg=[^&]*/,"").replace(/^&/,"?");try{history.replaceState(null,"",location.pathname+qs+location.hash);}catch(e){}}}};if(navigator.serviceWorker.controller){go();}else{navigator.serviceWorker.ready.then(go).catch(function(){});}navigator.serviceWorker.addEventListener("controllerchange",go);}catch(e){}})();