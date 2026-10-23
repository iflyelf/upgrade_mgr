package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestCert(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "upgrade-mgr-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath = filepath.Join(dir, "server.crt")
	keyPath = filepath.Join(dir, "server.key")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600)
	return certPath, keyPath
}

func TestServerTLSConfigDisabled(t *testing.T) {
	cfg, err := ServerConfig{}.ServerTLSConfig()
	if err != nil || cfg != nil {
		t.Fatalf("未配置证书应返回 nil: %v %v", cfg, err)
	}
}

func TestServerTLSConfigRequiresBothFiles(t *testing.T) {
	if _, err := (ServerConfig{TLSCert: "a"}).ServerTLSConfig(); err == nil {
		t.Fatal("只配置 cert 应报错")
	}
}

func TestServerTLSConfigMutualTLS(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTestCert(t, dir)

	cfg, err := ServerConfig{
		TLSCert: certPath, TLSKey: keyPath,
		TLSClientCA: certPath, TLSClientAuth: "require",
	}.ServerTLSConfig()
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	if cfg == nil || cfg.ClientAuth != 0x4 { // tls.RequireAndVerifyClientCert == 4
		t.Fatalf("应要求客户端证书: %+v", cfg)
	}
	if cfg.ClientCAs == nil {
		t.Fatal("应加载客户端 CA")
	}
	if cfg.MinVersion == 0 {
		t.Fatal("应设置最低 TLS 版本")
	}
}

func TestValidateRequiredChecksTLS(t *testing.T) {
	c := Default()
	if err := c.ValidateRequired(); err != nil {
		t.Fatalf("默认配置应通过: %v", err)
	}
	c.Server.TLSCert = "/tmp/x.crt"
	if err := c.ValidateRequired(); err == nil {
		t.Fatal("cert/key 不成对应报错")
	}
	c.Server.TLSKey = "/tmp/x.key"
	c.Server.TLSClientAuth = "require"
	if err := c.ValidateRequired(); err == nil {
		t.Fatal("require 缺少 client CA 应报错")
	}
	c.Server.TLSClientCA = "/tmp/ca.crt"
	if err := c.ValidateRequired(); err != nil {
		t.Fatalf("配置完整应通过: %v", err)
	}
}
