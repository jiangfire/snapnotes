package protocol

import (
	"bytes"
	"crypto/ecdh"
	"io"
	"testing"
)

type repeatingReader struct{ value byte }

func (r repeatingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.value
	}
	return len(p), nil
}

func testAAD() EnvelopeAAD {
	return EnvelopeAAD{
		ProtocolVersion: 1,
		StreamID:        bytes.Repeat([]byte{0x10}, 32),
		NoteID:          bytes.Repeat([]byte{0x20}, 32),
		TransactionID:   bytes.Repeat([]byte{0x30}, 32),
		KeyEpoch:        7,
		Field:           "wrapped_dek",
	}
}

func TestKeyEnvelopeRoundTrip(t *testing.T) {
	recipient, err := ecdh.X25519().GenerateKey(repeatingReader{0x42})
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("deterministic envelope payload")
	encoded, err := EncryptKeyEnvelope(recipient.PublicKey(), testAAD(), plaintext, repeatingReader{0x99})
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) == 0 {
		t.Fatal("empty envelope")
	}
	decoded, err := DecryptKeyEnvelope(recipient, testAAD(), encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, plaintext) {
		t.Fatalf("plaintext mismatch: %q", decoded)
	}
}

func TestKeyEnvelopeRejectsAADAndRecipientTampering(t *testing.T) {
	recipient, err := ecdh.X25519().GenerateKey(repeatingReader{0x41})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncryptKeyEnvelope(recipient.PublicKey(), testAAD(), []byte("secret"), io.Reader(repeatingReader{0x88}))
	if err != nil {
		t.Fatal(err)
	}
	badAAD := testAAD()
	badAAD.Field = "encrypted_payload"
	if _, err := DecryptKeyEnvelope(recipient, badAAD, encoded); err == nil {
		t.Fatal("expected modified AAD to fail")
	}
	other, err := ecdh.X25519().GenerateKey(repeatingReader{0x43})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptKeyEnvelope(other, testAAD(), encoded); err == nil {
		t.Fatal("expected wrong recipient key to fail")
	}
}

func TestKeyEnvelopeRejectsMalformedLengthsAndTrailingData(t *testing.T) {
	recipient, err := ecdh.X25519().GenerateKey(repeatingReader{0x40})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncryptKeyEnvelope(recipient.PublicKey(), testAAD(), []byte("secret"), repeatingReader{0x77})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptKeyEnvelope(recipient, testAAD(), append(encoded, 0)); err == nil {
		t.Fatal("expected trailing bytes to fail")
	}
	if _, err := DecryptKeyEnvelope(recipient, testAAD(), []byte{0xa0}); err == nil {
		t.Fatal("expected malformed envelope to fail")
	}
}
