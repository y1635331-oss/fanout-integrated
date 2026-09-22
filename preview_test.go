package main

import (
	"net/http"
	"os"
	"testing"
	"time"
)

// Local UI fixture only; disabled in normal tests and excluded from binaries.
func TestDashboardPreview(t *testing.T) {
	if os.Getenv("FANOUT_UI_PREVIEW") != "1" {
		t.Skip("local preview only")
	}
	p, e := NewPoolStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	setPublicIPOverride("203.0.113.10")
	m := NewManager(20, t.TempDir())
	m.pool = p
	m.nodes = []Node{{HostName: "jp-demo", CountryCode: "JP", Country: "Japan", Kind: "openvpn", Source: "vpngate"}}
	m.tunnels[1] = &Tunnel{Slot: 1, Port: 23001, Status: "up", ExitIP: "198.51.100.23", Node: m.nodes[0], Cred: SocksCred{User: "demo", Pass: "demo-only-password"}, Since: time.Now(), Quality: IPQuality{Type: "unknown"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", handlePoolPage)
	registerIntegrated(mux, m)
	mux.HandleFunc("/api/regions", apiRegions(m))
	mux.HandleFunc("/api/nodes", apiNodes(m))
	srv := &http.Server{Addr: "127.0.0.1:18789", Handler: secureRequests(mux)}
	defer srv.Close()
	t.Log("UI fixture http://127.0.0.1:18789 (example addresses only)")
	if e = srv.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		t.Fatal(e)
	}
}
