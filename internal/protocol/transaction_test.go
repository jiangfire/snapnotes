package protocol

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"testing"
)

func fixedUnsignedBody() UnsignedBody {
	return UnsignedBody{
		ProtocolVersion:  1,
		StreamID:         bytes.Repeat([]byte{0x11}, 32),
		NoteID:           bytes.Repeat([]byte{0x22}, 32),
		OperationType:    "create",
		OperationPayload: []byte{0xa1, 0x61, 0x78, 0x01},
		ClientCreatedAt:  1700000000123,
		AuthorPublicKey:  bytes.Repeat([]byte{0x33}, ed25519.PublicKeySize),
	}
}

func TestCanonicalUnsignedBodyIsStable(t *testing.T) {
	body := fixedUnsignedBody()
	encoded, err := CanonicalUnsignedBody(body)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := hex.DecodeString("a7676e6f74655f6964582022222222222222222222222222222222222222222222222222222222222222226973747265616d5f6964582011111111111111111111111111111111111111111111111111111111111111116e6f7065726174696f6e5f74797065666372656174657070726f746f636f6c5f76657273696f6e0171617574686f725f7075626c69635f6b65795820333333333333333333333333333333333333333333333333333333333333333371636c69656e745f637265617465645f61741b0000018bcfe5687b716f7065726174696f6e5f7061796c6f616444a1617801")
	if !bytes.Equal(encoded, want) {
		t.Fatalf("unexpected canonical encoding: %x", encoded)
	}
	if len(encoded) == 0 {
		t.Fatal("canonical encoding is empty")
	}
	t.Logf("canonical unsigned body: %x", encoded)
	// Encoding the same body repeatedly must be byte-for-byte identical.
	second, err := CanonicalUnsignedBody(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, second) {
		t.Fatalf("encoding is not deterministic: %x != %x", encoded, second)
	}
}

func TestTransactionIDAndSignatureVector(t *testing.T) {
	body := fixedUnsignedBody()
	seed := bytes.Repeat([]byte{0x44}, ed25519.SeedSize)
	privateKey := ed25519.NewKeyFromSeed(seed)
	if !bytes.Equal(body.AuthorPublicKey, privateKey.Public().(ed25519.PublicKey)) {
		body.AuthorPublicKey = privateKey.Public().(ed25519.PublicKey)
	}
	txID, err := TransactionID(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(txID) != 32 {
		t.Fatalf("transaction id length = %d", len(txID))
	}
	wantTxID, _ := hex.DecodeString("da3e855dae5691d31059602895b320771b3ffb1ea89fe0ad59943c65d5076f22")
	if !bytes.Equal(txID, wantTxID) {
		t.Fatalf("unexpected transaction id: %x", txID)
	}
	signature, err := SignUnsignedBody(body, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(privateKey.Public().(ed25519.PublicKey), signingMessage(body), signature) {
		t.Fatal("signature does not verify")
	}
	wantSignature, _ := hex.DecodeString("17d8cd4f1c2c36f8f5181adab72605e48527ae1dc90759530a424698081717debb05e6d6399cdb6048302b42f05cb0306ce031ea110f1de8bc9df8423998600a")
	if !bytes.Equal(signature, wantSignature) {
		t.Fatalf("unexpected signature: %x", signature)
	}
	if _, err := SignUnsignedBody(body, ed25519.PrivateKey{}); err == nil {
		t.Fatal("expected invalid private key error")
	}
}

func TestPowPreimageUsesBigEndianUint64(t *testing.T) {
	body := fixedUnsignedBody()
	txID := bytes.Repeat([]byte{0x55}, 32)
	preimage, err := PowPreimage(body, txID, 0x0102030405060708, 0x1112131415161718)
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := []byte{1, 2, 3, 4, 5, 6, 7, 8, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18}
	if !bytes.HasSuffix(preimage, wantSuffix) {
		t.Fatalf("pow preimage does not end in big-endian epoch/nonce: %x", preimage)
	}
	t.Logf("pow preimage: %x", preimage)
}

func TestTransactionHashChangesWhenSignedFieldsChange(t *testing.T) {
	body := fixedUnsignedBody()
	signature := bytes.Repeat([]byte{0x66}, ed25519.SignatureSize)
	txID := bytes.Repeat([]byte{0x77}, 32)
	hash, err := TransactionHash(body, txID, signature, 2, 9)
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) != 32 {
		t.Fatalf("transaction hash length = %d", len(hash))
	}
	changed, err := TransactionHash(body, txID, signature, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(hash, changed) {
		t.Fatal("changing PoW nonce did not change transaction hash")
	}
	wantHash, _ := hex.DecodeString("2f8de1af723e0cf45aba35a0005cc6f72b0acc8e5349713cdcddb9f26cb603ce")
	if !bytes.Equal(hash, wantHash) {
		t.Fatalf("unexpected transaction hash: %x", hash)
	}
}

func TestDecodeUnsignedBodyRejectsUnknownDuplicateAndTrailingData(t *testing.T) {
	body := fixedUnsignedBody()
	encoded, err := CanonicalUnsignedBody(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeUnsignedBody(append(append([]byte{}, encoded...), 0)); err == nil {
		t.Fatal("expected trailing data to be rejected")
	}
	unknown := append([]byte{0xa8}, encoded[1:]...)
	unknown = append(unknown, 0x61, 'x', 0x01)
	if _, err := DecodeUnsignedBody(unknown); err == nil {
		t.Fatal("expected unknown field to be rejected")
	}
	duplicate := append([]byte{0xa8}, encoded[1:]...)
	duplicate = append(duplicate, []byte{0x67, 'n', 'o', 't', 'e', '_', 'i', 'd', 0x58, 0x20}...)
	duplicate = append(duplicate, bytes.Repeat([]byte{0x22}, 32)...)
	if _, err := DecodeUnsignedBody(duplicate); err == nil {
		t.Fatal("expected duplicate field to be rejected")
	}
}
