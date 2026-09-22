package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type IPQuality struct {
	Type      string    `json:"type"`
	ISP       string    `json:"isp,omitempty"`
	Country   string    `json:"country,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	Error     string    `json:"error,omitempty"`
}
type PoolConfig struct {
	Auto            bool   `json:"auto"`
	Target          int    `json:"target"`
	Country         string `json:"country"`
	RefreshMinutes  int    `json:"refresh_minutes"`
	ResidentialOnly bool   `json:"residential_only"`
	QualityURL      string `json:"quality_url"`
}
type Source struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Kind    string    `json:"kind"`
	URL     string    `json:"url,omitempty"`
	Content string    `json:"content,omitempty"`
	Country string    `json:"country"`
	User    string    `json:"user,omitempty"`
	Pass    string    `json:"pass,omitempty"`
	Updated time.Time `json:"updated"`
	Error   string    `json:"error,omitempty"`
}
type NodeHistory struct {
	Success  int       `json:"success"`
	Failures int       `json:"failures"`
	RetryAt  time.Time `json:"retry_at"`
}
type PoolStore struct {
	mu          sync.Mutex
	refreshMu   sync.Mutex
	dir         string
	Settings    PoolConfig             `json:"settings"`
	Sources     []Source               `json:"sources"`
	History     map[string]NodeHistory `json:"history"`
	ExportToken string                 `json:"export_token"`
}

func NewPoolStore(dir string) (*PoolStore, error) {
	p := &PoolStore{dir: dir, Settings: PoolConfig{Target: 5, RefreshMinutes: 10}, History: map[string]NodeHistory{}}
	b, e := os.ReadFile(filepath.Join(dir, "pool.json"))
	if e == nil {
		if e = json.Unmarshal(b, p); e != nil {
			return nil, e
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if p.History == nil {
		p.History = map[string]NodeHistory{}
	}
	if p.ExportToken == "" {
		p.ExportToken, e = randomToken(32)
		if e != nil {
			return nil, e
		}
	}
	return p, p.saveLocked()
}
func (p *PoolStore) saveLocked() error {
	b, e := json.MarshalIndent(p, "", "  ")
	if e != nil {
		return e
	}
	file := filepath.Join(p.dir, "pool.json")
	if e = os.WriteFile(file+".tmp", b, 0600); e != nil {
		return e
	}
	return os.Rename(file+".tmp", file)
}
func (p *PoolStore) Config() PoolConfig { p.mu.Lock(); defer p.mu.Unlock(); return p.Settings }
func (p *PoolStore) SetConfig(c PoolConfig, max int) error {
	c.Country = strings.ToUpper(strings.TrimSpace(c.Country))
	if c.Target < 1 || c.Target > max || c.RefreshMinutes < 2 || c.RefreshMinutes > 1440 || len(c.Country) > 2 {
		return fmt.Errorf("数量须在 1-%d，刷新间隔 2-1440 分钟，国家填两位代码", max)
	}
	if c.QualityURL != "" {
		if e := validFeedURL(strings.ReplaceAll(c.QualityURL, "{ip}", "1.1.1.1")); e != nil {
			return e
		}
	}
	if c.ResidentialOnly && c.QualityURL == "" {
		return fmt.Errorf("严格住宅模式需要配置返回结构化结果的检测接口")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	old := p.Settings
	p.Settings = c
	if e := p.saveLocked(); e != nil {
		p.Settings = old
		return e
	}
	return nil
}
func (p *PoolStore) Token() string { p.mu.Lock(); defer p.mu.Unlock(); return p.ExportToken }
func (p *PoolStore) RotateToken() (string, error) {
	t, e := randomToken(32)
	if e != nil {
		return "", e
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	old := p.ExportToken
	p.ExportToken = t
	if e = p.saveLocked(); e != nil {
		p.ExportToken = old
		return "", e
	}
	return t, nil
}
func (p *PoolStore) Cooling(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return time.Now().Before(p.History[id].RetryAt)
}
func (p *PoolStore) Record(id string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	h := p.History[id]
	if ok {
		h.Success++
		h.Failures = 0
		h.RetryAt = time.Time{}
	} else {
		h.Failures++
		delay := time.Duration(h.Failures) * time.Minute
		if delay > 30*time.Minute {
			delay = 30 * time.Minute
		}
		h.RetryAt = time.Now().Add(delay)
	}
	p.History[id] = h
	if len(p.History) > 10000 {
		for k, v := range p.History {
			if k != id && time.Since(v.RetryAt) > 24*time.Hour {
				delete(p.History, k)
			}
		}
	}
	_ = p.saveLocked()
}
func (p *PoolStore) Views() []Source {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Source, 0, len(p.Sources))
	for _, s := range p.Sources {
		s.Content = ""
		s.User = ""
		s.Pass = ""
		if s.URL != "" {
			s.URL = "已配置 HTTPS 订阅"
		}
		out = append(out, s)
	}
	return out
}
func validFeedURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("订阅/检测接口必须是 HTTPS URL")
	}
	return nil
}
func fetchFeed(raw string) ([]byte, error) {
	if e := validFeedURL(raw); e != nil {
		return nil, e
	}
	client := &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return fmt.Errorf("重定向过多")
		}
		return validFeedURL(r.URL.String())
	}}
	r, e := client.Get(raw)
	if e != nil {
		return nil, fmt.Errorf("HTTPS 请求失败")
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", r.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, (4<<20)+1))
	if len(b) > 4<<20 {
		return nil, fmt.Errorf("来源超过 4 MB")
	}
	return b, e
}
func sourceNodes(s Source) ([]Node, error) {
	var out []Node
	prefix := "import-" + s.ID + "-"
	country := strings.ToUpper(s.Country)
	if country == "" {
		country = "UN"
	}
	switch s.Kind {
	case "openvpn":
		_, e := safeOpenVPNConfig(s.Content)
		if e != nil {
			return nil, e
		}
		out = append(out, Node{HostName: prefix + "vpn", Source: s.ID, Kind: "openvpn", CountryCode: country, Country: country, Config: s.Content, VPNUser: s.User, VPNPass: s.Pass})
	case "proxy", "socks5", "http":
		for i, line := range strings.Split(s.Content, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if !strings.Contains(line, "://") && s.Kind != "proxy" {
				line = s.Kind + "://" + line
			}
			u, e := validateUpstream(line)
			if e != nil {
				return nil, fmt.Errorf("第 %d 行: %v", i+1, e)
			}
			h := sha256.Sum256([]byte(u))
			out = append(out, Node{HostName: "proxy-" + hex.EncodeToString(h[:8]), Source: s.ID, Kind: "proxy", CountryCode: country, Country: country, Upstream: u})
			if len(out) >= 2000 {
				break
			}
		}
	case "vpngate":
		nodes, e := parseNodeCSV(s.Content)
		if e != nil {
			return nil, e
		}
		for _, n := range nodes {
			n.Source = s.ID
			n.Kind = "openvpn"
			out = append(out, n)
		}
	default:
		return nil, fmt.Errorf("来源类型无效")
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("来源没有节点")
	}
	return out, nil
}
func (p *PoolStore) Add(s Source) error {
	if len(strings.TrimSpace(s.Name)) == 0 || len(s.Name) > 100 {
		return fmt.Errorf("来源名称需为 1-100 字符")
	}
	if len(s.Country) > 2 || strings.ContainsAny(s.User+s.Pass, "\r\n") {
		return fmt.Errorf("国家或 OpenVPN 凭据无效")
	}
	if s.URL != "" {
		b, e := fetchFeed(s.URL)
		if e != nil {
			return e
		}
		s.Content = string(b)
	}
	if len(s.Content) > 4<<20 {
		return fmt.Errorf("来源内容太大")
	}
	id, e := randomToken(8)
	if e != nil {
		return e
	}
	s.ID = id
	if _, e = sourceNodes(s); e != nil {
		return e
	}
	s.Updated = time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.Sources) >= 50 {
		return fmt.Errorf("最多 50 个来源")
	}
	for _, old := range p.Sources {
		if s.URL != "" && old.URL == s.URL {
			return fmt.Errorf("此订阅已经添加")
		}
	}
	p.Sources = append(p.Sources, s)
	if e = p.saveLocked(); e != nil {
		p.Sources = p.Sources[:len(p.Sources)-1]
	}
	return e
}
func (p *PoolStore) Delete(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, s := range p.Sources {
		if s.ID == id {
			old := append([]Source(nil), p.Sources...)
			p.Sources = append(p.Sources[:i], p.Sources[i+1:]...)
			if e := p.saveLocked(); e != nil {
				p.Sources = old
				return e
			}
			return nil
		}
	}
	return fmt.Errorf("来源不存在")
}

func (p *PoolStore) HasSource(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.Sources {
		if s.ID == id {
			return true
		}
	}
	return false
}

// Serialize deletion with refresh/start so removed sources cannot re-enter the
// in-memory pool if VPN Gate happens to be unavailable during the next fetch.
func (m *Manager) RemoveSource(id string) error {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tunnels {
		if t.snapshot().Node.Source == id {
			return fmt.Errorf("请先停止使用此来源的出口")
		}
	}
	if err := m.pool.Delete(id); err != nil {
		return err
	}
	kept := make([]Node, 0, len(m.nodes))
	for _, n := range m.nodes {
		if n.Source != id {
			kept = append(kept, n)
		}
	}
	m.nodes = kept
	m.fetched = time.Now()
	return nil
}
func (p *PoolStore) Refresh() {
	p.refreshMu.Lock()
	defer p.refreshMu.Unlock()
	p.mu.Lock()
	sources := append([]Source(nil), p.Sources...)
	p.mu.Unlock()
	for _, s := range sources {
		if s.URL == "" {
			continue
		}
		b, e := fetchFeed(s.URL)
		if e == nil {
			s.Content = string(b)
			_, e = sourceNodes(s)
		}
		p.mu.Lock()
		for i, v := range p.Sources {
			if v.ID != s.ID {
				continue
			}
			if e == nil {
				p.Sources[i].Content = s.Content
				p.Sources[i].Updated = time.Now()
				p.Sources[i].Error = ""
			} else {
				p.Sources[i].Error = e.Error()
			}
		}
		_ = p.saveLocked()
		p.mu.Unlock()
	}
}
func (p *PoolStore) Nodes() ([]Node, error) {
	p.mu.Lock()
	sources := append([]Source(nil), p.Sources...)
	p.mu.Unlock()
	out := []Node{}
	seen := map[string]bool{}
	for _, s := range sources {
		nodes, e := sourceNodes(s)
		if e != nil {
			continue
		}
		for _, n := range nodes {
			if !seen[n.HostName] {
				out = append(out, n)
				seen[n.HostName] = true
			}
		}
	}
	return out, nil
}
func (p *PoolStore) CheckIP(ip string) IPQuality {
	q := IPQuality{Type: "unknown", CheckedAt: time.Now()}
	c := p.Config()
	if c.QualityURL == "" {
		return q
	}
	if net.ParseIP(ip) == nil {
		return q
	}
	raw := strings.ReplaceAll(c.QualityURL, "{ip}", url.QueryEscape(ip))
	b, e := fetchFeed(raw)
	if e != nil {
		q.Error = e.Error()
		return q
	}
	var answer struct {
		IP      string `json:"ip"`
		Type    string `json:"type"`
		ISP     string `json:"isp"`
		Country string `json:"country"`
	}
	if e = json.Unmarshal(b, &answer); e != nil || answer.IP != ip {
		q.Error = "检测响应缺少匹配的 IP 或不是 JSON"
		return q
	}
	switch answer.Type {
	case "residential", "datacenter", "unknown":
		q.Type = answer.Type
	default:
		q.Error = "未知检测类型"
	}
	q.ISP = answer.ISP
	q.Country = answer.Country
	return q
}
func (m *Manager) WatchPool(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		c := m.pool.Config()
		if time.Since(last) >= time.Duration(c.RefreshMinutes)*time.Minute {
			m.pool.Refresh()
			_, _ = m.RefreshNodes()
			last = time.Now()
		}
		if !c.Auto {
			continue
		}
		occupied := len(m.Tunnels())
		if occupied >= c.Target {
			continue
		}
		picks, e := m.pickNodes(c.Country, c.Target-occupied)
		if e != nil {
			continue
		}
		for _, n := range picks {
			if ctx.Err() != nil {
				return
			}
			_, _ = m.Start(n)
		}
	}
}
