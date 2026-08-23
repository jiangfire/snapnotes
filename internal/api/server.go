package api

import (
	"bytes"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/jiangfire/snapnotes/internal/protocol"
	notesync "github.com/jiangfire/snapnotes/internal/sync"
)

// Protocol error codes returned by the transaction endpoint.
const (
	CodeInvalidEncoding    = "INVALID_ENCODING"
	CodeUnauthorizedKey    = "UNAUTHORIZED_KEY"
	CodeDuplicateNoteID    = "DUPLICATE_NOTE_ID"
	CodeDuplicateTx        = "DUPLICATE_TRANSACTION"
	CodePayloadTooLarge    = "PAYLOAD_TOO_LARGE"
	CodeInvalidTransaction = "INVALID_TRANSACTION"
	CodeStalePowEpoch      = "STALE_POW_EPOCH"
	CodeChainMismatch      = "CHAIN_MISMATCH"
	CodeStreamNotFound     = "STREAM_NOT_FOUND"
	CodeRateLimited        = "RATE_LIMITED"
	CodeInternal           = "INTERNAL_ERROR"
)

const maxBodyBytes = 1 << 20 // 1 MiB

const (
	defaultBlockLimit = 100
	maxHeaderLimit    = 2000
	maxBlockLimit     = 100
)

// StreamConfig configures one stream a node serves in the MVP.
type StreamConfig struct {
	StreamID       []byte
	Genesis        protocol.Block
	AuthorizedKeys []ed25519.PublicKey
}

type streamState struct {
	target     []byte
	authorized map[string]bool
	txIDs      map[string]bool
	noteIDs    map[string]bool
	count      int64
	blocks     []blockRecord
	peaks      [][]byte // running MMR peaks (ascending height) for bagged mmr_root
	leafHashes [][]byte // ordered leaf hashes (one per block) for inclusion proofs
	chainwork  *big.Int // cumulative proof-of-work, real (Phase 5)
	genesisHash []byte
	owner       []byte
	ownerEnc    []byte
	members     map[string]*memberRecord
	currentKeyEpoch uint64
}

type memberRecord struct {
	signing []byte
	enc     []byte
	label   string
}

type blockRecord struct {
	header  protocol.BlockHeader
	hash    []byte
	txCBOR  []byte
}

// Ledger is the authoritative state for the MVP single-node server. The in-memory
// streamState is the hot cache used by all read paths; every accepted block is
// persisted to a SQLite database (opened at dataDir/ledger.db) so the server can
// rebuild identical chain + MMR state after a restart.
type Ledger struct {
	mu      sync.Mutex
	streams map[string]*streamState
	db      *sql.DB
	anchors *anchorLog // append-only external root anchor log (Phase 5)
}

// NewLedger opens (creating if necessary) the on-disk database at dataDir and
// loads each configured stream from disk. A stream with no stored data is seeded
// from its genesis block; a stream that already has data is rebuilt verbatim, so
// the -genesis flag is only consulted when the data directory is empty.
func NewLedger(configs []StreamConfig, dataDir string) (*Ledger, error) {
	for _, cfg := range configs {
		if len(cfg.StreamID) != 32 {
			return nil, errors.New("stream_id must be 32 bytes")
		}
		gp, err := protocol.DecodeGenesisPayload(cfg.Genesis.Transaction.UnsignedBody.OperationPayload)
		if err != nil {
			return nil, fmt.Errorf("genesis payload: %w", err)
		}
		if len(gp.InitialPowTarget) != 32 {
			return nil, errors.New("genesis initial_pow_target must be 32 bytes")
		}
	}
	db, err := openLedgerDB(dataDir)
	if err != nil {
		return nil, fmt.Errorf("open ledger db: %w", err)
	}
	anchors, err := openAnchorLog(dataDir)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open anchor log: %w", err)
	}
	l := &Ledger{streams: make(map[string]*streamState), db: db, anchors: anchors}
	for _, cfg := range configs {
		if err := l.loadOrSeedStream(cfg); err != nil {
			db.Close()
			anchors.Close()
			return nil, err
		}
	}
	return l, nil
}

// Close releases the ledger's database handle.
func (l *Ledger) Close() error {
	if l.db == nil {
		return nil
	}
	_ = l.anchors.Close()
	return l.db.Close()
}

