package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 极简本地账号体系（家庭/内网单管理员场景）：
//   - 首次启动（还没有 auth.json）时，所有页面导到 /setup 设置用户名和密码；
//   - 之后所有请求要求 cookie nwg_sess 有效，否则导到 /login；
//   - 密码 PBKDF2-HMAC-SHA256 加盐存储，会话写入 auth.json，重启不掉登录。

const (
	authFile    = "auth.json"
	sessCookie  = "nwg_sess"
	sessTTL     = 30 * 24 * time.Hour
	pbkdf2Iter  = 12000
	pbkdf2KeyLn = 32
)

type authData struct {
	User     string           `json:"user"`
	Salt     string           `json:"salt"`
	Hash     string           `json:"hash"`
	Iter     int              `json:"iter"`
	Sessions map[string]int64 `json:"sessions"`
}

type authStore struct {
	mu      sync.Mutex
	path    string
	enabled bool
	d       authData
}

func NewAuth(dataDir string, enabled bool) *authStore {
	a := &authStore{path: filepath.Join(dataDir, authFile), enabled: enabled}
	if b, err := os.ReadFile(a.path); err == nil {
		_ = json.Unmarshal(b, &a.d)
	}
	if a.d.Sessions == nil {
		a.d.Sessions = map[string]int64{}
	}
	a.gcLocked()
	return a
}

func (a *authStore) save() error {
	b, err := json.MarshalIndent(&a.d, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.path)
}

func (a *authStore) gcLocked() {
	now := time.Now().Unix()
	for k, exp := range a.d.Sessions {
		if exp < now {
			delete(a.d.Sessions, k)
		}
	}
}

func (a *authStore) ready() bool { return a.d.User != "" && a.d.Hash != "" }

// ---------- 密码 ----------

// pbkdf2SHA256 是单块 PBKDF2-HMAC-SHA256（输出 <= 32 字节）。
func pbkdf2SHA256(pw, salt []byte, iter, klen int) []byte {
	prf := hmac.New(sha256.New, pw)
	prf.Write(salt)
	prf.Write([]byte{0, 0, 0, 1})
	sum := prf.Sum(nil)
	out := make([]byte, len(sum))
	copy(out, sum)
	if iter < 1 {
		iter = 1
	}
	for i := 2; i <= iter; i++ {
		prf.Reset()
		prf.Write(sum)
		sum = prf.Sum(nil)
		for j := range out {
			out[j] ^= sum[j]
		}
	}
	if klen > len(out) {
		klen = len(out)
	}
	return out[:klen]
}

func hashPassword(pw string, iter int) (saltHex, hashHex string, err error) {
	salt := make([]byte, 16)
	if _, err = rand.Read(salt); err != nil {
		return
	}
	b := pbkdf2SHA256([]byte(pw), salt, iter, pbkdf2KeyLn)
	return hex.EncodeToString(salt), hex.EncodeToString(b), nil
}

func (a *authStore) checkPassword(user, pw string) bool {
	if !a.ready() || subtle.ConstantTimeCompare([]byte(a.d.User), []byte(user)) != 1 {
		return false
	}
	salt, err := hex.DecodeString(a.d.Salt)
	if err != nil {
		return false
	}
	iter := a.d.Iter
	if iter <= 0 {
		iter = pbkdf2Iter
	}
	got := hex.EncodeToString(pbkdf2SHA256([]byte(pw), salt, iter, pbkdf2KeyLn))
	return subtle.ConstantTimeCompare([]byte(got), []byte(a.d.Hash)) == 1
}

// ---------- 会话 ----------

func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (a *authStore) newSession(w http.ResponseWriter, r *http.Request) {
	id := make([]byte, 32)
	_, _ = rand.Read(id)
	sid := hex.EncodeToString(id)
	a.mu.Lock()
	a.gcLocked()
	a.d.Sessions[sid] = time.Now().Add(sessTTL).Unix()
	_ = a.save()
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     sessCookie,
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(r),
		MaxAge:   int(sessTTL.Seconds()),
	})
}

func (a *authStore) dropSession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessCookie); err == nil {
		a.mu.Lock()
		delete(a.d.Sessions, c.Value)
		_ = a.save()
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
}

