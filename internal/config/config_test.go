package config

import "testing"

func TestLoadServerEnvOverrides(t *testing.T) {
	t.Setenv("SERVER_EXTERNAL_URL", "http://10.0.88.88:8090/")
	t.Setenv("TRUST_PROXY", "true")
	t.Setenv("COOKIE_SECURE", "false")

	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Server.ExternalURL != "http://10.0.88.88:8090" {
		t.Fatalf("ExternalURL = %q, want %q", c.Server.ExternalURL, "http://10.0.88.88:8090")
	}
	if !c.Server.TrustProxy {
		t.Fatal("TrustProxy = false, want true")
	}
	if c.Server.CookieSecure != "false" {
		t.Fatalf("CookieSecure = %q, want %q", c.Server.CookieSecure, "false")
	}
}

func TestLoadConsulSyncEnv(t *testing.T) {
	t.Setenv("CONSUL_ENABLED", "true")
	t.Setenv("CONSUL_SYNC_ON_START", "true")
	t.Setenv("CONSUL_SYNC_INTERVAL", "5m")
	t.Setenv("CONSUL_CONCURRENCY", "7")
	t.Setenv("CONSUL_TIMEOUT", "12")

	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.Consul.Enabled || !c.Consul.SyncOnStart {
		t.Fatalf("Consul sync flags not applied: %+v", c.Consul)
	}
	if c.Consul.SyncInterval != "5m" {
		t.Fatalf("SyncInterval = %q, want 5m", c.Consul.SyncInterval)
	}
	if c.Consul.Concurrency != 7 || c.Consul.Timeout != 12 {
		t.Fatalf("Consul 并发/超时未应用: %+v", c.Consul)
	}
}

func TestLoadCORSAndClockSkewEnv(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://OPS.example.com/,http://localhost:5173")
	t.Setenv("AGENT_CLOCK_SKEW", "3m")
	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Server.CORSOrigins) != 2 || c.Server.CORSOrigins[0] != "https://ops.example.com" {
		t.Fatalf("CORSOrigins=%#v", c.Server.CORSOrigins)
	}
	if c.Agent.ClockSkew != "3m" {
		t.Fatalf("ClockSkew=%q", c.Agent.ClockSkew)
	}
}

func TestLoadRejectsInvalidCORS(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "*")
	if _, err := Load(""); err == nil {
		t.Fatal("通配符 CORS Origin 应被拒绝")
	}
}

func TestLoadRejectsInvalidClockSkew(t *testing.T) {
	t.Setenv("AGENT_CLOCK_SKEW", "2h")
	if _, err := Load(""); err == nil {
		t.Fatal("超过 30m 的 clock_skew 应被拒绝")
	}
}

func TestLoadArtifactS3Env(t *testing.T) {
	t.Setenv("ARTIFACT_STORAGE_TYPE", "s3")
	t.Setenv("ARTIFACT_S3_ENDPOINT", "obs.cn-east-3.myhuaweicloud.com")
	t.Setenv("ARTIFACT_S3_BUCKET", "upgrade-mgr")
	t.Setenv("ARTIFACT_S3_ACCESS_KEY", "ak")
	t.Setenv("ARTIFACT_S3_SECRET_KEY", "sk")
	t.Setenv("ARTIFACT_S3_USE_SSL", "true")

	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Artifact.StorageType != "s3" || c.Artifact.S3Bucket != "upgrade-mgr" {
		t.Fatalf("artifact S3 config not applied: %+v", c.Artifact)
	}
	if c.Artifact.S3AccessKey != "ak" || c.Artifact.S3SecretKey != "sk" || !c.Artifact.S3UseSSL {
		t.Fatalf("artifact S3 credentials/ssl not applied: %+v", c.Artifact)
	}
}

// TestLoadExternalProxyEnv 外部文件加速代理可由环境变量覆盖。
func TestLoadExternalProxyEnv(t *testing.T) {
	t.Setenv("EXTERNAL_BINARY_PROXY", "https://proxy.example/proxy?url=")
	t.Setenv("EXTERNAL_BINARY_PROXY_HOSTS", "github.com, raw.example.com")
	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Server.ExternalBinaryProxy != "https://proxy.example/proxy?url=" {
		t.Fatalf("proxy=%q", c.Server.ExternalBinaryProxy)
	}
	if len(c.Server.ExternalBinaryProxyHosts) != 2 ||
		c.Server.ExternalBinaryProxyHosts[1] != "raw.example.com" {
		t.Fatalf("proxy hosts=%v", c.Server.ExternalBinaryProxyHosts)
	}
}
