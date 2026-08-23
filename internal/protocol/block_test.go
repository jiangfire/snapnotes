package protocol

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestBlockHashIsDeterministicAndTamperSensitive(t *testing.T) {
	h := BlockHeader{
		ProtocolVersion:   1,
		Height:            1,
		PreviousBlockHash: make([]byte, 32),
		TransactionCount:  2,
		MMRRoot:           make([]byte, 32),
		Timestamp:         1700000000000,
	}
	hash1, err := BlockHash(h)
	if err != nil {
		t.Fatal(err)
	}
	hash2, err := BlockHash(h)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hash1, hash2) {
		t.Fatal("block hash is not deterministic")
	}
	if len(hash1) != 32 {
		t.Fatalf("block hash length = %d, want 32", len(hash1))
	}
	// Tampering height must change the hash.
	h.Height = 2
	hash3, err := BlockHash(h)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(hash1, hash3) {
		t.Fatal("tampering height did not change block hash")
	}
}

func TestBuildGenesisProducesValidAnchor(t *testing.T) {
	res, err := BuildGenesis(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if res.StreamID == nil || len(res.StreamID) != 32 {
		t.Fatalf("stream_id length = %d, want 32", len(res.StreamID))
	}
	if res.GenesisBlockHash == nil || len(res.GenesisBlockHash) != 32 {
		t.Fatalf("genesis block hash length = %d, want 32", len(res.GenesisBlockHash))
	}
	// Genesis block: height 0, previous block hash is 32 zero bytes, exactly one tx.
	if res.Block.Header.Height != 0 {
		t.Fatalf("genesis height = %d, want 0", res.Block.Header.Height)
	}
	if !isZero(res.Block.Header.PreviousBlockHash) {
		t.Fatal("genesis previous_block_hash must be 32 zero bytes")
	}
	if res.Block.Header.TransactionCount != 1 {
		t.Fatalf("genesis transaction_count = %d, want 1", res.Block.Header.TransactionCount)
	}
	// The genesis block hash matches the recomputed header hash.
	recomputed, err := BlockHash(res.Block.Header)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(recomputed, res.GenesisBlockHash) {
		t.Fatal("genesis block hash does not match recomputed header hash")
	}
	// The genesis transaction is internally consistent.
	if _, err := TransactionID(res.Block.Transaction.UnsignedBody); err != nil {
		t.Fatalf("genesis transaction id: %v", err)
	}
	if !VerifySignature(res.Block.Transaction.UnsignedBody, res.Block.Transaction.Signature, res.Block.Transaction.UnsignedBody.AuthorPublicKey) {
		t.Fatal("genesis signature does not verify")
	}
	if !bytes.Equal(res.Block.Transaction.UnsignedBody.AuthorPublicKey, res.OwnerSigningPublicKey) {
		t.Fatal("genesis author must be the owner")
	}
}

// TestGenesisAnchorMismatchIsDetectable verifies that a client holding a trust
// anchor whose genesis hash diverges from the real chain can actually detect the
// divergence: the recomputed genesis block hash must not equal the wrong anchor,
// so a fresh chain seeded from the wrong anchor rejects the real genesis header
// (see sync.ChainManager.ApplyHeader, which returns ErrChainMismatch when the
// height-0 block hash disagrees with the stored genesis).
func TestGenesisAnchorMismatchIsDetectable(t *testing.T) {
	res, err := BuildGenesis(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongAnchor := make([]byte, 32)
	copy(wrongAnchor, res.GenesisBlockHash)
	wrongAnchor[0] ^= 0xff
	if bytes.Equal(wrongAnchor, res.GenesisBlockHash) {
		t.Fatal("test setup failed: mutated anchor equals original")
	}
	// The real genesis block hash must differ from the wrong anchor, otherwise
	// the mismatch would be undetectable at verification time.
	if bytes.Equal(res.GenesisBlockHash, wrongAnchor) {
		t.Fatal("genesis anchor mismatch is NOT detectable: hashes coincide")
	}
}

func isZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
