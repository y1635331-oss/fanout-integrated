package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func domainCertificate(t *testing.T, dir, host string, serial int64, expiry time.Time) (string, string) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: expiry, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	kd, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	cert, keyPath := filepath.Join(dir, "domain.pem"), filepath.Join(dir, "domain.key")
	if e = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
		t.Fatal(e)
	}
	return cert, keyPath
}

func TestDomainCertificateValidationAndRenewal(t *testing.T) {
	t.Setenv("FANOUT_TLS_CERT", "")
	t.Setenv("FANOUT_TLS_KEY", "")
	t.Setenv("FANOUT_TLS_DOMAIN", "")
	dir := t.TempDir()
	cert, key := domainCertificate(t, dir, "panel.example.com", 1, time.Now().Add(time.Hour))
	if _, e := checkedCertificate(cert, key, "wrong.example.com"); e == nil {
		t.Fatal("wrong domain accepted")
	}
	b, _ := json.Marshal(map[string]string{"domain": "panel.example.com", "cert": cert, "key": key})
	os.WriteFile(filepath.Join(dir, "tls.json"), b, 0600)
	cfg, _, e := managementTLS(dir)
	if e != nil {
		t.Fatal(e)
	}
	first, e := cfg.GetCertificate(nil)
	if e != nil || first.Leaf.SerialNumber.Int64() != 1 {
		t.Fatal("initial certificate not loaded")
	}
	domainCertificate(t, dir, "panel.example.com", 2, time.Now().Add(time.Hour))
	next, e := cfg.GetCertificate(nil)
	if e != nil || next.Leaf.SerialNumber.Int64() != 2 {
		t.Fatal("renewed certificate not loaded")
	}
	domainCertificate(t, dir, "panel.example.com", 3, time.Now().Add(-time.Minute))
	if _, e = cfg.GetCertificate(nil); e == nil {
		t.Fatal("expired certificate accepted")
	}
}
