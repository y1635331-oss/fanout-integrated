package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

func validateExportHost(raw string) (string, error) {
	host := strings.TrimSpace(raw)
	if host == "" {
		return "", nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	if len(host) > 253 || strings.ContainsAny(host, "/:@?#\\ \r\n\t") {
		return "", fmt.Errorf("只填写域名或 IP，不含 https://、端口或路径")
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !strings.Contains(host, ".") {
		return "", fmt.Errorf("请填写完整域名")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("域名格式无效")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", fmt.Errorf("域名格式无效，请使用 ASCII/Punycode 域名")
			}
		}
	}
	return host, nil
}

func (m *Manager) exportHost() string {
	if h, e := validateExportHost(m.pool.Config().ExportHost); e == nil && h != "" {
		return h
	}
	if h, e := validateExportHost(os.Getenv("FANOUT_TLS_DOMAIN")); e == nil && h != "" {
		return h
	}
	var cfg struct {
		Domain string `json:"domain"`
	}
	if b, e := os.ReadFile(filepath.Join(m.workDir, "tls.json")); e == nil && json.Unmarshal(b, &cfg) == nil {
		if h, e := validateExportHost(cfg.Domain); e == nil && h != "" {
			return h
		}
	}
	return hostPublicIP()
}
