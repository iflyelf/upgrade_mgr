package consul

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func newProbeServer(t *testing.T, agentStatus int, agentDC string, dcList string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		switch r.URL.Path {
		case "/v1/agent/self":
			if agentStatus != http.StatusOK {
				w.WriteHeader(agentStatus)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Config":{"Datacenter":"` + agentDC + `"}}`))
		case "/v1/catalog/datacenters":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(dcList))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestNewAutoDetectDatacenterFromAgent(t *testing.T) {
	srv, _ := newProbeServer(t, http.StatusOK, "dc-agent", `["dc-agent","dc-other"]`)

	c, err := New(Config{Address: srv.URL, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := c.Datacenter(); got != "dc-agent" {
		t.Fatalf("Datacenter() = %q, want dc-agent", got)
	}
}

func TestNewAutoDetectDatacenterFromCatalogFallback(t *testing.T) {
	// Agent API 不可达时，单数据中心可确定本地 DC。
	srv, _ := newProbeServer(t, http.StatusInternalServerError, "", `["dc-solo"]`)

	c, err := New(Config{Address: srv.URL, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := c.Datacenter(); got != "dc-solo" {
		t.Fatalf("Datacenter() = %q, want dc-solo", got)
	}
}

func TestNewAutoDetectDatacenterAmbiguous(t *testing.T) {
	// 多数据中心且 Agent API 不可达时无法判断本地 DC，应保持空值。
	srv, _ := newProbeServer(t, http.StatusInternalServerError, "", `["dc-a","dc-b"]`)

	c, err := New(Config{Address: srv.URL, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := c.Datacenter(); got != "" {
		t.Fatalf("Datacenter() = %q, want empty", got)
	}
}

func TestNewExplicitDatacenterSkipsProbe(t *testing.T) {
	srv, calls := newProbeServer(t, http.StatusOK, "dc-agent", `["dc-agent"]`)

	c, err := New(Config{Address: srv.URL, Datacenter: "explicit", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := c.Datacenter(); got != "explicit" {
		t.Fatalf("Datacenter() = %q, want explicit", got)
	}
	if n := atomic.LoadInt32(calls); n != 0 {
		t.Fatalf("显式配置时不应发起探测请求，实际 %d 次", n)
	}
}

func TestNewRequiresAddress(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("空地址应返回错误")
	}
}
