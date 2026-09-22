package main

import (
	"fmt"
	"strings"
)

// Only connection directives and inline keys/certificates are accepted. No
// executable hooks, plugins, config includes, file paths, or management sockets.
func safeOpenVPNConfig(raw string) (string, error) {
	if len(raw) > 1<<20 {
		return "", fmt.Errorf("OpenVPN 配置超过 1 MB")
	}
	allowed := map[string]bool{}
	for _, k := range strings.Fields("client dev dev-type proto remote resolv-retry nobind persist-key persist-tun remote-cert-tls verify-x509-name cipher auth auth-nocache auth-retry reneg-sec keepalive ping ping-restart connect-retry connect-timeout connect-retry-max verb mute explicit-exit-notify tls-client tls-version-min tls-cipher tls-ciphersuites key-direction data-ciphers data-ciphers-fallback sndbuf rcvbuf tun-mtu mssfix fragment comp-lzo compress topology remote-random float pull route-metric route-delay fast-io mute-replay-warnings setenv opt tls-timeout") {
		allowed[k] = true
	}
	var out []string
	block := ""
	remote := false
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n") {
		s := strings.TrimSpace(line)
		if block != "" {
			out = append(out, line)
			if s == "</"+block+">" {
				block = ""
			}
			continue
		}
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, ";") {
			continue
		}
		if strings.HasPrefix(s, "<") {
			tag := strings.Trim(s, "<>")
			switch tag {
			case "ca", "cert", "key", "tls-auth", "tls-crypt":
				block = tag
				out = append(out, s)
				continue
			default:
				return "", fmt.Errorf("不支持的 OpenVPN 内联块 %s", tag)
			}
		}
		f := strings.Fields(s)
		k := strings.TrimPrefix(f[0], "--")
		if k == "auth-user-pass" || k == "redirect-gateway" {
			continue
		}
		if !allowed[k] {
			return "", fmt.Errorf("禁止或不支持的 OpenVPN 选项: %s", k)
		}
		if k == "dev" || k == "dev-type" {
			continue
		}
		if k == "remote" {
			if len(f) < 2 {
				return "", fmt.Errorf("remote 缺少地址")
			}
			remote = true
		}
		out = append(out, s)
	}
	if block != "" || !remote {
		return "", fmt.Errorf("OpenVPN 配置不完整")
	}
	return strings.Join(out, "\n") + "\ndev tun0\nredirect-gateway def1\npull-filter ignore route-ipv6\npull-filter ignore ifconfig-ipv6\n", nil
}
