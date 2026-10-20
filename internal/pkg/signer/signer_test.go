package signer

import (
	"testing"
	"time"
)

func env() Envelope {
	return Envelope{
		Method:    "POST",
		Path:      "/api/agent/commands/cmd1/ack",
		Body:      []byte(`{"status":"accepted"}`),
		Timestamp: time.Now().Unix(),
		Nonce:     "abc123",
	}
}

func TestSignVerify(t *testing.T) {
	secret := "super-secret"
	e := env()
	sig := Sign(secret, e)
	if err := Verify(secret, sig, e, 5*time.Minute); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerifyBadSecret(t *testing.T) {
	e := env()
	sig := Sign("secret-a", e)
	if err := Verify("secret-b", sig, e, 5*time.Minute); err == nil {
		t.Fatal("expected signature mismatch")
	}
}

func TestVerifyTampered(t *testing.T) {
	secret := "s"
	e := env()
	sig := Sign(secret, e)
	e.Body = []byte(`{"status":"tampered"}`)
	if err := Verify(secret, sig, e, 5*time.Minute); err == nil {
		t.Fatal("expected mismatch after tamper")
	}
}

func TestVerifyExpired(t *testing.T) {
	secret := "s"
	e := env()
	e.Timestamp = time.Now().Add(-10 * time.Minute).Unix()
	sig := Sign(secret, e)
	if err := Verify(secret, sig, e, 5*time.Minute); err == nil {
		t.Fatal("expected expiry error")
	}
}

func TestVerifyNoNonce(t *testing.T) {
	secret := "s"
	e := env()
	e.Nonce = ""
	sig := Sign(secret, e)
	if err := Verify(secret, sig, e, 5*time.Minute); err == nil {
		t.Fatal("expected nonce error")
	}
}
