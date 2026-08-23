package sync

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jiangfire/snapnotes/internal/protocol"
	"github.com/jiangfire/snapnotes/internal/storage/sqlite"
)

// Error codes returned by the server, mirrored from internal/api to avoid an
// import cycle (api imports sync for wire decoding).
const (
	errDuplicateNoteID = "DUPLICATE_NOTE_ID"
	errDuplicateTx     = "DUPLICATE_TRANSACTION"
	errStalePowEpoch   = "STALE_POW_EPOCH"
	errChainMismatch   = "CHAIN_MISMATCH"
)

// SyncRepository is the local persistence surface the SyncClient needs: it must
// serve the pending outbox and remember the device's verified chain position.
type SyncRepository interface {
	ListPendingOutbox() ([]sqlite.OutboxItem, error)
	MarkOutboxSynced(operationID string) error
	MarkOutboxFailed(operationID string) error
	GetSyncState(streamID []byte) (sqlite.SyncState, bool, error)
	SaveSyncState(state sqlite.SyncState) error
}

// TrustAnchor is the out-of-band bootstrap the client uses to verify a chain.
// It is distributed with the join request and never trusted from the server.
type TrustAnchor struct {
	StreamID         []byte
	GenesisBlockHash []byte
	OwnerPublicKey   []byte
}

// SyncClient drains the outbox and catches up the local chain position. Network
// errors leave items pending or the anchor unchanged so the next Sync retries.
type SyncClient struct {
	Repo     SyncRepository
	Endpoint string
	HTTP     *http.Client
	Anchor   TrustAnchor
	DeviceID string
}

// Sync submits pending outbox items and catches up the local chain position.
// A failure in either stage is reported but never blocks the other.
//
// Catch-up uses the headers-first synchronisation (CatchUpHeadersFirst): it
// fetches the full header set, lets the ChainManager select the active chain by
// cumulative chainwork (reorganising on a strictly-heavier fork), and finalises
// only the active chain's blocks. This supersedes the legacy CatchUp, which is
// retained for direct testing.
func (c *SyncClient) Sync(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	submitErr := c.submitPending(ctx)
	catchUpErr := c.CatchUpHeadersFirst(ctx)
	if submitErr != nil {
		return submitErr
	}
	return catchUpErr
}

func (c *SyncClient) http() *http.Client {
	if c.HTTP == nil {
		return http.DefaultClient
	}
	return c.HTTP
}

// VerifyLeafInclusion fetches the MMR inclusion proof for the leaf (transaction)
// at the given 0-indexed position from the server and verifies it against the
// client's locally verified chain. The claimed root is the mmr_root of the
// active tip the client already pinned during CatchUpHeadersFirst
// (state.LastMMRRoot); totalLeaves is the tip height + 1 (genesis plus one leaf
// per block). This is the core multi-node verification primitive: a client can
// independently prove a historical transaction belongs to the chain it trusts,
// without re-downloading the whole MMR.
//
// It returns an error if the client has not yet synced (no verified chain
// position), the server rejects the request, or the proof fails to verify.
func (c *SyncClient) VerifyLeafInclusion(ctx context.Context, leafIndex uint64) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	state, ok, err := c.Repo.GetSyncState(c.Anchor.StreamID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, fmt.Errorf("no verified chain position for stream; run Sync first")
	}
	// The claimed root is the mmr_root of the verified active tip. totalLeaves is
	// the number of leaves in that tip's MMR (genesis leaf + one per block).
	claimedRoot := state.LastMMRRoot
	totalLeaves := state.LastBlockHeight + 1

	url := fmt.Sprintf("%s/api/v1/streams/%s/proof?leaf_index=%d",
		c.Endpoint, base64.RawURLEncoding.EncodeToString(c.Anchor.StreamID), leafIndex)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("proof request failed with status %d", resp.StatusCode)
	}
	var pr struct {
		Proof string `json:"proof"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return false, err
	}
	raw, err := base64.RawURLEncoding.DecodeString(pr.Proof)
	if err != nil {
		return false, fmt.Errorf("proof field not valid base64: %w", err)
	}
	proof, err := protocol.DecodeMMRProof(raw)
	if err != nil {
		return false, fmt.Errorf("proof not valid canonical CBOR: %w", err)
	}
	return protocol.VerifyInclusionProof(proof, claimedRoot, totalLeaves), nil
}

// submitPending uploads every pending outbox item once. Network and retryable
// server errors leave the item pending; idempotent duplicate responses mark the
// item synced; permanent errors mark it failed.
func (c *SyncClient) submitPending(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	pending, err := c.Repo.ListPendingOutbox()
	if err != nil {
		return err
	}
	for _, item := range pending {
		if err := c.syncOne(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (c *SyncClient) syncOne(ctx context.Context, item sqlite.OutboxItem) error {
	tx, err := DecodeTransaction(item.Payload)
	if err != nil {
		_ = c.Repo.MarkOutboxFailed(item.OperationID)
		return nil
	}
	url := fmt.Sprintf("%s/api/v1/streams/%s/transactions",
		c.Endpoint, base64.RawURLEncoding.EncodeToString(tx.StreamID))
	body, err := tx.MarshalWireJSON()
	if err != nil {
		_ = c.Repo.MarkOutboxFailed(item.OperationID)
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		_ = c.Repo.MarkOutboxFailed(item.OperationID)
		return nil
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http().Do(req)
	if err != nil {
		// Server unreachable: keep pending and retry on the next Sync.
		return nil
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return c.Repo.MarkOutboxSynced(item.OperationID)
	case http.StatusConflict:
		switch errorCode(resp) {
		case errDuplicateNoteID, errDuplicateTx:
			return c.Repo.MarkOutboxSynced(item.OperationID)
		case errStalePowEpoch:
			return nil // retryable; leave pending
		default:
			return c.Repo.MarkOutboxFailed(item.OperationID)
		}
	case http.StatusTooManyRequests:
		return nil // retryable; leave pending
	default:
		return c.Repo.MarkOutboxFailed(item.OperationID)
	}
}

type wireError struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func errorCode(resp *http.Response) string {
	var e wireError
	if json.NewDecoder(resp.Body).Decode(&e) == nil {
		return e.Error.Code
	}
	return ""
}
