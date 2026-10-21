package agentrun

import "testing"

func TestNewTrimsServerURL(t *testing.T) {
	a := New(Config{ServerURL: "http://10.0.88.88:8090/"})
	if a.cfg.ServerURL != "http://10.0.88.88:8090" {
		t.Fatalf("ServerURL = %q, want %q", a.cfg.ServerURL, "http://10.0.88.88:8090")
	}
}

func TestResolveArtifactURL(t *testing.T) {
	a := New(Config{ServerURL: "http://10.0.88.88:8090/", AgentID: "agent-1", Token: "tok"})

	u, auth := a.resolveArtifactURL("/api/artifacts/1/download")
	if u != "http://10.0.88.88:8090/api/artifacts/1/download" {
		t.Fatalf("平台制品 URL = %q", u)
	}
	if auth != "Bearer agent-1:tok" {
		t.Fatalf("平台制品应携带 Agent 凭据，got %q", auth)
	}

	u, auth = a.resolveArtifactURL("https://repo.example.com/install.sh")
	if u != "https://repo.example.com/install.sh" {
		t.Fatalf("自定义 URL = %q", u)
	}
	if auth != "" {
		t.Fatalf("自定义 script_url 不应携带 Agent 凭据，got %q", auth)
	}
}