// ReadAnchors returns the append-only external root anchor log (Phase 5). Each
// record is a checkpoint of the active tip at the time a block was accepted.
func (l *Ledger) ReadAnchors() ([]AnchorRecord, error) {
	return l.anchors.ReadAnchors()
}

func hexKey(b []byte) string { return fmt.Sprintf("%x", b) }

func (l *Ledger) isAuthorized(streamID, pubKey []byte) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.streams[hexKey(streamID)]
	if !ok {
		return false
	}
	return st.authorized[hexKey(pubKey)]
}

type acceptResult int

const (
	acceptOK acceptResult = iota
	acceptDuplicateTx
	acceptDuplicateNote
	acceptRejected
)

// createOpPayload mirrors the full create operation payload so the server can
// read key_epoch without the strict decoder rejecting the remaining fields.
type createOpPayload struct {
	KeyEpoch         uint64 `cbor:"key_epoch"`
	EncryptedPayload []byte `cbor:"encrypted_payload"`
	PayloadNonce     []byte `cbor:"payload_nonce"`
	WrappedDEK       []byte `cbor:"wrapped_dek"`
	WrappedDEKNonce  []byte `cbor:"wrapped_dek_nonce"`
}

// tryAccept records a validated transaction as a new block and returns its height.
// Operation-specific validation and membership-state mutation happen under the
// same lock, so an atomic operation such as key_rotation_bundle cannot be
// interleaved with a normal write.
func (l *Ledger) tryAccept(streamID []byte, tx notesync.Transaction) (acceptResult, uint64, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.streams[hexKey(streamID)]
	if st == nil {
		return acceptRejected, 0, CodeStreamNotFound
	}
	txID := tx.TransactionID
	if st.txIDs[hexKey(txID)] {
		return acceptDuplicateTx, 0, ""
	}
	opType := tx.OperationType
	// note_id uniqueness only constrains create operations
	if opType == "create" && st.noteIDs[hexKey(tx.NoteID)] {
		return acceptDuplicateNote, 0, ""
	}

	// PoW epoch is checked under this lock against the count at append time, so a
	// transaction can never slip through on a stale epoch across the 1000-boundary
	// (no previous-epoch grace window, per protocol-v1.md).
	if int64(tx.PowEpoch) != st.count/1000 {
		return acceptRejected, 0, CodeStalePowEpoch
	}

	var d persistDelta
	switch opType {
	case "create":
		var p createOpPayload
		if _, err := protocol.StrictDecode(tx.OperationPayload, &p); err != nil {
			return acceptRejected, 0, CodeInvalidTransaction
		}
		if p.KeyEpoch != st.currentKeyEpoch {
			return acceptRejected, 0, CodeInvalidTransaction
		}
	case "member_add":
		if !bytes.Equal(tx.AuthorPublicKey, st.owner) {
			return acceptRejected, 0, CodeUnauthorizedKey
		}
		var p protocol.MemberAddPayload
		if _, err := protocol.StrictDecode(tx.OperationPayload, &p); err != nil {
			return acceptRejected, 0, CodeInvalidTransaction
		}
		if len(p.DeviceID) == 0 || len(p.SigningPublicKey) != ed25519.PublicKeySize || len(p.EncryptionPublicKey) != 32 {
			return acceptRejected, 0, CodeInvalidTransaction
		}
		rec := &memberRecord{
			signing: append([]byte(nil), p.SigningPublicKey...),
			enc:     append([]byte(nil), p.EncryptionPublicKey...),
			label:   p.Label,
		}
		st.members[hexKey(p.DeviceID)] = rec
		st.authorized[hexKey(p.SigningPublicKey)] = true
		d.newMember = rec
		d.memberID = append([]byte(nil), p.DeviceID...)
		d.addAuth = append(d.addAuth, hexKey(p.SigningPublicKey))
	case "key_grant":
		if !bytes.Equal(tx.AuthorPublicKey, st.owner) {
			return acceptRejected, 0, CodeUnauthorizedKey
		}
		// Accepted as a relay; the recipient decrypts the key_envelope client-side.
		// Decode defensively so a malformed grant is rejected rather than stored.
		var p protocol.KeyGrantPayload
		if _, err := protocol.StrictDecode(tx.OperationPayload, &p); err != nil {
			return acceptRejected, 0, CodeInvalidTransaction
		}
		if len(p.RecipientDeviceID) == 0 || len(p.RecipientEncryptionPublicKey) != 32 || len(p.KeyEnvelope) == 0 {
			return acceptRejected, 0, CodeInvalidTransaction
		}
	case "key_rotation_bundle":
		if !bytes.Equal(tx.AuthorPublicKey, st.owner) {
			return acceptRejected, 0, CodeUnauthorizedKey
		}
		var p protocol.KeyRotationBundlePayload
		if _, err := protocol.StrictDecode(tx.OperationPayload, &p); err != nil {
			return acceptRejected, 0, CodeInvalidTransaction
		}
		if bytes.Equal(p.RevokedSigningPublicKey, st.owner) {
			return acceptRejected, 0, CodeInvalidTransaction // cannot revoke the owner
		}
		if p.NewKeyEpoch <= st.currentKeyEpoch {
			return acceptRejected, 0, CodeInvalidTransaction // must strictly advance
		}
		delete(st.authorized, hexKey(p.RevokedSigningPublicKey))
		st.currentKeyEpoch = p.NewKeyEpoch
		d.delAuth = append(d.delAuth, hexKey(p.RevokedSigningPublicKey))
		d.epochChanged = true
		d.newEpoch = p.NewKeyEpoch
		// Fail-closed: every grant must name an already-enrolled member or the owner.
		// A grant to an unknown device would hand it the epoch key without write
		// authorisation, so the whole bundle is rejected rather than silently skipped.
		for _, g := range p.Grants {
			m, isMember := st.members[hexKey(g.RecipientDeviceID)]
			if !isMember && !bytes.Equal(g.RecipientEncryptionPublicKey, st.ownerEnc) {
				return acceptRejected, 0, CodeInvalidTransaction
			}
			if isMember {
				st.authorized[hexKey(m.signing)] = true
				d.addAuth = append(d.addAuth, hexKey(m.signing))
			}
		}
	case "genesis":
		return acceptRejected, 0, CodeInvalidTransaction
	default:
		return acceptRejected, 0, CodeInvalidTransaction
	}

	txCBOR, err := tx.Encode()
	if err != nil {
		return acceptRejected, 0, CodeInvalidTransaction
	}
	txHash, err := tx.Hash()
	if err != nil {
		return acceptRejected, 0, CodeInvalidTransaction
	}
	prev := st.blocks[len(st.blocks)-1]
	height := uint64(len(st.blocks)) // genesis is height 0
	// Append the new leaf to the running peak-bagging MMR. leafCount equals the
	// number of leaves already present (genesis + every prior accepted transaction).
	st.peaks = protocol.AddLeaf(st.peaks, protocol.LeafHash(txHash), uint64(len(st.blocks)))
	st.leafHashes = append(st.leafHashes, protocol.LeafHash(txHash))
	mmrRoot := protocol.MMRRootFromPeaks(st.peaks)
	header := protocol.BlockHeader{
		ProtocolVersion:   1,
		Height:            height,
		PreviousBlockHash: prev.hash,
		TransactionCount:  height + 1,
		MMRRoot:           mmrRoot,
		Timestamp:         uint64(time.Now().UTC().UnixMilli()),
	}
	// Mine the block: search for a Nonce so the header hash meets the stream's
	// current PoW target. The winning Nonce and target are embedded in the header
	// so any verifier can re-check the work self-contained.
	nonce, hash, ok := protocol.MineBlock(header, st.target, 0, nil)
	if !ok {
		return acceptRejected, 0, CodeInternal
	}
	header.Nonce = nonce
	header.PowTarget = st.target
	rec := blockRecord{header: header, hash: hash, txCBOR: txCBOR}
	st.blocks = append(st.blocks, rec)
	st.txIDs[hexKey(txID)] = true
	if opType == "create" {
		st.noteIDs[hexKey(tx.NoteID)] = true
	}
	st.count++
	// Real cumulative proof-of-work (Phase 5), not height+1.
	st.chainwork.Add(st.chainwork, protocol.BlockWork(header.PowTarget))
	d.newBlock = rec
	d.leafHash = protocol.LeafHash(txHash)
	d.txIDHex = hexKey(txID)
	if opType == "create" {
		d.noteIDHex = hexKey(tx.NoteID)
	}
	// Persist before acknowledging so a client only learns of a block once it is
	// durable; the in-memory state above is rebuilt verbatim from disk on restart.
	if err := l.persistDelta(streamID, d); err != nil {
		return acceptRejected, 0, CodeInternal
	}
	// Phase 5 external root anchor: checkpoint the tip into the append-only log so
	// an operator can mirror it to an immutable location for third-party verifiability.
	_ = l.anchors.append(AnchorRecord{
		Height:    height,
		BlockHash: b64(hash),
		MMRRoot:   b64(header.MMRRoot),
	})
	return acceptOK, height, ""
}

