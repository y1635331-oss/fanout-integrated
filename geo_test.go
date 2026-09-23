package main

import (
	"testing"
	"time"
)

func TestGeoDoesNotInventResidentialStatus(t *testing.T) {
	q := parseGeo([]byte(`{"success":true,"ip":"8.8.8.8","type":"IPv4","country_code":"us","connection":{"isp":"Google"},"security":{"hosting":false}}`), "8.8.8.8")
	if q.Country != "US" || q.ISP != "Google" || q.Type != "unknown" || q.GeoError != "" {
		t.Fatalf("unexpected classification: %+v", q)
	}
	for _, body := range []string{`{"success":true,"ip":"1.1.1.1","country_code":"US"}`, `{"success":false,"ip":"8.8.8.8"}`, `not json`, `{"success":true,"ip":"8.8.8.8","country_code":"XX-invalid"}`} {
		q = parseGeo([]byte(body), "8.8.8.8")
		if q.GeoError == "" || q.Type != "unknown" || q.Country != "" {
			t.Fatalf("bad response accepted: %+v", q)
		}
	}
}

func TestExportUsesDetectedCountry(t *testing.T) {
	ts := []*Tunnel{{Status: "up", ExitIP: "8.8.8.8", Node: Node{CountryCode: "JP"}, Quality: IPQuality{Country: "US", Type: "unknown"}}}
	if len(exportRows(ts, "203.0.113.10", "JP", 0, false)) != 0 {
		t.Fatal("source label overrode detected country")
	}
	rows := exportRows(ts, "203.0.113.10", "US", 0, false)
	if len(rows) != 1 || rows[0].Country != "US" {
		t.Fatal("detected country missing")
	}
	if len(exportRows(ts, "203.0.113.10", "US", 0, true)) != 0 {
		t.Fatal("geolocation bypassed strict residential filter")
	}
}

func TestGeoCacheAndDisable(t *testing.T) {
	p, e := NewPoolStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	p.geoCache = map[string]IPQuality{"8.8.8.8": {Type: "unknown", Country: "US", CheckedAt: time.Now()}}
	if p.CheckIP("8.8.8.8").Country != "US" {
		t.Fatal("cache ignored")
	}
	c := p.Config()
	c.GeoLookup = false
	if e = p.SetConfig(c, 20); e != nil {
		t.Fatal(e)
	}
	if p.CheckIP("8.8.8.8").Country != "" {
		t.Fatal("disabled geolocation reused cache")
	}
}
