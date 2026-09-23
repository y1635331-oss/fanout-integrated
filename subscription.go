package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

func decodeSubscription(s string) ([]byte, error) {
	s = strings.Join(strings.Fields(s), "")
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, e := enc.DecodeString(s); e == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("Base64 内容无效")
}
func str(m map[string]any, k string) string {
	if v, ok := m[k]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}
func pick(m map[string]any, keys string) map[string]any {
	o := map[string]any{}
	for _, k := range strings.Fields(keys) {
		if v, ok := m[k]; ok {
			o[k] = v
		}
	}
	return o
}

// Reconstruct only outbound connection fields. Never import routes, listeners,
// file paths, scripts, plugins, detours or an external control API from a feed.
func sanitizeOutbound(m map[string]any) (map[string]any, error) {
	typ := str(m, "type")
	switch typ {
	case "vless", "vmess", "trojan", "shadowsocks", "hysteria2", "tuic", "anytls", "socks", "http":
	default:
		return nil, fmt.Errorf("不支持的协议 %s", typ)
	}
	host := str(m, "server")
	p, e := strconv.Atoi(str(m, "server_port"))
	if e != nil || p < 1 || p > 65535 || host == "" || strings.ContainsAny(host, " /\\\r\n\t") {
		return nil, fmt.Errorf("服务器或端口无效")
	}
	if m["plugin"] != nil || m["detour"] != nil {
		return nil, fmt.Errorf("不支持插件或链式出站")
	}
	o := pick(m, "type server server_port uuid password username method security alter_id flow version congestion_control udp_relay_mode zero_rtt_handshake")
	o["server_port"] = p
	o["tag"] = "proxy"
	if tls, ok := m["tls"].(map[string]any); ok {
		if tls["insecure"] == true {
			return nil, fmt.Errorf("节点要求跳过证书验证，已跳过")
		}
		t := pick(tls, "enabled server_name alpn min_version max_version")
		for _, k := range []string{"utls", "reality"} {
			if v, ok := tls[k].(map[string]any); ok {
				keys := "enabled fingerprint"
				if k == "reality" {
					keys = "enabled public_key short_id"
				}
				t[k] = pick(v, keys)
			}
		}
		o["tls"] = t
	}
	if tr, ok := m["transport"].(map[string]any); ok {
		switch str(tr, "type") {
		case "ws", "http", "httpupgrade", "grpc":
		default:
			return nil, fmt.Errorf("不支持的传输方式")
		}
		o["transport"] = pick(tr, "type path headers host service_name max_early_data early_data_header_name")
	}
	if ob, ok := m["obfs"].(map[string]any); ok {
		o["obfs"] = pick(ob, "type password")
	}
	return o, nil
}

