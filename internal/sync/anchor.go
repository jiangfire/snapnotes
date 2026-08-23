package sync

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/jiangfire/snapnotes/internal/protocol"
	"github.com/jiangfire/snapnotes/internal/storage/sqlite"
)

// ErrChainMismatch indicates the server's chain diverged from the device's known
// anchor at the supplied known_block_hash. The client must not accept such a chain.
var ErrChainMismatch = errors.New("chain mismatch: server block hash differs from known anchor")

const catchUpPageSize = 100

type wireBlockHeader struct {
	ProtocolVersion   uint64 `json:"protocol_version"`
	Height            uint64 `json:"height"`
	PreviousBlockHash string `json:"previous_block_hash"`
	TransactionCount  uint64 `json:"transaction_count"`
	MMRRoot           string `json:"mmr_root"`
	Timestamp         uint64 `json:"timestamp"`
	Nonce             uint64 `json:"nonce"`
	PowTarget         string `json:"pow_target"`
}

// wireBlock is the JSON envelope for a /blocks entry: the full block is carried
// as a single canonical CBOR item (header + block_hash + transaction),
// base64url-encoded. The client decodes it with protocol.DecodeBlock, which
// strictly rejects non-canonical CBOR, unknown fields, and trailing data (M1).
type wireBlock struct {
	Block string `json:"block"`
}

type wirePage struct {
	Blocks         []wireBlock `json:"blocks"`
	NextFromHeight *uint64     `json:"next_from_height"`
}

