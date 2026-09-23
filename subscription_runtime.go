package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const subscriptionCore = "/usr/local/lib/fanout-integrated/sing-box"

func coreConfig(raw string, port int, user, pass string) ([]byte, error) {
	b, e := decodeSubscription(raw[len("singbox://"):])
	if e != nil {
		return nil, e
	}
	var o map[string]any
	if json.Unmarshal(b, &o) != nil {
		return nil, fmt.Errorf("节点配置无效")
	}
	o, e = sanitizeOutbound(o)
	if e != nil {
		return nil, e
	}
	return json.Marshal(map[string]any{
		"log":       map[string]any{"disabled": true},
		"inbounds":  []any{map[string]any{"type": "socks", "tag": "local", "listen": "127.0.0.1", "listen_port": port, "users": []any{map[string]any{"username": user, "password": pass}}}},
		"outbounds": []any{o},
		"dns":       map[string]any{"servers": []any{map[string]any{"type": "local", "tag": "system"}}},
		"route":     map[string]any{"final": "proxy", "default_domain_resolver": "system"},
	})
}

// Every live exit owns one isolated core process. No DIRECT fallback is present.
// The core only exposes an authenticated loopback listener, never a public port.
func (t *Tunnel) startSubscription(workDir string) error {
	if _, e := os.Stat(subscriptionCore); e != nil {
		return fmt.Errorf("缺少订阅内核，请在 VPS 执行 sudo fanoutctl core-install")
	}
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return e
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	user, e := randomToken(16)
	if e != nil {
		return e
	}
	pass, e := randomToken(24)
	if e != nil {
		return e
	}
	b, e := coreConfig(t.snapshot().Node.Upstream, port, user, pass)
	if e != nil {
		return e
	}
	dir, e := os.MkdirTemp(workDir, "core-")
	if e != nil {
		return e
	}
	t.coreDir = dir
	path := filepath.Join(dir, "config.json")
	if e = os.WriteFile(path, b, 0600); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if exec.CommandContext(ctx, subscriptionCore, "check", "-c", path).Run() != nil {
		return fmt.Errorf("sing-box 配置检查未通过（协议参数不兼容）；请更换候选节点")
	}
	cmd := exec.Command(subscriptionCore, "run", "-c", path)
	if e = cmd.Start(); e != nil {
		return fmt.Errorf("启动订阅内核失败")
	}
	t.ovpn = cmd
	t.processDone = make(chan error, 1)
	done := t.processDone
	go func() { done <- cmd.Wait() }()
	addr := coreAddress("127.0.0.1", port)
	for i := 0; i < 50; i++ {
		select {
		case <-done:
			return fmt.Errorf("订阅内核提前退出")
		default:
		}
		c, e := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if e == nil {
			c.Close()
			t.mu.Lock()
			t.runtimeUpstream = "socks5://" + url.UserPassword(user, pass).String() + "@" + addr
			t.mu.Unlock()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("等待订阅内核超时")
}

func subscriptionCoreReady() bool { _, err := os.Stat(subscriptionCore); return err == nil }
