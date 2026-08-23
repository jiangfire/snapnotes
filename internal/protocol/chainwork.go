package protocol

import (
	"math/big"
)

// twoTo256Minus1 is 2^256 - 1, the inclusive upper bound on a 256-bit value. The
// chainwork formula divides by (target + 1); using 2^256-1 (rather than 2^256)
// keeps the result within 32 bytes and matches project-spec §10.4's intent
// (floor(2^256/(target+1))) for every in-range target, including target = 0.
var twoTo256Minus1 = func() *big.Int {
	// 2^256 = 1 << 256; subtract 1.
	n := new(big.Int).Lsh(big.NewInt(1), 256)
	return n.Sub(n, big.NewInt(1))
}()

// BlockWork returns the chainwork contributed by a single block mined under the
// given 32-byte PoW target, per project-spec §10.4:
//
//	work = floor((2^256 - 1) / (target + 1))
//
// A harder target (smaller numeric value) yields strictly greater work, so an
// accumulated chainwork reflects real proof-of-work rather than merely block
// count. Targets are derived by the client from the epoch schedule: the genesis
// target (from the out-of-band trust anchor) for the first 1000 blocks, then the
// retargeted target every subsequent 1000 blocks.
func BlockWork(target []byte) *big.Int {
	t := new(big.Int).SetBytes(target)
	denom := new(big.Int).Add(t, big.NewInt(1))
	if denom.Sign() == 0 {
		return new(big.Int)
	}
	return new(big.Int).Quo(twoTo256Minus1, denom)
}

// CumulativeChainwork returns the sum of per-block works for a chain, in height
// order. It is the total accumulated work a client uses to compare candidate
// chains.
func CumulativeChainwork(perBlockWork ...*big.Int) *big.Int {
	total := new(big.Int)
	for _, w := range perBlockWork {
		total.Add(total, w)
	}
	return total
}

// SelectBestChain reports whether chain A is strictly better than chain B under
// the project-spec §10.4 / protocol-v1.md Phase 5 selection rule: the greatest
// cumulative (chainwork, block_hash) wins, lexicographically. Height is never an
// input — only accumulated work and the tip hash matter. Equal chains are not
// strictly better for either side (the caller treats that as "no reorg needed").
func SelectBestChain(aWork *big.Int, aTip []byte, bWork *big.Int, bTip []byte) bool {
	cmp := aWork.Cmp(bWork)
	if cmp != 0 {
		return cmp > 0
	}
	// Equal work: lexicographic comparison of the tip block hashes (big-endian).
	return bytesGreater(aTip, bTip)
}

// bytesGreater reports whether a > b as big-endian unsigned byte strings.
func bytesGreater(a, b []byte) bool {
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false // equal
}
