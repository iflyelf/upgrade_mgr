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

func TestLoadConsulHAEnv(t *testing.T) {
	t.Setenv("CONSUL_ENABLED", "true")
	t.Setenv("CONSUL_LEADER_ELECTION", "true")
	t.Setenv("CONSUL_SYNC_ON_START", "true")
	t.Setenv("CONSUL_SYNC_INTERVAL", "5m")

	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.Consul.Enabled || !c.Consul.LeaderElection || !c.Consul.SyncOnStart {
		t.Fatalf("Consul HA flags not applied: %+v", c.Consul)
	}
	if c.Consul.SyncInterval != "5m" {
		t.Fatalf("SyncInterval = %q, want 5m", c.Consul.SyncInterval)
	}
}