func uriOutbound(line string) (map[string]any, error) {
	if strings.HasPrefix(line, "vmess://") {
		b, e := decodeSubscription(strings.TrimPrefix(line, "vmess://"))
		if e != nil {
			return nil, e
		}
		var v map[string]any
		if json.Unmarshal(b, &v) != nil {
			return nil, fmt.Errorf("VMess JSON 无效")
		}
		o := map[string]any{"type": "vmess", "server": v["add"], "server_port": v["port"], "uuid": v["id"], "security": "auto"}
		if a, e := strconv.Atoi(str(v, "aid")); e == nil {
			o["alter_id"] = a
		}
		if str(v, "tls") == "tls" {
			o["tls"] = map[string]any{"enabled": true, "server_name": str(v, "sni")}
		}
		if n := str(v, "net"); n != "" && n != "tcp" {
			tr := map[string]any{"type": n, "path": str(v, "path")}
			if n == "ws" && str(v, "host") != "" {
				tr["headers"] = map[string]any{"Host": v["host"]}
			}
			if n == "grpc" {
				tr["service_name"] = v["path"]
			}
			o["transport"] = tr
		}
		return sanitizeOutbound(o)
	}
	if strings.HasPrefix(line, "ss://") && !strings.Contains(strings.SplitN(line, "#", 2)[0], "@") {
		tail := strings.TrimPrefix(strings.SplitN(line, "#", 2)[0], "ss://")
		b, e := decodeSubscription(tail)
		if e != nil {
			return nil, e
		}
		line = "ss://" + string(b)
	}
	u, e := url.Parse(line)
	if e != nil || u.User == nil {
		return nil, fmt.Errorf("节点链接无效")
	}
	typ := u.Scheme
	if typ == "ss" {
		typ = "shadowsocks"
	}
	if typ == "hy2" {
		typ = "hysteria2"
	}
	q := u.Query()
	o := map[string]any{"type": typ, "server": u.Hostname(), "server_port": u.Port()}
	user := u.User.Username()
	pass, hasPass := u.User.Password()
	switch typ {
	case "vless":
		o["uuid"] = user
		if q.Get("flow") != "" {
			o["flow"] = q.Get("flow")
		}
	case "trojan", "anytls", "hysteria2":
		o["password"] = user
		if hasPass {
			o["password"] = user + ":" + pass
		}
	case "tuic":
		o["uuid"] = user
		o["password"] = pass
	case "shadowsocks":
		if !hasPass {
			b, e := decodeSubscription(user)
			if e != nil {
				return nil, e
			}
			a := strings.SplitN(string(b), ":", 2)
			if len(a) != 2 {
				return nil, fmt.Errorf("SS 凭据无效")
			}
			user, pass = a[0], a[1]
		}
		o["method"] = user
		o["password"] = pass
		if q.Get("plugin") != "" {
			return nil, fmt.Errorf("暂不支持 SS 插件")
		}
	default:
		return nil, fmt.Errorf("不支持的节点链接协议")
	}
	sec := q.Get("security")
	if sec == "tls" || sec == "reality" || typ == "trojan" || typ == "hysteria2" || typ == "tuic" || typ == "anytls" {
		sni := q.Get("sni")
		if sni == "" {
			sni = q.Get("peer")
		}
		t := map[string]any{"enabled": true, "server_name": sni}
		if q.Get("insecure") == "1" || q.Get("allowInsecure") == "1" {
			return nil, fmt.Errorf("节点要求跳过证书验证")
		}
		if a := q.Get("alpn"); a != "" {
			t["alpn"] = strings.Split(a, ",")
		}
		if fp := q.Get("fp"); fp != "" {
			t["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
		}
		if sec == "reality" {
			t["reality"] = map[string]any{"enabled": true, "public_key": q.Get("pbk"), "short_id": q.Get("sid")}
		}
		o["tls"] = t
	}
	if n := q.Get("type"); n != "" && n != "tcp" {
		tr := map[string]any{"type": n, "path": q.Get("path")}
		if n == "ws" && q.Get("host") != "" {
			tr["headers"] = map[string]any{"Host": q.Get("host")}
		}
		if n == "grpc" {
			tr["service_name"] = q.Get("serviceName")
		}
		o["transport"] = tr
	}
	if q.Get("obfs") != "" {
		o["obfs"] = map[string]any{"type": q.Get("obfs"), "password": q.Get("obfs-password")}
	}
	return sanitizeOutbound(o)
}

func clashOutbound(v map[string]any) (map[string]any, error) {
	typ := str(v, "type")
	if typ == "ss" {
		typ = "shadowsocks"
	}
	if typ == "hy2" {
		typ = "hysteria2"
	}
	if typ == "socks5" {
		typ = "socks"
	}
	o := pick(v, "server uuid password username flow security congestion-control")
	delete(o, "congestion-control")
	o["type"] = typ
	o["server_port"] = v["port"]
	if v["cipher"] != nil {
		if typ == "shadowsocks" {
			o["method"] = v["cipher"]
		} else if typ == "vmess" {
			o["security"] = v["cipher"]
		}
	}
	if v["alterId"] != nil {
		o["alter_id"] = v["alterId"]
	}
	if v["plugin"] != nil {
		return nil, fmt.Errorf("不支持 Clash 插件")
	}
	if v["tls"] == true || typ == "trojan" || typ == "hysteria2" || typ == "tuic" || typ == "anytls" {
		sni := str(v, "servername")
		if sni == "" {
			sni = str(v, "sni")
		}
		t := map[string]any{"enabled": true, "server_name": sni}
		if v["skip-cert-verify"] == true {
			return nil, fmt.Errorf("节点要求跳过证书验证")
		}
		if v["alpn"] != nil {
			t["alpn"] = v["alpn"]
		}
		if fp := str(v, "client-fingerprint"); fp != "" {
			t["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
		}
		if r, ok := v["reality-opts"].(map[string]any); ok {
			t["reality"] = map[string]any{"enabled": true, "public_key": r["public-key"], "short_id": r["short-id"]}
		}
		o["tls"] = t
	}
	if n := str(v, "network"); n != "" && n != "tcp" {
		tr := map[string]any{"type": n}
		if w, ok := v[n+"-opts"].(map[string]any); ok {
			for _, k := range []string{"path", "headers", "host"} {
				if w[k] != nil {
					tr[k] = w[k]
				}
			}
			if n == "grpc" {
				tr["service_name"] = w["grpc-service-name"]
			}
		}
		o["transport"] = tr
	}
	if v["obfs"] != nil {
		o["obfs"] = map[string]any{"type": v["obfs"], "password": v["obfs-password"]}
	}
	return sanitizeOutbound(o)
}

func subscriptionNodes(s Source) ([]Node, int, error) {
	body := strings.TrimSpace(strings.TrimPrefix(s.Content, "\ufeff"))
	if strings.HasPrefix(body, "<") {
		return nil, 0, fmt.Errorf("收到网页而非订阅，请填写 raw 原始文件直链")
	}
	if !strings.Contains(body, "://") && !strings.HasPrefix(body, "{") && !strings.HasPrefix(body, "[") && !strings.Contains(body, "proxies:") {
		if b, e := decodeSubscription(body); e == nil {
			body = strings.TrimSpace(string(b))
		}
	}
	var candidates []map[string]any
	var lines []string
	clash := false
	if strings.HasPrefix(body, "{") || strings.HasPrefix(body, "[") {
		if strings.HasPrefix(body, "[") {
			if json.Unmarshal([]byte(body), &candidates) != nil {
				return nil, 0, fmt.Errorf("节点 JSON 无效")
			}
		} else {
			var doc struct {
				Outbounds []map[string]any `json:"outbounds"`
			}
			if json.Unmarshal([]byte(body), &doc) != nil {
				return nil, 0, fmt.Errorf("sing-box JSON 无效")
			}
			candidates = doc.Outbounds
		}
	} else if strings.Contains(body, "proxies:") {
		var doc struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if yaml.Unmarshal([]byte(body), &doc) != nil {
			return nil, 0, fmt.Errorf("Clash YAML 无效")
		}
		candidates = doc.Proxies
		clash = true
	} else {
		lines = strings.Split(body, "\n")
	}
	out := []Node{}
	seen := map[string]bool{}
	skipped := 0
	add := func(o map[string]any, e error) {
		if e != nil {
			skipped++
			return
		}
		b, _ := json.Marshal(o)
		h := sha256.Sum256(b)
		id := "sub-" + hex.EncodeToString(h[:12])
		if seen[id] {
			return
		}
		seen[id] = true
		country := strings.ToUpper(s.Country)
		if country == "" {
			country = "UN"
		}
		out = append(out, Node{HostName: id, Source: s.ID, Kind: str(o, "type"), Country: country, CountryCode: country, Upstream: "singbox://" + base64.RawURLEncoding.EncodeToString(b)})
	}
	for _, v := range candidates {
		if len(out) >= 2000 {
			break
		}
		typ := str(v, "type")
		if typ == "selector" || typ == "urltest" || typ == "direct" || typ == "block" || typ == "dns" {
			continue
		}
		if clash {
			add(clashOutbound(v))
		} else {
			add(sanitizeOutbound(v))
		}
	}
	for _, line := range lines {
		if len(out) >= 2000 {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "socks5://") || strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			u, e := validateUpstream(line)
			if e != nil {
				skipped++
				continue
			}
			h := sha256.Sum256([]byte(u))
			id := "proxy-" + hex.EncodeToString(h[:8])
			if !seen[id] {
				seen[id] = true
				out = append(out, Node{HostName: id, Source: s.ID, Kind: "proxy", Country: "UN", CountryCode: "UN", Upstream: u})
			}
			continue
		}
		add(uriOutbound(line))
	}
	if len(out) == 0 {
		return nil, skipped, fmt.Errorf("没有可导入节点（跳过 %d 条）；支持 sing-box JSON、Clash proxies、Base64 或协议链接；不支持 HTML 网页/任意 CSV", skipped)
	}
	return out, skipped, nil
}

func coreEndpoint(raw string) bool             { return strings.HasPrefix(raw, "singbox://") }
func coreAddress(host string, port int) string { return net.JoinHostPort(host, strconv.Itoa(port)) }
