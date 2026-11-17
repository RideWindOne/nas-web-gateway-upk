// Service Worker：代理模式下拦“逃出前缀”的请求。
//
// 原来的路径前缀反代（/api/p/<b64>/...）对 SPA 有死结：页面里
// location.href='/login' 是整页导航，会绕开前缀打到网关根 → 404 → 前端
// “拽回” → redirect_uri 层层嵌套。SW 能拦到这类导航，于是：
//
//   地址栏留在干净路径 /login   ←  内容却从 /api/p/<seg>/login 取回
//
// 取回时仍走后端原有的前缀代理，所以 HTML 照样被改写（base href、属性补前缀、
// 注入脚本），资源和后续请求都自然落回前缀，不会再有裸根路径漏出去。
//
// clientId 对同一 iframe 的所有请求稳定，注入脚本会 postMessage 绑定。
//
// 关键：这份“哪个 client 属于哪个上游”的映射必须落盘。浏览器空闲 30 秒就会把
// SW 停掉，内存里的 Map 跟着没了；而 iframe 的地址停在干净的 "/" 上，没有映射
// 时它下一次导航要么落回本应用首页、要么被兜到别的标签的上游——表现出来就是
// “开了第二个标签，回第一个标签内容串了”。所以映射双写 IndexedDB，SW 重启后
// 先查内存、再查库、最后从 client 自己的 URL 里还原。

const DB = 'nwg-sw';
const STORE = 'bind';
const MAP = new Map(); // clientId -> seg（内存缓存，IndexedDB 的加速层）
// 主页面自己的 clientId。主页面的资源和被代理页面的资源路径会撞车
// （两边都有 /、/css、/js），必须先按 clientId 分清谁是谁。
const APP = new Set();

let dbPromise = null;
function openDB() {
  if (dbPromise) return dbPromise;
  dbPromise = new Promise((resolve) => {
    try {
      const req = indexedDB.open(DB, 1);
      req.onupgradeneeded = () => {
        const db = req.result;
        if (!db.objectStoreNames.contains(STORE)) db.createObjectStore(STORE);
      };
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => resolve(null);
    } catch (e) { resolve(null); }
  });
  return dbPromise;
}

async function dbPut(key, seg) {
  const db = await openDB();
  if (!db) return;
  try { db.transaction(STORE, 'readwrite').objectStore(STORE).put(seg, key); } catch (e) { /* 落盘失败不阻塞请求 */ }
}

async function dbGet(key) {
  const db = await openDB();
  if (!db) return null;
  try {
    return await new Promise((resolve) => {
      const q = db.transaction(STORE, 'readonly').objectStore(STORE).get(key);
      q.onsuccess = () => resolve(q.result || null);
      q.onerror = () => resolve(null);
    });
  } catch (e) { return null; }
}

self.addEventListener('install', () => self.skipWaiting());

self.addEventListener('activate', (e) => {
  e.waitUntil(self.clients.claim());
});

self.addEventListener('message', (e) => {
  const d = e.data || {};
  if (!e.source || !e.source.id) return;
  if (d.__nwg === 'app') {
    APP.add(e.source.id);
    return;
  }
  if (d.__nwg === 'bind') {
    const seg = segOf(d.prefix);
    if (seg) {
      MAP.set(e.source.id, seg);
      dbPut(e.source.id, seg);
    }
  }
});

// 应用自身的资源与接口直连；/api/p/ 带前缀的也放行（那是后端改写链路本身）
function isOwnPath(path) {
  if (path === '/' || path === '/sw.js' || path === '/favicon.ico') return true;
  if (path === '/boot.html') return true;
  if (path.indexOf('/css/') === 0 || path.indexOf('/js/') === 0) return true;
  if (path.indexOf('/api/') === 0) return true;
  // 注入脚本由应用自己下发，不能当代理请求转发到上游
  if (path.indexOf('/api/inject.js') === 0) return true;
  return false;
}

function segOf(text) {
  if (!text) return null;
  let s = text;
  try {
    s = decodeURIComponent(text);
  } catch (err) { /* 保持原样 */ }
  const m = s.match(/\/api\/p\/([A-Za-z0-9_-]+)/);
  if (m) return m[1];
  // 首帧导航会把上游 seg 直接写在 URL 上（?__nwg=<seg>），这样不依赖
  // postMessage 与导航之间的先后顺序，避免绑定竞态。
  const q = s.match(/[?&]__nwg=([A-Za-z0-9_-]+)/);
  return q ? q[1] : null;
}

