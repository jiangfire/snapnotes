package protocol

import (
	"bytes"
	"crypto/sha256"
	"errors"
)

// MMR domain separators (Phase 5 amendment to protocol-v1.md). LeafHash and its
// "snapnotes/mmr-leaf/v1" separator live in block.go to avoid a duplicate
// declaration; they are reused here unchanged.
const (
	mmrNodeDomain    = "snapnotes/mmr-node/v1"    // hash of two child node hashes
	mmrPeakBagDomain = "snapnotes/mmr-peak-bag/v1" // hash of all bagged peaks
)

// Node hashes two child node hashes (left then right) into their parent node hash.
func Node(left, right []byte) []byte {
	h := sha256.New()
	h.Write([]byte(mmrNodeDomain))
	h.Write(left)
	h.Write(right)
	return h.Sum(nil)
}

// EmptyMMRRoot is the root of an MMR with zero leaves: 32 zero bytes. It is only
// used as the starting sentinel; a committed block always has at least the genesis
// leaf, so the empty root never appears on a real block header.
func EmptyMMRRoot() []byte {
	return make([]byte, 32)
}

// AddLeaf appends a leaf hash to an MMR described by its current peaks and returns
// the new peaks. leafCount is the number of leaves already present. Peaks are kept
// ordered by ascending height; this matches the set bits of leafCount, so a verifier
// can reconstruct peak heights from the leaf count alone.
//
// The merge rule mirrors the standard peak-bagging MMR: the new leaf climbs while
// the i-th bit of leafCount is set, merging with peaks[i] at each step, then the
// resulting node is prepended above the consumed peaks.
func AddLeaf(peaks [][]byte, leafHash []byte, leafCount uint64) [][]byte {
	node := append([]byte(nil), leafHash...)
	i := 0
	for i < len(peaks) && (leafCount&(1<<i)) != 0 {
		node = Node(peaks[i], node)
		i++
	}
	res := make([][]byte, 0, len(peaks)+1)
	res = append(res, node)
	res = append(res, peaks[i:]...)
	return res
}

// MMRRootFromPeaks returns the bagged MMR root over the given peaks (ordered by
// ascending height). An empty peak list yields the empty root (32 zero bytes).
func MMRRootFromPeaks(peaks [][]byte) []byte {
	if len(peaks) == 0 {
		return EmptyMMRRoot()
	}
	h := sha256.New()
	h.Write([]byte(mmrPeakBagDomain))
	for _, p := range peaks {
		h.Write(p)
	}
	return h.Sum(nil)
}

// MMRRootFromLeaves builds the peak-bagging root directly from a full, ordered set
// of leaf hashes. It is the reference used for correctness checks and tests.
func MMRRootFromLeaves(leaves [][]byte) []byte {
	peaks := [][]byte{}
	for i := range leaves {
		peaks = AddLeaf(peaks, leaves[i], uint64(i))
	}
	return MMRRootFromPeaks(peaks)
}

// mountainStart returns the leaf offset of the mountain of height bit b within an MMR
// of n leaves. Mountains are laid out left-to-right by descending height, so the
// offset of bit b is the sum of the sizes (2^c) of all higher set bits.
func mountainStart(n, b int) int {
	s := 0
	for c := b + 1; c < 64; c++ {
		if n&(1<<c) != 0 {
			s += 1 << c
		}
	}
	return s
}

// merkleRoot returns the root hash of the perfect binary subtree covering leaves
// [lo, lo+size), where size is a power of two.
func merkleRoot(leaves [][]byte, lo, size int) []byte {
	if size == 1 {
		return append([]byte(nil), leaves[lo]...)
	}
	half := size / 2
	return Node(merkleRoot(leaves, lo, half), merkleRoot(leaves, lo+half, half))
}

// mountainSummits returns the summit (peak) of every mountain in an MMR of n leaves,
// ordered by ascending height. Each mountain is a perfect tree of size 2^b for each
// set bit b of n.
func mountainSummits(leaves [][]byte, n int) [][]byte {
	peaks := [][]byte{}
	for b := 0; b < 64; b++ {
		if n&(1<<b) == 0 {
			continue
		}
		start := mountainStart(n, b)
		peaks = append(peaks, merkleRoot(leaves, start, 1<<b))
	}
	return peaks
}

