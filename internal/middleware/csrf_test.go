package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCSRFGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	allowed := []string{"https://ops.example.com"}
	h := CSRFGuard(func() []string { return allowed })(ok)

	cases := []struct {
		name   string
		method string
		origin string
		ref    string
		want   int
	}{
		{"GET 无来源放行", http.MethodGet, "", "", http.StatusOK},
		{"POST 非浏览器客户端放行", http.MethodPost, "", "", http.StatusOK},
		{"POST 同源放行", http.MethodPost, "http://upgrade.local:8090", "", http.StatusOK},
		{"POST 同主机不同端口放行（反代改 Host）", http.MethodPost, "https://upgrade.local", "", http.StatusOK},
		{"POST 白名单放行", http.MethodPost, "https://ops.example.com", "", http.StatusOK},
		{"POST 跨站拒绝", http.MethodPost, "https://evil.example.com", "", http.StatusForbidden},
		{"POST null Origin 拒绝", http.MethodPost, "null", "", http.StatusForbidden},
		{"POST 非法 Origin 拒绝", http.MethodPost, "javascript:alert(1)", "", http.StatusForbidden},
		{"POST Referer 同源放行", http.MethodPost, "", "http://upgrade.local:8090/hosts", http.StatusOK},
		{"POST Referer 跨站拒绝", http.MethodPost, "", "https://evil.example.com/hosts", http.StatusForbidden},
		{"PUT 跨站拒绝", http.MethodPut, "https://evil.example.com", "", http.StatusForbidden},
		{"DELETE 跨站拒绝", http.MethodDelete, "https://evil.example.com", "", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://upgrade.local:8090/api/tasks", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.ref != "" {
				req.Header.Set("Referer", tc.ref)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestOriginOf(t *testing.T) {
	if got := originOf("https://Example.com/path?q=1"); got != "https://example.com" {
		t.Fatalf("originOf = %q", got)
	}
	for _, bad := range []string{"", "null", "javascript:alert(1)", "not a url", "file:///etc/passwd"} {
		if got := originOf(bad); got != "" {
			t.Fatalf("originOf(%q) = %q, want empty", bad, got)
		}
	}
}
