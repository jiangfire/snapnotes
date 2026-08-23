package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
)

const payloadNonceSize = 24

// SealWithStreamKey encrypts plaintext with the raw 32-byte Stream Key using
// XChaCha20-Poly1305 and the canonical CBOR of aad as the AEAD associated data.
// It returns the ciphertext (including its 16-byte Poly1305 tag). The caller
// transmits nonce alongside the ciphertext; nonces must never be reused.
func SealWithStreamKey(streamKey []byte, aad EnvelopeAAD, plaintext, nonce []byte) ([]byte, error) {
	if len(streamKey) != chacha20poly1305.KeySize {
		return nil, errors.New("stream key must be 32 bytes")
	}
	if err := validateEnvelopeAAD(aad); err != nil {
		return nil, err
	}
	if len(nonce) != payloadNonceSize {
		return nil, errors.New("nonce must be 24 bytes")
	}
	aead, err := chacha20poly1305.NewX(streamKey)
	if err != nil {
		return nil, err
	}
	aadBytes, err := canonicalEncoder.Marshal(aad)
	if err != nil {
		return nil, err
	}
	return aead.Seal(nil, nonce, plaintext, aadBytes), nil
}

// OpenWithStreamKey reverses SealWithStreamKey.
func OpenWithStreamKey(streamKey []byte, aad EnvelopeAAD, nonce, ciphertext []byte) ([]byte, error) {
	if len(streamKey) != chacha20poly1305.KeySize {
		return nil, errors.New("stream key must be 32 bytes")
	}
	if err := validateEnvelopeAAD(aad); err != nil {
		return nil, err
	}
	if len(nonce) != payloadNonceSize {
		return nil, errors.New("nonce must be 24 bytes")
	}
	aead, err := chacha20poly1305.NewX(streamKey)
	if err != nil {
		return nil, err
	}
	aadBytes, err := canonicalEncoder.Marshal(aad)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, aadBytes)
	if err != nil {
		return nil, fmt.Errorf("decrypt with stream key: %w", err)
	}
	return plaintext, nil
}

// SealPayloadRandom generates a random 24-byte nonce and seals plaintext.
func SealPayloadRandom(streamKey []byte, aad EnvelopeAAD, plaintext []byte, randomness io.Reader) (nonce, ciphertext []byte, err error) {
	nonce = make([]byte, payloadNonceSize)
	if _, err = io.ReadFull(randomness, nonce); err != nil {
		return nil, nil, err
	}
	ciphertext, err = SealWithStreamKey(streamKey, aad, plaintext, nonce)
	if err != nil {
		return nil, nil, err
	}
	return nonce, ciphertext, nil
}

// MarshalTransaction returns the canonical CBOR encoding of a full transaction.
func MarshalTransaction(body UnsignedBody, transactionID, signature []byte, powEpoch, powNonce uint64) ([]byte, error) {
	return canonicalEncoder.Marshal(transactionFields{body, transactionID, signature, powEpoch, powNonce})
}

// UnmarshalTransaction decodes the canonical CBOR of a full transaction and
// rejects trailing data or non-canonical input.
func UnmarshalTransaction(data []byte) (UnsignedBody, []byte, []byte, uint64, uint64, error) {
	var fields transactionFields
	rest, err := strictDecoder.UnmarshalFirst(data, &fields)
	if err != nil {
		return UnsignedBody{}, nil, nil, 0, 0, err
	}
	if len(rest) != 0 {
		return UnsignedBody{}, nil, nil, 0, 0, errors.New("trailing CBOR data")
	}
	canonical, err := canonicalEncoder.Marshal(fields)
	if err != nil || !bytes.Equal(canonical, data) {
		return UnsignedBody{}, nil, nil, 0, 0, errors.New("transaction is not canonical CBOR")
	}
	return fields.UnsignedBody, fields.TransactionID, fields.Signature, fields.PowEpoch, fields.PowNonce, nil
}

// CheckPoW reports whether the PoW preimage hashes below the 256-bit target.
// Both hash and target are compared as big-endian unsigned 256-bit integers.
func CheckPoW(preimage, target []byte) bool {
	if len(target) != sha256.Size {
		return false
	}
	hash := sha256.Sum256(preimage)
	return bytes.Compare(hash[:], target) < 0
}

// BigEndianUint64s encodes two uint64 values as a 16-byte big-endian buffer,
// matching the layout used by PowPreimage.
func BigEndianUint64s(values ...uint64) []byte {
	buf := make([]byte, 8*len(values))
	for i, v := range values {
		binary.BigEndian.PutUint64(buf[i*8:], v)
	}
	return buf
}
