package sync

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"time"

	"github.com/jiangfire/snapnotes/internal/domain"
	"github.com/jiangfire/snapnotes/internal/protocol"
	"github.com/jiangfire/snapnotes/internal/storage/sqlite"
)

// headerPage is the JSON envelope returned by GET /headers. Each entry carries
// the canonical BlockHeader fields plus the block hash recomputed by the server.
type headerPage struct {
	Headers        []headerEntryLocal `json:"headers"`
	NextFromHeight *uint64            `json:"next_from_height"`
}

// headerEntryLocal mirrors the server's headerEntry wire envelope: a header plus
// its recomputed block hash.
type headerEntryLocal struct {
	Header    wireBlockHeader `json:"header"`
	BlockHash string          `json:"block_hash"`
}

// CatchUpHeadersFirst performs the Phase 5 headers-first synchronisation. It
// fetches the full header set first, applies every header through the
// ChainManager (which selects the active chain by cumulative chainwork and
// reorganises when a competing chain has strictly greater work), then finalises
// only the blocks on the active chain. The verified active tip — including its
// cumulative chainwork — is persisted to sqlite.SyncState so a later restart
// resumes from the same chain without re-deriving it.
//
// Unlike the legacy CatchUp, this path never advances local state on a malformed
// header or a chain that does not share the trust anchor's genesis.
func (c *SyncClient) CatchUpHeadersFirst(ctx context.Context) error {
	if c.Anchor.GenesisBlockHash == nil {
		return nil // not configured for chain catch-up
	}
	if ctx == nil {
		ctx = context.Background()
	}

	store := newMemChainStore(c.Anchor.GenesisBlockHash)

	// Restore or seed the chain root.
	state, ok, err := c.Repo.GetSyncState(c.Anchor.StreamID)
	if err != nil {
		return err
	}
	mgr := NewChainManager(store, c.powTarget(), c.Anchor.StreamID)
	if ok {
		// Resume from the last verified active tip. Its peaks snapshot is the
		// running MMR after that block; chainwork resumes from the saved value.
		restored := VerifiedHeader{
			Height:    state.LastBlockHeight,
			Hash:      append([]byte(nil), state.LastBlockHash...),
			MMRRoot:   append([]byte(nil), state.LastMMRRoot...),
			Chainwork: decodeChainwork(state.LastChainwork),
			Peaks:     splitPeaks(state.LastPeaks),
			Finalized: true,
		}
		mgr.SeedActive(restored)
	}

	// --- Header phase: fetch every header and let the ChainManager select/reorg.
	from := uint64(0)
	for {
		url := fmt.Sprintf("%s/api/v1/streams/%s/headers?from_height=%d&limit=%d",
			c.Endpoint, base64.RawURLEncoding.EncodeToString(c.Anchor.StreamID), from, catchUpHeaderPageSize)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := c.http().Do(req)
		if err != nil {
			// Network unreachable: nothing new verified this round. Leave the
			// saved anchor unchanged so the next Sync retries.
			return nil
		}
		if resp.StatusCode == http.StatusConflict {
			resp.Body.Close()
			return ErrChainMismatch
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("headers request failed with status %d", resp.StatusCode)
		}
		var page headerPage
		dec := json.NewDecoder(resp.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&page); err != nil {
			resp.Body.Close()
			return err
		}
		resp.Body.Close()

		for _, wh := range page.Headers {
			header, hash, err := decodeWireHeader(wh.Header, wh.BlockHash)
			if err != nil {
				return err
			}
			if err := mgr.ApplyHeader(header, hash); err != nil {
				// A malformed header (tampered prev_hash, hash mismatch, or a
				// chain that does not share our genesis) must not advance state.
				// Stop the header phase; the active chain stays as it was.
				return err
			}
		}
		if page.NextFromHeight == nil {
			break
		}
		from = *page.NextFromHeight
	}

	// --- Finalize phase: verify only the active chain's blocks from the saved
	// height upward. Orphaned fork blocks are never presented. This runs BEFORE
	// persistence so the finalize start height is derived from the pre-sync state
	// (not the header-phase tip we are about to write); otherwise a re-read of the
	// just-persisted tip would skip every block below it.
	tip, ok := store.ActiveTip()
	if !ok {
		return fmt.Errorf("no active chain after header phase")
	}
	if err := c.finalizeActiveChain(ctx, mgr, tip); err != nil {
		return err
	}

	// --- Persistence: record the now-fully-finalised active tip (peaks advanced
	// by finalization) so a later restart resumes from a known-good chain. Done
	// after finalization so the persisted mmr_root reflects every leaf.
	finalTip, _ := mgr.store.ActiveTip()
	return c.persistActiveTip(state, finalTip)
}

// powTarget returns the genesis PoW target for chainwork derivation. The anchor
// does not yet carry a target, so we fall back to the protocol default; this is
// replaced by the real genesis target once the trust anchor distributes it.
func (c *SyncClient) powTarget() []byte {
	return protocol.DefaultGenesisTarget()
}

