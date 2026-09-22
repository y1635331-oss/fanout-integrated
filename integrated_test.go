package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDomainUDPResponseRoundTrip(t *testing.T) {
	p := buildUDPRespHeader(atypDomain, []byte("example.org"), 53, []byte("payload"))
	a, _, host, port, data, ok := parseUDPReqHeader(p)
	if !ok || a != atypDomain || host != "example.org" || port != 53 || string(data) != "payload" {
		t.Fatalf("invalid domain response: %x", p)
	}
}
func TestUDPPeerIsolationAndRelay(t *testing.T) {
	ln, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	var count atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, e := ln.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		serveUDPAssociate(c, func(network, addr string) (net.Conn, error) {
			count.Add(1)
			a, b := net.Pipe()
			go func() {
				defer b.Close()
				buf := make([]byte, 50)
				n, e := b.Read(buf)
				if e == nil {
					b.Write(buf[:n])
				}
			}()
			return a, nil
		})
	}()
	ctrl, e := net.Dial("tcp4", ln.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer ctrl.Close()
	ctrl.SetReadDeadline(time.Now().Add(3 * time.Second))
	head := make([]byte, 10)
	if _, e = io.ReadFull(ctrl, head); e != nil {
		t.Fatal(e)
	}
	port := int(head[8])*256 + int(head[9])
	relayAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
	wrong, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.2")})
	if e != nil {
		t.Fatal(e)
	}
	defer wrong.Close()
	pkt := buildUDPRespHeader(atypDomain, []byte("example.org"), 53, []byte("test"))
	wrong.WriteToUDP(pkt, relayAddr)
	wrong.SetReadDeadline(time.Now().Add(120 * time.Millisecond))
	buf := make([]byte, 100)
	if _, _, e = wrong.ReadFromUDP(buf); e == nil {
		t.Fatal("unauthorized UDP source received reply")
	}
	if count.Load() != 0 {
		t.Fatal("unauthorized packet dialed upstream")
	}
	valid, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if e != nil {
		t.Fatal(e)
	}
	defer valid.Close()
	valid.WriteToUDP(pkt, relayAddr)
	valid.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, e := valid.ReadFromUDP(buf)
	if e != nil {
		t.Fatal(e)
	}
	_, _, host, _, body, ok := parseUDPReqHeader(buf[:n])
	if !ok || host != "example.org" || string(body) != "test" {
		t.Fatalf("bad echo %x", buf[:n])
	}
	ctrl.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("association survived control close")
	}
}
func TestPasswordChangeRevokesSession(t *testing.T) {
	a, _, e := NewAuth(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	token, _ := a.issue()
	if e = a.SetPassword("new-password-123456"); e != nil {
		t.Fatal(e)
	}
	if a.valid(token) {
		t.Fatal("old session survived password change")
	}
}

func TestStaleBoundOutboundIsBlocked(t *testing.T) {
	cfg := map[string]any{"routing": map[string]any{"rules": []any{map[string]any{"outboundTag": "fanout-missing"}}}}
	outs := preserveBoundOutbounds(cfg, nil)
	if len(outs) != 1 || outs[0].(map[string]any)["protocol"] != "blackhole" {
		t.Fatal(outs)
	}
}

func TestLoginCannotUseRevokedPassword(t *testing.T) {
	a, _, err := NewAuth(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = a.SetPassword("first-password-123"); err != nil {
		t.Fatal(err)
	}
	token, ok, err := a.login("first-password-123")
	if err != nil || !ok {
		t.Fatal("login failed")
	}
	if err = a.SetPassword("second-password-456"); err != nil {
		t.Fatal(err)
	}
	if a.valid(token) {
		t.Fatal("old token still valid")
	}
	if _, ok, err = a.login("first-password-123"); err != nil || ok {
		t.Fatal("revoked password accepted")
	}
}

func TestTLSConfigurationAndSecureCookie(t *testing.T) {
	publicIPMu.Lock()
	old := publicIPOverride
	publicIPOverride = "203.0.113.9"
	publicIPMu.Unlock()
	defer setPublicIPOverride(old)
	t.Setenv("FANOUT_TLS_CERT", "")
	t.Setenv("FANOUT_TLS_KEY", "")
	dir := t.TempDir()
	cfg, fingerprint, err := managementTLS(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(fingerprint) != 64 || len(cfg.Certificates) != 1 {
		t.Fatal("missing certificate")
	}
	_, again, err := managementTLS(dir)
	if err != nil || again != fingerprint {
		t.Fatal("certificate unexpectedly changed")
	}
	a, _, _ := NewAuth(t.TempDir())
	a.SetPassword("secure-password-123")
	r := httptest.NewRequest("POST", "https://example.org/login", strings.NewReader("password=secure-password-123"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	a.handleLogin(w, r)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatal("session cookie missing protections")
	}
}
func TestSafeConfigRejectsExecutableOptions(t *testing.T) {
	base := "client\nremote vpn.example.org 1194\n<ca>\nCERT\n</ca>\n"
	for _, option := range []string{"plugin /tmp/evil.so", "up /tmp/evil.sh", "config /tmp/other", "log /etc/passwd", "management 0.0.0.0 1", "script-security 3", "ca /etc/secret"} {
		if _, e := safeOpenVPNConfig(base + option); e == nil {
			t.Errorf("accepted %s", option)
		}
	}
	if _, e := safeOpenVPNConfig(base); e != nil {
		t.Fatal(e)
	}
}
func TestSourceImportAndPersistence(t *testing.T) {
	dir := t.TempDir()
	p, e := NewPoolStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	if e = p.Add(Source{Name: "my source", Kind: "socks5", Country: "JP", Content: "127.0.0.1:10001\nsocks5://user:password@127.0.0.1:10002"}); e != nil {
		t.Fatal(e)
	}
	nodes, _ := p.Nodes()
	if len(nodes) != 2 || nodes[0].Kind != "proxy" {
		t.Fatalf("bad nodes: %+v", nodes)
	}
	loaded, e := NewPoolStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	n, _ := loaded.Nodes()
	if len(n) != 2 || n[1].Upstream != nodes[1].Upstream {
		t.Fatal("source credentials not restored")
	}
	b, _ := json.Marshal(p.Views())
	if bytes.Contains(b, []byte("password")) {
		t.Fatal("source views leaked content")
	}
	p.Record(nodes[0].HostName, false)
	if !p.Cooling(nodes[0].HostName) {
		t.Fatal("missing cooldown")
	}
	p.Record(nodes[0].HostName, true)
	if p.Cooling(nodes[0].HostName) {
		t.Fatal("success did not clear cooldown")
	}
}

func TestRemovedSourceCannotBeStartedFromStaleSelection(t *testing.T) {
	p, e := NewPoolStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = p.Add(Source{Name: "temp", Kind: "socks5", Content: "127.0.0.1:12345"}); e != nil {
		t.Fatal(e)
	}
	nodes, _ := p.Nodes()
	m := NewManager(20, t.TempDir())
	m.pool = p
	m.nodes = nodes
	if e = m.RemoveSource(nodes[0].Source); e != nil {
		t.Fatal(e)
	}
	if len(m.nodes) != 0 {
		t.Fatal("deleted source remained in memory")
	}
	if _, e = m.Start(nodes[0]); e == nil {
		t.Fatal("stale selection started deleted source")
	}
}
func TestOpenVPNImportValidatesWithoutDoubleTransform(t *testing.T) {
	s := Source{ID: "x", Name: "vpn", Kind: "openvpn", Content: "client\nremote vpn.example.org 1194\n"}
	nodes, e := sourceNodes(s)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = safeOpenVPNConfig(nodes[0].Config); e != nil {
		t.Fatal(e)
	}
}
func TestExportFiltersAndEscapes(t *testing.T) {
	ts := []*Tunnel{{Slot: 1, Port: 10001, Status: "up", ExitIP: "8.8.8.8", Node: Node{CountryCode: "JP"}, Cred: SocksCred{User: "a@b", Pass: "p#?"}, Quality: IPQuality{Type: "residential"}}, {Slot: 2, Status: "failed", ExitIP: "1.1.1.1"}, {Slot: 3, Status: "up", ExitIP: "8.8.8.8"}, {Slot: 4, Status: "up", ExitIP: "9.9.9.9", Quality: IPQuality{Type: "unknown"}}}
	rows := exportRows(ts, "203.0.113.1", "", 0, true)
	if len(rows) != 1 {
		t.Fatalf("expected one eligible unique residential exit: %+v", rows)
	}
	u, e := url.Parse(rows[0].URL)
	if e != nil {
		t.Fatal(e)
	}
	pass, _ := u.User.Password()
	if u.User.Username() != "a@b" || pass != "p#?" {
		t.Fatal("credentials not escaped")
	}
}
func TestExportTokenCannotMutate(t *testing.T) {
	p, e := NewPoolStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	a, _, _ := NewAuth(t.TempDir())
	apiProxiesHandler = func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("proxies")) }
	handler := secureRequests(p.exportAuth(a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("admin")) }))))
	for _, tc := range []struct {
		path, method string
		want         int
	}{{"/api/proxies", "GET", 200}, {"/api/pool", "POST", 401}, {"/api/start", "GET", 405}, {"/api/proxies", "POST", 401}} {
		r := httptest.NewRequest(tc.method, "https://example.org"+tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+p.Token())
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s %s: %d", tc.method, tc.path, w.Code)
		}
	}
}
func TestCrossOriginWriteRejected(t *testing.T) {
	h := secureRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	r := httptest.NewRequest("POST", "https://panel.example/api/pool", strings.NewReader("{}"))
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestUpstreamSOCKSAndHTTPConnect(t *testing.T) {
	for _, kind := range []string{"socks5", "http"} {
		t.Run(kind, func(t *testing.T) {
			ln, e := net.Listen("tcp4", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			defer ln.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				c, e := ln.Accept()
				if e != nil {
					return
				}
				defer c.Close()
				if kind == "socks5" {
					serveSocks(c, &SocksCred{User: "test", Pass: "password"}, func(network, addr string) (net.Conn, error) {
						a, b := net.Pipe()
						go func() { defer b.Close(); io.Copy(b, b) }()
						return a, nil
					})
				} else {
					buf := make([]byte, 4096)
					n, _ := c.Read(buf)
					if !strings.Contains(string(buf[:n]), "Proxy-Authorization: Basic ") {
						return
					}
					io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
					io.Copy(c, c)
				}
			}()
			c, e := upstreamDialer(kind+"://test:password@"+ln.Addr().String())("tcp", "example.org:443")
			if e != nil {
				t.Fatal(e)
			}
			c.SetDeadline(time.Now().Add(3 * time.Second))
			if _, e = c.Write([]byte("hello")); e != nil {
				t.Fatal(e)
			}
			b := make([]byte, 5)
			if _, e = io.ReadFull(c, b); e != nil || string(b) != "hello" {
				t.Fatalf("echo failed %s %v", b, e)
			}
			c.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("upstream leaked")
			}
		})
	}
}
func TestSnapshotConcurrentState(t *testing.T) {
	m := NewManager(10, t.TempDir())
	tr := &Tunnel{Slot: 1, Status: "starting"}
	m.tunnels[1] = tr
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				tr.state("up", "")
				tr.setCredential(SocksCred{User: "user", Pass: "pass"})
				_, _ = json.Marshal(m.Tunnels())
				_ = m.saveState()
			}
		}()
	}
	wg.Wait()
	if _, e := os.ReadFile(statePath(m.workDir)); e != nil {
		t.Fatal(e)
	}
}
