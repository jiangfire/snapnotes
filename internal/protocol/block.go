package protocol

import (
	"bytes"
	"crypto/sha256"
	"errors"
)

// BlockHeader is the canonical CBOR header shared by every chain block. The
// mmr_root field carries the peak-bagging MMR root defined in the Phase 5 amendment
// (see mmr.go): a bagged hash of all mountain summits over the stream's leaf hashes.
type BlockHeader struct {
	ProtocolVersion   uint64 `cbor:"protocol_version"`
	Height            uint64 `cbor:"height"`
	PreviousBlockHash []byte `cbor:"previous_block_hash"`
	TransactionCount  uint64 `cbor:"transaction_count"`
	MMRRoot           []byte `cbor:"mmr_root"`
	Timestamp         uint64 `cbor:"timestamp"`
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
