package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSubscriptionFormatsAndMixedFailures(t *testing.T) {
	uri := "vless://00000000-0000-0000-0000-000000000001@example.com:443?security=tls&sni=example.com#test"
	for _, body := range []string{uri, base64.StdEncoding.EncodeToString([]byte(uri)), `{"outbounds":[{"type":"direct"},{"type":"vless","server":"example.com","server_port":443,"uuid":"00000000-0000-0000-0000-000000000001"}]}`, "proxies:\n  - name: test\n    type: vless\n    server: example.com\n    port: 443\n    uuid: 00000000-0000-0000-0000-000000000001\n"} {
		ns, _, e := subscriptionNodes(Source{Kind: "subscription", Content: body})
		if e != nil || len(ns) != 1 {
			t.Fatalf("format failed: %v count=%d", e, len(ns))
		}
	}
	ns, skipped, e := subscriptionNodes(Source{Content: uri + "\ninvalid\n" + uri})
	if e != nil || len(ns) != 1 || skipped != 1 {
		t.Fatalf("mixed/dedupe: %v %d %d", e, len(ns), skipped)
	}
}
func TestSubscriptionStripsUntrustedConfig(t *testing.T) {
	body := `{"inbounds":[{"listen":"0.0.0.0"}],"outbounds":[{"type":"vless","server":"example.com","server_port":443,"uuid":"x","bind_interface":"eth0","tls":{"enabled":true,"certificate_path":"/etc/shadow","server_name":"example.com"}}]}`
	ns, _, e := subscriptionNodes(Source{Content: body})
	if e != nil {
		t.Fatal(e)
	}
	cfg, e := coreConfig(ns[0].Upstream, 12345, "u", "p")
	if e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{"/etc/shadow", "bind_interface", "0.0.0.0", "\"direct\""} {
		if strings.Contains(string(cfg), bad) {
			t.Fatalf("untrusted field survived %s", bad)
		}
	}
	var c map[string]any
	json.Unmarshal(cfg, &c)
	if c["route"].(map[string]any)["final"] != "proxy" {
		t.Fatal("not proxy only")
	}
	_, _, e = subscriptionNodes(Source{Content: `{"outbounds":[{"type":"trojan","server":"example.com","server_port":443,"tls":{"enabled":true,"insecure":true}}]}`})
	if e == nil {
		t.Fatal("accepted insecure")
	}
}
func TestSubscriptionLiveSourceSamples(t *testing.T) {
	dir := os.Getenv("FANOUT_SOURCE_SAMPLES")
	if dir == "" {
		t.Skip("optional downloaded source fixtures")
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		b, e := os.ReadFile(filepath.Join(dir, entry.Name()))
		if e != nil {
			t.Fatal(e)
		}
		kind := "subscription"
		if entry.Name() == "monosans.json" {
			kind = "metadata"
		}
		if entry.Name() == "rooster.txt" {
			kind = "socks5"
		}
		ns, e := sourceNodes(Source{Kind: kind, Content: string(b)})
		skip := 0
		if e != nil {
			t.Errorf("%s: %v", entry.Name(), e)
		} else {
			classified := 0
			for _, n := range ns {
				if normalizedCountry(n.CountryCode) != "" {
					classified++
				}
			}
			t.Logf("%s: accepted=%d source_country=%d skipped=%d", entry.Name(), len(ns), classified, skip)
		}
	}
}
func TestSubscriptionCoreForwardsAndFailsClosed(t *testing.T) {
	if os.Getenv("FANOUT_CORE_TEST") != "1" {
		t.Skip("requires installed official sing-box")
	}
	echo, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer echo.Close()
	go func() {
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	upstream, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer upstream.Close()
	go func() {
		for {
			c, e := upstream.Accept()
			if e != nil {
				return
			}
			go serveSocks(c, &SocksCred{User: "user", Pass: "pass"}, net.Dial)
		}
	}()
	host, port, _ := net.SplitHostPort(upstream.Addr().String())
	b, _ := json.Marshal(map[string]any{"outbounds": []any{map[string]any{"type": "socks", "server": host, "server_port": port, "username": "user", "password": "pass", "version": "5"}}})
	ns, _, e := subscriptionNodes(Source{Content: string(b)})
	if e != nil {
		t.Fatal(e)
	}
	tunnel := &Tunnel{Node: ns[0]}
	defer tunnel.cleanup()
	if e = tunnel.startSubscription(t.TempDir()); e != nil {
		t.Fatal(e)
	}
	dial := tunnel.dial()
	c, e := dial("tcp", echo.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	c.SetDeadline(time.Now().Add(3 * time.Second))
	c.Write([]byte("hello"))
	got := make([]byte, 5)
	_, e = io.ReadFull(c, got)
	c.Close()
	if e != nil || string(got) != "hello" {
		t.Fatalf("proxy transfer %q %v", got, e)
	}
	tunnel.cleanup()
	if c, e = dial("tcp", echo.Addr().String()); e == nil {
		c.Close()
		t.Fatal("core death must not fall back to direct")
	}
}
