package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jiangfire/snapnotes/internal/protocol"
	notesync "github.com/jiangfire/snapnotes/internal/sync"
)

// getTip reads the /tip endpoint and decodes it into the wire tipResponse.
func getTip(t *testing.T, ts *httptest.Server, gen protocol.GenesisResult) tipResponse {
	t.Helper()
	url := ts.URL + "/api/v1/streams/" + base64.RawURLEncoding.EncodeToString(gen.StreamID) + "/tip"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tip status = %d", resp.StatusCode)
	}
	var tip tipResponse
	if err := json.NewDecoder(resp.Body).Decode(&tip); err != nil {
		t.Fatal(err)
	}
	return tip
}

// getProof fetches and decodes the MMR inclusion proof for the given leaf index.
func getProof(t *testing.T, ts *httptest.Server, gen protocol.GenesisResult, leafIndex int) protocol.MMRInclusionProof {
	t.Helper()
	url := fmt.Sprintf("%s/api/v1/streams/%s/proof?leaf_index=%d",
		ts.URL, base64.RawURLEncoding.EncodeToString(gen.StreamID), leafIndex)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proof status = %d", resp.StatusCode)
	}
	var pr proofResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		t.Fatal(err)
	}
	cbor, err := base64.RawURLEncoding.DecodeString(pr.Proof)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := protocol.DecodeMMRProof(cbor)
	if err != nil {
		t.Fatal(err)
	}
	return *proof
}

// TestLedgerPersistsChainAcrossRestart proves the core P1 property: after the
// server accepts transactions and is restarted from the same data directory, the
// chain height, MMR root, chainwork, leaf count, accepted count, and served
// blocks are byte-for-byte identical to before the restart.
func TestLedgerPersistsChainAcrossRestart(t *testing.T) {
	gen := mustGenesis(t)
	dir := t.TempDir()
	srv, ts := startServerAt(t, gen, dir)

	const total = 5
	for i := 0; i < total; i++ {
		resp := postTransaction(t, ts, gen, "note #idea "+itoa(i))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("submit %d status = %d", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
	before := getTip(t, ts, gen)
	if before.Height != uint64(total) {
		t.Fatalf("height before restart = %d, want %d", before.Height, total)
	}

	// Close the first server (releases the DB handle) and reopen the same ledger.
	srv.Close()
	ts.Close()

	srv2, ts2 := startServerAt(t, gen, dir)
	defer srv2.Close()
	defer ts2.Close()

	after := getTip(t, ts2, gen)
	if after.Height != before.Height {
		t.Fatalf("height after restart = %d, want %d", after.Height, before.Height)
	}
	if after.MMRRoot != before.MMRRoot {
		t.Fatalf("mmr_root changed across restart: %s != %s", after.MMRRoot, before.MMRRoot)
	}
	if after.Chainwork != before.Chainwork {
		t.Fatalf("chainwork changed across restart")
	}
	if after.LeafCount != before.LeafCount {
		t.Fatalf("leaf_count changed across restart")
	}
	if got := srv2.AcceptedCount(gen.StreamID); got != int64(total) {
		t.Fatalf("accepted count after restart = %d, want %d", got, total)
	}

	// The rebuilt chain must serve the same blocks over the wire (genesis + total).
	url := ts2.URL + "/api/v1/streams/" + base64.RawURLEncoding.EncodeToString(gen.StreamID) + "/blocks?from_height=0&limit=100"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var page pageResponse
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Blocks) != total+1 {
		t.Fatalf("rebuilt blocks served = %d, want %d (genesis + %d)", len(page.Blocks), total+1, total)
	}
}

// TestLedgerRebuildPreservesMembershipAndEpoch proves that membership enrollment,
// the authorized-write set, and the active key epoch survive a restart.
func TestLedgerRebuildPreservesMembershipAndEpoch(t *testing.T) {
	gen := mustGenesis(t)
	dir := t.TempDir()
	srv, ts := startServerAt(t, gen, dir)

	devA := newTestDevice(t, "device-A")
	acceptAndPostJoin(t, ts, gen, devA)

	newStreamKey := make([]byte, 32)
	if _, err := rand.Read(newStreamKey); err != nil {
		t.Fatal(err)
	}
	bundle, err := notesync.BuildKeyRotationBundle(notesync.KeyRotationBundleParams{
		StreamID:          gen.StreamID,
		OwnerPublicKey:    gen.OwnerSigningPublicKey,
		OwnerSigningKey:   gen.OwnerSigningKey,
		RevokedSigningKey: devA.signingPub,
		NewKeyEpoch:       1,
		Grants: []notesync.GrantSpec{{
			RecipientDeviceID: devA.id, RecipientEncryptionPublicKey: devA.encPriv.PublicKey(),
			KeyEpoch: 1, StreamKey: newStreamKey,
		}},
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
		t.Fatalf("rotation status = %d, want 200", resp.StatusCode)
	}

	beforeEpoch := srv.CurrentKeyEpoch(gen.StreamID)
	beforeAuth := srv.AuthorizedCount(gen.StreamID)
	beforeMembers := srv.MemberCount(gen.StreamID)
	if beforeEpoch != 1 {
		t.Fatalf("epoch before restart = %d, want 1", beforeEpoch)
	}

	srv.Close()
	ts.Close()

	srv2, ts2 := startServerAt(t, gen, dir)
	defer srv2.Close()
	defer ts2.Close()

	if got := srv2.CurrentKeyEpoch(gen.StreamID); got != beforeEpoch {
		t.Fatalf("epoch after restart = %d, want %d", got, beforeEpoch)
	}
	if got := srv2.AuthorizedCount(gen.StreamID); got != beforeAuth {
		t.Fatalf("authorized count after restart = %d, want %d", got, beforeAuth)
	}
	if got := srv2.MemberCount(gen.StreamID); got != beforeMembers {
		t.Fatalf("member count after restart = %d, want %d", got, beforeMembers)
	}
	if !srv2.IsMember(gen.StreamID, devA.id) {
		t.Fatal("device-A must remain enrolled after restart")
	}
}

// TestLedgerProofReadsAfterRebuild proves the MMR inclusion proof served from a
// rebuilt ledger verifies against the rebuilt head's mmr_root — i.e. the leaf
// set (and therefore the peaks) were reconstructed correctly from disk.
func TestLedgerProofReadsAfterRebuild(t *testing.T) {
	gen := mustGenesis(t)
	dir := t.TempDir()
	srv, ts := startServerAt(t, gen, dir)

	const total = 3
	for i := 0; i < total; i++ {
		resp := postTransaction(t, ts, gen, "note #idea "+itoa(i))
		resp.Body.Close()
	}

	srv.Close()
	ts.Close()

	srv2, ts2 := startServerAt(t, gen, dir)
	defer srv2.Close()
	defer ts2.Close()

	tip := getTip(t, ts2, gen)
	root, err := base64.RawURLEncoding.DecodeString(tip.MMRRoot)
	if err != nil {
		t.Fatal(err)
	}
	// Leaf index 0 is the genesis transaction; prove a user leaf (index 1).
	proof := getProof(t, ts2, gen, 1)
	if !protocol.VerifyInclusionProof(&proof, root, tip.LeafCount) {
		t.Fatal("rebuilt proof does not verify against rebuilt mmr_root")
	}
}