// persistActiveTip writes the verified active tip (height, hash, mmr root, peaks,
// and cumulative chainwork) into sqlite.SyncState.
func (c *SyncClient) persistActiveTip(prev sqlite.SyncState, tip VerifiedHeader) error {
	return c.Repo.SaveSyncState(sqlite.SyncState{
		StreamID:         c.Anchor.StreamID,
		LastBlockHeight:  tip.Height,
		LastBlockHash:    append([]byte(nil), tip.Hash...),
		LastMMRRoot:      protocol.MMRRootFromPeaks(tip.Peaks),
		LastPeaks:        joinPeaks(tip.Peaks),
		LastChainwork:    encodeChainwork(tip.Chainwork),
		GenesisBlockHash: c.Anchor.GenesisBlockHash,
		DeviceID:         c.DeviceID,
	})
}

// finalizeActiveChain fetches the blocks of the active chain from the saved
// height and verifies each via FinalizeBlock. Blocks on orphaned forks are
// skipped because GetByHeight prefers the active header.
func (c *SyncClient) finalizeActiveChain(ctx context.Context, mgr *ChainManager, tip VerifiedHeader) error {
	state, _, err := c.Repo.GetSyncState(c.Anchor.StreamID)
	if err != nil {
		return err
	}
	// Finalize every active block from the chain root upward. We always start at
	// height 0 because the in-memory ChainManager is rebuilt on every sync and its
	// MMR peaks snapshot must be re-derived from genesis; a prior finalization is
	// not carried over. FinalizeBlock is idempotent (a block already marked
	// finalized returns immediately) so re-touching previously-verified blocks is
	// a cheap no-op, and the genesis leaf (leaf 0) is always folded in.
	start := uint64(0)
	for h := start; h <= tip.Height; h++ {
		vh, found := mgr.store.GetByHeight(h)
		if !found || vh.Status != StatusActive {
			continue
		}
		blk, err := c.fetchBlock(ctx, h, vh.Hash)
		if err != nil {
			return err
		}
		if err := mgr.FinalizeBlock(blk, toSyncTransaction(blk.Transaction)); err != nil {
			return fmt.Errorf("finalize height %d: %w", h, err)
		}
		// Persist the note carried by a create block so it becomes visible in the
		// local UI. Idempotent: re-finalizing on a later sync re-ingests the same
		// note without duplicating it.
		if err := c.ingestBlockNote(blk); err != nil {
			return fmt.Errorf("ingest height %d: %w", h, err)
		}
	}
	// Persist the now-finalized active tip (peaks advanced by finalization).
	finalTip, _ := mgr.store.ActiveTip()
	return c.persistActiveTip(state, finalTip)
}

