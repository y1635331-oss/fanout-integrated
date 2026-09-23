package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SocksCred 是一条隧道的 SOCKS5 访问凭据。
//
// 每条隧道一套独立凭据：泄露一条不会连累其他出口，
// 换节点时也能只重置这一条而不影响已分发的其他配置。
type SocksCred struct {
	User string `json:"user"`
	Pass string `json:"pass"`
}

// Tunnel 是一条运行中的隧道：一个 netns + 一个 openvpn 进程 + 一个本地 SOCKS5 端口。
type Tunnel struct {
	Slot    int       `json:"slot"`
	Port    int       `json:"port"`
	Node    Node      `json:"node"`
	Status  string    `json:"status"` // starting | up | failed | stopped
	ExitIP  string    `json:"exit_ip"`
	Err     string    `json:"err,omitempty"`
	Since   time.Time `json:"since"`
	Cred    SocksCred `json:"cred"`
	Quality IPQuality `json:"quality"`

	coreDir         string
	runtimeUpstream string
	ns              string
	listener        net.Listener
	ovpn            *exec.Cmd
	mu              sync.Mutex
	opMu            sync.Mutex
	closed          bool
	processDone     chan error
	accepting       chan struct{}
}

func (t *Tunnel) nsName() string { return fmt.Sprintf("fi%d", t.Slot) }
func (t *Tunnel) subnet() string { return fmt.Sprintf("10.98.%d", t.Slot) }

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// runQuiet 执行清理类命令，忽略"本来就不存在"这类错误。
func runQuiet(name string, args ...string) {
	_ = exec.Command(name, args...).Run()
}

// setupNetns 建立 netns 与 veth 链路，并配好 NAT 与转发放行。
func (t *Tunnel) setupNetns() error {
	ns, sub := t.nsName(), t.subnet()
	veth, peer := fmt.Sprintf("fiv%d", t.Slot), fmt.Sprintf("fip%d", t.Slot)

	t.teardownNetns()

	if err := run("ip", "netns", "add", ns); err != nil {
		return err
	}
	if err := run("ip", "netns", "exec", ns, "ip", "link", "set", "lo", "up"); err != nil {
		return err
	}
	if err := run("ip", "link", "add", veth, "type", "veth", "peer", "name", peer); err != nil {
		return err
	}
	if err := run("ip", "link", "set", peer, "netns", ns); err != nil {
		return err
	}
	if err := run("ip", "addr", "add", sub+".1/30", "dev", veth); err != nil {
		return err
	}
	if err := run("ip", "link", "set", veth, "up"); err != nil {
		return err
	}
	if err := run("ip", "netns", "exec", ns, "ip", "addr", "add", sub+".2/30", "dev", peer); err != nil {
		return err
	}
	if err := run("ip", "netns", "exec", ns, "ip", "link", "set", peer, "up"); err != nil {
		return err
	}
	if err := run("ip", "netns", "exec", ns, "ip", "route", "add", "default", "via", sub+".1"); err != nil {
		return err
	}

	// netns 内的 DNS，仅用于 openvpn 解析远端主机名
	nsDir := filepath.Join("/etc/netns", ns)
	if err := os.MkdirAll(nsDir, 0755); err != nil {
		return fmt.Errorf("创建 %s 失败: %w", nsDir, err)
	}
	if err := os.WriteFile(filepath.Join(nsDir, "resolv.conf"), []byte("nameserver 8.8.8.8\n"), 0644); err != nil {
		return fmt.Errorf("写 resolv.conf 失败: %w", err)
	}

	cidr := sub + ".0/30"
	ensureRule("nat", "POSTROUTING", "-s", cidr, "-j", "MASQUERADE")
	ensureRuleInsert("filter", "FORWARD", "-s", cidr, "-j", "ACCEPT")
	ensureRuleInsert("filter", "FORWARD", "-d", cidr, "-j", "ACCEPT")
	return nil
}