func (a *authStore) authed(r *http.Request) bool {
	c, err := r.Cookie(sessCookie)
	if err != nil {
		return false
	}
	now := time.Now().Unix()
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.d.Sessions[c.Value]
	return ok && exp >= now
}

// ---------- 中间件 ----------

func (a *authStore) allowPath(p string) bool {
	switch p {
	case "/login", "/setup", "/logout", "/api/health", "/api/info", "/sw.js", "/boot.html", "/favicon.ico":
		return true
	}
	// 静态资源不拦：未登录时也要能取到样式/脚本，Service Worker 的更新请求
	// 更不能被 302 到登录页（否则 SW 取到 HTML，接管直接失效）。
	for _, pre := range []string{"/css/", "/js/", "/assets/", "/img/"} {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

func (a *authStore) Wrap(next http.Handler) http.Handler {
	if !a.enabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		// base 部署（飞牛挂 /app/naswebgateway）时，Wrap 看到的是完整路径，
		// 判断自家路由前先剥掉前缀。
		lp := p
		if uiBase != "" && strings.HasPrefix(lp, uiBase) {
			lp = strings.TrimPrefix(lp, uiBase)
			if lp == "" {
				lp = "/"
			}
		}

		// 已经登录的人不应该再看到登录页：撞到 /login、/setup 一律送回首页。
		if a.authed(r) && (lp == "/login" || lp == "/setup") {
			http.Redirect(w, r, uiBase+"/", http.StatusFound)
			return
		}

		// 飞牛、绿联这类面板自己的登录路由也叫 /login，它们会把带我们前缀的地址
		// 塞进 redirect_uri 再跳一次，正好撞上我们的 /login，于是「打开面板却看到
		// 内网浏览器的登录页」。这里把请求送回那条代理路径，交还给上游自己处理。
		if r.Method == http.MethodGet && (lp == "/login" || lp == "/") {
			if ru := r.URL.Query().Get("redirect_uri"); strings.Contains(ru, "/api/p/") || strings.Contains(ru, "/__p/") {
				http.Redirect(w, r, uiBase+ru, http.StatusFound)
				return
			}
		}

		if a.allowPath(lp) || a.authed(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/__p/") {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"ok":false,"error":"auth required"}`)
			return
		}
		a.mu.Lock()
		ready := a.ready()
		a.mu.Unlock()
		if !ready {
			http.Redirect(w, r, uiBase+"/setup", http.StatusFound)
			return
		}
		http.Redirect(w, r, uiBase+"/login?next="+queryEscape(r.URL.RequestURI()), http.StatusFound)
	})
}

func queryEscape(s string) string {
	r := strings.NewReplacer("%", "%25", "&", "%26", " ", "%20", "#", "%23")
	return r.Replace(s)
}

// ---------- 页面 ----------

const authPageTmpl = `<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title>
<style>
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;
background:#0f1115;color:#e6e8eb;font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Helvetica Neue",Arial,sans-serif}
.card{width:min(92vw,380px);background:#171a21;border:1px solid #242833;border-radius:14px;padding:26px 24px}
h1{font-size:19px;margin:0 0 6px;font-weight:600}
p.sub{margin:0 0 20px;color:#8b93a1;font-size:13px}
label{display:block;font-size:13px;color:#a9b1bf;margin:14px 0 6px}
input{width:100%%;padding:11px 12px;border-radius:9px;border:1px solid #2b3040;background:#0f1219;color:#e6e8eb;font-size:15px}
input:focus{outline:none;border-color:#3d7eff}
button{width:100%%;margin-top:20px;padding:12px;border:0;border-radius:9px;background:#3d7eff;color:#fff;font-size:15px;font-weight:500;cursor:pointer}
button:active{opacity:.85}
.err{margin-top:14px;padding:10px 12px;border-radius:9px;background:#3a1b1b;color:#ff9b9b;font-size:13px}
.hint{margin-top:16px;font-size:12px;color:#6b7280;text-align:center}
</style></head><body><div class="card">
<h1>%s</h1><p class="sub">%s</p>
%s
%s
</div></body></html>`