// MMRInclusionProof is the wire form of a proof that a leaf belongs to an MMR.
// It follows the Phase 5 amendment: leaf_index and leaf_hash identify the proven
// leaf; peaks are all mountain summits (including the proven leaf's own summit),
// ordered by ascending height; proof are the sibling subtree hashes from the leaf
// up to, but excluding, its summit.
type MMRInclusionProof struct {
	LeafIndex uint64   `cbor:"leaf_index"`
	LeafHash  []byte   `cbor:"leaf_hash"`
	Peaks     [][]byte `cbor:"peaks"`
	Proof     [][]byte `cbor:"proof"`
}

// GenerateInclusionProof produces a proof for the leaf at position pos (0-indexed)
// within the ordered leaf set leaves.
func GenerateInclusionProof(leaves [][]byte, pos int) (*MMRInclusionProof, error) {
	n := len(leaves)
	if pos < 0 || pos >= n {
		return nil, errors.New("mmr: leaf index out of range")
	}
	for b := 63; b >= 0; b-- {
		if n&(1<<b) == 0 {
			continue
		}
		start := mountainStart(n, b)
		size := 1 << b
		if pos < start || pos >= start+size {
			continue
		}
		local := pos - start
		proof := [][]byte{}
		node := append([]byte(nil), leaves[pos]...)
		for j := 0; j < b; j++ {
			sub := local >> j                  // index of this node's subtree at level j
			sibIdx := sub ^ 1                  // sibling subtree index
			sibStart := start + (sibIdx << j)  // sibling subtree's first leaf
			sibHash := merkleRoot(leaves, sibStart, 1<<j)
			if sub&1 == 0 {
				node = Node(node, sibHash) // current is the left child
			} else {
				node = Node(sibHash, node) // current is the right child
			}
			proof = append(proof, sibHash)
		}
		// node is now the summit (peak) of this mountain at height b.
		return &MMRInclusionProof{
			LeafIndex: uint64(pos),
			LeafHash:  append([]byte(nil), leaves[pos]...),
			Peaks:     mountainSummits(leaves, n),
			Proof:     proof,
		}, nil
	}
	return nil, errors.New("mmr: unable to locate leaf mountain")
}

// VerifyInclusionProof returns true iff the proven leaf is a member of the MMR whose
// bagged root is claimedRoot. totalLeaves is the number of leaves in the full MMR
// (used to locate the leaf's mountain and derive peak positions).
func VerifyInclusionProof(p *MMRInclusionProof, claimedRoot []byte, totalLeaves uint64) bool {
	if p == nil || len(p.LeafHash) != 32 || len(p.Peaks) == 0 {
		return false
	}
	n := int(totalLeaves)
	if n <= 0 || int(p.LeafIndex) >= n {
		return false
	}
	pos := int(p.LeafIndex)
	for b := 63; b >= 0; b-- {
		if n&(1<<b) == 0 {
			continue
		}
		start := mountainStart(n, b)
		size := 1 << b
		if pos < start || pos >= start+size {
			continue
		}
		local := pos - start
		node := append([]byte(nil), p.LeafHash...)
		k := 0
		for j := 0; j < b; j++ {
			if k >= len(p.Proof) {
				return false
			}
			sibHash := p.Proof[k]
			k++
			sub := local >> j
			if sub&1 == 0 {
				node = Node(node, sibHash)
			} else {
				node = Node(sibHash, node)
			}
		}
		if k != len(p.Proof) {
			return false // extra or missing proof hashes
		}
		// node is the summit at height b. Replace the matching peak and re-bag.
		idx := 0
		for c := 0; c < b; c++ {
			if n&(1<<c) != 0 {
				idx++
			}
		}
		if idx >= len(p.Peaks) {
			return false
		}
		allPeaks := make([][]byte, len(p.Peaks))
		copy(allPeaks, p.Peaks)
		allPeaks[idx] = node
		return bytes.Equal(MMRRootFromPeaks(allPeaks), claimedRoot)
	}
	return false
}

// MarshalMMRProof serialises an inclusion proof to canonical CBOR.
func MarshalMMRProof(p *MMRInclusionProof) ([]byte, error) {
	return CanonicalMarshal(p)
}

// DecodeMMRProof parses a strict canonical CBOR inclusion proof.
func DecodeMMRProof(data []byte) (*MMRInclusionProof, error) {
	var p MMRInclusionProof
	rest, err := StrictDecode(data, &p)
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, errors.New("mmr: trailing data in inclusion proof")
	}
	return &p, nil
}