// ensureRule 幂等追加一条 iptables 规则。
func ensureRule(table, chain string, spec ...string) {
	check := append([]string{"-w", "5", "-t", table, "-C", chain}, spec...)
	if exec.Command("iptables", check...).Run() == nil {
		return
	}
	add := append([]string{"-w", "5", "-t", table, "-A", chain}, spec...)
	runQuiet("iptables", add...)
}

// ensureRuleInsert 幂等插入规则到链首。
// FORWARD 链末尾常有兜底 REJECT，必须插到最前面才生效。
func ensureRuleInsert(table, chain string, spec ...string) {
	check := append([]string{"-w", "5", "-t", table, "-C", chain}, spec...)
	if exec.Command("iptables", check...).Run() == nil {
		return
	}
	ins := append([]string{"-w", "5", "-t", table, "-I", chain, "1"}, spec...)
	runQuiet("iptables", ins...)
}

func (t *Tunnel) teardownNetns() {
	ns, sub := t.nsName(), t.subnet()
	cidr := sub + ".0/30"
	runQuiet("ip", "netns", "del", ns)
	runQuiet("ip", "link", "del", fmt.Sprintf("fiv%d", t.Slot))
	runQuiet("iptables", "-w", "5", "-t", "nat", "-D", "POSTROUTING", "-s", cidr, "-j", "MASQUERADE")
	runQuiet("iptables", "-w", "5", "-D", "FORWARD", "-s", cidr, "-j", "ACCEPT")
	runQuiet("iptables", "-w", "5", "-D", "FORWARD", "-d", cidr, "-j", "ACCEPT")
}

// startOpenVPN validates untrusted config and waits for a routed VPN address.
func (t *Tunnel) startOpenVPN(dir string) error {
	ns := t.nsName()
	n := t.snapshot().Node
	cfg, err := safeOpenVPNConfig(n.Config)
	if err != nil {
		return err
	}
	cfgPath := filepath.Join(dir, ns+".ovpn")
	if err = os.WriteFile(cfgPath, []byte(cfg), 0600); err != nil {
		return err
	}
	user, pass := n.VPNUser, n.VPNPass
	if user == "" {
		user, pass = "vpn", "vpn"
	}
	authPath := filepath.Join(dir, ns+"-auth.txt")
	if err = os.WriteFile(authPath, []byte(user+"\n"+pass+"\n"), 0600); err != nil {
		return err
	}
	logPath := filepath.Join(dir, ns+".log")
	// Truncate per attempt; OpenVPN rotates with each reconnect instead of growing forever.
	if err = os.WriteFile(logPath, nil, 0600); err != nil {
		return err
	}
	cmd := exec.Command("ip", "netns", "exec", ns, "openvpn", "--config", cfgPath, "--auth-user-pass", authPath, "--auth-nocache", "--dev", "tun0", "--connect-retry-max", "1", "--connect-timeout", "15", "--data-ciphers", "AES-128-CBC:AES-256-GCM:AES-128-GCM:CHACHA20-POLY1305", "--data-ciphers-fallback", "AES-128-CBC", "--script-security", "1", "--verb", "2", "--log", logPath)
	if err = cmd.Start(); err != nil {
		return err
	}
	t.ovpn = cmd
	t.processDone = make(chan error, 1)
	done := t.processDone
	go func() { done <- cmd.Wait() }()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			return fmt.Errorf("OpenVPN 退出: %v；检查日志 %s", err, logPath)
		default:
		}
		if t.snapshot().Status == "stopped" {
			return fmt.Errorf("已停止")
		}
		if out, e := exec.Command("ip", "netns", "exec", ns, "ip", "-4", "addr", "show", "tun0").Output(); e == nil && strings.Contains(string(out), "inet ") {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("等待 VPN 接口超时")
}