// 整页导航会在同一个浏览上下文里换一个 clientId：老 id 随旧文档作废，
// 新文档拿到的是 e.resultingClientId。必须跟着一起登记，否则新文档后续的
// 子资源请求（/assets/*.js 这类绝对路径）就找不到上游，全部打到网关根 404。
function remember(seg, e) {
  const cid = e.clientId || '';
  const rid = e.resultingClientId || '';
  if (cid) { MAP.set(cid, seg); dbPut(cid, seg); }
  if (rid) { MAP.set(rid, seg); dbPut(rid, seg); }
}

self.addEventListener('fetch', (e) => {
  const req = e.request;
  let u;
  try {
    u = new URL(req.url);
  } catch (err) {
    return;
  }

  // 只接管同源请求；直连模式的 iframe 是跨源，本来就不需要转发
  if (u.origin !== self.location.origin) return;

  const cid = e.clientId || '';

  // 0) 首帧导航把上游 seg 直接写在 ?__nwg=<seg> 上，优先级最高。
  //    iframe 的首次导航，e.clientId 常常是“发起这次导航的父文档”——
  //    父文档就在 APP 集合里，如果先按 APP 放行，这个 iframe 拿回来的
  //    就是我们自己的首页，看起来就是“点开应用结果回到桌面”。
  if (req.mode === 'navigate' && u.search.indexOf('__nwg=') >= 0) {
    const qseg = segOf(u.pathname + u.search);
    if (qseg) {
      remember(qseg, e);
      e.respondWith(forward(req, qseg, u));
      return;
    }
  }

  // 1) 主页面自身：一律直连后端，绝不转发
  if (APP.has(cid)) return;

  e.respondWith(handle(req, u, e, cid));
});

async function handle(req, u, e, cid) {
  // 2) 已绑定上游：先查内存，再查落盘的映射（SW 被回收重启后内存是空的），
  //    最后从 client 自己的 URL 里还原（干净的 "/?__nwg=<seg>" 就属于这种）
  let seg = cid ? MAP.get(cid) : null;
  if (!seg && cid) {
    seg = await dbGet(cid);
    if (seg) MAP.set(cid, seg);
  }
  if (!seg && cid) {
    try {
      const c = await self.clients.get(cid);
      if (c) seg = segOf(c.url);
    } catch (err) { /* 忽略 */ }
  }
  if (seg) {
    remember(seg, e);
    return forward(req, seg, u);
  }

  // 3) 还没绑定的自有资源，直接放行（引导页 boot.html、sw.js 等）
  if (isOwnPath(u.pathname)) return fetch(req);

  seg = segOf(u.pathname + u.search) || segOf(req.referrer);
  // 注意：这里不再用「最近一次绑定的上游」兜底。两个标签同时在用时，
  // 那个全局兜底会把 A 的请求打到 B 的上游，正是“内容串了”的来源。
  // 认不出来就原样放行，宁可 404 也不串到别的设备。
  if (!seg) return fetch(req);

  remember(seg, e);
  return forward(req, seg, u);
}

// 请求打到“干净路径”，内容从“带前缀路径”取回，两边靠这一段映射衔接
async function forward(req, seg, u) {
  // 已经带前缀的请求（改写后的 HTML 里 base href 生成的那种）不能再加一次前缀，
  // 否则会拼成 /api/p/<seg>/api/p/<seg>/xxx 全 404。
  const p = u.pathname.indexOf('/api/p/') === 0 ? u.pathname : '/api/p/' + seg + u.pathname;
  // __nwg 只是我们用来告诉 SW 归属的标记，不能透传给上游站点
  const sp = new URLSearchParams(u.search);
  sp.delete('__nwg');
  const qs = sp.toString();
  const target = p + (qs ? '?' + qs : '');
  try {
    const init = {
      method: req.method,
      headers: stripHeaders(req.headers),
      redirect: 'follow',
      credentials: 'same-origin',
    };
    if (req.method !== 'GET' && req.method !== 'HEAD') {
      init.body = await req.arrayBuffer();
    }

    const res = await fetch(target, init);
    const out = new Headers();
    res.headers.forEach((v, k) => {
      const lk = k.toLowerCase();
      // 这两个交给浏览器按实际字节重新算，原值会误导
      if (lk === 'content-length' || lk === 'content-encoding') return;
      out.set(k, v);
    });

    const buf = await res.arrayBuffer();
    return new Response(buf, {
      status: res.status,
      statusText: res.statusText,
      headers: out,
    });
  } catch (err) {
    return new Response('转发失败：' + err, {
      status: 502,
      headers: { 'Content-Type': 'text/plain; charset=utf-8' },
    });
  }
}

function stripHeaders(h) {
  const out = new Headers();
  h.forEach((v, k) => {
    const lk = k.toLowerCase();
    if (lk === 'host' || lk === 'cookie' || lk === 'referer' ||
        lk === 'origin' || lk === 'content-length') return;
    out.set(k, v);
  });
  return out;
}
