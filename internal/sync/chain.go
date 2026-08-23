package sync

import (
	"bytes"
	"errors"
	"math/big"

	"github.com/jiangfire/snapnotes/internal/protocol"
)

// Header status values used by the multi-node chain index.
type HeaderStatus int

const (
	// StatusActive marks a header that is part of the currently selected best chain.
	StatusActive HeaderStatus = iota
	// StatusOrphaned marks a header that was verified but displaced by a chain with
	// strictly greater cumulative chainwork. Its transactions are not presented as
	// current until re-confirmed by a new active chain.
	StatusOrphaned
)

// Errors returned by the ChainManager when peer data fails verification.
var (
	ErrMalformedHeader = errors.New("malformed peer header: failed verification")
	ErrMissingParent   = errors.New("parent header not yet known; fetch in height order")
)

// VerifiedHeader is a block header the client has verified, together with the
// chain state needed for selection and reorganisation: its cumulative chainwork,
// the running MMR peaks snapshot after this block (for reorg restore), and its
// current status (active or orphaned).
type VerifiedHeader struct {
	Height    uint64
	Hash      []byte
	PrevHash  []byte
	MMRRoot   []byte
	Chainwork *big.Int
	Status    HeaderStatus
	Peaks     [][]byte // running MMR peaks after this block (snapshot for reorg restore)
	Finalized bool     // true once the full block (tx + mmr_root) has been verified
}

// ChainStore is the persistence surface the ChainManager needs. The in-memory
// implementation is used by tests and by the runtime headers-first sync; the
// SyncClient persists only the active tip into sqlite.SyncState.
type ChainStore interface {
	GenesisHash() []byte
	GetByHeight(h uint64) (VerifiedHeader, bool)
	GetByHash(hash []byte) (VerifiedHeader, bool)
	Put(h VerifiedHeader)
	Remove(h VerifiedHeader)
	ActiveTip() (VerifiedHeader, bool)
	MarkOrphanedFrom(height uint64)
	All() []VerifiedHeader
}

// memChainStore is an in-memory ChainStore. Headers are keyed by hash (the source
// of truth) so competing fork headers at the same height can coexist; a height→
// hashes index supports height lookups.
type memChainStore struct {
	genesis  []byte
	byHash   map[string]VerifiedHeader
	byHeight map[uint64][]string
}

func newMemChainStore(genesisHash []byte) *memChainStore {
	return &memChainStore{
		genesis:  append([]byte(nil), genesisHash...),
		byHash:   make(map[string]VerifiedHeader),
		byHeight: make(map[uint64][]string),
	}
}

func (m *memChainStore) GenesisHash() []byte { return append([]byte(nil), m.genesis...) }

func (m *memChainStore) GetByHeight(h uint64) (VerifiedHeader, bool) {
	hashes, ok := m.byHeight[h]
	if !ok || len(hashes) == 0 {
		return VerifiedHeader{}, false
	}
	// Prefer an active header at this height; otherwise return the first.
	for _, hs := range hashes {
		if v, ok := m.byHash[hs]; ok && v.Status == StatusActive {
			return v, true
		}
	}
	return m.byHash[hashes[0]], true
}

func (m *memChainStore) GetByHash(hash []byte) (VerifiedHeader, bool) {
	v, ok := m.byHash[string(hash)]
	return v, ok
}

func (m *memChainStore) Put(h VerifiedHeader) {
	key := string(h.Hash)
	m.byHash[key] = h
	found := false
	for _, hs := range m.byHeight[h.Height] {
		if hs == key {
			found = true
			break
		}
	}
	if !found {
		m.byHeight[h.Height] = append(m.byHeight[h.Height], key)
	}
}

func (m *memChainStore) Remove(h VerifiedHeader) {
	key := string(h.Hash)
	delete(m.byHash, key)
	hs := m.byHeight[h.Height]
	for i, k := range hs {
		if k == key {
			m.byHeight[h.Height] = append(hs[:i], hs[i+1:]...)
			break
		}
	}
}

func (m *memChainStore) ActiveTip() (VerifiedHeader, bool) {
	var best VerifiedHeader
	ok := false
	for _, v := range m.byHash {
		if v.Status != StatusActive {
			continue
		}
		if !ok || v.Height > best.Height {
			best = v
			ok = true
		}
	}
	return best, ok
}

func (m *memChainStore) MarkOrphanedFrom(height uint64) {
	for _, v := range m.byHash {
		if v.Status == StatusActive && v.Height >= height {
			v.Status = StatusOrphaned
			m.byHash[string(v.Hash)] = v
		}
	}
}

