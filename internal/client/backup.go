package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"filippo.io/age"
	"filippo.io/age/armor"
)

// ExportKeys produces an age-encrypted, armored backup of the full device
// config (which carries the private signing/encryption keys and the epoch-0
// Stream Key). The backup is sealed with a passphrase via age's scrypt
// recipient, so the private material never appears in cleartext on disk and is
// never held by the sync server. The resulting bytes are PEM-like ASCII safe to
// write to a ".age" file.
func ExportKeys(cfg Config, passphrase string) ([]byte, error) {
	if passphrase == "" {
		return nil, errors.New("passphrase must not be empty")
	}
	plaintext, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}

	var buf bytes.Buffer
	arm := armor.NewWriter(&buf)
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, fmt.Errorf("age recipient: %w", err)
	}
	w, err := age.Encrypt(arm, recipient)
	if err != nil {
		return nil, fmt.Errorf("age encrypt: %w", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, fmt.Errorf("age write: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("age close: %w", err)
	}
	if err := arm.Close(); err != nil {
		return nil, fmt.Errorf("armor close: %w", err)
	}
	return buf.Bytes(), nil
}

// ImportKeys reverses ExportKeys: it decrypts an armored age backup with the
// passphrase and reconstructs the device Config. An empty passphrase or a
// backup that does not decrypt is rejected. The recovered Config must still
// satisfy ClientKeys/TrustAnchor (corrupt-but-decryptable blobs fail there).
func ImportKeys(armored []byte, passphrase string) (Config, error) {
	if passphrase == "" {
		return Config{}, errors.New("passphrase must not be empty")
	}
	arm := armor.NewReader(bytes.NewReader(armored))
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return Config{}, fmt.Errorf("age identity: %w", err)
	}
	r, err := age.Decrypt(arm, identity)
	if err != nil {
		return Config{}, fmt.Errorf("age decrypt (wrong passphrase or corrupt backup): %w", err)
	}
	var plaintext bytes.Buffer
	if _, err := plaintext.ReadFrom(r); err != nil {
		return Config{}, fmt.Errorf("age read: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(plaintext.Bytes(), &cfg); err != nil {
		return Config{}, fmt.Errorf("unmarshal config: %w", err)
	}
	return cfg, nil
}
