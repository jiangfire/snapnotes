package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// deterministicLeaf returns a stable leaf hash for index i, simulating a
// transaction hash. It is deterministic so the tests are reproducible.
func deterministicLeaf(i int) []byte {
	h := sha256.Sum256([]byte("snapnotes-test-leaf-" + string(rune('A'+i%26)) + itoa(i)))
	return h[:]
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

func buildLeaves(n int) [][]byte {
	leaves := make([][]byte, n)
	for i := range leaves {
		leaves[i] = LeafHash(deterministicLeaf(i))
	}
	return leaves
}

// Pinned byte-exact MMR roots (Task 5.1 interoperability vector #1: empty/1/7/100).
const (
	root1   = "5f3784875969d1c7ea486869076123662103a9ee4730e1582c69dc2816e514cc"
	root7   = "f539a9923d300d499e280a76133e6976eb1c56f1333bd906e44e3db3c2d0e102"
	root100 = "d5c8deac8063d8c3e5af7dfd4223977eddb6983b54d305a1b26b10d2311cd866"
)

// Pinned inclusion-proof fixture for leaf 3 of a 7-leaf MMR (vector #2).
var (
	proof7LeafHash = "5d65052fd93653252ae728db83d235b77b40d31c2dbdd63dc085c0aa3243882a"
	proof7Peaks    = []string{
		"7e1b61d575fc635f8ea7d32ee667290dc0f885dd2d7d70ed7be8b15872eeadb6",
		"6db12387475fbc12310fa847e9a87c0e692479ab0534ba6bb624f1bb0d5b19ca",
		"b5cc0fa540fdff0e8ac45608762c47fa3626d9eb3113b2dbc7ccd2f07f381532",
	}
	proof7Proof = []string{
		"843bac4e525e74c62dc3fa10c662826f604eb455cdf79265cd036e2c4786f9f0",
		"abddd91ccbedaca3330c3a13528d9dbd8eee6ed35913826db52abe7cc62e740c",
	}
)

// TestMMRRootByteExact asserts the bagged MMR roots match the approved fixtures.
func TestMMRRootByteExact(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{1, root1},
		{7, root7},
		{100, root100},
	}
	for _, c := range cases {
		got := MMRRootFromLeaves(buildLeaves(c.n))
		if hex.EncodeToString(got) != c.want {
			t.Fatalf("MMRRoot n=%d = %s, want %s", c.n, hex.EncodeToString(got), c.want)
		}
	}
	// Empty MMR root is 32 zero bytes (vector #1 special case).
	if !bytes.Equal(MMRRootFromLeaves(nil), make([]byte, 32)) {
		t.Fatal("empty MMR root is not 32 zero bytes")
	}
}

// TestMMRRootMatchesAddLeaf confirms the incremental AddLeaf path produces the same
// peaks as the direct mountain-summit path.
func TestMMRRootMatchesAddLeaf(t *testing.T) {
	leaves := buildLeaves(100)
	peaks := [][]byte{}
	for i := range leaves {
		peaks = AddLeaf(peaks, leaves[i], uint64(i))
	}
	if !bytes.Equal(MMRRootFromPeaks(peaks), hexBytes(root100)) {
		t.Fatal("AddLeaf peaks root differs from MMRRootFromLeaves")
	}
}

// TestInclusionProofByteExact pins the proof fixture and verifies it.
func TestInclusionProofByteExact(t *testing.T) {
	leaves := buildLeaves(7)
	root := hexBytes(root7)
	p, err := GenerateInclusionProof(leaves, 3)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(p.LeafHash) != proof7LeafHash {
		t.Fatalf("leaf_hash = %s, want %s", hex.EncodeToString(p.LeafHash), proof7LeafHash)
	}
	if len(p.Peaks) != len(proof7Peaks) {
		t.Fatalf("peaks len = %d, want %d", len(p.Peaks), len(proof7Peaks))
	}
	for i, want := range proof7Peaks {
		if hex.EncodeToString(p.Peaks[i]) != want {
			t.Fatalf("peaks[%d] = %s, want %s", i, hex.EncodeToString(p.Peaks[i]), want)
		}
	}
	if len(p.Proof) != len(proof7Proof) {
		t.Fatalf("proof len = %d, want %d", len(p.Proof), len(proof7Proof))
	}
	for i, want := range proof7Proof {
		if hex.EncodeToString(p.Proof[i]) != want {
			t.Fatalf("proof[%d] = %s, want %s", i, hex.EncodeToString(p.Proof[i]), want)
		}
	}
	if !VerifyInclusionProof(p, root, 7) {
		t.Fatal("pinned inclusion proof failed to verify")
	}
}