func (m *memChainStore) All() []VerifiedHeader {
	out := make([]VerifiedHeader, 0, len(m.byHash))
	for _, v := range m.byHash {
		out = append(out, v)
	}
	return out
}

// ChainManager applies peer headers and blocks using the Phase 5 headers-first
// protocol: validate each header, select the best chain by cumulative chainwork
// (never by height), and reorganise when a competing chain with strictly greater
// chainwork shares the genesis anchor. Malformed peer data never advances verified
// state.
type ChainManager struct {
	store    ChainStore
	targetAt func(uint64) []byte
	streamID []byte // stream this chain belongs to (from the trust anchor)
}

// NewChainManager builds a manager over the given store. genesisTarget is the PoW
// target in effect for the first 1000 blocks (from the out-of-band trust anchor);
// after every 1000 blocks the target is retargeted per project-spec §10.3.
// streamID is the verified stream identifier from the out-of-band trust anchor.
func NewChainManager(store ChainStore, genesisTarget, streamID []byte) *ChainManager {
	return &ChainManager{
		store:    store,
		streamID: append([]byte(nil), streamID...),
		targetAt: func(uint64) []byte {
			return append([]byte(nil), genesisTarget...)
		},
	}
}

// SetTargetSchedule overrides the per-height PoW target. The default always
// returns the genesis target; callers (and tests) use this to model difficulty
// retargets or forks mined at a different difficulty.
func (m *ChainManager) SetTargetSchedule(f func(uint64) []byte) {
	m.targetAt = f
}

// SeedGenesis records the verified genesis header as the initial active chain. It
// must be called before any ApplyHeader/FinalizeBlock so linkage and chainwork
// have a root. peaks is the single peak after the genesis leaf.
func (m *ChainManager) SeedGenesis(hash, prevHash, mmrRoot []byte, peaks [][]byte) {
	h := VerifiedHeader{
		Height:    0,
		Hash:      append([]byte(nil), hash...),
		PrevHash:  append([]byte(nil), prevHash...),
		MMRRoot:   append([]byte(nil), mmrRoot...),
		Chainwork: protocol.BlockWork(m.targetAt(0)),
		Status:    StatusActive,
		Peaks:     peaks,
		Finalized: true,
	}
	m.store.Put(h)
}

// SeedActive records an already-verified active tip (e.g. restored from
// sqlite.SyncState) so the manager continues from the device's last position.
func (m *ChainManager) SeedActive(h VerifiedHeader) {
	h.Status = StatusActive
	m.store.Put(h)
}

// ApplyHeader validates a single block header and updates chain selection. It
// checks canonical structure indirectly (the caller decodes strictly), the
// recomputed block hash, previous_block_hash linkage, and genesis anchoring, then
// records cumulative chainwork and recomputes the active chain. A header that
// fails verification returns ErrMalformedHeader/ErrMissingParent/ErrChainMismatch
// and leaves verified state unchanged.
func (m *ChainManager) ApplyHeader(header protocol.BlockHeader, blockHash []byte) error {
	// 1. Recomputed block hash must match the transmitted hash (catches tampering).
	recomputed, err := protocol.BlockHash(header)
	if err != nil {
		return err
	}
	if !bytes.Equal(recomputed, blockHash) {
		return ErrMalformedHeader
	}

	// 2. Genesis / linkage validation.
	if header.Height == 0 {
		if !isZero(header.PreviousBlockHash) {
			return ErrMalformedHeader
		}
		if !bytes.Equal(blockHash, m.store.GenesisHash()) {
			return ErrChainMismatch
		}
	} else {
		parent, ok := m.store.GetByHash(header.PreviousBlockHash)
		if !ok {
			return ErrMissingParent
		}
		if !bytes.Equal(header.PreviousBlockHash, parent.Hash) {
			return ErrMalformedHeader
		}
	}

	// 3. Cumulative chainwork along this header's own parent chain.
	var parentWork *big.Int
	var parentPeaks [][]byte
	if header.Height == 0 {
		parentWork = big.NewInt(0)
	} else {
		parent, _ := m.store.GetByHash(header.PreviousBlockHash)
		parentWork = parent.Chainwork
		parentPeaks = parent.Peaks
	}
	blockWork := protocol.BlockWork(m.targetAt(header.Height))
	chainwork := new(big.Int).Add(parentWork, blockWork)

	// 4. Record the header (peaks here is the snapshot to extend at finalize).
	newHeader := VerifiedHeader{
		Height:    header.Height,
		Hash:      append([]byte(nil), blockHash...),
		PrevHash:  append([]byte(nil), header.PreviousBlockHash...),
		MMRRoot:   append([]byte(nil), header.MMRRoot...),
		Chainwork: chainwork,
		Status:    StatusActive,
		Peaks:     parentPeaks,
		Finalized: false,
	}
	m.store.Put(newHeader)

	// 5. Recompute which chain is active from the verified headers.
	m.recomputeActive()
	return nil
}