func writeAuthPage(w http.ResponseWriter, title, heading, sub, form, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	e := ""
	if errMsg != "" {
		e = `<div class="err">` + html.EscapeString(errMsg) + `</div>`
	}
	fmt.Fprintf(w, authPageTmpl, title, heading, sub, form, e)
}

func (a *authStore) SetupHandler(w http.ResponseWriter, r *http.Request) {
	if !a.enabled {
		http.Redirect(w, r, uiBase+"/", http.StatusFound)
		return
	}
	a.mu.Lock()
	ready := a.ready()
	a.mu.Unlock()
	if ready {
		http.Redirect(w, r, uiBase+"/login", http.StatusFound)
		return
	}
	if r.Method == http.MethodPost {
		user := strings.TrimSpace(r.FormValue("user"))
		pw := r.FormValue("password")
		pw2 := r.FormValue("password2")
		switch {
		case len(user) < 1 || len(user) > 32:
			writeAuthPage(w, "初始化", "设置管理员账号", "首次使用，先设置登录账号", setupForm(user, "用户名长度需 1–32 位"), "")
			return
		case len(pw) < 6:
			writeAuthPage(w, "初始化", "设置管理员账号", "首次使用，先设置登录账号", setupForm(user, "密码至少 6 位"), "")
			return
		case pw != pw2:
			writeAuthPage(w, "初始化", "设置管理员账号", "首次使用，先设置登录账号", setupForm(user, "两次输入的密码不一致"), "")
			return
		}
		salt, hash, err := hashPassword(pw, pbkdf2Iter)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		a.mu.Lock()
		a.d.User = user
		a.d.Salt = salt
		a.d.Hash = hash
		a.d.Iter = pbkdf2Iter
		if a.d.Sessions == nil {
			a.d.Sessions = map[string]int64{}
		}
		_ = a.save()
		a.mu.Unlock()
		log.Printf("[nwg] auth: admin account %q created", user)
		a.newSession(w, r)
		http.Redirect(w, r, uiBase+"/", http.StatusFound)
		return
	}
	writeAuthPage(w, "初始化", "设置管理员账号", "首次使用，先设置登录账号", setupForm("", ""), "")
}

func setupForm(user, errMsg string) string {
	return `<form method="post" action="` + uiBase + `/setup">
<label>用户名</label><input name="user" autocomplete="username" value="` + html.EscapeString(user) + `" autofocus>
<label>密码</label><input name="password" type="password" autocomplete="new-password">
<label>确认密码</label><input name="password2" type="password" autocomplete="new-password">
<button type="submit">创建并进入</button></form>
<div class="hint">密码以 PBKDF2-SHA256 加盐存储，只保存在本机</div>`
}

func (a *authStore) LoginHandler(w http.ResponseWriter, r *http.Request) {
	if !a.enabled {
		http.Redirect(w, r, uiBase+"/", http.StatusFound)
		return
	}
	a.mu.Lock()
	ready := a.ready()
	a.mu.Unlock()
	if !ready {
		http.Redirect(w, r, uiBase+"/setup", http.StatusFound)
		return
	}
	next := r.URL.Query().Get("next")
	if next == "" || !strings.HasPrefix(next, "/") {
		next = uiBase + "/"
	}
	if r.Method == http.MethodPost {
		user := strings.TrimSpace(r.FormValue("user"))
		pw := r.FormValue("password")
		if a.checkPassword(user, pw) {
			a.newSession(w, r)
			http.Redirect(w, r, next, http.StatusFound)
			return
		}
		time.Sleep(300 * time.Millisecond)
		writeAuthPage(w, "登录", "内网浏览器", "请登录后使用", loginForm(user, next), "用户名或密码不正确")
		return
	}
	writeAuthPage(w, "登录", "内网浏览器", "请登录后使用", loginForm("", next), "")
}

func loginForm(user, next string) string {
	return `<form method="post" action="` + uiBase + `/login">
<input type="hidden" name="next" value="` + html.EscapeString(next) + `">
<label>用户名</label><input name="user" autocomplete="username" value="` + html.EscapeString(user) + `" autofocus>
<label>密码</label><input name="password" type="password" autocomplete="current-password">
<button type="submit">登录</button></form>`
}

func (a *authStore) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	a.dropSession(w, r)
	http.Redirect(w, r, uiBase+"/login", http.StatusFound)
}