func (l *Ledger) expectedPowEpoch(streamID []byte) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.streams[hexKey(streamID)]
	if st == nil {
		return -1
	}
	return st.count / 1000
}

func (l *Ledger) target(streamID []byte) []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.streams[hexKey(streamID)]
	if st == nil {
		return nil
	}
	return st.target
}

// CurrentKeyEpoch returns the stream's active key epoch. Writes must use it.
func (l *Ledger) CurrentKeyEpoch(streamID []byte) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.streams[hexKey(streamID)]
	if st == nil {
		return 0
	}
	return st.currentKeyEpoch
}

// IsMember reports whether a device_id is a known member of the stream.
func (l *Ledger) IsMember(streamID, deviceID []byte) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.streams[hexKey(streamID)]
	if st == nil {
		return false
	}
	_, ok := st.members[hexKey(deviceID)]
	return ok
}

// AuthorizedCount returns how many signing keys are currently authorised to write.
func (l *Ledger) AuthorizedCount(streamID []byte) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.streams[hexKey(streamID)]
	if st == nil {
		return 0
	}
	return len(st.authorized)
}

// MemberCount returns the number of enrolled devices.
func (l *Ledger) MemberCount(streamID []byte) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.streams[hexKey(streamID)]
	if st == nil {
		return 0
	}
	return len(st.members)
}

