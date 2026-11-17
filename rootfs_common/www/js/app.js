(function () {
  'use strict';

  // Service Worker 只在安全上下文可用（https 或 localhost）。
  // 它是代理模式的透明转发层：让 iframe 的地址停在干净路径上，
  // 从而 SPA 的整页跳转和动态 import 都不会再绕开前缀。
  if ('serviceWorker' in navigator &&
      (location.protocol === 'https:' || location.hostname === 'localhost' ||
       location.hostname === '127.0.0.1')) {
    navigator.serviceWorker.register('/sw.js?v=0057', { scope: '/' }).then(function () {
      // 告诉 SW：这个 client 是本应用主页面，它的 /、/css、/js 不能被当成上游页面转发
      navigator.serviceWorker.ready.then(function (r) {
        var w = navigator.serviceWorker.controller || (r && r.active);
        if (w) w.postMessage({ __nwg: 'app' });
      });
      navigator.serviceWorker.addEventListener('controllerchange', function () {
        var w = navigator.serviceWorker.controller;
        if (w) w.postMessage({ __nwg: 'app' });
      });
    }).catch(function () {});
  }

  var panesEl = document.getElementById('panes');
  var emptyEl = document.getElementById('empty');
  var recentEl = document.getElementById('recent');
  var recentWrap = document.getElementById('recentWrap');
  var recentHide = document.getElementById('recentHide');
  var urlInput = document.getElementById('urlInput');
  var clearInput = document.getElementById('clearInput');
  var addrForm = document.getElementById('addrForm');
  var menuBtn = document.getElementById('menuBtn');
  var menu = document.getElementById('menu');
  var layoutLabel = document.getElementById('layoutLabel');
  var drawer = document.getElementById('drawer');
  var drawerTitle = document.getElementById('drawerTitle');
  var drawerList = document.getElementById('drawerList');
  var clearBtn = document.getElementById('clearBtn');
  var tabbar = document.getElementById('tabbar');
  var tabsEl = document.getElementById('tabs');
  var tabAddBtn = document.getElementById('tabAdd');

  var appsEl = document.getElementById('apps');
  var emptyHint = document.getElementById('emptyHint');
  var addAppBtn = document.getElementById('addApp');

  var sheet = document.getElementById('sheet');
  var sheetTitle = document.getElementById('sheetTitle');
  var sheetCancel = document.getElementById('sheetCancel');
  var sheetDel = document.getElementById('sheetDel');
  var sheetOk = document.getElementById('sheetOk');
  var pvRow = document.querySelector('.preview-row');
  var pvIcon = document.getElementById('pvIcon');
  var pvName = document.getElementById('pvName');
  var appName = document.getElementById('appName');
  var appNote = document.getElementById('appNote');
  var appAddr = document.getElementById('appAddr');
  var appScheme = document.getElementById('appScheme');
  var appProxy = document.getElementById('appProxy');
  var appDesktop = document.getElementById('appDesktop');
  var colorsEl = document.getElementById('colors');

  var layout = localStorage.getItem('nwg.layout') === 'split' ? 'split' : 'single';
  var drawerMode = 'history';
  var appsCache = [];
  var editing = null;   // 当前编辑的应用（null 表示新建）
  var editColor = '#5b8def';

  var PALETTE = ['#c9a97e', '#5b8def', '#d99aa8', '#c05c72', '#d99b6a', '#5b9bd5', '#7ec49a', '#4fb3b3'];

  // ---------- 地址编码 ----------
  // 后端路由：/api/p/<base64url(origin)>/<path>
  // 必须挂在 /api 下：UGOS 网关只转发 proxy_path(/api) 范围内的请求，
  // /、/css、/js 由网关自己静态发。
  function b64u(str) {
    return btoa(unescape(encodeURIComponent(str)))
      .replace(/\+/g, '-')
      .replace(/\//g, '_')
      .replace(/=+$/, '');
  }

  // seg 里带 "d|" 前缀 = 这个站点要走电脑版 UA（后端据此强制桌面 UA，
  // 页面里改写出来的所有子资源地址会继承同一个 seg）。
  function segOf(origin, desktop) {
    return b64u((desktop ? 'd|' : '') + origin);
  }

  // 一个地址对应的「网页应用」记录。地址栏输入、历史/最近访问点开时，
  // 都要按这张表把该应用的「访问电脑版」带过去——否则同一个地址从卡片打开
  // 是电脑版、从历史打开就退回手机 UA，绿联云这类后台会直接摆出
  // “请使用 PC 浏览器”的拦截页。
  function bare(n) {
    return String(n || '').replace(/[#?].*$/, '').replace(/\/+$/, '');
  }

  // 只比到 host:port（含协议），路径不参与：网页应用记录存的是入口地址，
  // 而历史里存的是具体页面（/cgi-bin/luci/admin/status 这种）。
  // 端口不同不合并——同一台机器不同端口往往是完全不同的服务。
  function originOf(n) {
    try {
      var u = new URL(n);
      return u.protocol + '//' + u.host;
    } catch (e) {
      return bare(n);
    }
  }

  function appFor(raw) {
    var n = bare(normalize(raw));
    if (!n) return null;
    var i;
    for (i = 0; i < appsCache.length; i++) {
      if (bare(normalize(appsCache[i].url)) === n) return appsCache[i];
    }
    var o = originOf(n);
    for (i = 0; i < appsCache.length; i++) {
      if (originOf(normalize(appsCache[i].url)) === o) return appsCache[i];
    }
    return null;
  }

  // 记住“这个地址上次是不是按电脑版开的”，否则用户手动切过的选择，
  // 下次从历史/地址栏再打开就丢了。
  function deskPrefKey(n) {
    return 'nwg.desktop.' + originOf(bare(n));
  }

  function rememberDesk(n, on) {
    try {
      if (on) localStorage.setItem(deskPrefKey(n), '1');
      else localStorage.removeItem(deskPrefKey(n));
    } catch (e) { /* 隐私模式下忽略 */ }
  }

  function desktopFor(raw) {
    var n = normalize(raw);
    if (!n) return false;
    var a = appFor(n);
    if (a && a.desktop) return true;
    try {
      if (localStorage.getItem(deskPrefKey(n)) === '1') return true;
    } catch (e) { /* 忽略 */ }
    return false;
  }

  function normalize(raw) {
    var s = (raw || '').trim();
    if (!s) return '';
    if (/^https?:\/\//i.test(s)) return s;
    return 'http://' + s;
  }

  // Service Worker 只在安全上下文可用：https、localhost、127.0.0.1
  function swReady() {
    if (!('serviceWorker' in navigator)) return false;
    var h = location.hostname;
    return location.protocol === 'https:' || h === 'localhost' || h === '127.0.0.1' || h === '::1';
  }

  function proxySrc(fullURL, desktop) {
    var u;
    try { u = new URL(fullURL); } catch (e) { return null; }
    var origin = u.origin;
    var rest = fullURL.slice(origin.length) || '/';
    if (rest.charAt(0) !== '/') rest = '/' + rest;
    return '/api/p/' + segOf(origin, desktop) + rest;
  }

  // 私网目标才值得走 NAS 转发；公网站点本来就可达，代理逐条改写反而会打乱它。
  function isLanHost(h) {
    if (!h) return true;
    h = h.toLowerCase();
    if (h === 'localhost' || h === '127.0.0.1' || h === '::1') return true;
    if (/^10\./.test(h) || /^192\.168\./.test(h) || /^169\.254\./.test(h)) return true;
    if (/^172\.(1[6-9]|2\d|3[01])\./.test(h)) return true;
    if (/\.lan$/.test(h) || /\.local$/.test(h)) return true;
    return false;
  }

  function hostOf(fullURL) {
    try { return new URL(fullURL).host; } catch (e) { return fullURL; }
  }

  // ---------- 后端接口 ----------
  function api(path, opts) {
    return fetch(path, Object.assign({ headers: { 'Content-Type': 'application/json' } }, opts || {}))
      .then(function (r) { return r.ok ? r.json() : []; })
      .catch(function () { return []; });
  }

  // 引导页/占位页的标题不能当历史，否则首页会出现一堆“加载中”
  var PLACEHOLDER_TITLES = { '加载中': 1, '正在接管该站点…': 1, 'about:blank': 1, '': 1, '无法连接': 1, '无法连接到目标设备': 1, '错误': 1 };

  function recordHistory(url, title) {
    // 引导页（boot.html）只是装载中转，不属于用户的访问记录
    if (!url || url.indexOf('boot.html') >= 0) return;
    var t = (title || '').trim();
    if (PLACEHOLDER_TITLES[t] || t.indexOf('正在接管') === 0) t = hostOf(url);
    api('/api/history', {
      method: 'POST',
      body: JSON.stringify({ url: url, title: t })
    });
  }

  // ---------- 标签 ----------
  var activePane = null;

  function panesList() {
    return Array.prototype.slice.call(panesEl.querySelectorAll('.pane'));
  }

  // homeMode 为真时收起所有已开标签、回到「网页应用」卡片列表，
  // 但标签本身不关——用户随时能从标签栏点回去。
  var homeMode = false;

  function syncEmpty() {
    var tabs = panesList().length > 0;
    var showEmpty = !tabs || homeMode;
    emptyEl.classList.toggle('hidden', !showEmpty);
    // 并排模式下所有 pane 都是可见的，回首页时必须把它们整块藏掉，
    // 否则卡片会像浮层一样叠在页面之上（Chrome 桌面宽屏下特别明显）
    document.body.classList.toggle('home', showEmpty);
    if (showEmpty) {
      activePane = null;
      urlInput.value = '';
      syncClearBtn();
      renderRecent();
    }
    renderTabs();
  }

  function showHome() {
    homeMode = true;
    panesList().forEach(function (p) { p.classList.remove('on'); });
    syncEmpty();
  }

  // ---------- 虚拟鼠标 ----------
  // 手机上没指针，而 UGOS 桌面、LuCI 这类 PC 版界面靠 hover 和精确落点工作，
  // 手指直接戳往往点不中、窗口也拖不动。这里在 iframe 之上盖一层触控板：
  // 单指拖动 = 移动指针，轻点 = 左键点击，双指上下滑 = 滚轮。
  // 前提是 iframe 同源（代理模式）；直连的跨源页面拿不到 contentDocument。
  //
  // 绿联云桌面里“点开一个应用”其实是桌面再套一层 iframe，指针落点只命中外层
  // 那个 <iframe> 元素，事件就断在壳上——所以命中要做逐层下钻（deepHit），
  // 一直钻到最里层同源文档里的真实元素，再用那一层的 window 造事件。
  var vMouse = localStorage.getItem('nwg-vmouse') === '1';

  function reloadActive() {
    var cur = activePane && activePane.querySelector('.pane-frame');
    if (cur && cur.src) cur.src = cur.src;
  }
  var reloadBtn = document.getElementById('reloadBtn');
  if (reloadBtn) reloadBtn.addEventListener('click', reloadActive);
  var vLayer = null, vDot = null, vx = 0, vy = 0, vClampW = 0, vClampH = 0;

  function moveDot() {
    if (!vDot) return;
    if (!vx && !vy && vClampW && vClampH) { vx = vClampW / 2; vy = vClampH / 2; }
    vx = Math.max(6, Math.min(Math.max(6, vClampW - 6), vx));
    vy = Math.max(6, Math.min(Math.max(6, vClampH - 6), vy));
    vDot.style.transform = 'translate(' + vx + 'px,' + vy + 'px)';
  }

  // 触控板层要盖住 iframe，而不是盖住整个 pane：并排时 pane 头上有一条 38px 的标题栏。
  function layoutVLayer() {
    if (!vLayer || !vLayer.parentNode) return;
    var pane = vLayer.parentNode;
    var head = pane.querySelector('.pane-head');
    var hh = (head && head.getClientRects().length) ? head.offsetHeight : 0;
    vLayer.style.top = hh + 'px';
    vClampW = vLayer.clientWidth || vClampW;
    vClampH = vLayer.clientHeight || vClampH;
    moveDot();
  }

  function paneGeo() {
    if (!activePane || !vLayer) return null;
    var f = activePane.querySelector('.pane-frame');
    if (!f) return null;
    // 用 offset（相对 .pane 的局部坐标）而不是 getBoundingClientRect：
    // 横屏时外层整体旋转 90°，屏幕坐标系的矩形不再是局部坐标系的轴对齐盒。
    var z = Number(activePane.dataset.zoom) || 1;
    return {
      pane: activePane, f: f, zoom: z,
      offX: f.offsetLeft - vLayer.offsetLeft,
      offY: f.offsetTop - vLayer.offsetTop
    };
  }

  // x/y 是这个文档自己的视口坐标；命中到嵌套 iframe 就换到下一层继续找。
  function deepHit(win, x, y) {
    var out = { win: win, el: null, x: x, y: y };
    for (var i = 0; i < 8; i++) {
      var doc = null;
      try { doc = out.win.document; } catch (e) { return out; }
      if (!doc) return out;
      var el = null;
      try { el = doc.elementFromPoint(out.x, out.y); } catch (e) { el = null; }
      if (!el) el = doc.documentElement;
      if (!el) return out;
      var tag = (el.tagName || '').toUpperCase();
      if (tag === 'IFRAME' || tag === 'FRAME') {
        var inner = null;
        try { inner = el.contentWindow; } catch (e) { inner = null; }
        var idoc = null;
        try { idoc = inner && inner.document; } catch (e) { idoc = null; }
        if (inner && idoc) {
          var r = el.getBoundingClientRect();
          out.win = inner;
          out.x = out.x - r.left;
          out.y = out.y - r.top;
          out.el = null;
          continue;
        }
      }
      out.el = el;
      return out;
    }
    return out;
  }

  // 合成事件不会触发浏览器默认滚动，所以滚轮要顺手把最近的滚动容器自己滚一遍。
  function scrollDeepest(win, el, dy) {
    var node = el;
    while (node && node.nodeType === 1) {
      var st = null;
      try { st = win.getComputedStyle(node); } catch (e) { st = null; }
      if (st) {
        var oy = st.overflowY;
        if ((oy === 'auto' || oy === 'scroll' || oy === 'overlay') &&
            node.scrollHeight - node.clientHeight > 4) {
          node.scrollTop += dy;
          return true;
        }
      }
      node = node.parentNode;
    }
    try {
      var se = win.document.scrollingElement || win.document.documentElement;
      if (se && se.scrollHeight - se.clientHeight > 4) { se.scrollTop += dy; return true; }
    } catch (e) {}
    return false;
  }

  function fireAt(kind, dy) {
    var g = paneGeo();
    if (!g) return;
    var px = (vx - g.offX) / g.zoom, py = (vy - g.offY) / g.zoom;
    try {
      var win = g.f.contentWindow;
      if (!win) return;
      var h = deepHit(win, px, py);
      if (!h.el) return;
      var el = h.el, w = h.win;

      if (kind === 'wheel') {
        try {
          el.dispatchEvent(new w.WheelEvent('wheel', {
            bubbles: true, cancelable: true, deltaY: dy, deltaMode: 0, view: w
          }));
        } catch (e) {}
        scrollDeepest(w, el, dy);
        return;
      }

      var base = {
        bubbles: true, cancelable: true, composed: true, view: w, detail: 1,
        clientX: h.x, clientY: h.y, screenX: h.x, screenY: h.y,
        button: 0, buttons: kind === 'mousedown' ? 1 : 0,
        pointerId: 1, pointerType: 'mouse', isPrimary: true,
        width: 1, height: 1, pressure: kind === 'mousedown' ? 0.5 : 0
      };

      // 现在很多后台（包括绿联云桌面）用 Pointer Events 而不是 mouse*，
      // 两条都要发，否则看起来“点了没反应”。
      function pe(type) {
        try { el.dispatchEvent(new w.PointerEvent(type, base)); }
        catch (e) { el.dispatchEvent(new w.MouseEvent(type, base)); }
      }
      function me(type) { try { el.dispatchEvent(new w.MouseEvent(type, base)); } catch (e) {} }

      if (kind === 'mousemove') {
        pe('pointerover'); pe('pointerenter'); pe('pointermove');
        me('mouseover'); me('mouseenter'); me('mousemove');
        return;
      }
      if (kind === 'click') {
        pe('pointerover'); me('mouseover');
        pe('pointermove'); me('mousemove');
        pe('pointerdown'); me('mousedown');
        try { if (el.focus && /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)) el.focus(); } catch (e) {}
        pe('pointerup'); me('mouseup'); me('click');
        return;
      }
    } catch (e) {}
  }

  function bindVEvents() {
    var pts = {}, dragging = false, scrolling = false, sx = 0, sy = 0, lx = 0, ly = 0, t0 = 0, moved = 0, ly2 = 0;

    // 屏幕坐标 → 触控板局部坐标的增量。横屏时整个 body 转了 90°，
    // 屏幕上的 (dx,dy) 在局部是 (dy,-dx)。
    function rotNow() { return document.body.classList.contains("rot"); }
    function locDelta(dx, dy) {
      return rotNow() ? { x: dy, y: -dx } : { x: dx, y: dy };
    }

    vLayer.addEventListener('pointerdown', function (e) {
      pts[e.pointerId] = { x: e.clientX, y: e.clientY };
      if (Object.keys(pts).length >= 2) {
        scrolling = true; dragging = false;
        var k0 = Object.keys(pts);
        ly2 = rotNow()
          ? (pts[k0[0]].x + pts[k0[1]].x) / 2
          : (pts[k0[0]].y + pts[k0[1]].y) / 2;
        return;
      }
      if (scrolling) return;
      dragging = true;
      sx = lx = e.clientX; sy = ly = e.clientY;
      t0 = Date.now(); moved = 0;
      try { vLayer.setPointerCapture(e.pointerId); } catch (x) {}
    });

    vLayer.addEventListener('pointermove', function (e) {
      if (!pts[e.pointerId]) return;
      pts[e.pointerId] = { x: e.clientX, y: e.clientY };
      if (scrolling) {
        var ks = Object.keys(pts);
        if (ks.length < 2) return;
        var av = rotNow()
          ? (pts[ks[0]].x + pts[ks[1]].x) / 2
          : (pts[ks[0]].y + pts[ks[1]].y) / 2;
        var dy = rotNow() ? (av - ly2) : (ly2 - av);
        ly2 = av;
        if (Math.abs(dy) > 1) fireAt('wheel', dy * 3);
        return;
      }
      if (!dragging) return;
      var dx = e.clientX - lx, dyy = e.clientY - ly;
      lx = e.clientX; ly = e.clientY;
      var ld = locDelta(dx, dyy);
      moved += Math.abs(dx) + Math.abs(dyy);
      // 手指相对拖动，按 1.6 倍放大，方便把指针推过整个屏幕
      vx += ld.x * 1.6; vy += ld.y * 1.6;
      moveDot();
      fireAt('mousemove');
    });

    function end(e) {
      delete pts[e.pointerId];
      if (Object.keys(pts).length === 0) {
        if (!scrolling && dragging && moved < 10 && Date.now() - t0 < 400) fireAt('click');
        dragging = false; scrolling = false;
      }
    }
    vLayer.addEventListener('pointerup', end);
    vLayer.addEventListener('pointercancel', end);
    vLayer.addEventListener('wheel', function (e) {
      fireAt('wheel', e.deltaY);
      e.preventDefault();
    }, { passive: false });
    vLayer.addEventListener('contextmenu', function (e) { e.preventDefault(); });
  }

  function ensureVLayer(pane) {
    if (!vLayer) {
      vLayer = document.createElement('div');
      vLayer.className = 'vmouse-layer';
      vDot = document.createElement('div');
      vDot.className = 'vmouse-dot';
      vLayer.appendChild(vDot);
      bindVEvents();
    }
    if (pane && vLayer.parentNode !== pane) {
      pane.appendChild(vLayer);
    }
    if (pane) {
      vLayer.classList.add('on');
      layoutVLayer();
      if (!vx || !vy) { vx = vClampW / 2; vy = vClampH / 2; }
      moveDot();
    }
  }

  function hideVLayer() {
    if (vLayer) vLayer.classList.remove('on');
  }

  function applyVMouse() {
    try { localStorage.setItem('nwg-vmouse', vMouse ? '1' : '0'); } catch (e) {}
    var lb = document.getElementById('vMouseLabel');
    if (lb) lb.textContent = '虚拟鼠标：' + (vMouse ? '开' : '关');
    if (vMouse && activePane) {
      ensureVLayer(activePane);
    } else hideVLayer();
  }

  window.addEventListener('resize', function () {
    layoutVLayer();
    // 横屏开关是“希望横屏”：手机自己转成横屏后就不必再 CSS 旋转
    if (typeof rotOn !== 'undefined' && rotOn) applyRot();
  });

  // ---------- 缩放 ----------
  // PC 版后台（尤其 UGOS 桌面）是按 1280+ 宽排版的，手机窄屏里只能看到一小块。
  // 这里按 CSS transform 缩整块 iframe，并把 width/height 反向放大，
  // 让上游拿到的是“更宽的视口”，再整体缩回屏幕——既保住桌面版布局，又能一屏看全。
  var ZOOM_MIN = 0.3, ZOOM_MAX = 2.2;

  function paneZoom(pane) {
    var z = Number(pane && pane.dataset.zoom);
    return (isFinite(z) && z > 0) ? z : 1;
  }

  function syncZoomUI() {
    var z = paneZoom(activePane);
    var pct = Math.round(z * 100) + '%';
    var lbl = document.getElementById('zoomLabel');
    if (lbl) lbl.textContent = pct;
    var out = document.getElementById('menuZoomOut');
    var inn = document.getElementById('menuZoomIn');
    if (out) out.title = '缩小（当前 ' + pct + '）';
    if (inn) inn.title = '放大（当前 ' + pct + '）';
    applyDotScale(z);
  }

  // 页面缩小时指针也跟着变小，否则蓝点会显得比整页还大
  function applyDotScale(z) {
    if (!vDot) return;
    var size = Math.round(Math.max(11, Math.min(20, 20 * Math.pow(z, 0.6))));
    vDot.style.setProperty('--vs', size + 'px');
  }

  function applyZoom(pane, z) {
    if (!pane) return;
    var f = pane.querySelector('.pane-frame');
    if (!f) return;
    z = Math.round(Math.max(ZOOM_MIN, Math.min(ZOOM_MAX, z)) * 100) / 100;
    pane.dataset.zoom = String(z);
    f.style.transformOrigin = '0 0';
    f.style.transform = z === 1 ? '' : 'scale(' + z + ')';
    f.style.position = 'relative';
    f.style.flex = '0 0 auto';
    f.style.width = (100 / z) + '%';
    f.style.height = (100 / z) + '%';
    try { localStorage.setItem('nwg-zoom', String(z)); } catch (e) {}
    if (pane === activePane) { layoutVLayer(); syncZoomUI(); }
  }

  function stepZoom(dir) {
    if (!activePane) return;
    applyZoom(activePane, paneZoom(activePane) + dir * 0.1);
  }

  function fitZoom() {
    if (!activePane) return;
    var w = panesEl.clientWidth || 390;
    applyZoom(activePane, w / 1280);
  }

  // 地址栏跟着当前标签走：切换标签即回显该标签正在访问的地址
  function activate(pane) {
    if (!pane || !pane.parentNode) return;
    homeMode = false;
    activePane = pane;
    panesList().forEach(function (p) {
      p.classList.toggle('on', p === pane);
    });
    urlInput.value = pane.dataset.url || '';
    syncClearBtn();
    if (vMouse) ensureVLayer(pane); else hideVLayer();
    syncZoomUI();
    renderTabs();
    var xy = tabsEl.querySelector('.tab.on');
    if (xy && xy.scrollIntoView) xy.scrollIntoView({ block: 'nearest', inline: 'nearest' });
  }

  function renderTabs() {
    tabsEl.innerHTML = '';
    panesList().forEach(function (p) {
      var tab = document.createElement('div');
      tab.className = 'tab' + (p === activePane ? ' on' : '');
      tab.title = p.dataset.url || '';
      tab.addEventListener('click', function () { activate(p); });

      var dot = document.createElement('span');
      dot.className = 'tab-dot';

      var name = document.createElement('span');
      name.className = 'tab-name';
      name.textContent = p.dataset.name || hostOf(p.dataset.url || '');

      var x = document.createElement('button');
      x.type = 'button';
      x.className = 'tab-x';
      x.setAttribute('aria-label', '关闭该标签');
      x.textContent = '×';
      x.addEventListener('click', function (e) {
        e.stopPropagation();
        closePane(p);
      });

      tab.appendChild(dot);
      tab.appendChild(name);
      tab.appendChild(x);
      tabsEl.appendChild(tab);
    });
  }

  function closePane(pane) {
    var list = panesList();
    var idx = list.indexOf(pane);
    pane.remove();
    if (activePane === pane || !activePane) {
      var next = list[idx + 1] && list[idx + 1].parentNode ? list[idx + 1]
        : (list[idx - 1] && list[idx - 1].parentNode ? list[idx - 1] : null);
      activePane = null;
      if (next) activate(next);
    }
    syncEmpty();
  }

  // opts: { proxy: bool, name: string, focus: bool }
  function addPane(rawURL, opts) {
    opts = opts || {};
    var url = normalize(rawURL);
    if (!url) return null;

    // 没显式指定时按地址去反查应用配置，这样历史/地址栏打开也继承「访问电脑版」
    var desktop = opts.desktop === undefined ? desktopFor(url) : !!opts.desktop;
    if (opts.desktop !== undefined) rememberDesk(url, desktop);
    var proxy = opts.proxy === undefined
      ? (localStorage.getItem('nwg-proxyDefault') !== '0')
      : !!opts.proxy;
    var u0 = new URL(url);
    // 公网站点（百度这类）经代理会被逐条改写 URL，脚本里动态拼接的地址极容易出错；
    // 它本来就是公网可达的，直连更稳。例外：https 外壳里打 http 目标只能走代理。
    var lanTarget = isLanHost(u0.hostname);
    var mixedBlocked = location.protocol === 'https:' && u0.protocol === 'http:';
    var notice = '';
    if (proxy && !lanTarget && !mixedBlocked) {
      proxy = false;
      notice = '这是公网站点，已自动改为直连：代理只适合内网设备，改写公网页面容易出错。个别站点禁止被嵌入，浏览器仍会拦，这类请直接用系统浏览器打开。';
    }
    // 代理模式先落在引导页：等 Service Worker 接管后再跳到干净路径，
    // 否则 SPA 的第一帧会停在 /api/p/<seg>/，前端路由匹配不上直接 404。
    var src = null;
    if (proxy) {
      if (swReady()) {
        // 首帧要落到上游的“真实路径”上，不能只跳根 "/"：
        // 飞牛桌面点“相册”打开的是 /p，只跳 "/" 就会渲染成桌面自己。
        var pu = new URL(url);
        var rest = pu.pathname + pu.search + pu.hash;
        src = '/boot.html?seg=' + segOf(pu.origin, desktop) + '&path=' + encodeURIComponent(rest || '/');
      } else {
        src = proxySrc(url, desktop);
      }
    } else {
      src = url;
    }
    if (!src) return null;

    var pane = document.createElement('section');
    pane.className = 'pane';
    pane.dataset.url = url;
    pane.dataset.name = opts.name || hostOf(url);
    pane.dataset.proxy = proxy ? '1' : '0';
    pane.dataset.origin = u0.origin;
    pane.dataset.desktop = desktop ? '1' : '0';
    pane.dataset.seg = proxy && swReady() ? segOf(u0.origin, desktop) : '';

    var head = document.createElement('div');
    head.className = 'pane-head';

    var dot = document.createElement('span');
    dot.className = 'dot';

    var title = document.createElement('span');
    title.className = 'pane-title';
    title.textContent = pane.dataset.name;

    var badge = null;
    if (!proxy) {
      badge = document.createElement('span');
      badge.className = 'pane-badge';
      badge.textContent = '直连';
      badge.title = '未开启代理，浏览器直连目标地址';
    }

    var fav = document.createElement('button');
    fav.className = 'mini star';
    fav.type = 'button';
    fav.textContent = '★';
    fav.title = '收藏';
    fav.addEventListener('click', function () {
      addFavorite(pane.dataset.url, title.textContent);
      fav.classList.add('on');
      setTimeout(function () { fav.classList.remove('on'); }, 700);
    });

    var close = document.createElement('button');
    close.className = 'mini';
    close.type = 'button';
    close.textContent = '×';
    close.title = '关闭';
    close.addEventListener('click', function () { closePane(pane); });

    var frame = document.createElement('iframe');
    frame.className = 'pane-frame';
    frame.setAttribute('referrerpolicy', 'no-referrer');
    frame.setAttribute('allow', 'fullscreen');
    frame.src = src;

    head.appendChild(dot);
    head.appendChild(title);
    if (badge) head.appendChild(badge);
    head.appendChild(fav);
    head.appendChild(close);
    pane.appendChild(head);

    // 外层页面是 https（UGREENlink / UGOS 桌面容器）时，浏览器会以混合内容
    // 和父页面 CSP 双重理由拦掉 iframe 里的 http 内网地址，直连模式必然白屏。
    // 与其让用户对着“该内容被屏蔽了”发呆，不如直接说明原因和替代方案。
    var warnText = notice;
    if (!warnText && !proxy && location.protocol === 'https:' && /^http:\/\//i.test(src)) {
      warnText = '当前外层页面是 https，浏览器不允许加载 http 内网地址（直连模式）。请在「网页应用」里把这一项改为代理模式。';
    }
    if (warnText) {
      var warn = document.createElement('div');
      warn.className = 'pane-warn';
      warn.textContent = warnText;
      pane.appendChild(warn);
    }

    pane.appendChild(frame);
    panesEl.appendChild(pane);
    pane.dataset.zoom = localStorage.getItem('nwg-zoom') || '1';
    applyZoom(pane, paneZoom(pane));

    // 代理内容与主页面同源，标题一般可读；直连时读不到就保留名称
    // 父页面在 iframe 上再加一层 window.open 拦截：代理内容与本页面同源，
    // 可以直接改写它的 open，交给本页面开新标签，飞牛桌面那个标签保持不动。
    frame.addEventListener('load', function () {
      // 代理模式里 iframe 是同源的，可以读出它当前所在的（被改写成干净路径的）地址，
      // 再用目标的 origin 还原成完整 URL 回填地址栏。
      try {
        var loc = frame.contentWindow.location;
        var path = (loc.pathname || '/') + (loc.search || '');
        // ?__nwg=<seg> 只是给 Service Worker 认路的标记。上游不可达时返回的是
        // 我们自己的错误页（或非 HTML），注入的净化脚本不会跑，标记就会留在
        // 地址栏上（http://1.2.3.4/?__nwg=aHR0… 这种）。这里统一兜底摘掉。
        if (path.indexOf('__nwg=') >= 0) {
          var clean = path.replace(/[?&]__nwg=[^&]*/, '').replace(/\?$/, '').replace(/\?&/, '?');
          try {
            frame.contentWindow.history.replaceState(null, '', clean + (loc.hash || ''));
          } catch (e2) {}
          path = clean;
        }
        // 还在引导页（boot.html）等 Service Worker 接管时，地址栏不要跟着跳，
        // 否则会短暂显示 http://目标/boot.html?seg=...
        var isBoot = path.indexOf('boot.html') >= 0;
        var marker = pane.dataset.seg ? '/api/p/' + pane.dataset.seg : '';
        if (marker && path.indexOf(marker) === 0) {
          path = path.slice(marker.length) || '/';
        }
        if (!isBoot && pane.dataset.origin && path.charAt(0) === '/') {
          var full = pane.dataset.origin + path;
          if (pane.dataset.url !== full) {
            pane.dataset.url = full;
            if (pane === activePane && document.activeElement !== urlInput) {
              urlInput.value = full;
              syncClearBtn();
            }
            renderTabs();
          }
        }
      } catch (e) {}
      try {
        var w = frame.contentWindow;
        if (w && !w.__nwgOpenHooked) {
          w.__nwgOpenHooked = true;
          w.open = function (u) {
            try {
              if (typeof u === 'string' && u && u !== 'about:blank') {
                var a = new URL(u, w.location.href);
                if (a.origin === location.origin) {
                  openInNewTab(a.pathname + a.search + a.hash, pane);
                  return w;
                }
              }
            } catch (err) { /* 交给原生 */ }
            return frame.contentWindow;
          };
        }
      } catch (err) { /* 直连跨源，跳过 */ }

      var t = '';
      try {
        t = (frame.contentDocument && frame.contentDocument.title) || '';
      } catch (e) { t = ''; }
      if (t && t.trim()) {
        pane.dataset.name = t.trim().slice(0, 60);
        title.textContent = pane.dataset.name;
        renderTabs();
      }
      recordHistory(pane.dataset.url, pane.dataset.name);
    });

    if (opts.focus !== false) activate(pane);
    syncEmpty();
    return pane;
  }

  function openFromInput(raw) {
    var url = normalize(raw);
    if (!url) return;
    var app = appFor(url);
    var pane = addPane(url, app ? { proxy: app.proxy !== false, name: app.name, desktop: !!app.desktop } : {});
    if (pane) activate(pane);
    urlInput.blur();
  }

  // ---------- 空状态 ----------
  var recentHidden = false;
  // 访问电脑版：默认开。上游按桌面 UA 对待（绿联云 9999、LuCI 都要求桌面 UA），
  // 关掉后按移动端 UA 访问，个别站点会切到手机版页面。开关用本域 cookie 传给后端。
  var desktopUA = localStorage.getItem('nwg-desktopUA') === '1';
  try { recentHidden = localStorage.getItem('nwg-hideRecent') === '1'; } catch (e) {}
  if (recentWrap) recentWrap.classList.toggle('hidden', recentHidden);
  if (recentHide) {
    recentHide.addEventListener('click', function () {
      recentHidden = true;
      recentWrap.classList.add('hidden');
      try { localStorage.setItem('nwg-hideRecent', '1'); } catch (e) {}
    });
  }

  function renderRecent() {
    recentEl.innerHTML = '';
    if (recentHidden) return;
    api('/api/history').then(function (list) {
      if (Array.isArray(list)) {
        // 老数据里可能已经存进了“加载中”这类占位标题，显示时一并滤掉
        list = list.filter(function (it) {
          var t = (it && it.title || '').trim();
          return !PLACEHOLDER_TITLES[t] && t.indexOf('正在接管') < 0;
        });
      }
      if (!Array.isArray(list) || list.length === 0) {
        recentEl.innerHTML = '<div class="recent-tip">还没有访问记录</div>';
        return;
      }
      list.slice(0, 6).forEach(function (item) {
        var chip = document.createElement('span');
        chip.className = 'chip';

        var open = document.createElement('button');
        open.type = 'button';
        open.className = 'chip-open';
        open.textContent = item.title || hostOf(item.url);
        open.title = item.url;
        open.addEventListener('click', function () { openFromInput(item.url); });
        chip.appendChild(open);

        var del = document.createElement('button');
        del.type = 'button';
        del.className = 'chip-x';
        del.setAttribute('aria-label', '删除这条记录');
        del.textContent = '×';
        del.addEventListener('click', function (e) {
          e.stopPropagation();
          api('/api/history?url=' + encodeURIComponent(item.url), { method: 'DELETE' })
            .then(function () { renderRecent(); });
        });
        chip.appendChild(del);
        recentEl.appendChild(chip);
      });
    });
  }

  // ---------- 网页应用 ----------
  function initial(name) {
    var n = (name || '?').trim();
    return (n.charAt(0) || '?').toUpperCase();
  }

  function loadApps() {
    api('/api/apps').then(function (list) {
      appsCache = Array.isArray(list) ? list : [];
      renderApps();
    });
  }

  function renderApps() {
    appsEl.innerHTML = '';
    emptyHint.classList.toggle('hidden', appsCache.length > 0);

    appsCache.forEach(function (app) {
      var card = document.createElement('div');
      card.className = 'app';
      card.title = app.url;

      var more = document.createElement('button');
      more.className = 'app-more';
      more.type = 'button';
      more.textContent = '⋯';
      more.title = '编辑';
      more.addEventListener('click', function (e) {
        e.stopPropagation();
        openSheet(app);
      });

      var icon = document.createElement('span');
      icon.className = 'app-icon';
      icon.textContent = initial(app.name);
      icon.style.background = app.color || '#5b8def';

      var name = document.createElement('span');
      name.className = 'app-name';
      name.textContent = app.name || hostOf(app.url);

      var note = document.createElement('span');
      note.className = 'app-note';
      note.textContent = app.note || (app.proxy ? '经 NAS 转发' : '直连');

      card.appendChild(more);
      card.appendChild(icon);
      card.appendChild(name);
      card.appendChild(note);

      card.addEventListener('click', function () {
        addPane(app.url, { proxy: app.proxy !== false, name: app.name, desktop: !!app.desktop });
      });

      appsEl.appendChild(card);
    });
  }

  // ---------- 编辑页 ----------
  function paintPreview() {
    pvRow.style.setProperty('--pvbg', editColor);
    pvRow.style.setProperty('--pvfg', editColor);
    pvIcon.style.background = '#fff';
    pvIcon.style.color = editColor;
    pvIcon.textContent = initial(appName.value);
    pvName.textContent = appName.value.trim() || appAddr.value.trim() || '未命名';
  }

  function renderColors() {
    colorsEl.innerHTML = '';
    PALETTE.forEach(function (c) {
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'swatch' + (c === editColor ? ' on' : '');
      b.style.background = c;
      b.addEventListener('click', function () {
        editColor = c;
        renderColors();
        paintPreview();
      });
      colorsEl.appendChild(b);
    });
  }

  function openSheet(app) {
    editing = app || null;
    sheetTitle.textContent = app ? '编辑网页应用' : '新建网页应用';
    sheetDel.classList.toggle('hidden', !app);

    appName.value = app ? app.name : '';
    appNote.value = app ? (app.note || '') : '';
    appProxy.checked = app ? app.proxy !== false : true;
    if (appDesktop) appDesktop.checked = !!(app && app.desktop);
    editColor = (app && app.color) || PALETTE[1];

    var url = app ? app.url : '';
    if (/^https:\/\//i.test(url)) {
      appScheme.value = 'https://';
      appAddr.value = url.slice(8);
    } else {
      appScheme.value = 'http://';
      appAddr.value = url.replace(/^http:\/\//i, '');
    }

    renderColors();
    paintPreview();
    sheet.classList.remove('hidden');
    if (!app) appName.focus();
  }

  function closeSheet() {
    sheet.classList.add('hidden');
    editing = null;
  }

  function collect() {
    var addr = appAddr.value.trim().replace(/^https?:\/\//i, '');
    if (!addr) return null;
    var url = appScheme.value + addr;
    var name = appName.value.trim() || hostOf(url);
    return {
      id: editing ? editing.id : '',
      name: name,
      url: url,
      note: appNote.value.trim(),
      color: editColor,
      proxy: appProxy.checked,
      desktop: !!(appDesktop && appDesktop.checked)
    };
  }

  addAppBtn.addEventListener('click', function () { openSheet(null); });
  sheetCancel.addEventListener('click', closeSheet);

  appName.addEventListener('input', paintPreview);
  appAddr.addEventListener('input', paintPreview);

  sheetOk.addEventListener('click', function () {
    var item = collect();
    if (!item) {
      appAddr.focus();
      return;
    }
    api('/api/apps', { method: 'POST', body: JSON.stringify(item) }).then(function () {
      closeSheet();
      loadApps();
    });
  });

  sheetDel.addEventListener('click', function () {
    if (!editing) return;
    api('/api/apps?id=' + encodeURIComponent(editing.id), { method: 'DELETE' }).then(function () {
      closeSheet();
      loadApps();
    });
  });

  // ---------- 布局 ----------
  function applyLayout() {
    panesEl.classList.toggle('split', layout === 'split');
    layoutLabel.textContent = layout === 'split' ? '切换为单栏' : '切换为并排';
  }

  // ---------- 全屏 ----------
  var fsBtn = document.getElementById('fsBtn');

  function isFullscreen() {
    return !!(document.fullscreenElement || document.webkitFullscreenElement);
  }

  function toggleFullscreen() {
    var el = document.documentElement;
    if (isFullscreen()) {
      (document.exitFullscreen || document.webkitExitFullscreen).call(document);
      return;
    }
    var req = el.requestFullscreen || el.webkitRequestFullscreen;
    if (!req) return;
    var p = req.call(el);
    if (p && p.catch) p.catch(function () {});
  }

  fsBtn.addEventListener('click', toggleFullscreen);

  var rotOn = localStorage.getItem('nwg-rot') === '1';
  applyRot();
  applyRot();

  // ---------- 缩放按钮 ----------
  // 点一下步进 10%，长按复位 100%
  function bindZoomBtn(btn, dir) {
    if (!btn) return;
    var timer = null, hold = false;
    btn.addEventListener('pointerdown', function () {
      hold = false;
      timer = setTimeout(function () { hold = true; applyZoom(activePane, 1); }, 700);
    });
    ['pointerup', 'pointercancel', 'pointerleave'].forEach(function (ev) {
      btn.addEventListener(ev, function () {
        if (timer) { clearTimeout(timer); timer = null; }
      });
    });
    btn.addEventListener('click', function () {
      if (hold) { hold = false; return; }
      stepZoom(dir);
    });
  }
  bindZoomBtn(document.getElementById('menuZoomOut'), -1);
  bindZoomBtn(document.getElementById('menuZoomIn'), 1);
  (function () {
    var v = document.getElementById('zoomLabel');
    if (v) v.addEventListener('click', function () { applyZoom(activePane, 1); });
    var r = document.getElementById('menuZoomReset');
    if (r) r.addEventListener('click', function () { applyZoom(activePane, 1); });
  })();

  // ---------- 横屏 ----------
  // 不依赖系统旋转锁：直接把整个界面顺时针转 90°，body 的宽高对调，
  // 页面因此拿到“更宽的视口”，UGOS 桌面这类横版界面才能整屏显示。
  var rotOn = localStorage.getItem('nwg-rot') === '1';

  function applyRot() {
    // 手机自己已经横过来了（或平板）就别再转一次，否则会转成“倒过来”的界面
    var need = rotOn && (window.innerHeight > window.innerWidth);
    document.body.classList.toggle('rot', need);
    var lb = document.getElementById('rotLabel');
    if (lb) lb.textContent = rotOn ? '退出横屏' : '横屏显示';
    var mb = document.getElementById('menuRot');
    if (mb) mb.classList.toggle('on', rotOn);
    try { localStorage.setItem('nwg-rot', rotOn ? '1' : '0'); } catch (e) {}
    setTimeout(function () { layoutVLayer(); syncZoomUI(); }, 0);
  }

  // ---------- 隐藏工具栏（沉浸查看） ----------
  // 收起后地址栏和标签栏都不占地方，页面拿到整屏高度；
  // 右上角留一个半透明小胶囊，点一下把工具栏收回来。
  var hideBtn = document.getElementById('hideBtn');
  var chromeRestore = document.getElementById('chromeRestore');

  function setChromeHidden(on) {
    document.body.classList.toggle('chrome-hide', !!on);
    if (chromeFade) { clearTimeout(chromeFade); chromeFade = null; }
    if (chromeRestore) chromeRestore.style.opacity = '';
    if (on && chromeRestore) {
      // 3 秒后淡到 30%，碰一下（pointerdown）立刻回满
      chromeFade = setTimeout(function () {
        chromeFade = null;
        chromeRestore.style.opacity = '0.3';
      }, 3000);
    }
    setTimeout(function () { layoutVLayer(); }, 0);
  }

  var chromeFade = null;
  if (chromeRestore) {
    ['pointerdown', 'touchstart'].forEach(function (ev) {
      chromeRestore.addEventListener(ev, function () {
        if (chromeFade) { clearTimeout(chromeFade); chromeFade = null; }
        chromeRestore.style.opacity = '1';
      }, { passive: true });
    });
  }

  if (hideBtn) hideBtn.addEventListener('click', function () { setChromeHidden(true); });
  if (chromeRestore) chromeRestore.addEventListener('click', function () { setChromeHidden(false); });

  function syncFsBtn() {
    var on = isFullscreen();
    fsBtn.classList.toggle('on', on);
    fsBtn.title = on ? '退出全屏' : '全屏';
    setChromeHidden(on);
  }

  document.addEventListener('fullscreenchange', syncFsBtn);
  document.addEventListener('webkitfullscreenchange', syncFsBtn);

  // ---------- 一键切代理 ----------
  // 同一个地址，代理 ⇄ 直连；换模式时按当前缩放和标题重建一个标签，原标签关掉。
  function toggleProxyActive() {
    if (!activePane) {
      // 没有打开的标签时，拿地址栏里的地址开一个直连/代理标签
      var v = urlInput.value.trim();
      if (!v) return null;
      var cur = localStorage.getItem('nwg-proxyDefault') === '0';
      localStorage.setItem('nwg-proxyDefault', cur ? '1' : '0');
      return addPane(v, { proxy: cur });
    }
    var url = activePane.dataset.url;
    var name = activePane.dataset.name;
    var wasProxy = activePane.dataset.proxy === '1';
    var z = paneZoom(activePane);
    var old = activePane;
    var np = addPane(url, { proxy: !wasProxy, name: name });
    if (!np) return null;
    applyZoom(np, z);
    closePane(old);
    activate(np);
    return np;
  }

  function syncProxyLabel() {
    var lb = document.getElementById('proxyLabel');
    if (!lb) return;
    var on = activePane ? activePane.dataset.proxy === '1' : true;
    lb.textContent = activePane
      ? (on ? '切换为直连' : '切换为代理模式')
      : '切换代理模式';
  }

  // ---------- 菜单 ----------
  function setMenu(open) {
    menu.classList.toggle('hidden', !open);
    menuBtn.setAttribute('aria-expanded', open ? 'true' : 'false');
  }

  function toggleMenu() {
    syncProxyLabel();
    syncZoomUI();
    setMenu(menu.classList.contains('hidden'));
  }

  menuBtn.addEventListener('click', function (e) {
    e.stopPropagation();
    toggleMenu();
  });

  function applyUA() {
    try { localStorage.setItem('nwg-desktopUA', desktopUA ? '1' : '0'); } catch (e) {}
    document.cookie = 'nwg_ua=' + (desktopUA ? 'desktop' : 'mobile') + '; path=/; max-age=31536000';
    var lb = document.getElementById('uaLabel');
    if (lb) lb.textContent = '访问电脑版：' + (desktopUA ? '开' : '关');
  }
  applyUA();
  (function () {
    var lb = document.getElementById('vMouseLabel');
    if (lb) lb.textContent = '虚拟鼠标：' + (vMouse ? '开' : '关');
  })();

  document.addEventListener('click', function (e) {
    if (!menu.contains(e.target) && !menuBtn.contains(e.target)) setMenu(false);
  });

  menu.addEventListener('click', function (e) {
    var btn = e.target.closest('button[data-act]');
    if (!btn) return;
    var act = btn.dataset.act;
    setMenu(false);

    if (act === 'newApp') {
      openSheet(null);
    } else if (act === 'fav') {
      if (urlInput.value.trim()) addFavorite(normalize(urlInput.value), null);
      else openDrawer('fav');
    } else if (act === 'favList') {
      openDrawer('fav');
    } else if (act === 'history') {
      openDrawer('history');
    } else if (act === 'showRecent') {
      recentHidden = false;
      recentWrap.classList.remove('hidden');
      try { localStorage.removeItem('nwg-hideRecent'); } catch (e) {}
      renderRecent();
    } else if (act === 'vmouse') {
      vMouse = !vMouse;
      applyVMouse();
    } else if (act === 'toggleProxy') {
      toggleProxyActive();
    } else if (act === 'rotate') {
      rotOn = !rotOn;
      applyRot();
    } else if (act === 'fit') {
      fitZoom();
    } else if (act === 'uaMode') {
      desktopUA = !desktopUA;
      applyUA();
      // 已开的标签重新装载一遍，让新 UA 立刻生效
      Array.prototype.forEach.call(panesEl.querySelectorAll('.pane-frame'), function (f) {
        if (f.src) f.src = f.src;
      });
    } else if (act === 'layout') {
      layout = layout === 'split' ? 'single' : 'split';
      localStorage.setItem('nwg.layout', layout);
      applyLayout();
    } else if (act === 'closeAll') {
      panesEl.innerHTML = '';
      activePane = null;
      syncEmpty();
    }
  });

  // ---------- 抽屉 ----------
  function openDrawer(mode) {
    drawerMode = mode;
    drawerTitle.textContent = mode === 'fav' ? '收藏夹' : '浏览历史';
    clearBtn.textContent = mode === 'fav' ? '清空收藏' : '清空历史';
    drawer.classList.remove('hidden');
    renderDrawer();
  }

  function closeDrawer() { drawer.classList.add('hidden'); }

  function renderDrawer() {
    drawerList.innerHTML = '<li class="empty-tip">载入中…</li>';
    var path = drawerMode === 'fav' ? '/api/fav' : '/api/history';
    api(path).then(function (list) {
      if (!Array.isArray(list) || list.length === 0) {
        drawerList.innerHTML = '<li class="empty-tip">' +
          (drawerMode === 'fav' ? '还没有收藏' : '还没有记录') + '</li>';
        return;
      }
      drawerList.innerHTML = '';
      list.forEach(function (item) {
        var li = document.createElement('li');

        var main = document.createElement('div');
        main.className = 'li-main';

        var t = document.createElement('div');
        t.className = 'li-title';
        t.textContent = item.title || item.name || item.url;

        var s = document.createElement('div');
        s.className = 'li-sub';
        s.textContent = item.url;

        main.appendChild(t);
        main.appendChild(s);
        li.appendChild(main);

        if (drawerMode === 'fav') {
          var del = document.createElement('button');
          del.className = 'li-del';
          del.type = 'button';
          del.textContent = '×';
          del.title = '删除';
          del.addEventListener('click', function (ev) {
            ev.stopPropagation();
            api('/api/fav?url=' + encodeURIComponent(item.url), { method: 'DELETE' })
              .then(renderDrawer);
          });
          li.appendChild(del);
        } else {
          // 历史同样支持单条删除（后端 DELETE /api/history?url=）
          var hdel = document.createElement('button');
          hdel.className = 'li-del';
          hdel.type = 'button';
          hdel.textContent = '×';
          hdel.title = '删除这条记录';
          hdel.addEventListener('click', function (ev) {
            ev.stopPropagation();
            api('/api/history?url=' + encodeURIComponent(item.url), { method: 'DELETE' })
              .then(function () { renderDrawer(); renderRecent(); });
          });
          li.appendChild(hdel);
        }

        li.addEventListener('click', function () {
          closeDrawer();
          var pane = addPane(item.url);
          if (pane) activate(pane);
        });

        drawerList.appendChild(li);
      });
    });
  }

  document.getElementById('drawerClose').addEventListener('click', closeDrawer);

  clearBtn.addEventListener('click', function () {
    if (drawerMode === 'fav') {
      api('/api/fav').then(function (list) {
        (list || []).forEach(function (it) {
          api('/api/fav?url=' + encodeURIComponent(it.url), { method: 'DELETE' });
        });
        setTimeout(renderDrawer, 250);
      });
    } else {
      api('/api/history', { method: 'DELETE' }).then(renderDrawer);
    }
  });

  // ---------- 收藏 ----------
  function addFavorite(url, presetName) {
    var name = window.prompt('收藏名称', presetName || hostOf(url) || url);
    if (name === null) return;
    api('/api/fav', {
      method: 'POST',
      body: JSON.stringify({ name: name || hostOf(url), url: url })
    });
  }

  // ---------- 绑定 ----------
  function syncClearBtn() {
    clearInput.classList.toggle('hidden', !urlInput.value.trim());
  }

  urlInput.addEventListener('input', syncClearBtn);

  clearInput.addEventListener('click', function () {
    urlInput.value = '';
    syncClearBtn();
    urlInput.focus();
  });

  addrForm.addEventListener('submit', function (e) {
    e.preventDefault();
    openFromInput(urlInput.value);
  });

  // 标签栏右侧的「＋」：开一个空白标签，光标落到地址栏
  tabAddBtn.addEventListener('click', function () {
    showHome();
    urlInput.focus();
  });

  // 地址栏失焦后，如果没输入新内容，就回显当前标签的地址
  urlInput.addEventListener('blur', function () {
    if (!urlInput.value.trim() && activePane) {
      urlInput.value = activePane.dataset.url || '';
      syncClearBtn();
    }
  });

  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape') {
      setMenu(false);
      closeDrawer();
      if (!sheet.classList.contains('hidden')) closeSheet();
    }
    if (e.key === 'l' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      urlInput.focus();
      urlInput.select();
    }
  });

  // ---------- 被代理页面要求开新标签 ----------
  // from 是发起请求的那个标签（飞牛桌面），保留它，另开一个新标签承载目标页。
  function openInNewTab(relURL, from) {
    var base = from && from.dataset.url;
    var proxy = from ? from.dataset.proxy !== '0' : true;
    var full = relURL;
    try {
      var u = new URL(relURL, base || location.href);
      if (base) {
        // 上游页面发来的是它自己的路径，要拼回上游的 origin
        var bu = new URL(base);
        full = bu.origin + u.pathname + u.search + u.hash;
      } else {
        full = u.href;
      }
    } catch (err) {
      return null;
    }
    return addPane(full, { proxy: proxy });
  }

  // 飞牛桌面点应用图标走 window.open，原生行为会弹一个没有代理上下文的新窗口，
  // 那条请求以裸路径打到网关根变 404。注入脚本把它改成 postMessage 到这里，
  // 由我们用当前标签的上游地址给它开一个新标签，原标签（飞牛桌面）保持不动。
  window.addEventListener('message', function (e) {
    if (e.origin !== location.origin) return;
    var d = e.data || {};
    if (d.__nwg !== 'open' || typeof d.url !== 'string') return;
    openInNewTab(d.url, activePane);
  });


  applyLayout();
  syncZoomUI();
  loadApps();
  syncEmpty();
})();
