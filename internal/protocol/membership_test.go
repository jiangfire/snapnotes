package protocol

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func mustECDH(t *testing.T) *ecdh.PrivateKey {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// grantBody builds a key_grant UnsignedBody at the protocol level (no PoW or
// signature), wrapping streamKey to recipient at the given epoch.
func grantBody(t *testing.T, streamID, deviceID []byte, recipient *ecdh.PublicKey, keyEpoch uint64, streamKey []byte) UnsignedBody {
	t.Helper()
	envelope, err := EncryptKeyEnvelope(recipient, KeyGrantAAD(streamID, keyEpoch), streamKey, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := CanonicalMarshal(KeyGrantPayload{
		RecipientDeviceID:         deviceID,
		RecipientEncryptionPublicKey: recipient.Bytes(),
		KeyEpoch:                  keyEpoch,
		KeyEnvelope:               envelope,
	})
	if err != nil {
		t.Fatal(err)
	}
	return UnsignedBody{
		ProtocolVersion:  1,
		StreamID:         streamID,
		NoteID:           make([]byte, 32),
		OperationType:    "key_grant",
		OperationPayload: payload,
		ClientCreatedAt:   1,
		AuthorPublicKey:   randBytes(t, 32),
	}
}

func TestJoinRequestRequiresValidAnchorAndSignature(t *testing.T) {
	ownerPub, _, _ := ed25519.GenerateKey(rand.Reader)
	streamID := randBytes(t, 32)
	anchor := JoinAnchor{StreamID: streamID, GenesisBlockHash: randBytes(t, 32), OwnerSigningPublicKey: ownerPub}

	devPub, devPriv, _ := ed25519.GenerateKey(rand.Reader)
	devEnc := mustECDH(t)
	req := JoinRequest{
		StreamID:              streamID,
		DeviceID:              []byte("device-A"),
		Label:                 "laptop",
		SigningPublicKey:      devPub,
		EncryptionPublicKey:   devEnc.PublicKey().Bytes(),
		OwnerSigningPublicKey:  ownerPub,
		ClientCreatedAt:       42,
	}
	sig, err := SignJoinRequest(req, devPriv)
	if err != nil {
		t.Fatal(err)
	}

	// Valid anchor + valid signature.
	if err := VerifyJoinRequest(req, sig, anchor); err != nil {
		t.Fatalf("valid join request rejected: %v", err)
	}

	// Tampered signature.
	badSig := append([]byte(nil), sig...)
	badSig[0] ^= 0xff
	if err := VerifyJoinRequest(req, badSig, anchor); err == nil {
		t.Fatal("tampered signature must be rejected")
	}

	// Wrong stream in anchor.
	wrongStream := JoinAnchor{StreamID: randBytes(t, 32), GenesisBlockHash: anchor.GenesisBlockHash, OwnerSigningPublicKey: ownerPub}
	if err := VerifyJoinRequest(req, sig, wrongStream); err == nil {
		t.Fatal("mismatched stream_id must be rejected")
	}

	// Wrong owner key in anchor.
	wrongOwner := JoinAnchor{StreamID: streamID, GenesisBlockHash: anchor.GenesisBlockHash, OwnerSigningPublicKey: randBytes(t, 32)}
	if err := VerifyJoinRequest(req, sig, wrongOwner); err == nil {
		t.Fatal("mismatched owner key must be rejected")
	}
}

func TestKeyStoreDecryptsOnlyGrantedEpochs(t *testing.T) {
	streamID := randBytes(t, 32)
	streamKey0 := randBytes(t, 32)
	streamKey1 := randBytes(t, 32)

	devAEnc := mustECDH(t)
	devBEnc := mustECDH(t)
	devAID := []byte("device-A")
	devBID := []byte("device-B")

	// Device A is granted epoch 0 and epoch 1; device B only epoch 0.
	grantA0 := grantBody(t, streamID, devAID, devAEnc.PublicKey(), 0, streamKey0)
	grantA1 := grantBody(t, streamID, devAID, devAEnc.PublicKey(), 1, streamKey1)
	grantB0 := grantBody(t, streamID, devBID, devBEnc.PublicKey(), 0, streamKey0)

	storeA := NewKeyStore(streamID, devAID, devAEnc)
	storeB := NewKeyStore(streamID, devBID, devBEnc)

	if err := storeA.ProcessTransaction(grantA0); err != nil {
		t.Fatal(err)
	}
	if err := storeA.ProcessTransaction(grantA1); err != nil {
		t.Fatal(err)
	}
	if err := storeB.ProcessTransaction(grantB0); err != nil {
		t.Fatal(err)
	}

	// A holds both epochs.
	if key, ok := storeA.StreamKey(0); !ok || !bytes.Equal(key, streamKey0) {
		t.Fatalf("A epoch-0 key mismatch")
	}
	if key, ok := storeA.StreamKey(1); !ok || !bytes.Equal(key, streamKey1) {
		t.Fatalf("A epoch-1 key mismatch")
	}

	// B holds only epoch 0; it must not decrypt epoch-1 data.
	if _, ok := storeB.StreamKey(1); ok {
		t.Fatal("B must not hold the epoch-1 key it was never granted")
	}
	if _, err := storeB.DecryptWithStreamKey(1, KeyGrantAAD(streamID, 1), nil, nil); err == nil {
		t.Fatal("B must fail to decrypt epoch-1 payload")
	}

	// A can decrypt a payload sealed with streamKey1.
	plaintext := []byte("secret note")
	aad := EnvelopeAAD{ProtocolVersion: 1, StreamID: streamID, NoteID: randBytes(t, 32), TransactionID: make([]byte, 32), KeyEpoch: 1, Field: "encrypted_payload"}
	nonce := randBytes(t, 24)
	ciphertext, err := SealWithStreamKey(streamKey1, aad, plaintext, nonce)
	if err != nil {
		t.Fatal(err)
	}
	got, err := storeA.DecryptWithStreamKey(1, aad, nonce, ciphertext)
	if err != nil {
		t.Fatalf("A failed to decrypt epoch-1 payload: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("decrypted plaintext mismatch")
	}

	// B cannot decrypt that same epoch-1 payload.
	if _, err := storeB.DecryptWithStreamKey(1, aad, nonce, ciphertext); err == nil {
		t.Fatal("B must not be able to decrypt epoch-1 payload")
	}
}

func TestKeyStoreRejectsTamperedEnvelope(t *testing.T) {
	streamID := randBytes(t, 32)
	streamKey := randBytes(t, 32)
	devEnc := mustECDH(t)
	devID := []byte("device-A")

	envelope, err := EncryptKeyEnvelope(devEnc.PublicKey(), KeyGrantAAD(streamID, 0), streamKey, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	// Tamper the ciphertext inside the recipient envelope.
	var env keyEnvelope
	if _, err := StrictDecode(envelope, &env); err != nil {
		t.Fatal(err)
	}
	env.Ciphertext[len(env.Ciphertext)-1] ^= 0xff
	tamperedEnv, err := CanonicalMarshal(env)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := CanonicalMarshal(KeyGrantPayload{
		RecipientDeviceID:         devID,
		RecipientEncryptionPublicKey: devEnc.PublicKey().Bytes(),
		KeyEpoch:                  0,
		KeyEnvelope:               tamperedEnv,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := UnsignedBody{
		ProtocolVersion: 1, StreamID: streamID, NoteID: make([]byte, 32),
		OperationType: "key_grant", OperationPayload: payload, AuthorPublicKey: randBytes(t, 32),
	}
	store := NewKeyStore(streamID, devID, devEnc)
	if err := store.ProcessTransaction(body); err == nil {
		t.Fatal("tampered envelope must be rejected")
	}

	// Mismatched AAD: seal with epoch-1 AAD but label the payload as epoch 0.
	mismatched, err := EncryptKeyEnvelope(devEnc.PublicKey(), KeyGrantAAD(streamID, 1), streamKey, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload2, err := CanonicalMarshal(KeyGrantPayload{
		RecipientDeviceID:         devID,
		RecipientEncryptionPublicKey: devEnc.PublicKey().Bytes(),
		KeyEpoch:                  0, // claims epoch 0
		KeyEnvelope:               mismatched,
	})
	if err != nil {
		t.Fatal(err)
	}
	body2 := body
	body2.OperationPayload = payload2
	if err := store.ProcessTransaction(body2); err == nil {
		t.Fatal("envelope sealed with mismatched AAD must be rejected")
	}
}
