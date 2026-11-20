package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cookie 桶的 key：把“同一台机器、同一个端口”的不同写法收敛成同一个桶。
// 浏览器侧只有一个 origin，但同一个应用可能经多条路径进来：
//   飞牛桌面包装 /api/p/<seg>/…  → 上游 127.0.0.1:16601
//   局域网入口   http://172.30.2.231:16601
//   本机地址     http://localhost:16601
// 按原始 host 分桶时，登录态只落在其中一桶，页面只要跳一次别的写法就掉登录。
// 这里只收敛本机别名（回环 + 本机私网地址 + localhost），端口原样保留：
// 不同应用端口不同，所以仍然各是一个桶，不会互相串 cookie（飞牛 EasyTier 这类
// 自带鉴权的应用尤其不能被别的应用的 token 污染）。
func cookieKey(host string) string {
	name := host
	port := ""
	if h, p, err := net.SplitHostPort(host); err == nil {
		name, port = h, p
	} else if strings.HasPrefix(host, "[") {
		// IPv6 字面量
		name = strings.Trim(host, "[]")
	}
	if name == "localhost" || name == "::1" || name == "0.0.0.0" {
		name = "127.0.0.1"
	} else if ip := net.ParseIP(name); ip != nil && (ip.IsLoopback() || isLocalIP(ip)) {
		name = "127.0.0.1"
	}
	if port != "" {
		return net.JoinHostPort(name, port)
	}
	return name
}

// isLocalIP 判断是否是本机自己的私网地址（NAS 的局域网 IP）。
func isLocalIP(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && ipn.IP.Equal(v4) {
			return true
		}
	}
	return false
}

// CookieJar 按目标 host 保存 cookie。代理响应的 Set-Cookie 到不了浏览器，
// 所以由后端充当 HTTP 客户端持有一份，重启应用后登录态依然在。
type CookieJar struct {
	mu   sync.Mutex
	path string
	data map[string]map[string]cookieEntry
}

type cookieEntry struct {
	Value   string `json:"v"`
	Path    string `json:"p,omitempty"`
	Expires int64  `json:"e,omitempty"`
}

func NewCookieJar(path string) *CookieJar {
	j := &CookieJar{path: path, data: map[string]map[string]cookieEntry{}}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		_ = json.Unmarshal(b, &j.data)
	}
	if j.data == nil {
		j.data = map[string]map[string]cookieEntry{}
	}
	// 迁移旧桶：以前按原始 host（172.30.2.231:16601、127.0.0.1:16601…）分桶，
	// 现在按 cookieKey 收敛。老的键合并到新键下，本机别名桶里的同名 cookie 优先
	// （127.0.0.1 那一桶通常是长期在用的那份），免得升级后所有应用都要重新登录。
	j.migrateKeys()
	return j
}

func (j *CookieJar) migrateKeys() {
	merged := map[string]map[string]cookieEntry{}
	oldKeys := make([]string, 0, len(j.data))
	for k := range j.data {
		oldKeys = append(oldKeys, k)
	}
	sort.Strings(oldKeys)
	order := make([]string, 0, len(oldKeys))
	for _, k := range oldKeys {
		nk := cookieKey(k)
		if _, ok := merged[nk]; !ok {
			merged[nk] = map[string]cookieEntry{}
			order = append(order, nk)
		}
		for ck, e := range j.data[k] {
			if _, dup := merged[nk][ck]; dup {
				continue
			}
			merged[nk][ck] = e
		}
		delete(j.data, k)
	}
	j.mu.Lock()
	for k, v := range merged {
		j.data[k] = v
	}
	if len(order) > 0 {
		j.saveLocked()
	}
	j.mu.Unlock()
}

func (j *CookieJar) Get(host string) string {
	j.mu.Lock()
	defer j.mu.Unlock()

	m := j.data[host]
	if len(m) == 0 {
		return ""
	}
	now := time.Now().Unix()
	keys := make([]string, 0, len(m))
	for k, e := range m {
		if e.Expires > 0 && e.Expires < now {
			delete(m, k)
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k].Value)
	}
	return strings.Join(parts, "; ")
}

func (j *CookieJar) Set(host string, setCookies []string) {
	if len(setCookies) == 0 {
		return
	}
	header := http.Header{}
	for _, v := range setCookies {
		header.Add("Set-Cookie", v)
	}
	resp := &http.Response{Header: header}
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		return
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	m := j.data[host]
	if m == nil {
		m = map[string]cookieEntry{}
		j.data[host] = m
	}
	for _, c := range cookies {
		if c.MaxAge < 0 || (c.MaxAge == 0 && c.Value == "" && !c.Expires.IsZero()) {
			delete(m, c.Name)
			continue
		}
		e := cookieEntry{Value: c.Value, Path: c.Path}
		if !c.Expires.IsZero() {
			e.Expires = c.Expires.Unix()
		}
		m[c.Name] = e
	}
	j.saveLocked()
}

func (j *CookieJar) saveLocked() {
	b, err := json.Marshal(j.data)
	if err != nil {
		return
	}
	tmp := j.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, j.path)
}