// fetchBlock downloads the block at the given height and decodes it into a
// protocol.Block (which FinalizeBlock consumes). It anchors the request with the
// expected block hash via known_block_hash so the server rejects a rollback at
// that exact height; the server returns exactly one block (limit=1) at that
// height. The block arrives as a single canonical CBOR item, base64url-encoded;
// DecodeBlock strictly rejects non-canonical CBOR, unknown fields, and trailing
// data, closing the CBOR trust boundary on the receive path (M1).
func (c *SyncClient) fetchBlock(ctx context.Context, height uint64, expectedHash []byte) (protocol.Block, error) {
	url := fmt.Sprintf("%s/api/v1/streams/%s/blocks?from_height=%d&limit=1&known_block_hash=%s",
		c.Endpoint,
		base64.RawURLEncoding.EncodeToString(c.Anchor.StreamID),
		height,
		base64.RawURLEncoding.EncodeToString(expectedHash),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return protocol.Block{}, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return protocol.Block{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return protocol.Block{}, fmt.Errorf("block fetch failed with status %d", resp.StatusCode)
	}
	var page wirePage
	dec := json.NewDecoder(resp.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&page); err != nil {
		return protocol.Block{}, err
	}
	if len(page.Blocks) == 0 {
		return protocol.Block{}, fmt.Errorf("block at height %d not returned by server", height)
	}
	return decodeCborBlock(page.Blocks[0])
}

// toProtocolSignedTransaction converts a sync.Transaction into the
// protocol.SignedTransaction carried by a protocol.Block.
func toProtocolSignedTransaction(tx Transaction) protocol.SignedTransaction {
	return protocol.SignedTransaction{
		UnsignedBody:  tx.UnsignedBody,
		TransactionID: append([]byte(nil), tx.TransactionID...),
		Signature:     append([]byte(nil), tx.Signature...),
		PowEpoch:      tx.PowEpoch,
		PowNonce:      tx.PowNonce,
	}
}

// toSyncTransaction converts a protocol.SignedTransaction (as carried by
// protocol.Block) into the sync.Transaction that FinalizeBlock consumes.
func toSyncTransaction(tx protocol.SignedTransaction) Transaction {
	return Transaction{
		UnsignedBody:  tx.UnsignedBody,
		TransactionID: append([]byte(nil), tx.TransactionID...),
		Signature:     append([]byte(nil), tx.Signature...),
		PowEpoch:      tx.PowEpoch,
		PowNonce:      tx.PowNonce,
	}
}

// ingestBlockNote decrypts the note carried by a finalized create block and
// writes it into the local notes table so it becomes visible in the UI. The
// block's encrypted payload is double-wrapped: the DEK is sealed to the epoch
// Stream Key (wrapped_dek), and the plaintext payload is sealed to the DEK
// (encrypted_payload). Both envelopes bind note_id/stream_id/key_epoch/field in
// their AAD, so a wrong key or tampered field is rejected by the AEAD.
//
// Idempotent: SaveSyncedNote uses INSERT OR IGNORE on the note id, so re-running
// ingest on a later sync (block already finalized) never duplicates the row.
// Non-create operations (genesis, member_add, key_grant, ...) and any block the
// client lacks the epoch key for are silently skipped.
func (c *SyncClient) ingestBlockNote(blk protocol.Block) error {
	tx := blk.Transaction.UnsignedBody
	if tx.OperationType != "create" {
		return nil // only create blocks carry a user note
	}
	if c.StreamKey == nil {
		return nil // no epoch key available; note stays encrypted
	}

	var op createOperationPayload
	if _, err := protocol.StrictDecode(tx.OperationPayload, &op); err != nil {
		return fmt.Errorf("decode create operation payload: %w", err)
	}

	// AAD must match exactly what BuildCreate used. transaction_id is the 32-byte
	// placeholder (BuildCreate cannot include the real id without a circular
	// dependency); the authoritative binding is the Ed25519 signature, already
	// verified by FinalizeBlock before this runs.
	placeholder := make([]byte, 32)
	dekAAD := protocol.EnvelopeAAD{
		ProtocolVersion: 1,
		StreamID:        tx.StreamID,
		NoteID:          tx.NoteID,
		TransactionID:   placeholder,
		KeyEpoch:        op.KeyEpoch,
		Field:           "wrapped_dek",
	}
	dek, err := protocol.OpenWithStreamKey(c.StreamKey, dekAAD, op.WrappedDEKNonce, op.WrappedDEK)
	if err != nil {
		// Wrong epoch key (e.g. a post-rotation block this device cannot read)
		// is not a fatal error: skip the note rather than abort the whole sync.
		return nil
	}

	payloadAAD := protocol.EnvelopeAAD{
		ProtocolVersion: 1,
		StreamID:        tx.StreamID,
		NoteID:          tx.NoteID,
		TransactionID:   placeholder,
		KeyEpoch:        op.KeyEpoch,
		Field:           "encrypted_payload",
	}
	plaintext, err := protocol.OpenWithStreamKey(dek, payloadAAD, op.PayloadNonce, op.EncryptedPayload)
	if err != nil {
		return fmt.Errorf("decrypt encrypted payload: %w", err)
	}

	var content payloadContent
	if _, err := protocol.StrictDecode(plaintext, &content); err != nil {
		return fmt.Errorf("decode payload content: %w", err)
	}

	note := domain.Note{
		ID:              hex.EncodeToString(tx.NoteID),
		Body:            content.Text,
		CreatedAt:       time.UnixMilli(int64(tx.ClientCreatedAt)).UTC(),
		AuthorPublicKey: append([]byte(nil), tx.AuthorPublicKey...),
	}
	return c.Repo.SaveSyncedNote(note)
}

// decodeWireHeader parses a single wire header entry into a BlockHeader and its
// recomputed-from-transmitted block hash.
func decodeWireHeader(wh wireBlockHeader, blockHashB64 string) (protocol.BlockHeader, []byte, error) {
	prev, err := base64.RawURLEncoding.DecodeString(wh.PreviousBlockHash)
	if err != nil {
		return protocol.BlockHeader{}, nil, err
	}
	mmr, err := base64.RawURLEncoding.DecodeString(wh.MMRRoot)
	if err != nil {
		return protocol.BlockHeader{}, nil, err
	}
	hash, err := base64.RawURLEncoding.DecodeString(blockHashB64)
	if err != nil {
		return protocol.BlockHeader{}, nil, err
	}
	powTarget, err := base64.RawURLEncoding.DecodeString(wh.PowTarget)
	if err != nil {
		return protocol.BlockHeader{}, nil, err
	}
	return protocol.BlockHeader{
		ProtocolVersion:   wh.ProtocolVersion,
		Height:            wh.Height,
		PreviousBlockHash: prev,
		TransactionCount:  wh.TransactionCount,
		MMRRoot:           mmr,
		Timestamp:         wh.Timestamp,
		Nonce:             wh.Nonce,
		PowTarget:         powTarget,
	}, hash, nil
}

const catchUpHeaderPageSize = 2000

// encodeChainwork serialises a cumulative chainwork big.Int into a fixed-width
// 32-byte big-endian blob for storage. A nil value stores 32 zero bytes.
func encodeChainwork(w *big.Int) []byte {
	out := make([]byte, 32)
	if w != nil {
		wb := w.Bytes()
		copy(out[32-len(wb):], wb)
	}
	return out
}

// decodeChainwork reverses encodeChainwork. All-zero bytes decode to zero.
func decodeChainwork(b []byte) *big.Int {
	if len(b) == 0 {
		return big.NewInt(0)
	}
	return new(big.Int).SetBytes(b)
}
