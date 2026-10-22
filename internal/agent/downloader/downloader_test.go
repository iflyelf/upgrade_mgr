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

func TestDownloadRecoversFromHTTP416(t *testing.T) {
	payload := []byte("fresh full content")
	var sawRange int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			sawRange++
			// 模拟代理/服务端拒绝续传：本地残留文件比远端大时返回 416。
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Write(payload)
	}))
	defer srv.Close()

	dest := "/tmp/upgrade_test_dl_416.bin"
	defer os.Remove(dest)
	// 预置一个比远端更大的残留文件，触发 Range 续传。
	if err := os.WriteFile(dest, []byte("stale-file-larger-than-remote"), 0644); err != nil {
		t.Fatal(err)
	}

	err := Download(context.Background(), Spec{
		URL: srv.URL, Dest: dest, MaxRetries: 3, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("416 后应清理残留并重新下载: %v", err)
	}
	if sawRange == 0 {
		t.Fatal("测试未覆盖 Range 续传路径")
	}
	got, _ := os.ReadFile(dest)
	if string(got) != string(payload) {
		t.Fatalf("内容不匹配: %q", got)
	}
}

func md5Hash(b []byte) string {
	h := md5.Sum(b)
	return hex.EncodeToString(h[:])
}
