package sync_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jiangfire/snapnotes/internal/api"
	"github.com/jiangfire/snapnotes/internal/protocol"
	"github.com/jiangfire/snapnotes/internal/storage/sqlite"
	notesync "github.com/jiangfire/snapnotes/internal/sync"
)

func mustGenesis(t *testing.T) protocol.GenesisResult {
	t.Helper()
	gen, err := protocol.BuildGenesis(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return gen
}

func startAPI(t *testing.T, gen protocol.GenesisResult) (*api.Server, *httptest.Server) {
	t.Helper()
	srv, err := api.NewServer([]api.StreamConfig{{
		StreamID: gen.StreamID,
		Genesis:  gen.Block,
		AuthorizedKeys: []ed25519.PublicKey{gen.OwnerSigningPublicKey},
	}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { _ = srv.Close() })
	t.Cleanup(ts.Close)
	return srv, ts
}

func newStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "notes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func newService(t *testing.T, store *sqlite.Store, gen protocol.GenesisResult) *notesync.NoteService {
	t.Helper()
	return &notesync.NoteService{
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
}

func anchorFor(gen protocol.GenesisResult) notesync.TrustAnchor {
	return notesync.TrustAnchor{
		StreamID:         gen.StreamID,
		GenesisBlockHash: gen.GenesisBlockHash,
		OwnerPublicKey:   gen.OwnerSigningPublicKey,
	}
}

// flushOutbox POSTs every pending outbox item to the server exactly like
// SyncClient.submitPending would, without running CatchUp. It is used to push a
// locally created note so the server broadcasts a new_block notification.
func flushOutbox(t *testing.T, ts *httptest.Server, gen protocol.GenesisResult, store *sqlite.Store) {
	t.Helper()
	pending, err := store.ListPendingOutbox()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range pending {
		tx, err := notesync.DecodeTransaction(it.Payload)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := tx.MarshalWireJSON()
		if err != nil {
			t.Fatal(err)
		}
		url := ts.URL + "/api/v1/streams/" + base64.RawURLEncoding.EncodeToString(gen.StreamID) + "/transactions"
		resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
}

func TestCatchUpDownloadsChainAndAdvancesAnchor(t *testing.T) {
	gen := mustGenesis(t)
	srv, ts := startAPI(t, gen)
	store := newStore(t)
	svc := newService(t, store, gen)

	for i := 0; i < 3; i++ {
		if _, err := svc.Submit("note #idea", time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	client := &notesync.SyncClient{Repo: store, Endpoint: ts.URL, Anchor: anchorFor(gen)}
	if err := client.Sync(context.Background()); err != nil {
		t.Fatalf("Sync error: %v", err)
	}
	if got := srv.AcceptedCount(gen.StreamID); got != 3 {
		t.Fatalf("server accepted = %d, want 3", got)
	}
	state, ok, err := store.GetSyncState(gen.StreamID)
	if err != nil || !ok {
		t.Fatalf("sync state missing: ok=%v err=%v", ok, err)
	}
	if state.LastBlockHeight != 3 { // genesis (0) + 3 user blocks
		t.Fatalf("anchor height = %d, want 3", state.LastBlockHeight)
	}
	if !bytesEqual(state.GenesisBlockHash, gen.GenesisBlockHash) {
		t.Fatal("stored genesis hash does not match trust anchor")
	}

	// A second sync with no new data must be idempotent and not regress.
	if err := client.Sync(context.Background()); err != nil {
		t.Fatalf("second Sync error: %v", err)
	}
	state2, _, _ := store.GetSyncState(gen.StreamID)
	if state2.LastBlockHeight != 3 {
		t.Fatalf("anchor regressed to height %d", state2.LastBlockHeight)
	}
}

func TestCatchUpResumesAfterNewBlocks(t *testing.T) {
	gen := mustGenesis(t)
	srv, ts := startAPI(t, gen)
	store := newStore(t)
	svc := newService(t, store, gen)
	client := &notesync.SyncClient{Repo: store, Endpoint: ts.URL, Anchor: anchorFor(gen)}

	if _, err := svc.Submit("a", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := client.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, _, _ := store.GetSyncState(gen.StreamID)
	if state.LastBlockHeight != 1 {
		t.Fatalf("after first sync height = %d, want 1", state.LastBlockHeight)
	}

	// More blocks arrive on the server.
	if _, err := svc.Submit("b", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Submit("c", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := client.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := srv.AcceptedCount(gen.StreamID); got != 3 {
		t.Fatalf("server accepted = %d, want 3", got)
	}
	state, _, _ = store.GetSyncState(gen.StreamID)
	if state.LastBlockHeight != 3 {
		t.Fatalf("anchor height after resume = %d, want 3", state.LastBlockHeight)
	}
}

func TestCatchUpRejectsMismatchedGenesis(t *testing.T) {
	gen := mustGenesis(t)
	_, ts := startAPI(t, gen)
	store := newStore(t)
	svc := newService(t, store, gen)
	if _, err := svc.Submit("x", time.Now()); err != nil {
		t.Fatal(err)
	}

	wrongAnchor := anchorFor(gen)
	wrongAnchor.GenesisBlockHash = make([]byte, 32)
	wrongAnchor.GenesisBlockHash[0] = 0xaa
	client := &notesync.SyncClient{Repo: store, Endpoint: ts.URL, Anchor: wrongAnchor}
	err := client.CatchUp(context.Background())
	if err == nil {
		t.Fatal("expected a chain/genesis rejection, got nil")
	}
	// The pending outbox must remain (submit path untouched) and no state saved.
	pending, _ := store.ListPendingOutbox()
	if len(pending) != 1 {
		t.Fatalf("pending after rejected catch-up = %d, want 1", len(pending))
	}
	_, ok, _ := store.GetSyncState(gen.StreamID)
	if ok {
		t.Fatal("mismatched genesis must not persist a sync state")
	}
}

func TestCatchUpRecoversWithoutWebSocket(t *testing.T) {
	gen := mustGenesis(t)
	_, ts := startAPI(t, gen)
	store := newStore(t)
	svc := newService(t, store, gen)

	for i := 0; i < 4; i++ {
		if _, err := svc.Submit("note", time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	// No WebSocket listener is started; a plain Sync (submit pending + catch-up)
	// must still push the notes to the server and pull the full chain.
	client := &notesync.SyncClient{Repo: store, Endpoint: ts.URL, Anchor: anchorFor(gen)}
	if err := client.Sync(context.Background()); err != nil {
		t.Fatalf("Sync without WS error: %v", err)
	}
	state, ok, err := store.GetSyncState(gen.StreamID)
	if err != nil || !ok {
		t.Fatalf("sync state missing: ok=%v err=%v", ok, err)
	}
	if state.LastBlockHeight != 4 {
		t.Fatalf("anchor height = %d, want 4 (WS absence must not lose data)", state.LastBlockHeight)
	}
}

func TestWebSocketNotificationTriggersCatchUp(t *testing.T) {
	gen := mustGenesis(t)
	_, ts := startAPI(t, gen)
	store := newStore(t)
	svc := newService(t, store, gen)

	if _, err := svc.Submit("first", time.Now()); err != nil {
		t.Fatal(err)
	}
	client := &notesync.SyncClient{Repo: store, Endpoint: ts.URL, Anchor: anchorFor(gen)}
	if err := client.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Listen in the background; a new_block notification must drive a catch-up.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = client.Listen(ctx) }()

	if _, err := svc.Submit("second", time.Now()); err != nil {
		t.Fatal(err)
	}
	// Push the new note to the server. The server accepts it (reaching height 2)
	// and broadcasts a new_block notification, which must trigger the listener's
	// catch-up to the same height.
	flushOutbox(t, ts, gen, store)

	deadline := time.Now().Add(3 * time.Second)
	for {
		state, ok, _ := store.GetSyncState(gen.StreamID)
		if ok && state.LastBlockHeight == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("WebSocket notification did not trigger catch-up to height 2")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
