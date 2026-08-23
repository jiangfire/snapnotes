package sync

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/jiangfire/snapnotes/internal/protocol"
)

// TestUnmarshalWireJSONRejectsUnknownField closes M1: a wire transaction that
// carries fields the receiver does not understand must be rejected, not silently
// discarded by json.Unmarshal.
func TestUnmarshalWireJSONRejectsUnknownField(t *testing.T) {
	priv, pub, streamID, streamKey := testKeys(t)
	tx, err := BuildCreate(CreateParams{
		StreamID: streamID, StreamKey: streamKey, KeyEpoch: 0,
		AuthorPublicKey: pub, SigningKey: priv,
		Body: "reject unknown", ClientCreatedAt: time.Now(), Randomness: rand.Reader,
		PowTarget: LooseTestTarget(), PowEpoch: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := tx.MarshalWireJSON()
	if err != nil {
		t.Fatal(err)
	}
	// Inject an unknown field into the otherwise-valid wire envelope.
	poisoned := append([]byte{}, wire[:len(wire)-1]...)
	poisoned = append(poisoned, []byte(`,"bogus_field":true}`)...)

	if _, err := UnmarshalWireJSON(poisoned); err == nil {
		t.Fatal("UnmarshalWireJSON accepted a transaction with an unknown field")
	}
}

// TestUnmarshalWireJSONAcceptsValid confirms the strict decoder still admits a
// well-formed wire transaction.
func TestUnmarshalWireJSONAcceptsValid(t *testing.T) {
	priv, pub, streamID, streamKey := testKeys(t)
	tx, err := BuildCreate(CreateParams{
		StreamID: streamID, StreamKey: streamKey, KeyEpoch: 0,
		AuthorPublicKey: pub, SigningKey: priv,
		Body: "accept valid", ClientCreatedAt: time.Now(), Randomness: rand.Reader,
		PowTarget: LooseTestTarget(), PowEpoch: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := tx.MarshalWireJSON()
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalWireJSON(wire)
	if err != nil {
		t.Fatalf("UnmarshalWireJSON rejected a valid transaction: %v", err)
	}
	if !protocol.VerifySignature(got.UnsignedBody, got.Signature, pub) {
		t.Fatal("recovered transaction fails signature verification")
	}
}
