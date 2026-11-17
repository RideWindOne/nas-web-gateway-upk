package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type relayRequest struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"` // base64
}

// RelayHandler 给前端 Service Worker 用。
//
// 为什么要多这一层：路径前缀反代对 SPA 有死结——页面里 location.href='/login'
// 这种整页跳转会绕开 /api/p/ 前缀，请求直接打到网关根而 404，再被"拽回"
// 就形成 redirect_uri 层层嵌套。Service Worker 能拦截这类导航请求，把整页跳转
// 也变成一次可转发的请求，于是 iframe 的地址可以停在干净的 /login 上。
//
// 这里只负责"按 URL 取回原始响应"，改写一律交给前端按标签自己决定。
func (p *Proxy) RelayHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var rr relayRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 96<<20)).Decode(&rr); err != nil {
		http.Error(w, "bad relay payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	u, err := url.Parse(rr.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		http.Error(w, "bad relay url", http.StatusBadRequest)
		return
	}

	method := rr.Method
	if method == "" {
		method = http.MethodGet
	}

	var body io.Reader
	if rr.Body != "" {
		b, err := base64.StdEncoding.DecodeString(rr.Body)
		if err != nil {
			http.Error(w, "bad relay body", http.StatusBadRequest)
			return
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(r.Context(), method, rr.URL, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	for k, v := range rr.Headers {
		switch strings.ToLower(k) {
		case "host", "cookie", "accept-encoding", "content-length",
			"connection", "keep-alive", "upgrade", "te", "trailer", "transfer-encoding":
			continue
		}
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Host = u.Host

	// Cookie 统一由服务端的 jar 维持：SW 拿到的 Set-Cookie 无法回写浏览器，
	// 交给浏览器只会污染整个网关域，多应用之间会互相顶掉登录态。
	if ck := p.jar.Get(u.Host); ck != "" {
		req.Header.Set("Cookie", ck)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("relay upstream unreachable: " + err.Error()))
		return
	}
	defer resp.Body.Close()

	p.jar.Set(u.Host, resp.Header.Values("Set-Cookie"))

	copyHeader(w.Header(), resp.Header)
	for _, h := range hopHeaders {
		w.Header().Del(h)
	}
	w.Header().Del("Set-Cookie")
	w.Header().Del("X-Frame-Options")
	w.Header().Del("Frame-Options")
	w.Header().Del("Content-Security-Policy")
	w.Header().Del("Content-Security-Policy-Report-Only")
	w.Header().Del("Strict-Transport-Security")
	w.Header().Del("Content-Length")
	w.Header().Del("Content-Encoding")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 96<<20))
	if err != nil {
		http.Error(w, "relay read failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	raw = gunzipBytes(raw, resp.Header.Get("Content-Encoding"))

	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(raw)
}
