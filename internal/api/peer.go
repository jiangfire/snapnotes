package api

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"

	"github.com/jiangfire/snapnotes/internal/protocol"
)

// PeerSync pulls a peer server's chain at startup and reorganises the local
// ledger if the peer's chain carries strictly greater cumulative proof-of-work.
// It is the server-to-server half of Phase 5: a fresh node can join an existing
// network by pointing --peer at any already-synced node and converge on the same
// best chain without out-of-band trust beyond the genesis anchor.
//
// Scope of this slice: chain convergence (blocks + leaves + MMR peaks + cumulative
// work + tx/note dedup sets). Membership/authorized sets are NOT re-derived from
// the peer chain here; a node re-learns those when devices post join/grant
// requests. The trust boundary is the genesis anchor: a peer whose genesis hash
// differs is rejected outright.

// SyncFromPeer fetches each configured stream's chain from the peer and applies a
// reorg where the peer's work is strictly greater. It is safe to call when no peer
// is configured (it is a no-op).
func (l *Ledger) SyncFromPeer(peerURL string) error {
	if peerURL == "" {
		return nil
	}
	for hexID, st := range l.streams {
		streamID := hexDecodeHexKey(hexID)
		if streamID == nil {
			continue
		}
		if err := l.syncStreamFromPeer(peerURL, streamID, st.genesisHash); err != nil {
			return fmt.Errorf("peer sync stream %x: %w", streamID, err)
		}
	}
	return nil
}

func (l *Ledger) syncStreamFromPeer(peerURL string, streamID, genesisHash []byte) error {
	blocks, err := fetchPeerChain(peerURL, streamID)
	if err != nil {
		return err
	}
	if len(blocks) == 0 {
		return nil
	}
	if _, err := l.ValidateAndReplaceChain(streamID, genesisHash, blocks); err != nil {
		return err
	}
	return nil
}

// ValidateAndReplaceChain validates a candidate chain (already fetched from a
// peer) against the genesis anchor and, if its cumulative proof-of-work is
// strictly greater than the local chain's, atomically replaces the local chain.
// It returns whether a reorg occurred.
func (l *Ledger) ValidateAndReplaceChain(streamID, genesisHash []byte, blocks []protocol.Block) (bool, error) {
	if len(blocks) == 0 {
		return false, fmt.Errorf("empty candidate chain")
	}
	// 1. Validate the whole candidate chain structurally + by PoW.
	peaks := [][]byte{}
	var cumulative *big.Int = new(big.Int)
	prevLeaf := uint64(0)
	for i, b := range blocks {
		h := b.Header
		if int(h.Height) != i {
			return false, fmt.Errorf("candidate block %d has height %d", i, h.Height)
		}
		// Recompute + match block hash.
		recomputed, err := protocol.BlockHash(h)
		if err != nil {
			return false, fmt.Errorf("block %d hash: %w", i, err)
		}
		if !bytesEqual(recomputed, b.BlockHash) {
			return false, fmt.Errorf("block %d block_hash does not match recomputed header", i)
		}
		// Proof-of-work must meet the block's own declared target.
		if !protocol.BlockSatisfiesTarget(h) {
			return false, fmt.Errorf("block %d proof-of-work does not meet its target", i)
		}
		// Genesis / linkage.
		if i == 0 {
			if !isZero32(h.PreviousBlockHash) {
				return false, fmt.Errorf("genesis previous_block_hash must be 32 zero bytes")
			}
			if !bytesEqual(b.BlockHash, genesisHash) {
				return false, fmt.Errorf("candidate genesis hash diverges from trust anchor")
			}
		} else {
			if !bytesEqual(h.PreviousBlockHash, blocks[i-1].BlockHash) {
				return false, fmt.Errorf("block %d previous_block_hash breaks linkage", i)
			}
		}
		// Transaction consistency.
		txHash, err := protocol.TransactionHash(
			b.Transaction.UnsignedBody, b.Transaction.TransactionID,
			b.Transaction.Signature, b.Transaction.PowEpoch, b.Transaction.PowNonce,
		)
		if err != nil {
			return false, fmt.Errorf("block %d tx hash: %w", i, err)
		}
		leaf := protocol.LeafHash(txHash)
		peaks = protocol.AddLeaf(peaks, leaf, prevLeaf)
		prevLeaf++
		// MMR continuity.
		if !bytesEqual(protocol.MMRRootFromPeaks(peaks), h.MMRRoot) {
			return false, fmt.Errorf("block %d mmr_root does not match bagged root", i)
		}
		// Accumulate real chainwork.
		cumulative.Add(cumulative, protocol.BlockWork(h.PowTarget))
	}

	// 2. Compare cumulative work with the local chain.
	l.mu.Lock()
	local := l.streams[hexKey(streamID)]
	if local == nil {
		l.mu.Unlock()
		return false, fmt.Errorf("unknown stream")
	}
	if local.chainwork != nil && local.chainwork.Cmp(cumulative) >= 0 {
		// Local chain has at least as much work; keep it. No reorg.
		l.mu.Unlock()
		return false, nil
	}

	// 3. Reorg: wipe and re-persist the candidate chain atomically.
	if err := l.replaceChainLocked(streamID, blocks, peaks, cumulative); err != nil {
		l.mu.Unlock()
		return false, err
	}
	l.mu.Unlock()
	return true, nil
}

