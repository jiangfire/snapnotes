package api

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jiangfire/snapnotes/internal/protocol"
	notesync "github.com/jiangfire/snapnotes/internal/sync"
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

func mustGenesis(t *testing.T) protocol.GenesisResult {
	t.Helper()
	gen, err := protocol.BuildGenesis(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return gen
}

func startServer(t *testing.T, gen protocol.GenesisResult, extraAuth ... ed25519.PublicKey) (*Server, *httptest.Server) {
	t.Helper()
	auth := append([]ed25519.PublicKey{gen.OwnerSigningPublicKey}, extraAuth...)
	srv, err := NewServer([]StreamConfig{{StreamID: gen.StreamID, Genesis: gen.Block, AuthorizedKeys: auth}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { _ = srv.Close() })
	t.Cleanup(ts.Close)
	return srv, ts
}

// startServerAt is like startServer but uses a caller-supplied data directory so a
// test can Close the first server and reopen the same ledger to prove persistence.
func startServerAt(t *testing.T, gen protocol.GenesisResult, dataDir string, extraAuth ... ed25519.PublicKey) (*Server, *httptest.Server) {
	t.Helper()
	auth := append([]ed25519.PublicKey{gen.OwnerSigningPublicKey}, extraAuth...)
	srv, err := NewServer([]StreamConfig{{StreamID: gen.StreamID, Genesis: gen.Block, AuthorizedKeys: auth}}, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { _ = srv.Close(); ts.Close() })
	return srv, ts
}

func buildTransactionJSON(t *testing.T, gen protocol.GenesisResult, body string) []byte {
	t.Helper()
	tx, err := notesync.BuildCreate(notesync.CreateParams{
		StreamID: gen.StreamID, StreamKey: gen.StreamKey, KeyEpoch: 0,
		AuthorPublicKey: gen.OwnerSigningPublicKey, SigningKey: gen.OwnerSigningKey,
		Body: body, ClientCreatedAt: time.Now(), Randomness: rand.Reader,
		PowTarget: gen.PowTarget, PowEpoch: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tx.MarshalWireJSON()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// postTransaction builds a fresh transaction (with a fresh timestamp) and submits it.
func postTransaction(t *testing.T, ts *httptest.Server, gen protocol.GenesisResult, body string) *http.Response {
	t.Helper()
	return postRaw(t, ts, gen, buildTransactionJSON(t, gen, body))
}

// postRaw submits an already-built transaction body, so the same bytes can be
// resubmitted to test idempotency.
func postRaw(t *testing.T, ts *httptest.Server, gen protocol.GenesisResult, raw []byte) *http.Response {
	t.Helper()
	resp, err := http.Post(txURL(ts, gen), "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func txURL(ts *httptest.Server, gen protocol.GenesisResult) string {
	return ts.URL + "/api/v1/streams/" + base64.RawURLEncoding.EncodeToString(gen.StreamID) + "/transactions"
}

func TestServerAcceptsValidAndIsIdempotent(t *testing.T) {
	gen := mustGenesis(t)
	srv, ts := startServer(t, gen)

	// Build the transaction once, then submit the exact same bytes twice to prove
	// the server treats a resubmission as idempotent.
	raw := buildTransactionJSON(t, gen, "accepted note")
	resp := postRaw(t, ts, gen, raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first submit status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	if got := srv.AcceptedCount(gen.StreamID); got != 1 {
		t.Fatalf("accepted count = %d, want 1", got)
	}

	// Resubmit the exact same bytes: must be idempotent (200, no new block).
	resp = postRaw(t, ts, gen, raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("duplicate submit status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	if got := srv.AcceptedCount(gen.StreamID); got != 1 {
		t.Fatalf("accepted count after duplicate = %d, want 1", got)
	}
}

func TestServerRejectsUnauthorizedKey(t *testing.T) {
	gen := mustGenesis(t)
	_, ts := startServer(t, gen) // only the owner is authorized
	unauthPriv, unauthPub, _, _ := testKeys(t)

	tx, err := notesync.BuildCreate(notesync.CreateParams{
		StreamID: gen.StreamID, StreamKey: gen.StreamKey, KeyEpoch: 0,
		AuthorPublicKey: unauthPub, SigningKey: unauthPriv,
		Body: "unauthorized", ClientCreatedAt: time.Now(), Randomness: rand.Reader,
		PowTarget: gen.PowTarget, PowEpoch: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := tx.MarshalWireJSON()
	resp, err := http.Post(txURL(ts, gen), "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 UNAUTHORIZED_KEY", resp.StatusCode)
	}
}

func TestServerRejectsBadSignature(t *testing.T) {
	gen := mustGenesis(t)
	_, ts := startServer(t, gen)

	tx, err := notesync.BuildCreate(notesync.CreateParams{
		StreamID: gen.StreamID, StreamKey: gen.StreamKey, KeyEpoch: 0,
		AuthorPublicKey: gen.OwnerSigningPublicKey, SigningKey: gen.OwnerSigningKey,
		Body: "bad sig", ClientCreatedAt: time.Now(), Randomness: rand.Reader,
		PowTarget: gen.PowTarget, PowEpoch: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = make([]byte, ed25519.SignatureSize)
	raw, _ := tx.MarshalWireJSON()
	resp, err := http.Post(txURL(ts, gen), "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 INVALID_TRANSACTION", resp.StatusCode)
	}
}

func TestServerRejectsStalePowEpoch(t *testing.T) {
	gen := mustGenesis(t)
	_, ts := startServer(t, gen)

	tx, err := notesync.BuildCreate(notesync.CreateParams{
		StreamID: gen.StreamID, StreamKey: gen.StreamKey, KeyEpoch: 1, // server expects epoch 0
		AuthorPublicKey: gen.OwnerSigningPublicKey, SigningKey: gen.OwnerSigningKey,
		Body: "stale epoch", ClientCreatedAt: time.Now(), Randomness: rand.Reader,
		PowTarget: gen.PowTarget, PowEpoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := tx.MarshalWireJSON()
	resp, err := http.Post(txURL(ts, gen), "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 STALE_POW_EPOCH", resp.StatusCode)
	}
}

func TestServerRejectsOversizeBody(t *testing.T) {
	gen := mustGenesis(t)
	_, ts := startServer(t, gen)
	resp, err := http.Post(txURL(ts, gen), "application/json", bytes.NewReader(bytes.Repeat([]byte{'a'}, 2<<20)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 PAYLOAD_TOO_LARGE", resp.StatusCode)
	}
}

func TestServerRejectsDuplicateNoteID(t *testing.T) {
	gen := mustGenesis(t)
	srv, ts := startServer(t, gen)

	first, err := notesync.BuildCreate(notesync.CreateParams{
		StreamID: gen.StreamID, StreamKey: gen.StreamKey, KeyEpoch: 0,
		AuthorPublicKey: gen.OwnerSigningPublicKey, SigningKey: gen.OwnerSigningKey,
		Body: "first note", ClientCreatedAt: time.Now(), Randomness: rand.Reader,
		PowTarget: gen.PowTarget, PowEpoch: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := first.MarshalWireJSON()
	resp, err := http.Post(txURL(ts, gen), "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first submit status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// Second transaction reuses the same note_id but differs in content.
	second := first
	second.OperationPayload = []byte{0xa1, 0x61, 0x78, 0x02} // a distinct canonical CBOR map
	txID, _ := protocol.TransactionID(second.UnsignedBody)
	second.TransactionID = txID
	sig, _ := protocol.SignUnsignedBody(second.UnsignedBody, gen.OwnerSigningKey)
	second.Signature = sig
	second.PowNonce = mineFor(t, second.UnsignedBody, txID, gen.PowTarget)

	raw, _ = second.MarshalWireJSON()
	resp, err = http.Post(txURL(ts, gen), "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second submit status = %d, want 409 DUPLICATE_NOTE_ID", resp.StatusCode)
	}
	if got := srv.AcceptedCount(gen.StreamID); got != 1 {
		t.Fatalf("accepted count = %d, want 1", got)
	}
}

func TestBlocksPaginationIsBoundedAndContiguous(t *testing.T) {
	gen := mustGenesis(t)
	srv, ts := startServer(t, gen)

	const total = 5
	for i := 0; i < total; i++ {
		resp := postTransaction(t, ts, gen, "note #idea "+itoa(i))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("submit %d status = %d", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if got := srv.AcceptedCount(gen.StreamID); got != total {
		t.Fatalf("accepted count = %d, want %d", got, total)
	}

	url := ts.URL + "/api/v1/streams/" + base64.RawURLEncoding.EncodeToString(gen.StreamID) + "/blocks"
	from := 0
	seen := 0
	var prevHash string
	for {
		pageURL := fmt.Sprintf("%s?from_height=%d&limit=2", url, from)
		resp, err := http.Get(pageURL)
		if err != nil {
			t.Fatal(err)
		}
		var page pageResponse
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			resp.Body.Close()
			t.Fatal(err)
		}
		resp.Body.Close()
		if len(page.Blocks) == 0 {
			break
		}
		for i, b := range page.Blocks {
			raw, err := base64.RawURLEncoding.DecodeString(b.Block)
			if err != nil {
				t.Fatalf("block not base64: %v", err)
			}
			blk, err := protocol.DecodeBlock(raw)
			if err != nil {
				t.Fatalf("block not canonical CBOR: %v", err)
			}
			if i == 0 && from != 0 && string(blk.Header.PreviousBlockHash) != prevHash {
				t.Fatalf("page at from=%d not contiguous with previous page", from)
			}
			prevHash = string(blk.BlockHash)
			seen++
		}
		if page.NextFromHeight == nil {
			break
		}
		from = int(*page.NextFromHeight)
	}
	if seen != total+1 { // +1 for the genesis block
		t.Fatalf("walked %d blocks, want %d", seen, total+1)
	}
}

func TestChainMismatchRejectedByServer(t *testing.T) {
	gen := mustGenesis(t)
	_, ts := startServer(t, gen)
	wrong := make([]byte, 32)
	wrong[0] = 0xff
	url := ts.URL + "/api/v1/streams/" + base64.RawURLEncoding.EncodeToString(gen.StreamID) +
		"/blocks?from_height=0&known_block_hash=" + base64.RawURLEncoding.EncodeToString(wrong)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 CHAIN_MISMATCH", resp.StatusCode)
	}
}

func TestFullOfflineSubmitThenSync(t *testing.T) {
	gen := mustGenesis(t)
	srv, ts := startServer(t, gen)

	dbPath := filepath.Join(t.TempDir(), "notes.db")
	store, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	svc := &notesync.NoteService{
		Store: store,
		Keys: notesync.ClientKeys{
			StreamID:        gen.StreamID,
			StreamKey:       gen.StreamKey,
			KeyEpoch:        0,
			AuthorPublicKey: gen.OwnerSigningPublicKey,
			SigningKey:      gen.OwnerSigningKey,
			PowTarget:       gen.PowTarget,
			PowEpoch:        0,
		},
	}
	if _, err := svc.Submit("hello from offline #idea", time.Now()); err != nil {
		t.Fatalf("Submit error: %v", err)
	}
	if _, err := svc.Submit("second note #todo", time.Now()); err != nil {
		t.Fatalf("Submit error: %v", err)
	}

	client := &notesync.SyncClient{Repo: store, Endpoint: ts.URL}
	if err := client.Sync(nil); err != nil {
		t.Fatalf("Sync error: %v", err)
	}
	if got := srv.AcceptedCount(gen.StreamID); got != 2 {
		t.Fatalf("server accepted = %d, want 2", got)
	}
	if err := client.Sync(nil); err != nil {
		t.Fatalf("second Sync error: %v", err)
	}
	if got := srv.AcceptedCount(gen.StreamID); got != 2 {
		t.Fatalf("server accepted after retry = %d, want 2", got)
	}
	pending, err := store.ListPendingOutbox()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending after sync = %d, want 0", len(pending))
	}
}

func mineFor(t *testing.T, body protocol.UnsignedBody, txID, target []byte) uint64 {
	t.Helper()
	var n uint64
	for {
		pre, _ := protocol.PowPreimage(body, txID, 0, n)
		if protocol.CheckPoW(pre, target) {
			return n
		}
		n++
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// --- Phase 4: membership and multi-device authority ---

type testDevice struct {
	id         []byte
	signingPriv ed25519.PrivateKey
	signingPub  ed25519.PublicKey
	encPriv     *ecdh.PrivateKey
}

func newTestDevice(t *testing.T, id string) testDevice {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return testDevice{id: []byte(id), signingPriv: priv, signingPub: pub, encPriv: enc}
}

func postTx(t *testing.T, ts *httptest.Server, streamID []byte, tx notesync.Transaction) *http.Response {
	t.Helper()
	raw, err := tx.MarshalWireJSON()
	if err != nil {
		t.Fatal(err)
	}
	url := ts.URL + "/api/v1/streams/" + base64.RawURLEncoding.EncodeToString(streamID) + "/transactions"
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func signJoinFor(t *testing.T, dev testDevice, gen protocol.GenesisResult) (protocol.JoinRequest, []byte) {
	t.Helper()
	req := protocol.JoinRequest{
		StreamID:              gen.StreamID,
		DeviceID:              dev.id,
		Label:                 "test-" + string(dev.id),
		SigningPublicKey:      dev.signingPub,
		EncryptionPublicKey:   dev.encPriv.PublicKey().Bytes(),
		OwnerSigningPublicKey: gen.OwnerSigningPublicKey,
		ClientCreatedAt:       uint64(time.Now().UTC().UnixMilli()),
	}
	sig, err := protocol.SignJoinRequest(req, dev.signingPriv)
	if err != nil {
		t.Fatal(err)
	}
	return req, sig
}

// acceptAndPostJoin verifies the join request, builds member_add + key_grant,
// and submits both to the server. Returns the key_grant transaction so the
// caller can have the device recover the Stream Key from it.
func acceptAndPostJoin(t *testing.T, ts *httptest.Server, gen protocol.GenesisResult, dev testDevice) notesync.Transaction {
	t.Helper()
	req, sig := signJoinFor(t, dev, gen)
	anchor := protocol.JoinAnchor{
		StreamID:              gen.StreamID,
		GenesisBlockHash:      gen.GenesisBlockHash,
		OwnerSigningPublicKey: gen.OwnerSigningPublicKey,
	}
	memberAdd, keyGrant, err := notesync.AcceptJoin(notesync.AcceptJoinParams{
		StreamID:        gen.StreamID,
		OwnerPublicKey:  gen.OwnerSigningPublicKey,
		OwnerSigningKey: gen.OwnerSigningKey,
		CurrentEpoch:    0,
		StreamKey:       gen.StreamKey,
		PowTarget:       gen.PowTarget,
		PowEpoch:        0,
		Randomness:      rand.Reader,
	}, req, sig, anchor)
	if err != nil {
		t.Fatal(err)
	}
	for _, tx := range []notesync.Transaction{memberAdd, keyGrant} {
		resp := postTx(t, ts, gen.StreamID, tx)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("join tx status = %d, want 200", resp.StatusCode)
		}
	}
	return keyGrant
}

func TestAcceptJoinAuthorizesDevice(t *testing.T) {
	gen := mustGenesis(t)
	srv, ts := startServer(t, gen)

	devA := newTestDevice(t, "device-A")
	keyGrant := acceptAndPostJoin(t, ts, gen, devA)

	if !srv.IsMember(gen.StreamID, devA.id) {
		t.Fatal("device-A must be enrolled as a member")
	}
	if got := srv.AuthorizedCount(gen.StreamID); got != 2 {
		t.Fatalf("authorized count = %d, want 2 (owner + device-A)", got)
	}

	// Device A recovers the epoch-0 Stream Key from the key_grant it received.
	store := protocol.NewKeyStore(gen.StreamID, devA.id, devA.encPriv)
	if err := store.ProcessTransaction(keyGrant.UnsignedBody); err != nil {
		t.Fatalf("device-A failed to recover stream key: %v", err)
	}
	streamKey, ok := store.StreamKey(0)
	if !ok {
		t.Fatal("device-A must hold the epoch-0 stream key")
	}

	// Device A can now write a note signed with its own key.
	tx, err := notesync.BuildCreate(notesync.CreateParams{
		StreamID: gen.StreamID, StreamKey: streamKey, KeyEpoch: 0,
		AuthorPublicKey: devA.signingPub, SigningKey: devA.signingPriv,
		Body: "hello from device-A", ClientCreatedAt: time.Now(), Randomness: rand.Reader,
		PowTarget: gen.PowTarget, PowEpoch: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := postTx(t, ts, gen.StreamID, tx)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("device-A create status = %d, want 200", resp.StatusCode)
	}
}

func TestNonOwnerCannotIssueMembershipOps(t *testing.T) {
	gen := mustGenesis(t)
	srv, ts := startServer(t, gen)

	devA := newTestDevice(t, "device-A")
	acceptAndPostJoin(t, ts, gen, devA)

	// Device A (a member, not the owner) tries to add device C.
	devC := newTestDevice(t, "device-C")
	rogueAdd, err := notesync.BuildMemberAdd(notesync.MemberAddParams{
		StreamID:                  gen.StreamID,
		OwnerPublicKey:             devA.signingPub, // pretending to be authorised
		OwnerSigningKey:            devA.signingPriv,
		DeviceID:                   devC.id,
		Label:                      "rogue",
		MemberSigningPublicKey:     devC.signingPub,
		MemberEncryptionPublicKey:  devC.encPriv.PublicKey(),
		PowTarget:                  gen.PowTarget,
		PowEpoch:                   0,
		Randomness:                 rand.Reader,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := postTx(t, ts, gen.StreamID, rogueAdd)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owner member_add status = %d, want 403", resp.StatusCode)
	}
	if srv.IsMember(gen.StreamID, devC.id) {
		t.Fatal("device-C must not have been enrolled by a non-owner")
	}
}

func TestKeyRotationBundleRevokesAndAdvances(t *testing.T) {
	gen := mustGenesis(t)
	srv, ts := startServer(t, gen)

	devA := newTestDevice(t, "device-A")
	devB := newTestDevice(t, "device-B")
	grantA := acceptAndPostJoin(t, ts, gen, devA)
	grantB := acceptAndPostJoin(t, ts, gen, devB)

	// Both devices recover the epoch-0 Stream Key.
	storeA := protocol.NewKeyStore(gen.StreamID, devA.id, devA.encPriv)
	storeB := protocol.NewKeyStore(gen.StreamID, devB.id, devB.encPriv)
	if err := storeA.ProcessTransaction(grantA.UnsignedBody); err != nil {
		t.Fatal(err)
	}
	if err := storeB.ProcessTransaction(grantB.UnsignedBody); err != nil {
		t.Fatal(err)
	}

	// Owner generates a new Stream Key for epoch 1 and rotates, revoking device B.
	newStreamKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, newStreamKey); err != nil {
		t.Fatal(err)
	}
	ownerEncPub := gen.OwnerEncryptionPublicKey
	bundle, err := notesync.BuildKeyRotationBundle(notesync.KeyRotationBundleParams{
		StreamID:          gen.StreamID,
		OwnerPublicKey:    gen.OwnerSigningPublicKey,
		OwnerSigningKey:   gen.OwnerSigningKey,
		RevokedSigningKey: devB.signingPub,
		NewKeyEpoch:       1,
		Grants: []notesync.GrantSpec{
			{RecipientDeviceID: devA.id, RecipientEncryptionPublicKey: devA.encPriv.PublicKey(), KeyEpoch: 1, StreamKey: newStreamKey},
			{RecipientDeviceID: []byte("owner"), RecipientEncryptionPublicKey: ownerEncPub, KeyEpoch: 1, StreamKey: newStreamKey},
		},
		PowTarget:  gen.PowTarget,
		PowEpoch:   0,
		Randomness: rand.Reader,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := postTx(t, ts, gen.StreamID, bundle)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("key_rotation_bundle status = %d, want 200", resp.StatusCode)
	}

	// The epoch must have advanced; B's signing key must be revoked.
	if got := srv.CurrentKeyEpoch(gen.StreamID); got != 1 {
		t.Fatalf("current key epoch = %d, want 1", got)
	}

	// Device A receives the new epoch key from the bundle.
	if err := storeA.ProcessTransaction(bundle.UnsignedBody); err != nil {
		t.Fatalf("device-A failed to process rotation bundle: %v", err)
	}
	if _, ok := storeA.StreamKey(1); !ok {
		t.Fatal("device-A must hold the epoch-1 stream key after the bundle")
	}

	// Device A can write at epoch 1.
	tx, err := notesync.BuildCreate(notesync.CreateParams{
		StreamID: gen.StreamID, StreamKey: newStreamKey, KeyEpoch: 1,
		AuthorPublicKey: devA.signingPub, SigningKey: devA.signingPriv,
		Body: "post-rotation note", ClientCreatedAt: time.Now(), Randomness: rand.Reader,
		PowTarget: gen.PowTarget, PowEpoch: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp = postTx(t, ts, gen.StreamID, tx)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("device-A post-rotation create status = %d, want 200", resp.StatusCode)
	}

	// Device B cannot write at epoch 1: it is revoked (403) even before the
	// epoch check, proving the bundle left no interleaving write window.
	rogueTx, err := notesync.BuildCreate(notesync.CreateParams{
		StreamID: gen.StreamID, StreamKey: newStreamKey, KeyEpoch: 1,
		AuthorPublicKey: devB.signingPub, SigningKey: devB.signingPriv,
		Body: "rogue note", ClientCreatedAt: time.Now(), Randomness: rand.Reader,
		PowTarget: gen.PowTarget, PowEpoch: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp = postTx(t, ts, gen.StreamID, rogueTx)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("revoked device-B create status = %d, want 403", resp.StatusCode)
	}

	// Device B also cannot write at the old epoch (epoch 0 != current 1).
	oldTx, err := notesync.BuildCreate(notesync.CreateParams{
		StreamID: gen.StreamID, StreamKey: gen.StreamKey, KeyEpoch: 0,
		AuthorPublicKey: devB.signingPub, SigningKey: devB.signingPriv,
		Body: "old-epoch note", ClientCreatedAt: time.Now(), Randomness: rand.Reader,
		PowTarget: gen.PowTarget, PowEpoch: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp = postTx(t, ts, gen.StreamID, oldTx)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("revoked device-B old-epoch create status = %d, want 403", resp.StatusCode)
	}

	// Device B retains the historical epoch-0 key for reading (not revoked from
	// reading, only from writing).
	if _, ok := storeB.StreamKey(0); !ok {
		t.Fatal("revoked device-B must retain the epoch-0 key for reading history")
	}
	// But B did not receive the epoch-1 key.
	if storeB.HasEpoch(1) {
		t.Fatal("revoked device-B must not hold the epoch-1 key")
	}
}
