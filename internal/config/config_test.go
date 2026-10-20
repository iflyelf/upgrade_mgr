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
