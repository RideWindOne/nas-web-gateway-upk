// 0155：把"运行时拦截"补全，保证不改上游数据也能覆盖所有出口。
(function(){try{
	// 1) Worker / SharedWorker / importScripts
	//    第三方页面常用它们拉脚本；拦不到就会按页面 origin 发请求，直接绕开 /api/p 前缀。
	function wrapWorker(n){var O=window[n];if(!O)return;var W=function(u,o){try{if(typeof u==="string")u=fix(u);}catch(e){}return new O(u,o);};W.prototype=O.prototype;window[n]=W;}
	wrapWorker("Worker");wrapWorker("SharedWorker");
	try{if(typeof importScripts==="function"){var _is=importScripts;window.importScripts=function(){var a=[].slice.call(arguments);for(var i=0;i<a.length;i++){try{a[i]=fix(a[i]);}catch(e){}}return _is.apply(this,a);};}}catch(e){}

	// 2) 表单：action 指向别的设备/别的端口时，直接提交就跳出代理了。
	try{var _sub=HTMLFormElement.prototype.submit;HTMLFormElement.prototype.submit=function(){try{var a=this.getAttribute("action");if(a){var nv=fix(a);if(nv!==a)this.setAttribute("action",nv);}}catch(e){}return _sub.apply(this,arguments);};}catch(e){}
	try{document.addEventListener("submit",function(ev){var f=ev.target;if(!f||f.tagName!=="FORM")return;try{var a=f.getAttribute("action");if(!a)return;var nv=fix(a);if(nv!==a)f.setAttribute("action",nv);}catch(e){}},true);}catch(e){}

	// 3) 同标签跳转的普通链接：先把 href 改写成代理前缀再让默认行为发生。
	//    target=_blank 那类已由上面的 window.open 补丁处理。
	try{document.addEventListener("click",function(ev){if(ev.defaultPrevented||ev.button!==0||ev.metaKey||ev.ctrlKey||ev.shiftKey||ev.altKey)return;var n=ev.target;while(n&&n.nodeType===1&&n.tagName!=="A")n=n.parentNode;if(!n||n.tagName!=="A")return;var t=n.getAttribute("target");if(t&&t!=="_self")return;var h=n.getAttribute("href");if(!h)return;var nv=fix(h);if(nv!==h){try{ev.preventDefault();location.href=nv;return;}catch(e){}}},true);}catch(e){}
}catch(e){}})();