// AcceptedCount returns how many distinct user transactions the ledger has accepted.
func (l *Ledger) AcceptedCount(streamID []byte) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.streams[hexKey(streamID)]
	if st == nil {
		return 0
	}
	return int64(len(st.txIDs))
}

func (l *Ledger) sequenceFor(streamID, txID []byte) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.streams[hexKey(streamID)]
	if st == nil {
		return 0
	}
	return int64(len(st.txIDs))
}

func (l *Ledger) tip(streamID []byte) (tipResponse, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.streams[hexKey(streamID)]
	if st == nil {
		return tipResponse{}, false
	}
	last := st.blocks[len(st.blocks)-1]
	work := new(big.Int)
	if st.chainwork != nil {
		work = st.chainwork
	}
	return tipResponse{
		Height:       uint64(len(st.blocks) - 1),
		BlockHash:    b64(last.hash),
		Chainwork:    uint64(len(st.blocks)), // MVP display: block count
		ChainworkHex: fmt.Sprintf("%x", work), // Phase 5: real cumulative PoW
		MMRRoot:      b64(last.header.MMRRoot),
		LeafCount:    uint64(len(st.blocks)),
	}, true
}

type tipResponse struct {
	Height       uint64 `json:"height"`
	BlockHash    string `json:"block_hash"`
	Chainwork    uint64 `json:"chainwork"`
	ChainworkHex string `json:"chainwork_hex"`
	MMRRoot      string `json:"mmr_root"`
	LeafCount    uint64 `json:"leaf_count"`
}

// readPage returns a bounded page of block headers or full blocks, honouring an
// optional known_block_hash anchor at from_height (409 CHAIN_MISMATCH on mismatch).
func (l *Ledger) readPage(streamID []byte, fromHeight, limit int64, withTx bool, known []byte) (pageResponse, int, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.streams[hexKey(streamID)]
	if !ok {
		return pageResponse{}, http.StatusNotFound, CodeStreamNotFound
	}
	if fromHeight < 0 || fromHeight >= int64(len(st.blocks)) {
		return pageResponse{NextFromHeight: nil}, http.StatusOK, ""
	}
	if known != nil && !bytes.Equal(st.blocks[fromHeight].hash, known) {
		return pageResponse{}, http.StatusConflict, CodeChainMismatch
	}
	end := fromHeight + limit
	if end > int64(len(st.blocks)) {
		end = int64(len(st.blocks))
	}
	resp := pageResponse{}
	for i := fromHeight; i < end; i++ {
		rec := st.blocks[i]
		if withTx {
			cborBlock, err := marshalBlock(rec)
			if err != nil {
				return pageResponse{}, http.StatusInternalServerError, CodeInternal
			}
			resp.Blocks = append(resp.Blocks, blockEntry{
				Block: b64(cborBlock),
			})
		} else {
			resp.Headers = append(resp.Headers, headerEntry{
				Header:    toWireHeader(rec.header),
				BlockHash: b64(rec.hash),
			})
		}
	}
	if end < int64(len(st.blocks)) {
		n := uint64(end)
		resp.NextFromHeight = &n
	}
	return resp, http.StatusOK, ""
}