// serve 在母机上监听 SOCKS5 端口，出站连接则在 netns 内建立。
// 监听必须留在母机侧：netns 内的 loopback 与母机彼此独立，
// 监听在 netns 里的话外部根本连不上。
func (t *Tunnel) serve() error {
	port := t.snapshot().Port
	ln, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return fmt.Errorf("固定端口 %d 无法监听: %w", port, err)
	}
	t.listener = ln
	t.accepting = make(chan struct{}, 256)
	slots := t.accepting
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			select {
			case slots <- struct{}{}:
			default:
				conn.Close()
				continue
			}
			go func() {
				defer func() { <-slots }()
				v := t.snapshot()
				if v.Status != "up" {
					conn.Close()
					return
				}
				cred := v.Cred
				serveSocks(conn, &cred, t.dial())
			}()
		}
	}()
	return nil
}

// credential 取一份凭据副本，避免读写并发。
func (t *Tunnel) credential() SocksCred {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.Cred
}

// setCredential 换掉这条隧道的 SOCKS5 凭据。已建立的连接不受影响，
// 新连接立即按新凭据校验。
func (t *Tunnel) setCredential(c SocksCred) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Cred = c
}

// probeExitIP 通过隧道查询出口 IP，用于确认这条隧道确实换了 IP。
func (t *Tunnel) probeExitIP() (string, error) {
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) { return t.dial()(network, addr) }}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 15 * time.Second}
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		return "", fmt.Errorf("出口检测失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("出口检测 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(string(body))
	if net.ParseIP(ip).To4() == nil {
		return "", fmt.Errorf("出口不是 IPv4")
	}
	return ip, nil
}
func (t *Tunnel) dial() func(string, string) (net.Conn, error) {
	v := t.snapshot()
	if coreEndpoint(v.Node.Upstream) {
		if v.runtimeUpstream == "" {
			return func(string, string) (net.Conn, error) { return nil, fmt.Errorf("订阅内核未就绪") }
		}
		return upstreamDialer(v.runtimeUpstream)
	}
	if v.Node.Upstream != "" {
		return upstreamDialer(v.Node.Upstream)
	}
	return dialerInNetns(t.nsName())
}
func (t *Tunnel) snapshot() *Tunnel {
	t.mu.Lock()
	defer t.mu.Unlock()
	return &Tunnel{Slot: t.Slot, Port: t.Port, Node: t.Node, Status: t.Status, ExitIP: t.ExitIP, Err: t.Err, Since: t.Since, Cred: t.Cred, Quality: t.Quality, runtimeUpstream: t.runtimeUpstream}
}
func (t *Tunnel) MarshalJSON() ([]byte, error) {
	v := t.snapshot()
	type plain Tunnel
	return json.Marshal((*plain)(v))
}
func (t *Tunnel) state(status, msg string) {
	t.mu.Lock()
	if t.Status != "stopped" {
		t.Status = status
		t.Err = msg
		if status != "up" {
			t.ExitIP = ""
		}
	}
	t.mu.Unlock()
}

// cleanup is serialized by opMu. It never releases a slot until child teardown completes.
func (t *Tunnel) cleanup() {
	t.mu.Lock()
	t.runtimeUpstream = ""
	t.mu.Unlock()
	if t.ovpn != nil && t.ovpn.Process != nil {
		_ = t.ovpn.Process.Kill()
		if t.processDone != nil {
			select {
			case <-t.processDone:
			case <-time.After(2 * time.Second):
			}
		}
		t.ovpn = nil
	}
	if t.coreDir != "" {
		_ = os.RemoveAll(t.coreDir)
		t.coreDir = ""
	}
	if t.snapshot().Node.Upstream == "" {
		t.teardownNetns()
	}
}
func (t *Tunnel) stop() {
	t.mu.Lock()
	t.Status = "stopped"
	t.ExitIP = ""
	t.mu.Unlock()
	t.opMu.Lock()
	defer t.opMu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	if t.listener != nil {
		t.listener.Close()
		t.listener = nil
	}
	t.cleanup()
}
