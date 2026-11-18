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
