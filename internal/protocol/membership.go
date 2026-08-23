package protocol

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"errors"
)

// MemberAddPayload is the canonical CBOR operation_payload for member_add. Only
// the stream owner may issue it; the server authorises the device's signing key
// for future writes.
type MemberAddPayload struct {
	DeviceID            []byte `cbor:"device_id"`
	Label               string `cbor:"label"`
	SigningPublicKey    []byte `cbor:"signing_public_key"`
	EncryptionPublicKey []byte `cbor:"encryption_public_key"`
}

// KeyGrantPayload is the canonical CBOR operation_payload for key_grant. The
// key_envelope is the output of EncryptKeyEnvelope wrapping the epoch's Stream
// Key to the recipient's encryption public key.
type KeyGrantPayload struct {
	RecipientDeviceID            []byte `cbor:"recipient_device_id"`
	RecipientEncryptionPublicKey []byte `cbor:"recipient_encryption_public_key"`
	KeyEpoch                     uint64 `cbor:"key_epoch"`
	KeyEnvelope                  []byte `cbor:"key_envelope"`
}

// KeyRotationGrant has the same shape as KeyGrantPayload and is one entry of a
// key_rotation_bundle.grants list.
type KeyRotationGrant struct {
	RecipientDeviceID            []byte `cbor:"recipient_device_id"`
	RecipientEncryptionPublicKey []byte `cbor:"recipient_encryption_public_key"`
	KeyEpoch                     uint64 `cbor:"key_epoch"`
	KeyEnvelope                  []byte `cbor:"key_envelope"`
}

// KeyRotationBundlePayload is the canonical CBOR operation_payload for
// key_rotation_bundle. It is the single atomic operation that revokes a signing
// key, advances the key epoch, and re-grants the new Stream Key to the surviving
// devices. There is intentionally no separate "revoke" or "rotate" wire op.
type KeyRotationBundlePayload struct {
	RevokedSigningPublicKey []byte             `cbor:"revoked_signing_public_key"`
	NewKeyEpoch             uint64             `cbor:"new_key_epoch"`
	Grants                  []KeyRotationGrant `cbor:"grants"`
}

// JoinRequest is an out-of-band request a prospective device sends to the owner.
// The joining device signs it with the private key matching SigningPublicKey; the
// owner verifies both the signature and the implicit stream/owner binding before
// issuing member_add + key_grant.
type JoinRequest struct {
	StreamID              []byte `cbor:"stream_id"`
	DeviceID              []byte `cbor:"device_id"`
	Label                 string `cbor:"label"`
	SigningPublicKey      []byte `cbor:"signing_public_key"`
	EncryptionPublicKey   []byte `cbor:"encryption_public_key"`
	OwnerSigningPublicKey []byte `cbor:"owner_signing_public_key"`
	ClientCreatedAt       uint64 `cbor:"client_created_at"`
}

// JoinAnchor is the out-of-band trust material a joining client must already
// hold. It is distributed by the owner, never trusted from the server response.
type JoinAnchor struct {
	StreamID              []byte
	GenesisBlockHash      []byte
	OwnerSigningPublicKey []byte
}

// SignJoinRequest returns the Ed25519 signature over the canonical CBOR of req.
func SignJoinRequest(req JoinRequest, signingKey ed25519.PrivateKey) ([]byte, error) {
	if len(signingKey) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid Ed25519 signing key")
	}
	encoded, err := CanonicalMarshal(req)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(signingKey, encoded), nil
}

// VerifyJoinRequest checks the signature and the implicit stream/owner binding.
// It returns an error if the signature is invalid or req does not match the
// out-of-band anchor (wrong stream or wrong owner).
func VerifyJoinRequest(req JoinRequest, signature []byte, anchor JoinAnchor) error {
	if len(req.StreamID) != 32 || len(anchor.StreamID) != 32 {
		return errors.New("stream_id must be 32 bytes")
	}
	if !bytes.Equal(req.StreamID, anchor.StreamID) {
		return errors.New("join request stream_id does not match trust anchor")
	}
	if !bytes.Equal(req.OwnerSigningPublicKey, anchor.OwnerSigningPublicKey) {
		return errors.New("join request owner key does not match trust anchor")
	}
	if len(req.SigningPublicKey) != ed25519.PublicKeySize {
		return errors.New("join request signing public key must be 32 bytes")
	}
	encoded, err := CanonicalMarshal(req)
	if err != nil {
		return err
	}
	if !ed25519.Verify(req.SigningPublicKey, encoded, signature) {
		return errors.New("join request signature verification failed")
	}
	return nil
}

