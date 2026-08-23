// Package client holds the local device configuration and key/stream bootstrap
// for a SnapNotes stream. It is the missing client-side glue between the tested
// protocol/sync libraries and a runnable product: it persists the owner device's
// private material and the out-of-band trust anchor, and exposes them as the
// sync.ClientKeys / sync.TrustAnchor the TUI and SyncClient consume.
//
// Security note: the config file contains the device signing and encryption
// private keys. It is written with 0600 permissions under the user data
// directory. The server never receives these keys (see threat-model.md); the
// only server-shared artifact is the genesis block (GenesisBlock), which carries
// only public keys.
package client

import (
	"crypto/ed25519"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jiangfire/snapnotes/internal/protocol"
	"github.com/jiangfire/snapnotes/internal/sync"
)

// Config is the persisted state of one device for one stream.
type Config struct {
	Name                string `json:"name"`                  // human label, e.g. "main"
	StreamID            string `json:"stream_id"`             // base64
	GenesisBlockHash    string `json:"genesis_block_hash"`    // base64
	OwnerSigningPublicKey string `json:"owner_signing_public_key"` // base64 (32 bytes)
	ServerEndpoint      string `json:"server_endpoint"`
	DeviceID            string `json:"device_id"`

	// Private material — owner device keeps these; never sent to the server.
	SigningKey   string `json:"signing_key"`   // base64 ed25519 private key (64 bytes)
	EncryptionKey string `json:"encryption_key"` // base64 X25519 private key (32 bytes)
	StreamKey    string `json:"stream_key"`    // base64 epoch-0 Stream Key (32 bytes)
	GenesisBlock string `json:"genesis_block"` // base64 canonical CBOR genesis block (for the server)
	PowTarget    string `json:"pow_target"`    // base64 32-byte PoW target
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func unb64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// InitOwner creates a brand-new stream as its owner device: it generates the
// genesis block and the owner's signing/encryption keys, wraps the epoch-0
// Stream Key, and returns a Config holding every private field the device needs.
// The genesis block (GenesisBlock) is the only artifact that must be handed to
// the server (snapnotes-server -genesis <GenesisBlock>).
func InitOwner(serverEndpoint string, randomness io.Reader) (Config, error) {
	if randomness == nil {
		randomness = rand.Reader
	}
	gen, err := protocol.BuildGenesis(randomness)
	if err != nil {
		return Config{}, err
	}
	blockCBOR, err := protocol.MarshalBlock(gen.Block)
	if err != nil {
		return Config{}, err
	}
	encPriv := gen.OwnerEncryptionKey.Bytes()
	var devID [8]byte
	if _, err := io.ReadFull(randomness, devID[:]); err != nil {
		return Config{}, err
	}
	return Config{
		Name:                  "main",
		StreamID:              b64(gen.StreamID),
		GenesisBlockHash:      b64(gen.GenesisBlockHash),
		OwnerSigningPublicKey: b64(gen.OwnerSigningPublicKey),
		ServerEndpoint:        serverEndpoint,
		DeviceID:              hex.EncodeToString(devID[:]),
		SigningKey:            b64(gen.OwnerSigningKey),
		EncryptionKey:         b64(encPriv),
		StreamKey:             b64(gen.StreamKey),
		GenesisBlock:          b64(blockCBOR),
		PowTarget:             b64(gen.PowTarget),
	}, nil
}

// Save writes the config with 0600 permissions so only the owning user can read
// the private keys. Parent directories are created as needed.
func (c Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Load reads a config written by Save. A missing file is reported as
// os.ErrNotExist so callers can distinguish "first run" from corruption.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// ClientKeys reconstructs the sync.ClientKeys the NoteService needs to build and
// sign create transactions for this device (epoch 0, PoW epoch 0).
func (c Config) ClientKeys() (sync.ClientKeys, error) {
	signing, err := unb64(c.SigningKey)
	if err != nil {
		return sync.ClientKeys{}, fmt.Errorf("signing_key: %w", err)
	}
	streamKey, err := unb64(c.StreamKey)
	if err != nil {
		return sync.ClientKeys{}, fmt.Errorf("stream_key: %w", err)
	}
	target, err := unb64(c.PowTarget)
	if err != nil {
		return sync.ClientKeys{}, fmt.Errorf("pow_target: %w", err)
	}
	streamID, err := unb64(c.StreamID)
	if err != nil {
		return sync.ClientKeys{}, fmt.Errorf("stream_id: %w", err)
	}
	if len(streamID) != 32 {
		return sync.ClientKeys{}, errors.New("stream_id must be 32 bytes")
	}
	if len(streamKey) != 32 {
		return sync.ClientKeys{}, errors.New("stream_key must be 32 bytes")
	}
	if len(target) != 32 {
		return sync.ClientKeys{}, errors.New("pow_target must be 32 bytes")
	}
	priv := ed25519.PrivateKey(signing)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return sync.ClientKeys{}, errors.New("invalid signing key")
	}
	return sync.ClientKeys{
		StreamID:        streamID,
		StreamKey:       streamKey,
		KeyEpoch:        0,
		AuthorPublicKey: pub,
		SigningKey:      priv,
		PowTarget:       target,
		PowEpoch:        0,
	}, nil
}

// TrustAnchor reconstructs the out-of-band bootstrap the SyncClient uses to
// verify the chain. It is never trusted from the server response.
func (c Config) TrustAnchor() (sync.TrustAnchor, error) {
	streamID, err := unb64(c.StreamID)
	if err != nil {
		return sync.TrustAnchor{}, fmt.Errorf("stream_id: %w", err)
	}
	genesisHash, err := unb64(c.GenesisBlockHash)
	if err != nil {
		return sync.TrustAnchor{}, fmt.Errorf("genesis_block_hash: %w", err)
	}
	owner, err := unb64(c.OwnerSigningPublicKey)
	if err != nil {
		return sync.TrustAnchor{}, fmt.Errorf("owner_signing_public_key: %w", err)
	}
	return sync.TrustAnchor{
		StreamID:         streamID,
		GenesisBlockHash: genesisHash,
		OwnerPublicKey:   owner,
	}, nil
}

// EncryptionKey returns the device X25519 encryption private key (used to decrypt
// future key grants). It is not needed for owner epoch-0 writes.
func (c Config) X25519EncryptionKey() (*ecdh.PrivateKey, error) {
	raw, err := unb64(c.EncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("encryption_key: %w", err)
	}
	return ecdh.X25519().NewPrivateKey(raw)
}
