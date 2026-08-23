package client

import (
	"bytes"
	"strings"
	"testing"
)

func TestExportImportRoundTripsConfig(t *testing.T) {
	cfg, err := InitOwner("http://localhost:8333", nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Name = "main"

	armored, err := ExportKeys(cfg, "correct horse battery staple")
	if err != nil {
		t.Fatalf("ExportKeys: %v", err)
	}
	// The backup must be armored ASCII and must NOT contain the plaintext
	// private key material (base64 of the 64-byte ed25519 signing key, etc.).
	if !strings.HasPrefix(string(armored), "-----BEGIN AGE ENCRYPTED FILE-----") {
		t.Fatalf("backup not armored: %q", string(armored)[:40])
	}
	if bytes.Contains(armored, []byte(cfg.SigningKey)) {
		t.Fatal("armored backup leaks the plaintext signing key")
	}

	got, err := ImportKeys(armored, "correct horse battery staple")
	if err != nil {
		t.Fatalf("ImportKeys: %v", err)
	}
	if got.SigningKey != cfg.SigningKey || got.StreamKey != cfg.StreamKey {
		t.Fatal("imported config does not match exported config")
	}
	if got.Name != "main" || got.ServerEndpoint != cfg.ServerEndpoint {
		t.Fatal("imported metadata does not match")
	}
	// The restored config must still produce usable client keys / anchor.
	if _, err := got.ClientKeys(); err != nil {
		t.Fatalf("restored ClientKeys: %v", err)
	}
	if _, err := got.TrustAnchor(); err != nil {
		t.Fatalf("restored TrustAnchor: %v", err)
	}
}

func TestImportRejectsWrongPassphrase(t *testing.T) {
	cfg, err := InitOwner("http://localhost:8333", nil)
	if err != nil {
		t.Fatal(err)
	}
	armored, err := ExportKeys(cfg, "right-pass")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportKeys(armored, "wrong-pass"); err == nil {
		t.Fatal("expected import to fail with wrong passphrase")
	}
}

func TestExportRejectsEmptyPassphrase(t *testing.T) {
	cfg, _ := InitOwner("http://localhost:8333", nil)
	if _, err := ExportKeys(cfg, ""); err == nil {
		t.Fatal("expected error for empty passphrase")
	}
	if _, err := ImportKeys([]byte("x"), ""); err == nil {
		t.Fatal("expected error for empty passphrase on import")
	}
}