// TestInclusionProofRoundTrip verifies every leaf of several MMR sizes proves and
// verifies cleanly.
func TestInclusionProofRoundTrip(t *testing.T) {
	for _, n := range []int{1, 2, 3, 5, 7, 8, 13, 100} {
		leaves := buildLeaves(n)
		root := MMRRootFromLeaves(leaves)
		for pos := 0; pos < n; pos++ {
			p, err := GenerateInclusionProof(leaves, pos)
			if err != nil {
				t.Fatalf("n=%d pos=%d: %v", n, pos, err)
			}
			if !VerifyInclusionProof(p, root, uint64(n)) {
				t.Fatalf("n=%d pos=%d: proof did not verify", n, pos)
			}
		}
	}
}

// TestInclusionProofTamper proves altering any component fails verification
// (Task 5.1 acceptance: "Altering a transaction, sibling hash, peak, or root
// fails verification").
func TestInclusionProofTamper(t *testing.T) {
	leaves := buildLeaves(7)
	root := MMRRootFromLeaves(leaves)
	p, err := GenerateInclusionProof(leaves, 3)
	if err != nil {
		t.Fatal(err)
	}

	// Altering the leaf hash (the "transaction") must fail.
	tampered := *p
	tampered.LeafHash = append([]byte(nil), p.LeafHash...)
	tampered.LeafHash[0] ^= 0xff
	if VerifyInclusionProof(&tampered, root, 7) {
		t.Fatal("altered leaf_hash verified")
	}

	// Altering a sibling hash in the proof must fail.
	tampered = *p
	tampered.Proof = append([][]byte(nil), p.Proof...)
	tampered.Proof[0] = append([]byte(nil), p.Proof[0]...)
	tampered.Proof[0][0] ^= 0xff
	if VerifyInclusionProof(&tampered, root, 7) {
		t.Fatal("altered sibling hash verified")
	}

	// Altering a peak must fail.
	tampered = *p
	tampered.Peaks = append([][]byte(nil), p.Peaks...)
	tampered.Peaks[0] = append([]byte(nil), p.Peaks[0]...)
	tampered.Peaks[0][0] ^= 0xff
	if VerifyInclusionProof(&tampered, root, 7) {
		t.Fatal("altered peak verified")
	}

	// Altering the claimed root must fail.
	badRoot := append([]byte(nil), root...)
	badRoot[0] ^= 0xff
	if VerifyInclusionProof(p, badRoot, 7) {
		t.Fatal("altered root verified")
	}

	// Out-of-range leaf index must fail.
	oob := *p
	oob.LeafIndex = 99
	if VerifyInclusionProof(&oob, root, 7) {
		t.Fatal("out-of-range leaf_index verified")
	}

	// Wrong total leaf count must fail.
	if VerifyInclusionProof(p, root, 8) {
		t.Fatal("wrong total leaf count verified")
	}

	// A real, unmodified proof still verifies (sanity).
	if !VerifyInclusionProof(p, root, 7) {
		t.Fatal("unmodified proof failed to verify")
	}
}

// TestMMRProofMarshalRoundTrip checks the proof serialises to canonical CBOR and
// decodes back identically (deterministic, no unknown/trailing fields).
func TestMMRProofMarshalRoundTrip(t *testing.T) {
	leaves := buildLeaves(7)
	p, err := GenerateInclusionProof(leaves, 3)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := MarshalMMRProof(p)
	if err != nil {
		t.Fatal(err)
	}
	// Determinism.
	cb2, err := MarshalMMRProof(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cb, cb2) {
		t.Fatal("MMR proof marshalling is not deterministic")
	}
	dec, err := DecodeMMRProof(cb)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyInclusionProof(dec, MMRRootFromLeaves(leaves), 7) {
		t.Fatal("decoded proof failed to verify")
	}
}

func hexBytes(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}
