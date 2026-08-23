package sync

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/jiangfire/snapnotes/internal/protocol"
	"github.com/jiangfire/snapnotes/internal/storage/sqlite"
)

func testKeys(t *testing.T) (ed25519.PrivateKey, ed25519.PublicKey, []byte, []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	streamID := make([]byte, 32)
	streamKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, streamID); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(rand.Reader, streamKey); err != nil {
		t.Fatal(err)
	}
	return priv, pub, streamID, streamKey
}

func contains(items []string, want string) bool {
	for _, it := range items {
		if it == want {
			return true
		}
	}
	return false
}

func newService(t *testing.T, store *sqlite.Store, priv ed25519.PrivateKey, pub ed25519.PublicKey, streamID, streamKey []byte) *NoteService {
	t.Helper()
	return &NoteService{
		Store: store,
		Keys: ClientKeys{
			StreamID:        streamID,
			StreamKey:       streamKey,
			KeyEpoch:        0,
			AuthorPublicKey: pub,
			SigningKey:      priv,
			PowTarget:       LooseTestTarget(),
			PowEpoch:        0,
		},
	}
}

func TestBuildCreateProducesValidTransaction(t *testing.T) {
	priv, pub, streamID, streamKey := testKeys(t)
	createdAt := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	tx, err := BuildCreate(CreateParams{
		StreamID:        streamID,
		StreamKey:       streamKey,
		KeyEpoch:        0,
		AuthorPublicKey: pub,
		SigningKey:      priv,
		Body:            "#idea review this",
		ClientCreatedAt: createdAt,
		Randomness:      rand.Reader,
		PowTarget:       LooseTestTarget(),
		PowEpoch:        0,
	})
	if err != nil {
		t.Fatalf("BuildCreate returned error: %v", err)
	}
	computed, err := protocol.TransactionID(tx.UnsignedBody)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(computed, tx.TransactionID) {
		t.Fatal("transaction_id does not match recomputed value")
	}
	if !protocol.VerifySignature(tx.UnsignedBody, tx.Signature, pub) {
		t.Fatal("signature does not verify")
	}
	preimage, err := protocol.PowPreimage(tx.UnsignedBody, tx.TransactionID, tx.PowEpoch, tx.PowNonce)
	if err != nil {
		t.Fatal(err)
	}
	if !protocol.CheckPoW(preimage, LooseTestTarget()) {
		t.Fatal("proof of work does not meet target")
	}
}

