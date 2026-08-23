package sync_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jiangfire/snapnotes/internal/protocol"
	notesync "github.com/jiangfire/snapnotes/internal/sync"
)

// TestCatchUpHeadersFirstDownloadsChainAndPersistsChainwork exercises the
// Phase 5 headers-first path end to end: it pulls headers, selects the active
// chain by cumulative chainwork, finalises the active blocks, and persists the
// active tip (including LastChainwork) to sqlite.SyncState.
func TestCatchUpHeadersFirstDownloadsChainAndPersistsChainwork(t *testing.T) {
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
	// Push the locally created notes to the server (submission is orthogonal to
	// the headers-first catch-up path under test).
	flushOutbox(t, ts, gen, store)
	if err := client.CatchUpHeadersFirst(context.Background()); err != nil {
		t.Fatalf("CatchUpHeadersFirst error: %v", err)
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
	// LastChainwork must be persisted and non-zero (the chain has cumulative work).
	if len(state.LastChainwork) != 32 {
		t.Fatalf("LastChainwork length = %d, want 32", len(state.LastChainwork))
	}
	cw := new(big.Int).SetBytes(state.LastChainwork)
	if cw.Sign() == 0 {
		t.Fatal("LastChainwork persisted as zero; cumulative chainwork must be recorded")
	}

	// A second run must be idempotent and not regress.
	if err := client.CatchUpHeadersFirst(context.Background()); err != nil {
		t.Fatalf("second CatchUpHeadersFirst error: %v", err)
	}
	state2, _, _ := store.GetSyncState(gen.StreamID)
	if state2.LastBlockHeight != 3 {
		t.Fatalf("anchor regressed to height %d", state2.LastBlockHeight)
	}
	if !bytesEqual(state2.LastChainwork, state.LastChainwork) {
		t.Fatal("LastChainwork changed on idempotent re-run")
	}
}

// TestCatchUpHeadersFirstRejectsMismatchedGenesis mirrors the legacy CatchUp
// guard: a trust anchor whose genesis hash diverges from the server's chain must
// not advance or persist any local state.
func TestCatchUpHeadersFirstRejectsMismatchedGenesis(t *testing.T) {
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
	if err := client.CatchUpHeadersFirst(context.Background()); err == nil {
		t.Fatal("expected a chain/genesis rejection, got nil")
	}
	_, ok, _ := store.GetSyncState(gen.StreamID)
	if ok {
		t.Fatal("mismatched genesis must not persist a sync state")
	}
}

// TestSyncRoutesThroughHeadersFirst locks in the wiring decision: the public
// Sync entry point must perform the headers-first catch-up (chainwork selection
// + active-chain finalisation), not the legacy bounded CatchUp. It asserts that
// after Sync the persisted anchor carries cumulative chainwork — a property
// only CatchUpHeadersFirst produces — and matches a direct CatchUpHeadersFirst
// run.
func TestSyncRoutesThroughHeadersFirst(t *testing.T) {
	gen := mustGenesis(t)
	_, ts := startAPI(t, gen)
	store := newStore(t)
	svc := newService(t, store, gen)

	for i := 0; i < 4; i++ {
		if _, err := svc.Submit("note", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	flushOutbox(t, ts, gen, store)

	client := &notesync.SyncClient{Repo: store, Endpoint: ts.URL, Anchor: anchorFor(gen)}
	if err := client.Sync(context.Background()); err != nil {
		t.Fatalf("Sync error: %v", err)
	}

	state, ok, err := store.GetSyncState(gen.StreamID)
	if err != nil || !ok {
		t.Fatalf("sync state missing after Sync: ok=%v err=%v", ok, err)
	}
	if state.LastBlockHeight != 4 { // genesis (0) + 4 user blocks
		t.Fatalf("anchor height = %d, want 4", state.LastBlockHeight)
	}
	// CatchUpHeadersFirst is the only path that persists cumulative chainwork; if
	// Sync had fallen back to legacy CatchUp this blob would be 32 zero bytes.
	cw := new(big.Int).SetBytes(state.LastChainwork)
	if cw.Sign() == 0 {
		t.Fatal("Sync did not persist chainwork; it is not routing through CatchUpHeadersFirst")
	}

	// Idempotent re-run via Sync must not regress.
	if err := client.Sync(context.Background()); err != nil {
		t.Fatalf("second Sync error: %v", err)
	}
	state2, _, _ := store.GetSyncState(gen.StreamID)
	if state2.LastBlockHeight != 4 {
		t.Fatalf("Sync regressed to height %d", state2.LastBlockHeight)
	}
	if !bytesEqual(state2.LastChainwork, state.LastChainwork) {
		t.Fatal("chainwork changed on idempotent Sync re-run")
	}
}

// TestCatchUpHeadersFirstRejectsUnknownHeaderField closes M2: a peer that
// returns a /headers page carrying fields the receiver does not understand must
// be rejected, and local state must not advance.
func TestCatchUpHeadersFirstRejectsUnknownHeaderField(t *testing.T) {
	gen := mustGenesis(t)
	_, ts := startAPI(t, gen)
	store := newStore(t)
	svc := newService(t, store, gen)
	if _, err := svc.Submit("x", time.Now()); err != nil {
		t.Fatal(err)
	}
	flushOutbox(t, ts, gen, store)

	// Wrap the real server: inject an unknown field into every /headers response.
	tamper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/headers") {
			resp, err := http.Get(ts.URL + r.URL.Path + "?" + r.URL.RawQuery)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			// Insert an unknown top-level field right after the opening brace.
			poisoned := bytes.Replace(body, []byte("{"), []byte(`{"evil_field":1,`), 1)
			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(poisoned)
			return
		}
		// Proxy everything else (tip, blocks) untouched.
		resp, err := http.Get(ts.URL + r.URL.Path + "?" + r.URL.RawQuery)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
	}))
	defer tamper.Close()

	client := &notesync.SyncClient{Repo: store, Endpoint: tamper.URL, Anchor: anchorFor(gen)}
	if err := client.CatchUpHeadersFirst(context.Background()); err == nil {
		t.Fatal("expected rejection of unknown header field, got nil")
	}
	_, ok, _ := store.GetSyncState(gen.StreamID)
	if ok {
		t.Fatal("malformed header page must not persist a sync state")
	}
}

