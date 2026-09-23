package main

import (
	"crypto/subtle"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Enforce same-origin writes for both the new dashboard and inherited panel APIs.
func secureRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
		mutating := map[string]bool{"/api/start": true, "/api/stop": true, "/api/swap": true, "/api/cred": true, "/api/refresh": true, "/api/provision": true, "/api/jobs/dismiss": true, "/api/xui/bind": true, "/api/xui/clone": true, "/api/xui/delete": true, "/api/panel/inbound/new": true, "/api/panel/inbound/update": true, "/api/panel/client/add": true, "/api/panel/client/del": true, "/api/panel/client/reset": true, "/api/update/apply": true, "/api/source/delete": true, "/api/pool/refresh": true, "/api/export/token": true, "/api/quality/refresh": true}
		if mutating[r.URL.Path] && r.Method != "POST" {
			http.Error(w, "请使用 POST", 405)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			origin := r.Header.Get("Origin")
			if origin != "" {
				u, e := url.Parse(origin)
				if e != nil || !strings.EqualFold(u.Host, r.Host) {
					http.Error(w, "跨站请求被拒绝", 403)
					return
				}
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				http.Error(w, "跨站请求被拒绝", 403)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func (p *PoolStore) exportAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/proxies" && r.Method == "GET" {
			supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") && supplied != "" && subtle.ConstantTimeCompare([]byte(supplied), []byte(p.Token())) == 1 {
				apiProxiesHandler(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

var apiProxiesHandler http.HandlerFunc

type ExportProxy struct {
	Slot     int       `json:"slot"`
	Host     string    `json:"host"`
	Port     int       `json:"port"`
	Username string    `json:"username"`
	Password string    `json:"password"`
	Country  string    `json:"country"`
	ExitIP   string    `json:"exit_ip"`
	URL      string    `json:"url"`
	UDP      bool      `json:"udp"`
	Quality  IPQuality `json:"quality"`
}

func exportRows(tunnels []*Tunnel, host, country string, limit int, residential bool) []ExportProxy {
	rows := []ExportProxy{}
	seen := map[string]bool{}
	for _, t := range tunnels {
		countryCode := exitCountry(t)
		if t.Status != "up" || net.ParseIP(t.ExitIP) == nil || seen[t.ExitIP] || country != "" && !strings.EqualFold(country, countryCode) || residential && t.Quality.Type != "residential" {
			continue
		}
		seen[t.ExitIP] = true
		u := url.URL{Scheme: "socks5", User: url.UserPassword(t.Cred.User, t.Cred.Pass), Host: net.JoinHostPort(host, strconv.Itoa(t.Port))}
		rows = append(rows, ExportProxy{t.Slot, host, t.Port, t.Cred.User, t.Cred.Pass, countryCode, t.ExitIP, u.String(), t.Node.Upstream == "", t.Quality})
		if limit > 0 && len(rows) >= limit {
			break
		}
	}
	return rows
}
func apiProxies(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "GET required", 405)
			return
		}
		host := hostPublicIP()
		if net.ParseIP(host) == nil {
			http.Error(w, "VPS 公网 IP 未确定，请使用 -ip 启动参数指定", 503)
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("count"))
		rows := exportRows(m.Tunnels(), host, r.URL.Query().Get("country"), limit, m.pool.Config().ResidentialOnly)
		switch r.URL.Query().Get("format") {
		case "json":
			writeJSON(w, 200, map[string]any{"count": len(rows), "proxies": rows})
		case "csv":
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			writer := csv.NewWriter(w)
			writer.Write([]string{"host", "port", "username", "password", "country", "exit_ip", "udp", "type"})
			for _, p := range rows {
				writer.Write([]string{p.Host, strconv.Itoa(p.Port), p.Username, p.Password, p.Country, p.ExitIP, strconv.FormatBool(p.UDP), p.Quality.Type})
			}
			writer.Flush()
		default:
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			for _, p := range rows {
				fmt.Fprintln(w, p.URL)
			}
		}
	}
}
func registerIntegrated(mux *http.ServeMux, m *Manager) {
	mux.HandleFunc("/pool", handlePoolPage)
	mux.HandleFunc("/api/quality/refresh", apiRefreshQuality(m))
	apiProxiesHandler = apiProxies(m)
	mux.HandleFunc("/api/proxies", apiProxiesHandler)
	mux.HandleFunc("/api/pool", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var c PoolConfig
			if e := json.NewDecoder(r.Body).Decode(&c); e != nil {
				http.Error(w, "JSON 无效", 400)
				return
			}
			if e := m.pool.SetConfig(c, m.maxSlots); e != nil {
				http.Error(w, e.Error(), 400)
				return
			}
		}
		nodes, at := m.Nodes()
		writeJSON(w, 200, map[string]any{"config": m.pool.Config(), "sources": m.pool.Views(), "node_count": len(nodes), "fetched": at, "max": m.maxSlots, "public_ip": hostPublicIP(), "tunnels": m.Tunnels(), "jobs": m.jobs.Views(), "version": version})
	})
	mux.HandleFunc("/api/sources", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			writeJSON(w, 200, m.pool.Views())
			return
		}
		var s Source
		if e := json.NewDecoder(r.Body).Decode(&s); e != nil {
			http.Error(w, "JSON 无效", 400)
			return
		}
		if e := m.pool.Add(s); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		go m.RefreshNodes()
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/source/delete", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if e := m.RemoveSource(id); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/pool/refresh", func(w http.ResponseWriter, r *http.Request) {
		job := m.jobs.New("刷新所有来源", []string{"来源订阅与 VPN Gate"})
		go func() {
			defer job.Finish()
			m.pool.Refresh()
			n, e := m.RefreshNodes()
			if e != nil {
				job.Set(0, "failed", e.Error())
			} else {
				job.Set(0, "ok", fmt.Sprintf("%d 个候选节点", n))
			}
		}()
		writeJSON(w, 200, map[string]string{"job": job.ID()})
	})
	mux.HandleFunc("/api/export/token", func(w http.ResponseWriter, r *http.Request) {
		t, e := m.pool.RotateToken()
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		writeJSON(w, 200, map[string]string{"token": t})
	})
	mux.HandleFunc("/api/log", func(w http.ResponseWriter, r *http.Request) {
		slot, e := strconv.Atoi(r.URL.Query().Get("slot"))
		if e != nil || slot < 1 || slot > m.maxSlots {
			http.Error(w, "槽位无效", 400)
			return
		}
		f, e := os.Open(filepath.Join(m.workDir, fmt.Sprintf("fi%d.log", slot)))
		if e != nil {
			http.Error(w, "此出口暂无 OpenVPN 日志", 404)
			return
		}
		defer f.Close()
		stat, e := f.Stat()
		if e != nil {
			http.Error(w, "读取日志失败", 500)
			return
		}
		start := stat.Size() - 16384
		if start < 0 {
			start = 0
		}
		b := make([]byte, stat.Size()-start)
		_, _ = f.ReadAt(b, start)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(b)
	})
}
