//go:build linux

package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Explicit opt-in: creates only a temporary namespace/veth pair, and no VPN or
// third-party proxy traffic. Confirms a working fallback route is not used.
func TestNamespaceFailsClosedWithWorkingDefaultRoute(t *testing.T) {
	if os.Getenv("FANOUT_NETNS_TEST") != "1" {
		t.Skip("set FANOUT_NETNS_TEST=1 on Linux with root/netns capability")
	}
	if os.Geteuid() != 0 {
		t.Fatal("root required")
	}
	suffix := fmt.Sprint(os.Getpid())
	ns := "fotest" + suffix
	a := "fta" + suffix
	b := "ftb" + suffix
	must := func(args ...string) {
		t.Helper()
		out, e := exec.Command("ip", args...).CombinedOutput()
		if e != nil {
			t.Fatalf("ip %v: %v %s", args, e, out)
		}
	}
	must("netns", "add", ns)
	defer exec.Command("ip", "netns", "del", ns).Run()
	must("link", "add", a, "type", "veth", "peer", "name", b)
	defer exec.Command("ip", "link", "del", a).Run()
	must("link", "set", b, "netns", ns)
	must("addr", "add", "198.18.253.1/30", "dev", a)
	must("link", "set", a, "up")
	must("netns", "exec", ns, "ip", "addr", "add", "198.18.253.2/30", "dev", b)
	must("netns", "exec", ns, "ip", "link", "set", b, "up")
	must("netns", "exec", ns, "ip", "link", "set", "lo", "up")
	must("netns", "exec", ns, "ip", "route", "add", "default", "via", "198.18.253.1")
	ln, e := net.Listen("tcp4", "198.18.253.1:0")
	if e != nil {
		t.Fatal(e)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "fallback-works") })}
	go srv.Serve(ln)
	defer srv.Close()
	out, e := exec.Command("ip", "netns", "exec", ns, "curl", "--noproxy", "*", "-fsS", "--max-time", "3", "http://"+ln.Addr().String()).Output()
	if e != nil || strings.TrimSpace(string(out)) != "fallback-works" {
		t.Fatalf("test default route unavailable: %v %s", e, out)
	}
	if c, e := dialerInNetns(ns)("tcp", ln.Addr().String()); e == nil {
		c.Close()
		t.Fatal("proxy leaked through working default route without tun0")
	}
	must("netns", "exec", ns, "ip", "link", "add", "tun0", "type", "dummy")
	must("netns", "exec", ns, "ip", "link", "set", "tun0", "up")
	must("netns", "exec", ns, "ip", "link", "del", "tun0")
	if c, e := dialerInNetns(ns)("tcp", ln.Addr().String()); e == nil {
		c.Close()
		t.Fatal("proxy leaked after tunnel removal")
	}
}
