package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jiangfire/snapnotes/internal/protocol"
	notesync "github.com/jiangfire/snapnotes/internal/sync"
)

func postCreate(t *testing.T, ts *httptest.Server, gen protocol.GenesisResult, body string) {
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
	raw, _ := tx.MarshalWireJSON()
	resp, err := http.Post(txURL(ts, gen), "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post %q status = %d, want 200", body, resp.StatusCode)
	}
}

// TestPeerSyncConvergesToStrongerChain verifies Phase 5 server-to-server sync:
// a fresh node B started with --peer A pulls A's chain and reorganises onto it
// when A carries strictly greater cumulative proof-of-work.
func TestPeerSyncConvergesToStrongerChain(t *testing.T) {
	gen := mustGenesis(t)

	// Node A: grow a chain of several blocks.
	aSrv, aTS := startServer(t, gen)
	for i := 0; i < 5; i++ {
		postCreate(t, aTS, gen, "note-A-"+string(rune('0'+i)))
	}
	aTip, ok := aSrv.ledger.tip(gen.StreamID)
	if !ok {
		t.Fatal("node A has no tip")
	}
	aHeight := aTip.Height
	if aHeight < 5 {
		t.Fatalf("node A height = %d, want >= 5", aHeight)
	}

	// Node B: empty ledger, pointed at A as peer. It must converge onto A's chain.
	bSrv, err := NewServerWithPeer([]StreamConfig{{StreamID: gen.StreamID, Genesis: gen.Block, AuthorizedKeys: []ed25519.PublicKey{gen.OwnerSigningPublicKey}}}, t.TempDir(), aTS.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer bSrv.Close()

	bTip, ok := bSrv.ledger.tip(gen.StreamID)
	if !ok {
		t.Fatal("node B has no tip after peer sync")
	}
	if bTip.Height != aHeight {
		t.Fatalf("node B height = %d, want %d (A's height)", bTip.Height, aHeight)
	}
	// Work must match A's exactly (same chain).
	if bTip.ChainworkHex != aTip.ChainworkHex {
		t.Fatalf("node B chainwork %s != node A chainwork %s", bTip.ChainworkHex, aTip.ChainworkHex)
	}
	// The tip block hash must be identical (same best chain).
	if bTip.BlockHash != aTip.BlockHash {
		t.Fatalf("node B tip hash %s != node A tip hash %s", bTip.BlockHash, aTip.BlockHash)
	}
}

// TestPeerSyncNoReorgWhenLocalStronger verifies a node does NOT reorganise onto a
// peer whose chain has strictly less work: a pre-seeded longer chain is kept.
func TestPeerSyncNoReorgWhenLocalStronger(t *testing.T) {
	gen := mustGenesis(t)

	// Peer C has a short chain (4 blocks).
	cSrv, cTS := startServer(t, gen)
	for i := 0; i < 3; i++ {
		postCreate(t, cTS, gen, "note-C-"+string(rune('0'+i)))
	}
	cTip, _ := cSrv.ledger.tip(gen.StreamID)

	// Node E first builds its OWN longer chain (7 blocks), then starts pointed at C.
	eSrv, eTS := startServer(t, gen)
	for i := 0; i < 6; i++ {
		postCreate(t, eTS, gen, "note-E-"+string(rune('0'+i)))
	}
	eTipBefore, _ := eSrv.ledger.tip(gen.StreamID)
	if eTipBefore.Height <= cTip.Height {
		t.Fatalf("test setup: E height %d should exceed C height %d", eTipBefore.Height, cTip.Height)
	}

	// Node F starts with C as peer (C is weaker), but F is pre-seeded with E's longer
	// chain by reusing E's data dir is awkward; instead assert the rule directly via
	// ValidateAndReplaceChain: feeding C's shorter chain into E's ledger is a no-op.
	replaced, err := eSrv.ledger.ValidateAndReplaceChain(gen.StreamID, gen.GenesisBlockHash, fetchPeerBlocks(t, cTS, gen))
	if err != nil {
		t.Fatal(err)
	}
	if replaced {
		t.Fatal("E should NOT reorg onto weaker peer C")
	}
	// E's tip must be unchanged (still its own longer chain).
	eTipAfter, _ := eSrv.ledger.tip(gen.StreamID)
	if eTipAfter.Height != eTipBefore.Height {
		t.Fatalf("E height changed %d -> %d after rejecting weaker peer", eTipBefore.Height, eTipAfter.Height)
	}
}

// fetchPeerBlocks is a test helper wrapping fetchPeerChain.
func fetchPeerBlocks(t *testing.T, ts *httptest.Server, gen protocol.GenesisResult) []protocol.Block {
	t.Helper()
	blocks, err := fetchPeerChain(ts.URL, gen.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	return blocks
}

// TestExternalRootAnchorLog verifies Phase 5 external root anchoring: every
// accepted block appends a checkpoint to the append-only anchor log, and the log
// survives a server restart.
func TestExternalRootAnchorLog(t *testing.T) {
	gen := mustGenesis(t)
	dataDir := t.TempDir()
	srv, err := NewServer([]StreamConfig{{StreamID: gen.StreamID, Genesis: gen.Block, AuthorizedKeys: []ed25519.PublicKey{gen.OwnerSigningPublicKey}}}, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Genesis seeds one anchor (height 0).
	before, err := srv.ledger.ReadAnchors()
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || before[0].Height != 0 {
		t.Fatalf("initial anchors = %d, want 1 at height 0", len(before))
	}

	// Each posted transaction appends a checkpoint.
	for i := 0; i < 4; i++ {
		postCreate(t, ts, gen, "note-"+string(rune('0'+i)))
	}
	after, err := srv.ledger.ReadAnchors()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 5 {
		t.Fatalf("anchors after 4 blocks = %d, want 5 (genesis + 4)", len(after))
	}
	// Anchors are strictly increasing in height and the last matches the live tip.
	tip, _ := srv.ledger.tip(gen.StreamID)
	if after[len(after)-1].Height != tip.Height {
		t.Fatalf("last anchor height %d != live tip height %d", after[len(after)-1].Height, tip.Height)
	}
	for i := 1; i < len(after); i++ {
		if after[i].Height <= after[i-1].Height {
			t.Fatalf("anchor heights not strictly increasing at %d", i)
		}
	}

	// Restart: reopen the same data dir and confirm the anchor log persists.
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewServer([]StreamConfig{{StreamID: gen.StreamID, Genesis: gen.Block, AuthorizedKeys: []ed25519.PublicKey{gen.OwnerSigningPublicKey}}}, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := reopened.ledger.ReadAnchors()
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 5 {
		t.Fatalf("anchors after restart = %d, want 5 (must persist)", len(restored))
	}
}
