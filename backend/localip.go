package main

import (
	"net"
	"net/url"
)

// 本机的所有 IP。用于把"指向 NAS 自己的局域网地址"统一换成 127.0.0.1：
// 绿联桌面/飞牛 交给第三方应用的地址可能是 172.30.2.231:16601，而页面里
// 应用自己拼出来的是 127.0.0.1:16601。同一个上游被拆成两个 key 时，
// 网关按 host 存的会话 cookie 就会对不上，表现就是"登录成功后又退回登录页"。
var localIPs = map[string]bool{"127.0.0.1": true}

func initLocalIPs() {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			localIPs[ipnet.IP.String()] = true
		}
	}
}

// canonOrigin 把 origin 里指向本机的 IP 收敛成 127.0.0.1，端口保持不变。
// 别的机器（飞牛 172.30.2.126、IPC 172.30.3.171 之类）原样不动。
func canonOrigin(o string) string {
	u, err := url.Parse(o)
	if err != nil || u.Host == "" {
		return o
	}
	h := u.Hostname()
	if h == "127.0.0.1" || !localIPs[h] {
		return o
	}
	if p := u.Port(); p != "" {
		u.Host = "127.0.0.1:" + p
	} else {
		u.Host = "127.0.0.1"
	}
	return u.String()
}
