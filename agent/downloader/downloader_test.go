package downloader

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestDownloadGo(t *testing.T) {
	payload := []byte("test content for download")
	wantMD5 := md5Hash(payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(payload)
	}))
	defer srv.Close()

	dest := "/tmp/upgrade_test_dl.bin"
	defer os.Remove(dest)

	err := Download(context.Background(), Spec{
		URL: srv.URL, Dest: dest, MD5: wantMD5, MaxRetries: 2, Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != string(payload) {
		t.Fatalf("content mismatch")
	}
}

func TestDownloadMD5Mismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data"))
	}))
	defer srv.Close()

	dest := "/tmp/upgrade_test_dl_bad.bin"
	defer os.Remove(dest)

	err := Download(context.Background(), Spec{
		URL: srv.URL, Dest: dest, MD5: "wronghash", MaxRetries: 1, Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("expected md5 error")
	}
}

func md5Hash(b []byte) string {
	h := md5.Sum(b)
	return hex.EncodeToString(h[:])
}
