package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

func normalizedCountry(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) != 2 || s == "UN" || s[0] < 'A' || s[0] > 'Z' || s[1] < 'A' || s[1] > 'Z' {
		return ""
	}
	return s
}

func exitCountry(t *Tunnel) string {
	if c := normalizedCountry(t.Quality.Country); c != "" {
		return c
	}
	return t.Node.CountryCode
}

// Geolocation never establishes residential status. Provider type=IPv4 is not
// a residential classification. Only the configured quality service can do that.
func parseGeo(b []byte, ip string) IPQuality {
	q := IPQuality{Type: "unknown", CheckedAt: time.Now(), GeoProvider: "ipwho.is"}
	var v struct {
		IP          string `json:"ip"`
		Success     bool   `json:"success"`
		CountryCode string `json:"country_code"`
		Connection  struct {
			ISP string `json:"isp"`
		} `json:"connection"`
	}
	if json.Unmarshal(b, &v) != nil || !v.Success || net.ParseIP(v.IP) == nil || !net.ParseIP(v.IP).Equal(net.ParseIP(ip)) {
		q.GeoError = "地区查询响应无效或服务暂不可用"
		return q
	}
	q.Country = normalizedCountry(v.CountryCode)
	q.ISP = v.Connection.ISP
	if q.Country == "" {
		q.GeoError = "查询未返回有效国家码"
	}
	return q
}

func (p *PoolStore) lookupGeo(ip string) IPQuality {
	p.geoMu.Lock()
	defer p.geoMu.Unlock()
	if q, ok := p.geoCache[ip]; ok && time.Since(q.CheckedAt) < 24*time.Hour && q.GeoError == "" {
		return q
	}
	if q, ok := p.geoCache[ip]; ok && time.Since(q.CheckedAt) < 10*time.Minute && q.GeoError != "" {
		return q
	}
	q := IPQuality{Type: "unknown", CheckedAt: time.Now(), GeoProvider: "ipwho.is"}
	client := &http.Client{Timeout: 6 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get("https://ipwho.is/" + ip + "?fields=ip,success,country_code,connection.isp")
	if err != nil {
		q.GeoError = "地区查询连接失败，可稍后重试"
	} else {
		b, e := io.ReadAll(io.LimitReader(resp.Body, 65537))
		resp.Body.Close()
		if e != nil || len(b) > 65536 || resp.StatusCode != 200 {
			q.GeoError = "地区查询失败或达到服务限额"
		} else {
			q = parseGeo(b, ip)
		}
	}
	if q.GeoError != "" {
		fallback := fallbackGeo(ip)
		if fallback.GeoError == "" {
			q = fallback
		}
	}
	q = networkHint(q)
	if p.geoCache == nil || len(p.geoCache) >= 4096 {
		p.geoCache = make(map[string]IPQuality)
	}
	p.geoCache[ip] = q
	return q
}

func apiRefreshQuality(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "请使用 POST", 405)
			return
		}
		if !m.qualityMu.TryLock() {
			http.Error(w, "已有识别任务正在运行", 409)
			return
		}
		job := m.jobs.New("重新识别在线出口", []string{"国家、运营商与已配置的住宅检测"})
		go func() {
			defer m.qualityMu.Unlock()
			defer job.Finish()
			count := 0
			for _, v := range m.Tunnels() {
				if v.Status != "up" || net.ParseIP(v.ExitIP) == nil {
					continue
				}
				m.mu.RLock()
				original := m.tunnels[v.Slot]
				m.mu.RUnlock()
				m.pool.geoMu.Lock()
				delete(m.pool.geoCache, v.ExitIP)
				m.pool.geoMu.Unlock()
				q := m.pool.CheckIP(v.ExitIP)
				q.LatencyMS = v.Quality.LatencyMS
				m.mu.RLock()
				if original != nil && m.tunnels[v.Slot] == original {
					original.mu.Lock()
					if original.Status == "up" && original.ExitIP == v.ExitIP {
						original.Quality = q
						count++
					}
					original.mu.Unlock()
				}
				m.mu.RUnlock()
			}
			m.saveState()
			job.Set(0, "ok", fmt.Sprintf("已更新 %d 个在线出口；未返回的数据仍标记为未知", count))
		}()
		writeJSON(w, 200, map[string]string{"job": job.ID()})
	}
}
