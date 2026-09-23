package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportDomainPrecedenceAndValidation(t *testing.T) {
	for _, s := range []string{"https://panel.example.com", "panel.example.com:443", "user@panel.example.com", "a.example.com/x", "a.example.com;sh"} {
		if _, e := validateExportHost(s); e == nil {
			t.Fatalf("invalid domain accepted %q", s)
		}
	}
	dir := t.TempDir()
	p, e := NewPoolStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	m := NewManager(5, dir)
	m.pool = p
	os.WriteFile(filepath.Join(dir, "tls.json"), []byte(`{"domain":"panel.example.com"}`), 0600)
	if m.exportHost() != "panel.example.com" {
		t.Fatal("TLS domain not used")
	}
	c := p.Config()
	c.ExportHost = "socks.example.com"
	if e = p.SetConfig(c, 5); e != nil {
		t.Fatal(e)
	}
	if m.exportHost() != "socks.example.com" {
		t.Fatal("explicit domain not preferred")
	}
	rows := exportRows([]*Tunnel{{Status: "up", ExitIP: "8.8.8.8", Port: 23001, Cred: SocksCred{User: "u", Pass: "p"}}}, m.exportHost(), "", 0, false)
	if len(rows) != 1 || !strings.Contains(rows[0].URL, "@socks.example.com:23001") {
		t.Fatal("export did not use domain")
	}
}
func TestCountryHintsAreNotResidentialProof(t *testing.T) {
	for label, want := range map[string]string{"🇯🇵 Japan 家宽": "JP", "美国-住宅-US": "US", "HK test": "HK", "新加坡01": "SG", "unknown-node": ""} {
		if got := labelCountry(label); got != want {
			t.Errorf("%q: %q != %q", label, got, want)
		}
	}
	s := Source{Kind: "subscription", Content: `{"outbounds":[{"type":"vless","tag":"🇯🇵 家宽","server":"example.com","server_port":443,"uuid":"00000000-0000-0000-0000-000000000001"}]}`}
	ns, e := sourceNodes(s)
	if e != nil || len(ns) != 1 {
		t.Fatal(e)
	}
	if ns[0].CountryCode != "JP" || ns[0].CountryBasis != "source" || ns[0].Label == "" {
		t.Fatal("lost source label")
	}
	q := networkHint(IPQuality{Type: "unknown", ISP: "Chunghwa Telecom"})
	if q.Type != "unknown" || q.Network != "residential_candidate" {
		t.Fatal("ISP heuristic misclassified residential")
	}
	q = networkHint(IPQuality{Type: "unknown", ISP: "Amazon AWS"})
	if q.Network != "datacenter_likely" {
		t.Fatal("cloud hint missing")
	}
}
func TestQualityProviderMissingFieldsStayUnknown(t *testing.T) {
	q, ok := applyIPAPI([]byte(`{"ip":"8.8.8.8","company":"Google"}`), "8.8.8.8", IPQuality{Type: "unknown"})
	if ok || q.Type != "unknown" {
		t.Fatal("anonymous response must not imply residential")
	}
	q, ok = applyIPAPI([]byte(`{"ip":"8.8.8.8","is_datacenter":true,"company":{"type":"hosting","name":"Google"}}`), "8.8.8.8", IPQuality{Type: "unknown"})
	if !ok || q.Type != "datacenter" {
		t.Fatal("hosting flag lost")
	}
	q = parseIPSB([]byte(`{"ip":"1.1.1.1","country_code":"AU","isp":"Cloudflare"}`), "8.8.8.8")
	if q.GeoError == "" {
		t.Fatal("mismatched IP accepted")
	}
}
func TestMaintenanceRejectsArbitraryCommandsAndKeepsResults(t *testing.T) {
	for _, a := range []string{"", "reboot", "app-update; id", "/tmp/custom.sh"} {
		if _, e := maintenanceCommand(a); e == nil {
			t.Fatal("arbitrary action accepted")
		}
	}
	args, e := maintenanceCommand("app-update")
	if e != nil || args[len(args)-1] != "app-update" {
		t.Fatal("valid action unavailable")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "maintenance.json"), []byte(`{"status":"failed","action":"app-update","exit_code":1}`), 0600)
	os.WriteFile(filepath.Join(dir, "maintenance.log"), []byte("checksum failed"), 0600)
	status := maintenanceStatus(dir)
	b, _ := json.Marshal(status)
	if status["status"] != "failed" || !strings.Contains(string(b), "checksum failed") {
		t.Fatal("result did not survive app lifetime")
	}
}
