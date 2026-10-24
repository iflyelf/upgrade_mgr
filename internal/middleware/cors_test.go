package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSAllowedActualRequest(t *testing.T) {
	h := CORS(func() []string { return []string{"https://ops.example.com"} })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	req := httptest.NewRequest(http.MethodGet, "http://server/api/hosts", nil)
	req.Header.Set("Origin", "https://ops.example.com")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || rr.Header().Get("Access-Control-Allow-Origin") != "https://ops.example.com" {
		t.Fatalf("code=%d headers=%v", rr.Code, rr.Header())
	}
	if rr.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatal("凭证跨域必须显式允许 credentials")
	}
}

func TestCORSPreflight(t *testing.T) {
	h := CORS(func() []string { return []string{"https://ops.example.com"} })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("预检不应进入业务处理器") }))
	req := httptest.NewRequest(http.MethodOptions, "http://server/api/hosts", nil)
	req.Header.Set("Origin", "https://ops.example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("code=%d", rr.Code)
	}
}

func TestCORSRejectedPreflight(t *testing.T) {
	h := CORS(func() []string { return []string{"https://ops.example.com"} })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("拒绝的预检不应进入业务处理器") }))
	req := httptest.NewRequest(http.MethodOptions, "http://server/api/hosts", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("code=%d", rr.Code)
	}
}