// TestCrossNodeVerifyLeafInclusion exercises the Phase 5 multi-node verification
// primitive end to end. A server (node A) builds a chain of several transactions;
// an independent client (node B) with its own store syncs the chain, pins the
// active tip's mmr_root locally, then independently verifies an MMR inclusion
// proof for arbitrary historical leaves — without trusting anything the server
// asserts about the root. It also proves the proof is cryptographically bound:
// tampering with the leaf hash fails verification, and an out-of-range index is
// rejected by the server.
func TestCrossNodeVerifyLeafInclusion(t *testing.T) {
	gen := mustGenesis(t)
	_, ts := startAPI(t, gen)
	storeA := newStore(t)
	svc := newService(t, storeA, gen)

	const nNotes = 5
	for i := 0; i < nNotes; i++ {
		if _, err := svc.Submit("note", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	flushOutbox(t, ts, gen, storeA) // node A now has genesis + 5 blocks (height 5)

	// Node B: a separate device with its own store, syncing from node A.
	storeB := newStore(t)
	clientB := &notesync.SyncClient{Repo: storeB, Endpoint: ts.URL, Anchor: anchorFor(gen)}
	if err := clientB.Sync(context.Background()); err != nil {
		t.Fatalf("node B Sync error: %v", err)
	}
	state, ok, err := storeB.GetSyncState(gen.StreamID)
	if err != nil || !ok {
		t.Fatalf("node B sync state missing: ok=%v err=%v", ok, err)
	}
	if state.LastBlockHeight != nNotes { // genesis (0) + 5 user blocks
		t.Fatalf("node B anchor height = %d, want %d", state.LastBlockHeight, nNotes)
	}

	// The claimed root node B verifies against is its own pinned tip mmr_root,
	// never a value supplied by the server. Independently verify several leaves.
	for _, idx := range []uint64{0, 2, nNotes} {
		ok, err := clientB.VerifyLeafInclusion(context.Background(), idx)
		if err != nil {
			t.Fatalf("VerifyLeafInclusion(%d) error: %v", idx, err)
		}
		if !ok {
			t.Fatalf("VerifyLeafInclusion(%d) = false, want true (leaf must belong to verified chain)", idx)
		}
	}

	// The proof is cryptographically bound: fetch and decode it, tamper the leaf
	// hash, and verification must fail even with the correct claimed root.
	raw := fetchProofCBOR(t, ts, gen, 2)
	proof, err := protocol.DecodeMMRProof(raw)
	if err != nil {
		t.Fatal(err)
	}
	tampered := *proof
	tampered.LeafHash = append([]byte(nil), proof.LeafHash...)
	tampered.LeafHash[0] ^= 0xff
	if protocol.VerifyInclusionProof(&tampered, state.LastMMRRoot, state.LastBlockHeight+1) {
		t.Fatal("tampered leaf hash must fail inclusion verification")
	}

	// An out-of-range leaf index must be rejected by the server (404), surfacing
	// as an error rather than a false positive.
	if _, err := clientB.VerifyLeafInclusion(context.Background(), nNotes+1); err == nil {
		t.Fatal("expected error for out-of-range leaf index, got nil")
	}

	// Without a prior Sync, verification must refuse (no verified chain position).
	freshStore := newStore(t)
	lonely := &notesync.SyncClient{Repo: freshStore, Endpoint: ts.URL, Anchor: anchorFor(gen)}
	if _, err := lonely.VerifyLeafInclusion(context.Background(), 0); err == nil {
		t.Fatal("expected error when no verified chain position exists, got nil")
	}
}

// fetchProofCBOR hits the server /proof endpoint and returns the raw canonical
// CBOR bytes of the inclusion proof for the given leaf index.
func fetchProofCBOR(t *testing.T, ts *httptest.Server, gen protocol.GenesisResult, leafIndex uint64) []byte {
	t.Helper()
	url := ts.URL + "/api/v1/streams/" + base64.RawURLEncoding.EncodeToString(gen.StreamID) +
		"/proof?leaf_index=" + fmt.Sprint(leafIndex)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proof status = %d, want 200", resp.StatusCode)
	}
	var pr struct {
		Proof string `json:"proof"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(pr.Proof)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
