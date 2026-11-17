package main

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

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
	return j
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
