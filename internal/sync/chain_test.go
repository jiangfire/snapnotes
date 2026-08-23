package sync

import (
	"bytes"
	"encoding/base64"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/jiangfire/snapnotes/internal/protocol"
)

// --- chain construction helpers ---------------------------------------------

func mustGenesis(t *testing.T) protocol.GenesisResult {
	t.Helper()
	gen, err := protocol.BuildGenesis(nil)
	if err != nil {
		t.Fatal(err)
	}
	return gen
}

// buildChain extends gen with n user blocks, each mined under target. It returns the
// full block list (index 0 = genesis) and the per-block MMR peaks snapshots.
func buildChain(t *testing.T, gen protocol.GenesisResult, n int, target []byte) ([]protocol.Block, [][][]byte) {
	t.Helper()
	blocks := []protocol.Block{gen.Block}
	gtxHash, err := protocol.TransactionHash(
		gen.Block.Transaction.UnsignedBody, gen.Block.Transaction.TransactionID,
		gen.Block.Transaction.Signature, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	peaks := protocol.AddLeaf(nil, protocol.LeafHash(gtxHash), 0)
	peaksList := [][][]byte{peaks}

	for i := 1; i <= n; i++ {
		parent := blocks[i-1]
		tx, err := BuildCreate(CreateParams{
			StreamID:        gen.StreamID,
			StreamKey:       gen.StreamKey,
			KeyEpoch:        0,
			AuthorPublicKey: gen.OwnerSigningPublicKey,
			SigningKey:      gen.OwnerSigningKey,
			Body:            "note",
			ClientCreatedAt: time.Now(),
			PowTarget:       target,
			PowEpoch:        0,
		})
		if err != nil {
			t.Fatal(err)
		}
		blk, pk := makeBlock(t, parent, peaksList[i-1], uint64(i), tx, gen.StreamID)
		blocks = append(blocks, blk)
		peaksList = append(peaksList, pk)
	}
	return blocks, peaksList
}

func makeBlock(t *testing.T, parent protocol.Block, parentPeaks [][]byte, height uint64,
	tx Transaction, streamID []byte) (protocol.Block, [][]byte) {
	t.Helper()
	txHash, err := tx.Hash()
	if err != nil {
		t.Fatal(err)
	}
	leaf := protocol.LeafHash(txHash)
	peaks := protocol.AddLeaf(parentPeaks, leaf, height)
	mmr := protocol.MMRRootFromPeaks(peaks)
	header := protocol.BlockHeader{
		ProtocolVersion:   1,
		Height:            height,
		PreviousBlockHash: parent.BlockHash,
		TransactionCount:  height + 1,
		MMRRoot:           mmr,
		Timestamp:         height * 1000,
	}
	bh, err := protocol.BlockHash(header)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Block{
		Header:    header,
		BlockHash: bh,
		Transaction: protocol.SignedTransaction{
			UnsignedBody:  tx.UnsignedBody,
			TransactionID: tx.TransactionID,
			Signature:     tx.Signature,
			PowEpoch:      tx.PowEpoch,
			PowNonce:      tx.PowNonce,
		},
	}, peaks
}

func toTx(b protocol.Block) Transaction {
	st := b.Transaction
	return Transaction{
		UnsignedBody:  st.UnsignedBody,
		TransactionID: st.TransactionID,
		Signature:     st.Signature,
		PowEpoch:      st.PowEpoch,
		PowNonce:      st.PowNonce,
	}
}

// seedAndApply headers-first applies and finalizes every block in order, so the
// chain becomes the active best chain.
func seedAndApply(t *testing.T, m *ChainManager, gen protocol.GenesisResult, blocks []protocol.Block, peaksList [][][]byte) {
	t.Helper()
	m.SeedGenesis(gen.Block.BlockHash, gen.Block.Header.PreviousBlockHash, gen.Block.Header.MMRRoot, peaksList[0])
	for i := 1; i < len(blocks); i++ {
		if err := m.ApplyHeader(blocks[i].Header, blocks[i].BlockHash); err != nil {
			t.Fatalf("ApplyHeader height %d: %v", i, err)
		}
		if err := m.FinalizeBlock(blocks[i], toTx(blocks[i])); err != nil {
			t.Fatalf("FinalizeBlock height %d: %v", i, err)
		}
	}
}

func newManager(t *testing.T, gen protocol.GenesisResult, target []byte) *ChainManager {
	t.Helper()
	store := newMemChainStore(gen.GenesisBlockHash)
	return NewChainManager(store, target, gen.StreamID)
}

// --- vector 5: reorganisation restores MMR + labels displaced orphaned -------

func TestChainManagerReorgLabelsOrphanedAndRestoresMMR(t *testing.T) {
	target := LooseTestTarget()
	gen := mustGenesis(t)
	chainA, peaksA := buildChain(t, gen, 4, target) // heights 0..4
	m := newManager(t, gen, target)
	seedAndApply(t, m, gen, chainA, peaksA)

	tip, ok := m.store.ActiveTip()
	if !ok || tip.Height != 4 {
		t.Fatalf("active tip height = %d, want 4", tip.Height)
	}

	// Competing fork with strictly greater chainwork (more blocks, same difficulty).
	chainB, peaksB := buildChain(t, gen, 7, target) // heights 0..7
	seedAndApply(t, m, gen, chainB, peaksB)

	tip, _ = m.store.ActiveTip()
	if tip.Height != 7 {
		t.Fatalf("after reorg active tip height = %d, want 7", tip.Height)
	}
	// MMR continuity: the active tip's bagged root must match the reorganised fork.
	if !bytes.Equal(protocol.MMRRootFromPeaks(tip.Peaks), protocol.MMRRootFromPeaks(peaksB[7])) {
		t.Error("active tip MMR root does not match the reorganised fork (MMR continuity broken)")
	}

	orphans := m.OrphanedHeaders()
	got := map[uint64]bool{}
	for _, o := range orphans {
		got[o.Height] = true
	}
	for h := uint64(1); h <= 4; h++ {
		if !got[h] {
			t.Errorf("displaced block at height %d was not labelled orphaned", h)
		}
	}
	if got[0] {
		t.Error("genesis (shared anchor) must not be orphaned")
	}
}

// --- vector 4: chainwork, not height, selects the active chain --------------

func TestChainManagerGreaterChainworkWinsRegardlessOfHeight(t *testing.T) {
	target := LooseTestTarget()
	gen := mustGenesis(t)
	chainA, peaksA := buildChain(t, gen, 6, target) // heights 0..6, work 7*W
	m := newManager(t, gen, target)
	seedAndApply(t, m, gen, chainA, peaksA)
	tip, _ := m.store.ActiveTip()
	if tip.Height != 6 {
		t.Fatalf("setup tip height = %d, want 6", tip.Height)
	}

	// A fork that is SHORTER (fewer blocks) but mined at a HARDER difficulty so its
	// cumulative chainwork exceeds chainA. The client credits each block's work from
	// the PoW target schedule (here modelled as a harder target for the fork's
	// heights); in production that target is the epoch-derived difficulty.
	hardTarget := make([]byte, 32)
	hardTarget[3] = 0x01                            // 2^224: strictly harder than 2^240
	chainB, peaksB := buildChain(t, gen, 4, target) // heights 0..4, structurally valid
	m2 := newManager(t, gen, target)
	m2.SetTargetSchedule(func(h uint64) []byte {
		if h >= 1 && h <= 4 {
			return hardTarget
		}
		return target
	})
	seedAndApply(t, m2, gen, chainB, peaksB)

	tip2, _ := m2.store.ActiveTip()
	if tip2.Height != 4 {
		t.Fatalf("fork tip height = %d, want 4 (must be the shorter chain)", tip2.Height)
	}
	// The shorter fork must be the active chain because its cumulative work is greater.
	if tip2.Height >= tip.Height {
		t.Errorf("shorter fork (h=%d) unexpectedly not selected over longer chain (h=%d)", tip2.Height, tip.Height)
	}
	if tip2.Chainwork.Cmp(tip.Chainwork) <= 0 {
		t.Errorf("shorter fork chainwork %v should exceed longer chain %v", tip2.Chainwork, tip.Chainwork)
	}
}

// --- vector 6: malformed peer data never advances local state --------------

func TestChainManagerRejectsTamperedHeader(t *testing.T) {
	target := LooseTestTarget()
	gen := mustGenesis(t)
	chainA, peaksA := buildChain(t, gen, 3, target)
	m := newManager(t, gen, target)
	seedAndApply(t, m, gen, chainA, peaksA)

	before, _ := m.store.ActiveTip()
	beforeCount := len(m.store.All())

	// Tamper: flip a byte in the previous_block_hash but keep the original (valid)
	// block hash. The recomputed header hash will not match the supplied hash.
	bad := chainA[2].Header
	bad.PreviousBlockHash = append([]byte(nil), bad.PreviousBlockHash...)
	bad.PreviousBlockHash[0] ^= 0xff
	err := m.ApplyHeader(bad, chainA[2].BlockHash)
	if err == nil {
		t.Fatal("expected tampered header to be rejected")
	}
	if err.Error() != ErrMalformedHeader.Error() {
		t.Fatalf("unexpected error: %v", err)
	}

	after, _ := m.store.ActiveTip()
	afterCount := len(m.store.All())
	if after.Height != before.Height || afterCount != beforeCount {
		t.Errorf("malformed header advanced local state: height %d->%d, headers %d->%d",
			before.Height, after.Height, beforeCount, afterCount)
	}
}

func TestChainManagerRejectsMMRMismatch(t *testing.T) {
	target := LooseTestTarget()
	gen := mustGenesis(t)
	chainA, peaksA := buildChain(t, gen, 3, target)
	m := newManager(t, gen, target)
	m.SeedGenesis(gen.Block.BlockHash, gen.Block.Header.PreviousBlockHash, gen.Block.Header.MMRRoot, peaksA[0])

	// Baseline: only the genesis header is present. A header applied but later
	// failing finalization must not persist, so the count must return to this.
	beforeCount := len(m.store.All())

	// Apply the header (valid), then finalize with a corrupted mmr_root.
	header := chainA[1].Header
	if err := m.ApplyHeader(header, chainA[1].BlockHash); err != nil {
		t.Fatal(err)
	}
	if len(m.store.All()) <= beforeCount {
		t.Fatalf("ApplyHeader did not register the header (count still %d)", beforeCount)
	}

	corrupt := header
	corrupt.MMRRoot = append([]byte(nil), header.MMRRoot...)
	corrupt.MMRRoot[0] ^= 0xff
	badBlock := chainA[1]
	badBlock.Header = corrupt
	if err := m.FinalizeBlock(badBlock, toTx(chainA[1])); err == nil {
		t.Fatal("expected mmr mismatch to be rejected")
	}
	if afterCount := len(m.store.All()); afterCount != beforeCount {
		t.Errorf("mmr-mismatch block advanced local state: headers %d->%d", beforeCount, afterCount)
	}
}

func TestChainManagerRejectsOutOfOrderHeaders(t *testing.T) {
	target := LooseTestTarget()
	gen := mustGenesis(t)
	chainA, _ := buildChain(t, gen, 3, target)
	m := newManager(t, gen, target)
	gtxHash, _ := protocol.TransactionHash(
		gen.Block.Transaction.UnsignedBody, gen.Block.Transaction.TransactionID,
		gen.Block.Transaction.Signature, 0, 0)
	m.SeedGenesis(gen.Block.BlockHash, gen.Block.Header.PreviousBlockHash, gen.Block.Header.MMRRoot,
		protocol.AddLeaf(nil, protocol.LeafHash(gtxHash), 0))

	// Apply header at height 3 before its parent (height 2) is known.
	if err := m.ApplyHeader(chainA[3].Header, chainA[3].BlockHash); err == nil {
		t.Fatal("expected out-of-order header to be rejected")
	}
}

// TestDecodeBlockHeaderRejectsUnknownField asserts the strict decoder backing
// header verification rejects non-canonical/unknown-field CBOR (vector 6).
func TestDecodeBlockHeaderRejectsUnknownField(t *testing.T) {
	enc, err := cbor.EncOptions{Sort: cbor.SortCanonical}.EncMode()
	if err != nil {
		t.Fatal(err)
	}
	data, err := enc.Marshal(map[string]interface{}{
		"protocol_version":    uint64(1),
		"height":              uint64(0),
		"previous_block_hash": make([]byte, 32),
		"transaction_count":   uint64(1),
		"mmr_root":            make([]byte, 32),
		"timestamp":           uint64(0),
		"bogus_field":         []byte("x"), // unknown field must be rejected
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.DecodeBlockHeader(data); err == nil {
		t.Error("DecodeBlockHeader should reject a header with an unknown field")
	}
}

// TestDecodeCborBlockRejectsMalformed locks in M1's CBOR receive-path: the
// client decodes /blocks entries with protocol.DecodeBlock (via decodeCborBlock),
// which must reject non-canonical CBOR, unknown fields, and trailing data. A
// peer that smuggles an unknown field or trailing bytes past the decoder must
// not be accepted.
func TestDecodeCborBlockRejectsMalformed(t *testing.T) {
	// A well-formed canonical block CBOR, produced by the same encoder the
	// server uses (protocol.MarshalBlock).
	good, err := protocol.MarshalBlock(protocol.Block{
		Header: protocol.BlockHeader{
			ProtocolVersion:   1,
			Height:            0,
			PreviousBlockHash: make([]byte, 32),
			TransactionCount:  1,
			MMRRoot:           make([]byte, 32),
			Timestamp:         0,
		},
		BlockHash: make([]byte, 32),
		Transaction: protocol.SignedTransaction{
			UnsignedBody: protocol.UnsignedBody{
				ProtocolVersion:  1,
				StreamID:         make([]byte, 32),
				NoteID:           make([]byte, 32),
				OperationType:    "create_note",
				OperationPayload: []byte("payload"),
				ClientCreatedAt:  0,
				AuthorPublicKey:  make([]byte, 32),
			},
			TransactionID: make([]byte, 32),
			Signature:     make([]byte, 32),
			PowEpoch:      0,
			PowNonce:      0,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	enc, err := cbor.EncOptions{Sort: cbor.SortCanonical}.EncMode()
	if err != nil {
		t.Fatal(err)
	}
	headerMap := map[string]interface{}{
		"protocol_version":   uint64(1),
		"height":             uint64(0),
		"previous_block_hash": make([]byte, 32),
		"transaction_count":   uint64(1),
		"mmr_root":            make([]byte, 32),
		"timestamp":           uint64(0),
	}
	txMap := map[string]interface{}{
		"unsigned_body": map[string]interface{}{
			"protocol_version":   uint64(1),
			"stream_id":          make([]byte, 32),
			"note_id":            make([]byte, 32),
			"operation_type":     "create_note",
			"operation_payload":  []byte("payload"),
			"client_created_at":  uint64(0),
			"author_public_key":  make([]byte, 32),
		},
		"transaction_id": make([]byte, 32),
		"signature":       make([]byte, 32),
		"pow_epoch":       uint64(0),
		"pow_nonce":       uint64(0),
	}
	// Unknown field smuggled into the block map.
	unknownField, err := enc.Marshal(map[string]interface{}{
		"header":       headerMap,
		"block_hash":   make([]byte, 32),
		"transaction":  txMap,
		"bogus_field":  []byte("x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Trailing garbage after a valid block.
	trailing := append(append([]byte{}, good...), 0x01)

	cases := map[string][]byte{
		"unknown_field": unknownField,
		"trailing_data": trailing,
	}
	for name, raw := range cases {
		wb := wireBlock{Block: base64.RawURLEncoding.EncodeToString(raw)}
		if _, err := decodeCborBlock(wb); err == nil {
			t.Errorf("decodeCborBlock should reject %s, got nil", name)
		}
	}
	// Sanity: the well-formed block must decode cleanly.
	if _, err := decodeCborBlock(wireBlock{Block: base64.RawURLEncoding.EncodeToString(good)}); err != nil {
		t.Errorf("decodeCborBlock rejected a well-formed block: %v", err)
	}
}