// KeyGrantAAD returns the envelope AAD used for every key_envelope (stream-key
// grant). Per the protocol's documented deviation (also used by BuildCreate), the
// transaction_id is a 32-byte zero placeholder because the transaction_id is
// itself derived from the body that contains the envelope — a circular
// dependency. The authoritative binding is the Ed25519 signature over the body.
// Both the sender and the receiver must use this exact AAD to seal/open the grant.
//
// This is an intentional design choice, not a defect: the key_envelope is
// embedded inside OperationPayload, while transaction_id is computed from the
// whole UnsignedBody (which wraps that payload). Filling the real transaction_id
// into the AAD would require the id before the body exists. Integrity therefore
// rests on (a) the Ed25519 signature over the body and (b) the remaining AAD
// fields (StreamID, KeyEpoch, Field, NoteID) which already bind the envelope to
// the correct stream/epoch/field. Tampering with any AAD field is rejected by
// the AEAD (see TestKeyEnvelopeRejectsAADAndRecipientTampering).
func KeyGrantAAD(streamID []byte, keyEpoch uint64) EnvelopeAAD {
	return EnvelopeAAD{
		ProtocolVersion: protocolVersion,
		StreamID:        append([]byte(nil), streamID...),
		NoteID:          make([]byte, 32),
		TransactionID:   make([]byte, 32),
		KeyEpoch:        keyEpoch,
		Field:           "key_envelope",
	}
}

// KeyStore lets a device recover and hold the Stream Keys for the key epochs it
// has legitimately been granted. It processes on-chain transactions and decrypts
// only the key_envelope items addressed to this device. A revoked device keeps
// the historical keys it already held; it simply stops receiving new grants.
type KeyStore struct {
	streamID   []byte
	deviceID   []byte
	encPriv    *ecdh.PrivateKey
	streamKeys map[uint64][]byte
}

// NewKeyStore creates an empty key store for one device. Seed the owner's epoch-0
// key with SeedEpoch so the owner can read and write from genesis.
func NewKeyStore(streamID, deviceID []byte, encPriv *ecdh.PrivateKey) *KeyStore {
	return &KeyStore{
		streamID:   append([]byte(nil), streamID...),
		deviceID:   append([]byte(nil), deviceID...),
		encPriv:    encPriv,
		streamKeys: make(map[uint64][]byte),
	}
}

// SeedEpoch records a known Stream Key for an epoch (used by the owner, who
// receives the epoch-0 key directly from BuildGenesis rather than via a grant).
func (k *KeyStore) SeedEpoch(epoch uint64, streamKey []byte) {
	if len(streamKey) != 32 {
		return
	}
	key := append([]byte(nil), streamKey...)
	k.streamKeys[epoch] = key
}

// ProcessTransaction inspects a transaction's operation and, for key_grant and
// key_rotation_bundle operations, decrypts any envelope addressed to this device
// and stores the recovered Stream Key under its epoch.
func (k *KeyStore) ProcessTransaction(body UnsignedBody) error {
	switch body.OperationType {
	case "key_grant":
		var p KeyGrantPayload
		if _, err := StrictDecode(body.OperationPayload, &p); err != nil {
			return err
		}
		if !bytes.Equal(p.RecipientDeviceID, k.deviceID) {
			return nil
		}
		key, err := DecryptKeyEnvelope(k.encPriv, KeyGrantAAD(k.streamID, p.KeyEpoch), p.KeyEnvelope)
		if err != nil {
			return err
		}
		k.streamKeys[p.KeyEpoch] = append([]byte(nil), key...)
	case "key_rotation_bundle":
		var p KeyRotationBundlePayload
		if _, err := StrictDecode(body.OperationPayload, &p); err != nil {
			return err
		}
		for _, g := range p.Grants {
			if !bytes.Equal(g.RecipientDeviceID, k.deviceID) {
				continue
			}
			key, err := DecryptKeyEnvelope(k.encPriv, KeyGrantAAD(k.streamID, g.KeyEpoch), g.KeyEnvelope)
			if err != nil {
				return err
			}
			k.streamKeys[g.KeyEpoch] = append([]byte(nil), key...)
		}
	}
	return nil
}

// StreamKey returns the Stream Key for epoch, if this device holds it.
func (k *KeyStore) StreamKey(epoch uint64) ([]byte, bool) {
	key, ok := k.streamKeys[epoch]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), key...), true
}

// HasEpoch reports whether this device holds the Stream Key for epoch.
func (k *KeyStore) HasEpoch(epoch uint64) bool {
	_, ok := k.streamKeys[epoch]
	return ok
}

// LatestEpoch returns the highest key epoch this device holds. It returns false
// if the store is empty (the device has no usable Stream Key yet).
func (k *KeyStore) LatestEpoch() (uint64, bool) {
	var latest uint64
	ok := false
	for epoch := range k.streamKeys {
		if !ok || epoch > latest {
			latest = epoch
			ok = true
		}
	}
	return latest, ok
}

// DecryptWithStreamKey is a convenience wrapper that opens a stream-key-sealed
// payload (e.g. a note's wrapped_dek) using the Stream Key for epoch.
func (k *KeyStore) DecryptWithStreamKey(epoch uint64, aad EnvelopeAAD, nonce, ciphertext []byte) ([]byte, error) {
	key, ok := k.StreamKey(epoch)
	if !ok {
		return nil, errors.New("device does not hold the Stream Key for the requested epoch")
	}
	return OpenWithStreamKey(key, aad, nonce, ciphertext)
}