// proofFor returns the canonical-CBOR MMR inclusion proof for the leaf at the
// given 0-indexed position. It recomputes the proof from the full ordered leaf
// set so a client can verify membership against the header mmr_root.
func (l *Ledger) proofFor(streamID []byte, idx int64) ([]byte, int, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.streams[hexKey(streamID)]
	if !ok {
		return nil, http.StatusNotFound, CodeStreamNotFound
	}
	if idx >= int64(len(st.leafHashes)) {
		return nil, http.StatusNotFound, CodeInvalidEncoding
	}
	proof, err := protocol.GenerateInclusionProof(st.leafHashes, int(idx))
	if err != nil {
		return nil, http.StatusInternalServerError, CodeInternal
	}
	cbor, err := protocol.MarshalMMRProof(proof)
	if err != nil {
		return nil, http.StatusInternalServerError, CodeInternal
	}
	return cbor, http.StatusOK, ""
}
type Server struct {
	ledger *Ledger
	hub    *wsHub
}

func NewServer(configs []StreamConfig, dataDir string) (*Server, error) {
	return NewServerWithPeer(configs, dataDir, "")
}

// NewServerWithPeer builds the server and, when peerURL is non-empty, pulls the
// peer's chain at startup and reorganises onto it if the peer carries strictly
// greater cumulative proof-of-work (Phase 5 server-to-server sync).
func NewServerWithPeer(configs []StreamConfig, dataDir, peerURL string) (*Server, error) {
	ledger, err := NewLedger(configs, dataDir)
	if err != nil {
		return nil, err
	}
	if err := ledger.SyncFromPeer(peerURL); err != nil {
		ledger.Close()
		return nil, fmt.Errorf("peer sync: %w", err)
	}
	return &Server{ledger: ledger, hub: newWSHub()}, nil
}

// Close releases the server's ledger database handle.
func (s *Server) Close() error {
	return s.ledger.Close()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/streams/{stream_id}/transactions", s.handlePostTransaction)
	mux.HandleFunc("GET /api/v1/streams/{stream_id}/tip", s.handleTip)
	mux.HandleFunc("GET /api/v1/streams/{stream_id}/headers", s.handleHeaders)
	mux.HandleFunc("GET /api/v1/streams/{stream_id}/blocks", s.handleBlocks)
	mux.HandleFunc("GET /api/v1/streams/{stream_id}/proof", s.handleProof)
	mux.HandleFunc("GET /api/v1/streams/{stream_id}/events", s.handleEvents)
	return mux
}

func (s *Server) AcceptedCount(streamID []byte) int64 {
	return s.ledger.AcceptedCount(streamID)
}

// CurrentKeyEpoch returns the stream's active key epoch.
func (s *Server) CurrentKeyEpoch(streamID []byte) uint64 {
	return s.ledger.CurrentKeyEpoch(streamID)
}

// IsMember reports whether a device_id is enrolled in the stream.
func (s *Server) IsMember(streamID, deviceID []byte) bool {
	return s.ledger.IsMember(streamID, deviceID)
}

// AuthorizedCount returns how many signing keys may write to the stream.
func (s *Server) AuthorizedCount(streamID []byte) int {
	return s.ledger.AuthorizedCount(streamID)
}

