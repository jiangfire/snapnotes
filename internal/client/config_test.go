package client

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jiangfire/snapnotes/internal/sync"
)

func TestInitOwnerProducesValidBootstrap(t *testing.T) {
	cfg, err := InitOwner("http://localhost:8333", nil)
	if err != nil {
		t.Fatalf("InitOwner: %v", err)
	}

	streamID, err := decodeHexOrB64(cfg.StreamID)
	if err != nil || len(streamID) != 32 {
		t.Fatalf("stream_id not 32 bytes: len=%d err=%v", len(streamID), err)
	}
	if cfg.GenesisBlockHash == "" {
		t.Fatal("genesis_block_hash empty")
	}
	if cfg.GenesisBlock == "" {
		t.Fatal("genesis_block empty (server needs it)")
	}
	if cfg.DeviceID == "" {
		t.Fatal("device_id empty")
	}

	keys, err := cfg.ClientKeys()
	if err != nil {
		t.Fatalf("ClientKeys: %v", err)
	}
	if len(keys.StreamID) != 32 || len(keys.StreamKey) != 32 || len(keys.PowTarget) != 32 {
		t.Fatal("ClientKeys has wrong-length byte fields")
	}
	if keys.KeyEpoch != 0 || keys.PowEpoch != 0 {
		t.Fatalf("owner starts at epoch 0, got key=%d pow=%d", keys.KeyEpoch, keys.PowEpoch)
	}
	// The reconstructed author public key must match the private key.
	pub, ok := keys.SigningKey.Public().(ed25519.PublicKey)
	if !ok || string(pub) != string(keys.AuthorPublicKey) {
		t.Fatal("AuthorPublicKey does not match SigningKey")
	}

	anchor, err := cfg.TrustAnchor()
	if err != nil {
		t.Fatalf("TrustAnchor: %v", err)
	}
	if len(anchor.StreamID) != 32 || len(anchor.GenesisBlockHash) != 32 || len(anchor.OwnerPublicKey) != 32 {
		t.Fatal("TrustAnchor has wrong-length byte fields")
	}

	if _, err := cfg.X25519EncryptionKey(); err != nil {
		t.Fatalf("X25519EncryptionKey: %v", err)
	}
}

func TestConfigSaveLoadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.json")

	cfg, err := InitOwner("http://example.invalid", nil)
	if err != nil {
		t.Fatalf("InitOwner: %v", err)
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// On Windows the OS does not expose Unix permission bits, so the 0600 write
	// cannot be asserted there. The write intent is still correct for the
	// Linux/macOS deployment targets where private keys must stay user-only.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("config file perms = %v, want 0600", info.Mode().Perm())
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.StreamID != cfg.StreamID || loaded.SigningKey != cfg.SigningKey ||
		loaded.GenesisBlock != cfg.GenesisBlock || loaded.DeviceID != cfg.DeviceID {
		t.Fatal("loaded config does not match saved config")
	}

	// Rebuilt keys/anchor must be byte-identical after a reload.
	k1, _ := cfg.ClientKeys()
	k2, _ := loaded.ClientKeys()
	if string(k1.StreamID) != string(k2.StreamID) || string(k1.StreamKey) != string(k2.StreamKey) {
		t.Fatal("ClientKeys differ after reload")
	}
}

func TestLoadMissingFileIsNotFoundError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.json")); !os.IsNotExist(err) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

// decodeHexOrB64 is a tiny helper because Config stores base64; it mirrors the
// package's internal decode for assertions only.
func decodeHexOrB64(s string) ([]byte, error) {
	return unb64(s)
}

var _ = sync.ClientKeys{} // keep sync import referenced if tests change
