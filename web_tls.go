package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

func managementTLS(dir string) (*tls.Config, string, error) {
	certPath, keyPath := os.Getenv("FANOUT_TLS_CERT"), os.Getenv("FANOUT_TLS_KEY")
	domain := os.Getenv("FANOUT_TLS_DOMAIN")
	if certPath == "" && keyPath == "" {
		var cfg struct {
			Domain string
			Cert   string
			Key    string
		}
		if b, err := os.ReadFile(filepath.Join(dir, "tls.json")); err == nil {
			if err = json.Unmarshal(b, &cfg); err != nil {
				return nil, "", fmt.Errorf("tls.json: %w", err)
			}
			certPath, keyPath, domain = cfg.Cert, cfg.Key, cfg.Domain
			if certPath == "" || keyPath == "" || domain == "" {
				return nil, "", fmt.Errorf("tls.json 需要 domain、cert、key")
			}
		} else if !os.IsNotExist(err) {
			return nil, "", err
		}
	}
	if (certPath == "") != (keyPath == "") {
		return nil, "", fmt.Errorf("FANOUT_TLS_CERT 与 FANOUT_TLS_KEY 必须一起设置")
	}
	if certPath == "" {
		certPath = filepath.Join(dir, "web.crt")
		keyPath = filepath.Join(dir, "web.key")
		if _, e := os.Stat(certPath); os.IsNotExist(e) {
			key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if e != nil {
				return nil, "", e
			}
			serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
			if e != nil {
				return nil, "", e
			}
			template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "fanout integrated"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
			if ip := net.ParseIP(hostPublicIP()); ip != nil {
				template.IPAddresses = append(template.IPAddresses, ip)
			}
			der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
			if e != nil {
				return nil, "", e
			}
			keyDER, e := x509.MarshalPKCS8PrivateKey(key)
			if e != nil {
				return nil, "", e
			}
			if e = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); e != nil {
				return nil, "", e
			}
			if e = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
				return nil, "", e
			}
		}
	}
	pair, e := checkedCertificate(certPath, keyPath, domain)
	if e != nil {
		return nil, "", e
	}
	sum := sha256.Sum256(pair.Certificate[0])
	config := &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	// Custom certificate paths may be symlinks rotated by certbot/acme.sh.
	if domain != "" || os.Getenv("FANOUT_TLS_CERT") != "" {
		config.Certificates = nil
		config.GetCertificate = func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
			next, err := checkedCertificate(certPath, keyPath, domain)
			return &next, err
		}
	}
	return config, hex.EncodeToString(sum[:]), nil
}

func checkedCertificate(certPath, keyPath, domain string) (tls.Certificate, error) {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return pair, err
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return pair, err
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return pair, fmt.Errorf("证书已过期或尚未生效")
	}
	if domain != "" {
		if err = leaf.VerifyHostname(domain); err != nil {
			return pair, fmt.Errorf("证书与域名不匹配: %w", err)
		}
	}
	pair.Leaf = leaf
	return pair, nil
}