// replaceChainLocked wipes a stream's on-disk chain (blocks/leaves/tx_ids/note_ids)
// and re-persists the candidate chain, then rebuilds the in-memory state. Caller
// must hold l.mu.
func (l *Ledger) replaceChainLocked(streamID []byte, blocks []protocol.Block, peaks [][]byte, cumulative *big.Int) error {
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM blocks WHERE stream_id = ?`, streamID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM leaves WHERE stream_id = ?`, streamID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM tx_ids WHERE stream_id = ?`, streamID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM note_ids WHERE stream_id = ?`, streamID); err != nil {
		return err
	}

	leafHashes := make([][]byte, len(blocks))
	for i, b := range blocks {
		headerCBOR, err := protocol.MarshalBlockHeader(b.Header)
		if err != nil {
			return err
		}
		txCBOR, err := protocol.MarshalBlock(b)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO blocks (stream_id, height, header_cbor, block_hash, tx_cbor) VALUES (?,?,?,?,?)`,
			streamID, int64(b.Header.Height), headerCBOR, b.BlockHash, txCBOR,
		); err != nil {
			return err
		}
		txHash, err := protocol.TransactionHash(
			b.Transaction.UnsignedBody, b.Transaction.TransactionID,
			b.Transaction.Signature, b.Transaction.PowEpoch, b.Transaction.PowNonce,
		)
		if err != nil {
			return err
		}
		leaf := protocol.LeafHash(txHash)
		leafHashes[i] = leaf
		if _, err := tx.Exec(
			`INSERT INTO leaves (stream_id, pos, leaf_hash) VALUES (?,?,?)`,
			streamID, int64(i), leaf,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO tx_ids (stream_id, tx_id_hex) VALUES (?,?)`, streamID, hexKey(txHash)); err != nil {
			return err
		}
		if b.Transaction.UnsignedBody.OperationType == "create" {
			if _, err := tx.Exec(`INSERT INTO note_ids (stream_id, note_id_hex) VALUES (?,?)`,
				streamID, hexKey(b.Transaction.UnsignedBody.NoteID)); err != nil {
				return err
			}
		}
	}

	// Recompute peaks from the rebuilt leaf hashes (authoritative) rather than
	// trusting the caller's snapshot.
	rebuilt := [][]byte{}
	for i, lh := range leafHashes {
		rebuilt = protocol.AddLeaf(rebuilt, lh, uint64(i))
	}
	if !bytesEqual(protocol.MMRRootFromPeaks(rebuilt), protocol.MMRRootFromPeaks(peaks)) {
		return fmt.Errorf("rebuilt peaks diverge from candidate peaks")
	}

	if _, err := tx.Exec(
		`UPDATE streams SET height = ? WHERE stream_id = ?`,
		int64(len(blocks)-1), streamID,
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// Rebuild in-memory state verbatim from the persisted chain (chainwork is
	// recomputed by replaying each block's declared PoW target).
	if err := l.rebuildStream(streamID); err != nil {
		return err
	}
	// Re-anchor the new tip after a reorg (Phase 5 external root anchor).
	// NOTE: caller holds l.mu, so we must not call the locking l.tip(); read the
	// last block directly to avoid a self-deadlock on the non-reentrant mutex.
	last := blocks[len(blocks)-1]
	_ = l.anchors.append(AnchorRecord{
		Height:    last.Header.Height,
		BlockHash: b64(last.BlockHash),
		MMRRoot:   b64(last.Header.MMRRoot),
	})
	return nil
}

// fetchPeerChain downloads a stream's full block set from a peer server using the
// existing /blocks pagination endpoint. It pages to the end and decodes each CBOR
// block strictly.
func fetchPeerChain(peerURL string, streamID []byte) ([]protocol.Block, error) {
	encoded := base64.RawURLEncoding.EncodeToString(streamID)
	var out []protocol.Block
	from := 0
	client := &http.Client{Timeout: 30 * time.Second}
	for {
		url := fmt.Sprintf("%s/api/v1/streams/%s/blocks?from_height=%d&limit=%d", peerURL, encoded, from, 1000)
		resp, err := client.Get(url)
		if err != nil {
			return nil, fmt.Errorf("fetch blocks: %w", err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read blocks: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("peer returned %d fetching blocks", resp.StatusCode)
		}
		var page struct {
			Blocks         []struct{ Block string `json:"block"` } `json:"blocks"`
			NextFromHeight *int                                  `json:"next_from_height"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode blocks page: %w", err)
		}
		for _, entry := range page.Blocks {
			raw, err := base64.RawURLEncoding.DecodeString(entry.Block)
			if err != nil {
				return nil, fmt.Errorf("decode block cbor: %w", err)
			}
			blk, err := protocol.DecodeBlock(raw)
			if err != nil {
				return nil, fmt.Errorf("decode block: %w", err)
			}
			out = append(out, blk)
		}
		if page.NextFromHeight == nil || *page.NextFromHeight <= from {
			break
		}
		from = *page.NextFromHeight
	}
	return out, nil
}

// isZero32 reports whether b is exactly 32 zero bytes.
func isZero32(b []byte) bool {
	if len(b) != 32 {
		return false
	}
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func hexDecodeHexKey(hexID string) []byte {
	b, err := hex.DecodeString(hexID)
	if err != nil {
		return nil
	}
	return b
}