// MemberCount returns the number of enrolled devices.
func (s *Server) MemberCount(streamID []byte) int {
	return s.ledger.MemberCount(streamID)
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	body := errorBody{}
	body.Error.Code = code
	body.Error.Message = message
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type receipt struct {
	TransactionID string `json:"transaction_id"`
	StreamSeq     int64  `json:"stream_sequence"`
}

func (s *Server) handlePostTransaction(w http.ResponseWriter, r *http.Request) {
	streamID, err := decodeStreamID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidEncoding, "invalid stream_id")
		return
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "request body too large")
		return
	}
	tx, err := notesync.UnmarshalWireJSON(data)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidEncoding, err.Error())
		return
	}

	computedID, err := protocol.TransactionID(tx.UnsignedBody)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, CodeInvalidTransaction, "invalid unsigned body")
		return
	}
	if !bytes.Equal(computedID, tx.TransactionID) {
		writeError(w, http.StatusUnprocessableEntity, CodeInvalidTransaction, "transaction_id mismatch")
		return
	}
	if !protocol.VerifySignature(tx.UnsignedBody, tx.Signature, tx.AuthorPublicKey) {
		writeError(w, http.StatusUnprocessableEntity, CodeInvalidTransaction, "signature verification failed")
		return
	}
	if len(tx.OperationPayload) > maxBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "operation payload too large")
		return
	}
	if !s.ledger.isAuthorized(streamID, tx.AuthorPublicKey) {
		writeError(w, http.StatusForbidden, CodeUnauthorizedKey, "author public key is not authorized")
		return
	}
	// The PoW epoch is validated inside tryAccept under the ledger lock; a stale
	// epoch is returned as CodeStalePowEpoch below. Here we only validate the work.
	preimage, err := protocol.PowPreimage(tx.UnsignedBody, tx.TransactionID, tx.PowEpoch, tx.PowNonce)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, CodeInvalidTransaction, "invalid pow preimage inputs")
		return
	}
	if !protocol.CheckPoW(preimage, s.ledger.target(streamID)) {
		writeError(w, http.StatusUnprocessableEntity, CodeInvalidTransaction, "proof of work does not meet target")
		return
	}

	switch res, height, code := s.ledger.tryAccept(streamID, tx); res {
	case acceptDuplicateTx:
		writeReceipt(w, tx.TransactionID, s.ledger.sequenceFor(streamID, tx.TransactionID))
		s.hub.broadcast(height)
	case acceptDuplicateNote:
		writeError(w, http.StatusConflict, CodeDuplicateNoteID, "note_id already used by another transaction")
	case acceptRejected:
		status := http.StatusUnprocessableEntity
		switch code {
		case CodeUnauthorizedKey:
			status = http.StatusForbidden
		case CodeStreamNotFound:
			status = http.StatusNotFound
		case CodeStalePowEpoch:
			status = http.StatusConflict
		}
		writeError(w, status, code, code)
	default:
		writeReceipt(w, tx.TransactionID, s.ledger.sequenceFor(streamID, tx.TransactionID))
		s.hub.broadcast(height)
	}
}

func (s *Server) handleTip(w http.ResponseWriter, r *http.Request) {
	streamID, err := decodeStreamID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidEncoding, "invalid stream_id")
		return
	}
	tip, ok := s.ledger.tip(streamID)
	if !ok {
		writeError(w, http.StatusNotFound, CodeStreamNotFound, "stream not found")
		return
	}
	writeJSON(w, http.StatusOK, tip)
}

func (s *Server) handleHeaders(w http.ResponseWriter, r *http.Request) {
	s.handlePage(w, r, false)
}

func (s *Server) handleBlocks(w http.ResponseWriter, r *http.Request) {
	s.handlePage(w, r, true)
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request, withTx bool) {
	streamID, err := decodeStreamID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidEncoding, "invalid stream_id")
		return
	}
	q := r.URL.Query()
	fromHeight := parseInt64(q.Get("from_height"), 0)
	limit := parseInt64(q.Get("limit"), defaultBlockLimit)
	if limit <= 0 {
		limit = defaultBlockLimit
	}
	if withTx && limit > maxBlockLimit {
		limit = maxBlockLimit
	}
	if !withTx && limit > maxHeaderLimit {
		limit = maxHeaderLimit
	}
	known := decodeB64(q.Get("known_block_hash"))

	resp, status, code := s.ledger.readPage(streamID, fromHeight, limit, withTx, known)
	if status != http.StatusOK {
		writeError(w, status, code, code)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeReceipt(w http.ResponseWriter, txID []byte, seq int64) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(receipt{
		TransactionID: base64.RawURLEncoding.EncodeToString(txID),
		StreamSeq:     seq,
	})
}