func TestBuildCreateTamperFailsVerification(t *testing.T) {
	priv, pub, streamID, streamKey := testKeys(t)
	tx, err := BuildCreate(CreateParams{
		StreamID: streamID, StreamKey: streamKey, KeyEpoch: 0,
		AuthorPublicKey: pub, SigningKey: priv,
		Body: "tamper me", ClientCreatedAt: time.Now(), Randomness: rand.Reader,
		PowTarget: LooseTestTarget(), PowEpoch: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	tampered := tx
	tampered.OperationPayload = append([]byte{}, tx.OperationPayload...)
	tampered.OperationPayload[0] ^= 0xff
	if protocol.VerifySignature(tampered.UnsignedBody, tampered.Signature, pub) {
		t.Fatal("tampered body still verified")
	}
}

func TestBuildCreatePayloadDecrypts(t *testing.T) {
	priv, pub, streamID, streamKey := testKeys(t)
	body := "#idea review\n@remind 2026-08-21T09:00:00+08:00"
	tx, err := BuildCreate(CreateParams{
		StreamID: streamID, StreamKey: streamKey, KeyEpoch: 0,
		AuthorPublicKey: pub, SigningKey: priv,
		Body: body, ClientCreatedAt: time.Now(), Randomness: rand.Reader,
		PowTarget: LooseTestTarget(), PowEpoch: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	var op createOperationPayload
	if _, err := protocol.StrictDecode(tx.OperationPayload, &op); err != nil {
		t.Fatalf("decode operation payload: %v", err)
	}
	placeholder := make([]byte, 32)
	dek, err := protocol.OpenWithStreamKey(streamKey, protocol.EnvelopeAAD{
		ProtocolVersion: 1, StreamID: streamID, NoteID: tx.NoteID,
		TransactionID: placeholder, KeyEpoch: 0, Field: "wrapped_dek",
	}, op.WrappedDEKNonce, op.WrappedDEK)
	if err != nil {
		t.Fatalf("open wrapped dek: %v", err)
	}
	plaintext, err := protocol.OpenWithStreamKey(dek, protocol.EnvelopeAAD{
		ProtocolVersion: 1, StreamID: streamID, NoteID: tx.NoteID,
		TransactionID: placeholder, KeyEpoch: 0, Field: "encrypted_payload",
	}, op.PayloadNonce, op.EncryptedPayload)
	if err != nil {
		t.Fatalf("open encrypted payload: %v", err)
	}
	var content payloadContent
	if _, err := protocol.StrictDecode(plaintext, &content); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if content.Text != body {
		t.Fatalf("decrypted text = %q, want %q", content.Text, body)
	}
	if !contains(content.Tags, "idea") {
		t.Fatalf("decrypted tags = %v, want idea present", content.Tags)
	}
	if content.Reminder == nil || *content.Reminder != "2026-08-21T01:00:00Z" {
		t.Fatalf("decrypted reminder = %v, want 2026-08-21T01:00:00Z", content.Reminder)
	}
}

func TestTransactionEncodeDecodeAndWireRoundTrip(t *testing.T) {
	priv, pub, streamID, streamKey := testKeys(t)
	tx, err := BuildCreate(CreateParams{
		StreamID: streamID, StreamKey: streamKey, KeyEpoch: 0,
		AuthorPublicKey: pub, SigningKey: priv,
		Body: "wire round trip", ClientCreatedAt: time.Now(), Randomness: rand.Reader,
		PowTarget: LooseTestTarget(), PowEpoch: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := tx.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeTransaction(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.TransactionID, tx.TransactionID) {
		t.Fatal("decoded transaction id mismatch")
	}
	wire, err := tx.MarshalWireJSON()
	if err != nil {
		t.Fatal(err)
	}
	fromWire, err := UnmarshalWireJSON(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fromWire.TransactionID, tx.TransactionID) {
		t.Fatal("wire transaction id mismatch")
	}
}

func TestNoteServiceOfflineSubmitEnqueuesAndSurvivesRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "notes.db")
	priv, pub, streamID, streamKey := testKeys(t)
	store, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	svc := newService(t, store, priv, pub, streamID, streamKey)

	id, err := svc.Submit("offline note #idea", time.Now())
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}
	if id == "" {
		t.Fatal("Submit returned empty note id")
	}
	pending, err := store.ListPendingOutbox()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending count = %d, want 1", len(pending))
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate a restart: reopen the database.
	store, err = sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pending, err = store.ListPendingOutbox()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending after restart = %d, want 1", len(pending))
	}
	if pending[0].SyncStatus != sqlite.OutboxPending {
		t.Fatalf("outbox status = %q, want pending", pending[0].SyncStatus)
	}
}

func TestSyncClientLeavesPendingWhenServerUnreachable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "notes.db")
	priv, pub, streamID, streamKey := testKeys(t)
	store, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	tx, err := BuildCreate(CreateParams{
		StreamID: streamID, StreamKey: streamKey, KeyEpoch: 0,
		AuthorPublicKey: pub, SigningKey: priv,
		Body: "unreachable", ClientCreatedAt: time.Now(), Randomness: rand.Reader,
		PowTarget: LooseTestTarget(), PowEpoch: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := tx.Encode()
	if err := store.EnqueueOutbox(sqlite.OutboxItem{
		OperationID:   "op-1",
		StreamID:      tx.StreamID,
		EntityID:      hex.EncodeToString(tx.NoteID),
		TransactionID: tx.TransactionID,
		OperationType: "create",
		Payload:       encoded,
		CreatedAt:     time.Now(),
		SyncStatus:    sqlite.OutboxPending,
	}); err != nil {
		t.Fatal(err)
	}

	client := &SyncClient{Repo: store, Endpoint: "http://127.0.0.1:1"} // nothing listening
	if err := client.Sync(nil); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}
	pending, err := store.ListPendingOutbox()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending after unreachable sync = %d, want 1 (must retry)", len(pending))
	}
}
