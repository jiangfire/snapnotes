package client_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jiangfire/snapnotes/internal/api"
	"github.com/jiangfire/snapnotes/internal/client"
	"github.com/jiangfire/snapnotes/internal/protocol"
	"github.com/jiangfire/snapnotes/internal/storage/sqlite"
	"github.com/jiangfire/snapnotes/internal/sync"
)

// TestOwnerNoteSyncsEndToEnd proves the P0 wiring: a locally created note is
// encrypted, signed, enqueued, submitted to a real server, and marked synced —
// using the same genesis/keys the client bootstrapped.
func TestOwnerNoteSyncsEndToEnd(t *testing.T) {
	cfg, err := client.InitOwner("http://localhost:8333", nil)
	if err != nil {
		t.Fatalf("InitOwner: %v", err)
	}

	blockCBOR, err := base64.StdEncoding.DecodeString(cfg.GenesisBlock)
	if err != nil {
		t.Fatalf("decode genesis block: %v", err)
	}
	block, err := protocol.DecodeBlock(blockCBOR)
	if err != nil {
		t.Fatalf("decode block: %v", err)
	}
	anchor, err := cfg.TrustAnchor()
	if err != nil {
		t.Fatalf("TrustAnchor: %v", err)
	}

	srv, err := api.NewServer([]api.StreamConfig{{
		StreamID:       anchor.StreamID,
		Genesis:        block,
		AuthorizedKeys: []ed25519.PublicKey{anchor.OwnerPublicKey},
	}}, t.TempDir())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	defer srv.Close()

	db := t.TempDir() + "/snapnotes.db"
	store, err := sqlite.Open(db)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	keys, err := cfg.ClientKeys()
	if err != nil {
		t.Fatalf("ClientKeys: %v", err)
	}
	noteSvc := &sync.NoteService{Store: store, Keys: keys}
	syncCli := &sync.SyncClient{
		Repo:     store,
		Endpoint: ts.URL,
		Anchor:   anchor,
		DeviceID: cfg.DeviceID,
	}

	// A stopped server must never block a local create (acceptance criterion).
	id, err := noteSvc.Submit("first note #idea", time.Now())
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if id == "" {
		t.Fatal("Submit returned empty id")
	}
	if pending, _ := store.PendingOutboxCount(); pending != 1 {
		t.Fatalf("pending outbox = %d, want 1 before sync", pending)
	}

	// Sync uploads the pending transaction and catches up.
	if err := syncCli.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if pending, _ := store.PendingOutboxCount(); pending != 0 {
		t.Fatalf("pending outbox = %d after sync, want 0 (note not accepted)", pending)
	}

	// The server must have advanced past genesis.
	tip := fetchTip(t, ts.URL, anchor.StreamID)
	if tip.Height < 1 {
		t.Fatalf("server tip height = %d, want >= 1", tip.Height)
	}
}

func fetchTip(t *testing.T, endpoint string, streamID []byte) struct {
	Height uint64
} {
	t.Helper()
	url := endpoint + "/api/v1/streams/" + base64.RawURLEncoding.EncodeToString(streamID) + "/tip"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("tip request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("tip status = %d", resp.StatusCode)
	}
	var body struct {
		Height uint64 `json:"height"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode tip: %v", err)
	}
	return struct{ Height uint64 }{Height: body.Height}
}
