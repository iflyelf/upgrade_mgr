package tlsclient

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCert 生成自签证书并写盘，返回证书/私钥路径与证书 PEM。
func writeCert(t *testing.T, dir, name string, isCA bool, parentCert *x509.Certificate, parentKey *ecdsa.PrivateKey) (string, string, *x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  isCA,
		DNSNames:              []string{"127.0.0.1", "localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	signerCert, signerKey := tmpl, key
	if parentCert != nil {
		signerCert, signerKey = parentCert, parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signerCert, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, name+".crt")
	keyPath := filepath.Join(dir, name+".key")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600)
	return certPath, keyPath, cert, key
}

func TestNewDisabled(t *testing.T) {
	c, err := New(Config{})
	if err != nil || c != nil {
		t.Fatalf("未配置时应返回 nil 客户端: %v %v", c, err)
	}
}

func TestNewRejectsHalfClientCert(t *testing.T) {
	if _, err := New(Config{CertFile: "a"}); err == nil {
		t.Fatal("只配置 cert 应报错")
	}
}

func TestNewRejectsBadCA(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.pem")
	os.WriteFile(path, []byte("not a pem"), 0600)
	if _, err := New(Config{CAFile: path}); err == nil {
		t.Fatal("非法 CA 应报错")
	}
}

// TestMTLSHandshake 用自签 CA 建立双向 TLS：服务端要求客户端证书，
// 客户端配置 CA + 客户端证书后应成功；不配置客户端证书应失败。
func TestMTLSHandshake(t *testing.T) {
	dir := t.TempDir()
	caCertPath, caKeyPath, caCert, caKey := writeCert(t, dir, "ca", true, nil, nil)
	_, _ = caCertPath, caKeyPath
	serverCertPath, serverKeyPath, _, _ := writeCert(t, dir, "server", false, caCert, caKey)
	clientCertPath, clientKeyPath, _, _ := writeCert(t, dir, "client", false, caCert, caKey)

	serverCert, err := tls.LoadX509KeyPair(serverCertPath, serverKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"status":"ok","service":"upgrade_mgr"}`))
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}
	srv.StartTLS()
	defer srv.Close()

	// 带客户端证书：成功
	client, err := New(Config{CAFile: caCertPath, CertFile: clientCertPath, KeyFile: clientKeyPath})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("双向 TLS 请求应成功: %v", err)
	}
	resp.Body.Close()

	// 不带客户端证书：服务端拒绝
	plain, err := New(Config{CAFile: caCertPath})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.Get(srv.URL + "/health"); err == nil {
		t.Fatal("未提供客户端证书应被拒绝")
	}
}
