package protocol

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"math/big"
)

// BlockHeader is the canonical CBOR header shared by every chain block. The
// mmr_root field carries the peak-bagging MMR root defined in the Phase 5 amendment
// (see mmr.go): a bagged hash of all mountain summits over the stream's leaf hashes.
//
// Nonce and PowTarget make the header mineable: the block hash is taken over the
// canonical CBOR of this header (including Nonce and PowTarget), so a miner can
// search for a Nonce whose resulting hash is numerically <= PowTarget. PowTarget is
// embedded in the header so each block is self-verifying — a verifier needs no
// out-of-band schedule to check the work.
type BlockHeader struct {
	ProtocolVersion   uint64 `cbor:"protocol_version"`
	Height            uint64 `cbor:"height"`
	PreviousBlockHash []byte `cbor:"previous_block_hash"`
	TransactionCount  uint64 `cbor:"transaction_count"`
	MMRRoot           []byte `cbor:"mmr_root"`
	Timestamp         uint64 `cbor:"timestamp"`
	Nonce             uint64 `cbor:"nonce"`
	PowTarget         []byte `cbor:"pow_target"`
}

// MaxPoWTrials bounds a single MineBlock call so a caller can stay responsive; a
// real miner loops MineBlock with fresh randomness until ok is true.
const MaxPoWTrials uint64 = 1 << 20

// hashBigEndianAsBigInt interprets a 32-byte hash as a big-endian unsigned integer.
func hashBigEndianAsBigInt(h []byte) *big.Int {
	return new(big.Int).SetBytes(h)
}

// BlockSatisfiesTarget reports whether the header's own block hash meets its
// declared PoW target (hash <= target, big-endian unsigned comparison). An empty
// PowTarget means "no work required" (used only for test/fixture headers).
func BlockSatisfiesTarget(header BlockHeader) bool {
	if len(header.PowTarget) == 0 {
		return true
	}
	hash, err := BlockHash(header)
	if err != nil {
		return false
	}
	return hashBigEndianAsBigInt(hash).Cmp(hashBigEndianAsBigInt(header.PowTarget)) <= 0
}

// MineBlock searches for a Nonce such that BlockHash(header with that Nonce and the
// given PowTarget) is numerically <= target. It sets header.PowTarget = target,
// then iterates Nonce from a random start for up to maxTrials attempts. It returns
// the winning nonce and the resulting block hash. ok is false if no nonce satisfied
// the target within maxTrials (caller should retry with more trials / fresh start).
func MineBlock(header BlockHeader, target []byte, maxTrials uint64, randomness io.Reader) (nonce uint64, hash []byte, ok bool) {
	if randomness == nil {
		randomness = rand.Reader
	}
	if maxTrials == 0 {
		maxTrials = MaxPoWTrials
	}
	hdr := header
	hdr.PowTarget = target
	// Random starting offset so concurrent/retry miners don't scan the same range.
	var seed [8]byte
	if _, err := io.ReadFull(randomness, seed[:]); err == nil {
		nonce = hashBigEndianAsBigInt(seed[:]).Uint64()
	}
	limit := nonce + maxTrials
	for {
		hdr.Nonce = nonce
		h, err := BlockHash(hdr)
		if err == nil && BlockSatisfiesTarget(hdr) {
			return nonce, h, true
		}
		nonce++
		if nonce == limit {
			// If we wrapped exactly onto the limit, stop; otherwise the for-loop
			// condition handles termination.
			if limit == 0 {
				return 0, nil, false
			}
		}
		if nonce == 0 {
			// wrapped around uint64; stop to avoid infinite loop
			return 0, nil, false
		}
		_ = h
	}
}

// Block couples a header with its canonical transaction CBOR, so a client can
// re-verify the transaction without re-downloading it separately.
type Block struct {
	Header      BlockHeader
	BlockHash   []byte
	Transaction SignedTransaction
}

// SignedTransaction is a fully prepared protocol transaction (canonical CBOR
// wire form). It is the protocol-level counterpart of sync.Transaction and
// intentionally lives here so the protocol package has no dependency on sync.
type SignedTransaction struct {
	UnsignedBody  UnsignedBody `cbor:"unsigned_body"`
	TransactionID []byte       `cbor:"transaction_id"`
	Signature     []byte       `cbor:"signature"`
	PowEpoch      uint64       `cbor:"pow_epoch"`
	PowNonce      uint64       `cbor:"pow_nonce"`
}

// BlockHash returns the deterministic SHA-256 block hash over the canonical CBOR
// header with the domain separator "snapnotes/block/v1".
func BlockHash(h BlockHeader) ([]byte, error) {
	data, err := canonicalEncoder.Marshal(h)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(append([]byte("snapnotes/block/v1"), data...))
	return sum[:], nil
}

// MarshalBlockHeader serialises a BlockHeader to strict canonical CBOR, matching
// the encoding DecodeBlockHeader expects. The ledger uses it to persist block
// headers to disk so they can be rebuilt verbatim on restart.
func MarshalBlockHeader(h BlockHeader) ([]byte, error) {
	return canonicalEncoder.Marshal(h)
}

// DecodeBlockHeader parses a strict canonical CBOR BlockHeader.
func DecodeBlockHeader(data []byte) (BlockHeader, error) {
	var h BlockHeader
	rest, err := strictDecoder.UnmarshalFirst(data, &h)
	if err != nil {
		return BlockHeader{}, err
	}
	if len(rest) != 0 {
		return BlockHeader{}, errors.New("trailing CBOR data in block header")
	}
	canonical, err := canonicalEncoder.Marshal(h)
	if err != nil || !bytes.Equal(canonical, data) {
		return BlockHeader{}, errors.New("block header is not canonical CBOR")
	}
	return h, nil
}

// LeafHash returns the MMR leaf hash for a transaction hash, using the
// "snapnotes/mmr-leaf/v1" domain. The leaf hashes are the inputs to the peak-bagging
// MMR defined in mmr.go.
func LeafHash(transactionHash []byte) []byte {
	sum := sha256.Sum256(append([]byte("snapnotes/mmr-leaf/v1"), transactionHash...))
	return sum[:]
}

// MarshalBlock serialises a block's header and transaction into a single
// canonical CBOR item for transport: { header, block_hash, transaction }.
func MarshalBlock(b Block) ([]byte, error) {
	return canonicalEncoder.Marshal(blockWire{
		Header:      b.Header,
		BlockHash:   b.BlockHash,
		Transaction: b.Transaction,
	})
}

type blockWire struct {
	Header      BlockHeader       `cbor:"header"`
	BlockHash   []byte            `cbor:"block_hash"`
	Transaction SignedTransaction `cbor:"transaction"`
}

// DecodeBlock parses a canonical CBOR block produced by MarshalBlock.
func DecodeBlock(data []byte) (Block, error) {
	var w blockWire
	rest, err := strictDecoder.UnmarshalFirst(data, &w)
	if err != nil {
		return Block{}, err
	}
	if len(rest) != 0 {
		return Block{}, errors.New("trailing CBOR data in block")
	}
	canonical, err := canonicalEncoder.Marshal(w)
	if err != nil || !bytes.Equal(canonical, data) {
		return Block{}, errors.New("block is not canonical CBOR")
	}
	return Block{Header: w.Header, BlockHash: w.BlockHash, Transaction: w.Transaction}, nil
}
