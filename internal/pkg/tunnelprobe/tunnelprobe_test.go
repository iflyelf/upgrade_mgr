package tunnelprobe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbeHTTPHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","service":"upgrade_mgr"}`))
	}))
	defer srv.Close()

	res := Probe(context.Background(), srv.URL, 3*time.Second)
	if !res.OK {
		t.Fatalf("应探测成功: %+v", res)
	}
	if res.Service != "upgrade_mgr" {
		t.Fatalf("service=%q", res.Service)
	}
	if len(res.Stages) < 3 {
		t.Fatalf("应包含 DNS/TCP/HTTP 三个阶段: %+v", res.Stages)
	}
	for _, st := range res.Stages {
		if !st.OK {
			t.Fatalf("阶段失败: %+v", st)
		}
	}
}

func TestProbeSubpath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mgr/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"service":"upgrade_mgr"}`))
	}))
	defer srv.Close()

	res := Probe(context.Background(), srv.URL+"/mgr", 3*time.Second)
	if !res.OK {
		t.Fatalf("子路径应探测成功: %+v", res)
	}
}

func TestProbeDetectsWrongService(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	res := Probe(context.Background(), srv.URL, 3*time.Second)
	if res.OK {
		t.Fatal("非本平台响应不应判定成功")
	}
	if res.Hint == "" {
		t.Fatal("失败时应给出排查提示")
	}
}

func TestProbeTCPFailure(t *testing.T) {
	// 127.0.0.1 上一个几乎不可能监听的端口。
	res := Probe(context.Background(), "http://127.0.0.1:1", 500*time.Millisecond)
	if res.OK {
		t.Fatal("端口不通应失败")
	}
	found := false
	for _, st := range res.Stages {
		if strings.HasPrefix(st.Name, "TCP") && !st.OK {
			found = true
		}
	}
	if !found {
		t.Fatalf("应记录 TCP 失败阶段: %+v", res.Stages)
	}
}

func TestProbeRejectsBadScheme(t *testing.T) {
	res := Probe(context.Background(), "ftp://example.com", time.Second)
	if res.OK || len(res.Stages) == 0 {
		t.Fatalf("非 http(s) 应失败: %+v", res)
	}
}
