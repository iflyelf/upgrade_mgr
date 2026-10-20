package agentrun

import "testing"

func TestNewTrimsServerURL(t *testing.T) {
	a := New(Config{ServerURL: "http://10.0.88.88:8090/"})
	if a.cfg.ServerURL != "http://10.0.88.88:8090" {
		t.Fatalf("ServerURL = %q, want %q", a.cfg.ServerURL, "http://10.0.88.88:8090")
	}
}