// FinalizeBlock verifies the full block body against a previously applied header:
// transaction id/signature/stream consistency and MMR root continuity. On success
// the header is marked finalized and its peaks snapshot is advanced to include the
// new leaf. On failure no verified state advances and ErrMalformedHeader is
// returned.
func (m *ChainManager) FinalizeBlock(b protocol.Block, tx Transaction) error {
	header := b.Header
	blockHash := b.BlockHash

	stored, ok := m.store.GetByHash(blockHash)
	if !ok {
		// Apply the header first, then finalize.
		if err := m.ApplyHeader(header, blockHash); err != nil {
			return err
		}
		stored, _ = m.store.GetByHash(blockHash)
	}

	// Idempotent: a block already finalized keeps its verified peaks snapshot;
	// re-finalising would double-append its leaf to the MMR peaks.
	if stored.Finalized {
		return nil
	}

	// Transaction consistency.
	recomputedID, err := protocol.TransactionID(tx.UnsignedBody)
	if err != nil {
		return err
	}
	if !bytes.Equal(recomputedID, tx.TransactionID) {
		m.removeHeader(stored)
		return ErrMalformedHeader
	}
	if !protocol.VerifySignature(tx.UnsignedBody, tx.Signature, tx.AuthorPublicKey) {
		m.removeHeader(stored)
		return ErrMalformedHeader
	}
	if !bytes.Equal(tx.StreamID, m.streamID) {
		m.removeHeader(stored)
		return ErrMalformedHeader
	}

	// MMR root continuity: append this leaf to the parent's verified peaks
	// snapshot and verify it reproduces the header's mmr_root. The parent peaks
	// come from the already-finalized parent header (which carries the full
	// ancestor-leaf accumulator), never from the stale header-phase snapshot —
	// otherwise each block would start the accumulator from nil and the root
	// would never match the server's cumulative MMR.
	var parentPeaks [][]byte
	if header.Height != 0 {
		parent, ok := m.store.GetByHash(header.PreviousBlockHash)
		if !ok {
			return ErrMissingParent
		}
		parentPeaks = parent.Peaks
	}
	txHash, err := protocol.TransactionHash(tx.UnsignedBody, tx.TransactionID, tx.Signature, tx.PowEpoch, tx.PowNonce)
	if err != nil {
		return err
	}
	leaf := protocol.LeafHash(txHash)
	newPeaks := protocol.AddLeaf(parentPeaks, leaf, header.Height)
	if !bytes.Equal(protocol.MMRRootFromPeaks(newPeaks), header.MMRRoot) {
		m.removeHeader(stored)
		return ErrMalformedHeader
	}

	stored.Peaks = newPeaks
	stored.Finalized = true
	m.store.Put(stored)
	return nil
}

// removeHeader deletes a header that failed finalization so it never advances
// verified state. It is only safe to call for a header that was just applied in
// the same operation and has no children yet.
func (m *ChainManager) removeHeader(h VerifiedHeader) {
	m.store.Remove(h)
	m.recomputeActive()
}

// recomputeActive selects the best chain: the tip with the greatest cumulative
// (chainwork, block_hash) lexicographic pair, then walks back to genesis marking
// that path active and everything else orphaned.
func (m *ChainManager) recomputeActive() {
	headers := m.store.All()
	if len(headers) == 0 {
		return
	}
	best := headers[0]
	for _, h := range headers[1:] {
		if protocol.SelectBestChain(h.Chainwork, h.Hash, best.Chainwork, best.Hash) {
			best = h
		}
	}
	activeSet := make(map[string]bool)
	cur := best
	for {
		activeSet[string(cur.Hash)] = true
		if cur.Height == 0 {
			break
		}
		parent, ok := m.store.GetByHash(cur.PrevHash)
		if !ok {
			break
		}
		cur = parent
	}
	updated := make([]VerifiedHeader, len(headers))
	for i, h := range headers {
		if activeSet[string(h.Hash)] {
			h.Status = StatusActive
		} else {
			h.Status = StatusOrphaned
		}
		updated[i] = h
	}
	for _, h := range updated {
		m.store.Put(h)
	}
}

// OrphanedHeaders returns all verified headers currently marked orphaned (the
// records displaced by the active chain).
func (m *ChainManager) OrphanedHeaders() []VerifiedHeader {
	var out []VerifiedHeader
	for _, h := range m.store.All() {
		if h.Status == StatusOrphaned {
			out = append(out, h)
		}
	}
	return out
}
