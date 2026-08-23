package protocol

import (
	"bytes"
	"crypto/ecdh"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"crypto/hkdf"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	envelopeNonceSize = 24
	keyEnvelopeInfo   = "snapnotes/key-envelope/v1"
)

type EnvelopeAAD struct {
	ProtocolVersion uint64 `cbor:"protocol_version"`
	StreamID        []byte `cbor:"stream_id"`
	NoteID          []byte `cbor:"note_id"`
	TransactionID   []byte `cbor:"transaction_id"`
	KeyEpoch        uint64 `cbor:"key_epoch"`
	Field           string `cbor:"field"`
}

type keyEnvelope struct {
	EphemeralPublicKey []byte `cbor:"ephemeral_public_key"`
	Nonce              []byte `cbor:"nonce"`
	Ciphertext         []byte `cbor:"ciphertext"`
}

func EncryptKeyEnvelope(recipient *ecdh.PublicKey, aad EnvelopeAAD, plaintext []byte, randomness io.Reader) ([]byte, error) {
	if recipient == nil {
		return nil, errors.New("recipient public key is required")
	}
	if err := validateEnvelopeAAD(aad); err != nil {
		return nil, err
	}
	if randomness == nil {
		return nil, errors.New("randomness reader is required")
	}
	ephemeral, err := ecdh.X25519().GenerateKey(randomness)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, envelopeNonceSize)
	if _, err := io.ReadFull(randomness, nonce); err != nil {
		return nil, err
	}
	return encryptKeyEnvelope(recipient, aad, plaintext, ephemeral, nonce)
}

func encryptKeyEnvelope(recipient *ecdh.PublicKey, aad EnvelopeAAD, plaintext []byte, ephemeral *ecdh.PrivateKey, nonce []byte) ([]byte, error) {
	shared, err := ephemeral.ECDH(recipient)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Key(sha256.New, shared, make([]byte, sha256.Size), keyEnvelopeInfo, chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	aadBytes, err := canonicalEncoder.Marshal(aad)
	if err != nil {
		return nil, err
	}
	if len(nonce) != envelopeNonceSize {
		return nil, errors.New("nonce must be 24 bytes")
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, aadBytes)
	return canonicalEncoder.Marshal(keyEnvelope{ephemeral.PublicKey().Bytes(), nonce, ciphertext})
}

func DecryptKeyEnvelope(recipient *ecdh.PrivateKey, aad EnvelopeAAD, encoded []byte) ([]byte, error) {
	if recipient == nil {
		return nil, errors.New("recipient private key is required")
	}
	if err := validateEnvelopeAAD(aad); err != nil {
		return nil, err
	}
	var envelope keyEnvelope
	rest, err := strictDecoder.UnmarshalFirst(encoded, &envelope)
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, errors.New("trailing CBOR data")
	}
	canonical, err := canonicalEncoder.Marshal(envelope)
	if err != nil || !bytes.Equal(canonical, encoded) {
		return nil, errors.New("envelope is not canonical CBOR")
	}
	if len(envelope.EphemeralPublicKey) != 32 || len(envelope.Nonce) != envelopeNonceSize || len(envelope.Ciphertext) < chacha20poly1305.Overhead {
		return nil, errors.New("invalid envelope field length")
	}
	ephemeral, err := ecdh.X25519().NewPublicKey(envelope.EphemeralPublicKey)
	if err != nil {
		return nil, err
	}
	shared, err := recipient.ECDH(ephemeral)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Key(sha256.New, shared, make([]byte, sha256.Size), keyEnvelopeInfo, chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	aadBytes, err := canonicalEncoder.Marshal(aad)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, envelope.Nonce, envelope.Ciphertext, aadBytes)
	if err != nil {
		return nil, fmt.Errorf("decrypt key envelope: %w", err)
	}
	return plaintext, nil
}

func validateEnvelopeAAD(aad EnvelopeAAD) error {
	if aad.ProtocolVersion != protocolVersion {
		return errors.New("unsupported protocol version")
	}
	if len(aad.StreamID) != 32 || len(aad.NoteID) != 32 || len(aad.TransactionID) != 32 {
		return errors.New("envelope AAD IDs must be 32 bytes")
	}
	if aad.Field != "encrypted_payload" && aad.Field != "wrapped_dek" && aad.Field != "key_envelope" {
		return errors.New("invalid envelope AAD field")
	}
	return nil
}
