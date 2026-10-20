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