// proofResponse carries a canonical-CBOR MMR inclusion proof, base64url-encoded.
type proofResponse struct {
	Proof string `json:"proof"`
}

// handleProof serves an MMR inclusion proof for the leaf (transaction) at the
// given 0-indexed position. Servers MUST recompute the proof from the full,
// ordered leaf set (Phase 5 amendment, protocol-v1.md §171). The proof lets a
// client independently verify that a specific historical transaction is a member
// of the MMR whose root is the header's mmr_root — the core multi-node
// verification primitive.
func (s *Server) handleProof(w http.ResponseWriter, r *http.Request) {
	streamID, err := decodeStreamID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidEncoding, "invalid stream_id")
		return
	}
	idx := parseInt64(r.URL.Query().Get("leaf_index"), -1)
	if idx < 0 {
		writeError(w, http.StatusBadRequest, CodeInvalidEncoding, "missing or invalid leaf_index")
		return
	}
	proofCBOR, status, code := s.ledger.proofFor(streamID, idx)
	if status != http.StatusOK {
		writeError(w, status, code, code)
		return
	}
	writeJSON(w, http.StatusOK, proofResponse{Proof: base64.RawURLEncoding.EncodeToString(proofCBOR)})
}

func decodeStreamID(r *http.Request) ([]byte, error) {
	raw := r.PathValue("stream_id")
	if raw == "" {
		return nil, errors.New("missing stream_id")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(b) != 32 {
		return nil, errors.New("invalid stream_id")
	}
	return b, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func decodeB64(s string) []byte {
	if s == "" {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

func parseInt64(s string, def int64) int64 {
	if s == "" {
		return def
	}
	var v int64
	_, err := fmt.Sscanf(s, "%d", &v)
	if err != nil {
		return -1
	}
	return v
}

// marshalBlock wraps a stored block record into a single canonical CBOR block
// (header + block_hash + transaction) for transport. The client decodes it with
// protocol.DecodeBlock, which strictly rejects non-canonical CBOR, unknown
// fields, and trailing data — closing the CBOR trust boundary on the receive
// path (M1).
func marshalBlock(rec blockRecord) ([]byte, error) {
	tx, err := notesync.DecodeSignedTransaction(rec.txCBOR)
	if err != nil {
		return nil, err
	}
	return protocol.MarshalBlock(protocol.Block{
		Header:      rec.header,
		BlockHash:   rec.hash,
		Transaction: tx,
	})
}

// --- wire pagination shapes ---

type blockWireHeader struct {
	ProtocolVersion   uint64 `json:"protocol_version"`
	Height            uint64 `json:"height"`
	PreviousBlockHash string `json:"previous_block_hash"`
	TransactionCount  uint64 `json:"transaction_count"`
	MMRRoot           string `json:"mmr_root"`
	Timestamp         uint64 `json:"timestamp"`
	Nonce             uint64 `json:"nonce"`
	PowTarget         string `json:"pow_target"`
}

type headerEntry struct {
	Header    blockWireHeader `json:"header"`
	BlockHash string          `json:"block_hash"`
}

// blockEntry carries a full block as a single canonical CBOR item (header +
// block_hash + transaction) base64url-encoded. The client decodes it with
// protocol.DecodeBlock, which enforces canonical CBOR and rejects unknown,
// duplicate, or trailing fields — closing the CBOR trust boundary (M1).
type blockEntry struct {
	Block string `json:"block"`
}

type pageResponse struct {
	Headers        []headerEntry   `json:"headers,omitempty"`
	Blocks         []blockEntry    `json:"blocks,omitempty"`
	NextFromHeight *uint64         `json:"next_from_height"`
}

func toWireHeader(h protocol.BlockHeader) blockWireHeader {
	return blockWireHeader{
		ProtocolVersion:   h.ProtocolVersion,
		Height:            h.Height,
		PreviousBlockHash: b64(h.PreviousBlockHash),
		TransactionCount:  h.TransactionCount,
		MMRRoot:           b64(h.MMRRoot),
		Timestamp:         h.Timestamp,
		Nonce:             h.Nonce,
		PowTarget:         b64(h.PowTarget),
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