// CatchUp fetches the bounded block sequence from the device's last verified
// height and verifies every block against the trust anchor (genesis hash,
// previous_block_hash continuity, recomputed block hash, transaction id/signature,
// and mmr_root chaining). It is safe to call repeatedly; a dropped notification
// only means the next call repeats from the same anchor. Returns ErrChainMismatch
// if the server reports a known_block_hash conflict.
func (c *SyncClient) CatchUp(ctx context.Context) error {
	if c.Anchor.GenesisBlockHash == nil {
		return nil // not configured for chain catch-up
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state, ok, err := c.Repo.GetSyncState(c.Anchor.StreamID)
	if err != nil {
		return err
	}
	lastHeight := uint64(0)
	var lastHash []byte
	var peaks [][]byte
	if ok {
		lastHeight = state.LastBlockHeight
		lastHash = state.LastBlockHash
		peaks = splitPeaks(state.LastPeaks)
	} else {
		peaks = nil // first sync; peaks are seeded from the genesis block below
	}

	for {
		url := fmt.Sprintf("%s/api/v1/streams/%s/blocks?from_height=%d&limit=%d",
			c.Endpoint, base64.RawURLEncoding.EncodeToString(c.Anchor.StreamID), lastHeight, catchUpPageSize)
		if lastHash != nil {
			url += "&known_block_hash=" + base64.RawURLEncoding.EncodeToString(lastHash)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := c.http().Do(req)
		if err != nil {
			// Network unreachable: nothing verified this round. Leave the anchor
			// unchanged and retry on the next Sync; a notification is not required
			// to recover.
			return nil
		}
		if resp.StatusCode == http.StatusConflict {
			resp.Body.Close()
			return ErrChainMismatch
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("catch-up request failed with status %d", resp.StatusCode)
		}
		var page wirePage
		dec := json.NewDecoder(resp.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&page); err != nil {
			resp.Body.Close()
			return err
		}
		resp.Body.Close()
		if len(page.Blocks) == 0 {
			break
		}

		for i, wb := range page.Blocks {
			blk, err := decodeCborBlock(wb)
			if err != nil {
				return err
			}
			header := blk.Header
			hash := blk.BlockHash
			tx := blk.Transaction
			if i == 0 {
				if lastHash == nil {
					// First sync: the first block must be the genesis block.
					if header.Height != 0 {
						return fmt.Errorf("first block height = %d, want 0", header.Height)
					}
					if !isZero(header.PreviousBlockHash) {
						return errors.New("genesis previous_block_hash must be 32 zero bytes")
					}
					if !bytes.Equal(hash, c.Anchor.GenesisBlockHash) {
						return ErrChainMismatch
					}
				} else if bytes.Equal(hash, lastHash) {
					// Already-verified anchor block, re-sent at from_height. The server
					// already guaranteed it matches known_block_hash, and our running peaks
					// were restored from saved state, so just skip ahead.
					lastHash = hash
					lastHeight = header.Height
					continue
				} else {
					return ErrChainMismatch
				}
			} else if !bytes.Equal(header.PreviousBlockHash, lastHash) {
				return errors.New("block chain discontinuity: previous_block_hash mismatch")
			}

			// Recomputed block hash must match the transmitted hash.
			recomputed, err := protocol.BlockHash(header)
			if err != nil {
				return err
			}
			if !bytes.Equal(recomputed, hash) {
				return errors.New("block hash does not match recomputed header hash")
			}

			// Transaction must be internally consistent.
			recomputedID, err := protocol.TransactionID(tx.UnsignedBody)
			if err != nil {
				return err
			}
			if !bytes.Equal(recomputedID, tx.TransactionID) {
				return errors.New("transaction_id does not match recomputed value")
			}
			if !protocol.VerifySignature(tx.UnsignedBody, tx.Signature, tx.UnsignedBody.AuthorPublicKey) {
				return errors.New("transaction signature does not verify")
			}
			if !bytes.Equal(tx.UnsignedBody.StreamID, c.Anchor.StreamID) {
				return errors.New("transaction stream_id does not match trust anchor")
			}

			// Append this block's leaf to the running peak-bagging MMR and verify the
			// header's mmr_root matches the freshly computed bagged root. leafCount is
			// the number of leaves already present, which equals the block height.
			txHash, err := protocol.TransactionHash(tx.UnsignedBody, tx.TransactionID, tx.Signature, tx.PowEpoch, tx.PowNonce)
			if err != nil {
				return err
			}
			leaf := protocol.LeafHash(txHash)
			peaks = protocol.AddLeaf(peaks, leaf, header.Height)
			if !bytes.Equal(protocol.MMRRootFromPeaks(peaks), header.MMRRoot) {
				return errors.New("mmr_root does not match bagged root for this block")
			}

			lastHash = hash
			lastHeight = header.Height
		}

		if page.NextFromHeight == nil {
			break
		}
		lastHeight = *page.NextFromHeight
	}

	return c.Repo.SaveSyncState(sqlite.SyncState{
		StreamID:         c.Anchor.StreamID,
		LastBlockHeight:  lastHeight,
		LastBlockHash:    lastHash,
		LastMMRRoot:      protocol.MMRRootFromPeaks(peaks),
		LastPeaks:        joinPeaks(peaks),
		GenesisBlockHash: c.Anchor.GenesisBlockHash,
		DeviceID:         c.DeviceID,
	})
}

// decodeCborBlock decodes a /blocks wire entry (a single canonical CBOR block,
// base64url-encoded) into a protocol.Block using the strict CBOR decoder, which
// rejects non-canonical CBOR, unknown fields, and trailing data (M1).
func decodeCborBlock(wb wireBlock) (protocol.Block, error) {
	raw, err := base64.RawURLEncoding.DecodeString(wb.Block)
	if err != nil {
		return protocol.Block{}, err
	}
	return protocol.DecodeBlock(raw)
}

func isZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// splitPeaks reconstructs the running MMR peaks from the concatenated 32-byte blob
// stored in sync_state. Peaks are all 32 bytes, so the blob length is a multiple of 32.
func splitPeaks(blob []byte) [][]byte {
	if len(blob) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(blob)/32)
	for i := 0; i+32 <= len(blob); i += 32 {
		out = append(out, append([]byte(nil), blob[i:i+32]...))
	}
	return out
}

// joinPeaks serialises the running MMR peaks into a single concatenated blob for storage.
func joinPeaks(peaks [][]byte) []byte {
	var b []byte
	for _, p := range peaks {
		b = append(b, p...)
	}
	return b
}

// Listen connects to the notification-only WebSocket and triggers a local Sync
// on every "new_block" message. Sync performs the headers-first catch-up, so a
// dropped connection never loses changes: the caller should also Sync on launch
// and on a timer. Listen returns when the context is cancelled or the connection
// drops.
func (c *SyncClient) Listen(ctx context.Context) error {
	if c.Anchor.GenesisBlockHash == nil {
		return nil
	}
	wsURL := toWSURL(c.Endpoint) + "/api/v1/streams/" +
		base64.RawURLEncoding.EncodeToString(c.Anchor.StreamID) + "/events"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var note struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &note) == nil && note.Type == "new_block" {
			// A notification means "something changed": flush our own pending
			// outbox and pull everyone else's changes in one pass.
			_ = c.Sync(ctx)
		}
	}
}

func toWSURL(endpoint string) string {
	switch {
	case strings.HasPrefix(endpoint, "https://"):
		return "wss://" + strings.TrimPrefix(endpoint, "https://")
	case strings.HasPrefix(endpoint, "http://"):
		return "ws://" + strings.TrimPrefix(endpoint, "http://")
	default:
		return endpoint
	}
}

// compile-time assertion that *sqlite.Store satisfies SyncRepository.
var _ SyncRepository = (*sqlite.Store)(nil)
