package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

func parseIPSB(b []byte, ip string) IPQuality {
	q := IPQuality{Type: "unknown", CheckedAt: time.Now(), GeoProvider: "ip.sb"}
	var v struct {
		IP           string `json:"ip"`
		Country      string `json:"country_code"`
		ISP          string `json:"isp"`
		Organization string `json:"asn_organization"`
	}
	if json.Unmarshal(b, &v) != nil || net.ParseIP(v.IP) == nil || !net.ParseIP(v.IP).Equal(net.ParseIP(ip)) {
		q.GeoError = "备用地区接口响应无效"
		return q
	}
	q.Country = normalizedCountry(v.Country)
	q.ISP = v.ISP
	if q.ISP == "" {
		q.ISP = v.Organization
	}
	if q.Country == "" {
		q.GeoError = "备用接口未返回国家码"
	}
	return q
}

func fallbackGeo(ip string) IPQuality {
	q := IPQuality{Type: "unknown", CheckedAt: time.Now(), GeoProvider: "ip.sb", GeoError: "两个地区接口均不可用；稍后点击重新识别"}
	req, e := http.NewRequest("GET", "https://api.ip.sb/geoip/"+ip, nil)
	if e != nil {
		return q
	}
	req.Header.Set("User-Agent", "Fanout-Integrated/1.4")
	c := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, e := c.Do(req)
	if e != nil {
		return q
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return q
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, 65537))
	if e != nil || len(b) > 65536 {
		return q
	}
	return parseIPSB(b, ip)
}

// Network ownership is a heuristic, never proof that an exit is residential.
func networkHint(q IPQuality) IPQuality {
	if q.Network != "" {
		return q
	}
	s := strings.ToLower(q.ISP)
	for _, word := range []string{"amazon", "cloudflare", "google", "microsoft", "digitalocean", "hetzner", "ovh", "vultr", "linode", "akamai", "oracle", "hosting", "datacenter", "data center", "leaseweb", "choopa", "tencent cloud", "alibaba cloud", "cloud computing", "timeweb"} {
		if strings.Contains(s, word) {
			q.Network = "datacenter_likely"
			q.Evidence = "网络机构名称匹配云服务/托管特征（推断）"
			return q
		}
	}
	for _, word := range []string{"comcast", "chunghwa", "hinet", "kddi", "ntt", "softbank", "korea telecom", "sk broadband", "verizon", "at&t", "telefonica", "orange", "deutsche telekom", "china telecom", "china unicom", "china mobile", "broadband", "proxad", "viettel"} {
		if strings.Contains(s, word) {
			q.Network = "residential_candidate"
			q.Evidence = "网络机构属于接入运营商；可能为家宽、移动或企业线路，尚未确认住宅"
			return q
		}
	}
	q.Network = "unknown"
	q.Evidence = "没有足够的 IP 性质证据"
	return q
}

func applyIPAPI(b []byte, ip string, q IPQuality) (IPQuality, bool) {
	var v struct {
		IP         string `json:"ip"`
		Datacenter *bool  `json:"is_datacenter"`
		Mobile     *bool  `json:"is_mobile"`
		Company    struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"company"`
		Location struct {
			Country string `json:"country_code"`
		} `json:"location"`
	}
	if json.Unmarshal(b, &v) != nil || v.IP != ip || v.Datacenter == nil {
		return q, false
	}
	if c := normalizedCountry(v.Location.Country); c != "" {
		q.Country = c
	}
	if v.Company.Name != "" {
		q.ISP = v.Company.Name
	}
	if *v.Datacenter {
		q.Type = "datacenter"
		q.Network = "datacenter"
		q.Evidence = "IP 分类接口返回 is_datacenter=true"
	} else if v.Mobile != nil && *v.Mobile {
		q.Network = "mobile"
		q.Evidence = "IP 分类接口返回移动网络"
	} else if v.Company.Type == "isp" {
		q.Network = "residential_candidate"
		q.Evidence = "IP 分类接口返回 ISP，未证明为家庭住宅"
	} else {
		q.Network = "unknown"
		q.Evidence = "分类接口没有确认住宅属性"
	}
	return q, true
}
