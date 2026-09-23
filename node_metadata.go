package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var countryWord = regexp.MustCompile(`(?:^|[^A-Za-z])(?i:(US|GB|JP|KR|SG|HK|TW|CN|DE|FR|NL|CA|AU|RU|IN|BR|TR|VN|TH|ID|MY|PH|IT|ES|SE|CH|PL|FI|NO|ZA|AE))(?:[^A-Za-z]|$)`)

func labelCountry(label string) string {
	r := []rune(label)
	for i := 0; i+1 < len(r); i++ {
		if r[i] >= 0x1f1e6 && r[i] <= 0x1f1ff && r[i+1] >= 0x1f1e6 && r[i+1] <= 0x1f1ff {
			return string([]rune{'A' + r[i] - 0x1f1e6, 'A' + r[i+1] - 0x1f1e6})
		}
	}
	lower := strings.ToLower(label)
	names := [][2]string{{"香港", "HK"}, {"台湾", "TW"}, {"日本", "JP"}, {"韩国", "KR"}, {"新加坡", "SG"}, {"美国", "US"}, {"英国", "GB"}, {"德国", "DE"}, {"法国", "FR"}, {"加拿大", "CA"}, {"澳大利亚", "AU"}, {"俄罗斯", "RU"}, {"荷兰", "NL"}, {"hong kong", "HK"}, {"taiwan", "TW"}, {"japan", "JP"}, {"korea", "KR"}, {"singapore", "SG"}, {"united states", "US"}, {"germany", "DE"}, {"france", "FR"}, {"netherlands", "NL"}, {"canada", "CA"}}
	for _, pair := range names {
		if strings.Contains(lower, pair[0]) {
			return pair[1]
		}
	}
	if m := countryWord.FindStringSubmatch(label); len(m) > 1 {
		return strings.ToUpper(m[1])
	}
	return ""
}
func safeNodeLabel(label string) string {
	label = strings.Join(strings.Fields(label), " ")
	r := []rune(label)
	if len(r) > 100 {
		return string(r[:100])
	}
	return label
}
func annotateNodes(nodes []Node, s Source) {
	labels := map[string]string{}
	body := strings.TrimSpace(s.Content)
	// Match nodes by canonical connection configuration, not by subscription order.
	add := func(o map[string]any, label string) {
		if o == nil {
			return
		}
		b, _ := json.Marshal(o)
		h := sha256.Sum256(b)
		labels["sub-"+hex.EncodeToString(h[:12])] = safeNodeLabel(label)
	}
	var doc struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if json.Unmarshal([]byte(body), &doc) == nil {
		for _, v := range doc.Outbounds {
			o, e := sanitizeOutbound(v)
			if e == nil {
				add(o, str(v, "tag"))
			}
		}
	} else {
		if !strings.Contains(body, "://") && !strings.Contains(body, "proxies:") {
			if b, e := decodeSubscription(body); e == nil {
				body = string(b)
			}
		}
		if strings.Contains(body, "proxies:") {
			for _, v := range clashMetadata(body) {
				o, e := clashOutbound(v)
				if e == nil {
					add(o, str(v, "name"))
				}
			}
		} else {
			for _, line := range strings.Split(body, "\n") {
				line = strings.TrimSpace(line)
				o, e := uriOutbound(line)
				if e != nil {
					continue
				}
				label := ""
				if u, e := url.Parse(line); e == nil {
					label = u.Fragment
				}
				if strings.HasPrefix(line, "vmess://") {
					if b, e := decodeSubscription(strings.TrimPrefix(line, "vmess://")); e == nil {
						var v map[string]any
						if json.Unmarshal(b, &v) == nil {
							label = str(v, "ps")
						}
					}
				}
				add(o, label)
			}
		}
	}
	for i := range nodes {
		n := &nodes[i]
		n.Label = labels[n.HostName]
		if n.CountryCode == "UN" || n.CountryCode == "" {
			if c := labelCountry(n.Label); c != "" {
				n.CountryCode = c
				n.Country = c
			}
		}
		if n.CountryCode != "UN" && n.CountryCode != "" {
			n.CountryBasis = "source"
		}
	}
}

func metadataProxyNodes(s Source) ([]Node, error) {
	var rows []struct {
		Protocol string `json:"protocol"`
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Username string `json:"username"`
		Password string `json:"password"`
		Geo      struct {
			Country struct {
				Code string `json:"iso_code"`
			} `json:"country"`
		} `json:"geolocation"`
	}
	if json.Unmarshal([]byte(s.Content), &rows) != nil {
		return nil, fmt.Errorf("代理元数据 JSON 无效")
	}
	out := []Node{}
	seen := map[string]bool{}
	for _, v := range rows {
		if v.Protocol != "socks5" && v.Protocol != "http" && v.Protocol != "https" {
			continue
		}
		u := url.URL{Scheme: v.Protocol, Host: net.JoinHostPort(v.Host, strconv.Itoa(v.Port))}
		if v.Username != "" {
			u.User = url.UserPassword(v.Username, v.Password)
		}
		raw, e := validateUpstream(u.String())
		if e != nil {
			continue
		}
		h := sha256.Sum256([]byte(raw))
		id := "proxy-" + hex.EncodeToString(h[:8])
		if seen[id] {
			continue
		}
		seen[id] = true
		c := normalizedCountry(v.Geo.Country.Code)
		if c == "" {
			c = "UN"
		}
		out = append(out, Node{HostName: id, Label: v.Protocol + " " + c, CountryBasis: "source", Country: c, CountryCode: c, Source: s.ID, Kind: "proxy", Upstream: raw})
		if len(out) >= 2000 {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有支持的 HTTP/SOCKS5 节点")
	}
	return out, nil
}
