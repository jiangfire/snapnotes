package protocol

import (
	"math/big"
	"testing"
)

// targetFromBig builds a 32-byte big-endian PoW target.
func targetFromBig(t *testing.T, n int64) []byte {
	t.Helper()
	b := make([]byte, 32)
	big.NewInt(n).FillBytes(b)
	return b
}

func TestBlockWorkTargetsPowersOfTwo(t *testing.T) {
	// target = 2^k - 1  =>  work = 2^(256-k) - 1  (per project-spec §10.4)
	cases := []struct {
		target int64
		want   *big.Int
	}{
		{1, new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(1))}, // 2^255-1
		{3, new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 254), big.NewInt(1))}, // 2^254-1
		{7, new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 253), big.NewInt(1))}, // 2^253-1
	}
	for _, c := range cases {
		got := BlockWork(targetFromBig(t, c.target))
		if got.Cmp(c.want) != 0 {
			t.Errorf("BlockWork(%d) = %v, want %v", c.target, got, c.want)
		}
	}
}

func TestBlockWorkMonotonicInTarget(t *testing.T) {
	// A harder target (smaller numeric value) implies strictly greater work.
	easy := BlockWork(targetFromBig(t, 7))
	harder := BlockWork(targetFromBig(t, 3))
	evenHarder := BlockWork(targetFromBig(t, 1))
	if !(evenHarder.Cmp(harder) > 0 && harder.Cmp(easy) > 0) {
		t.Errorf("BlockWork not monotonic: 1->%v, 3->%v, 7->%v", evenHarder, harder, easy)
	}
}

func TestSelectBestChainByWorkNotHeight(t *testing.T) {
	// The selection rule must use chainwork, not height: a chain with greater
	// cumulative work wins even when it is shorter. (Height is not an input to the
	// selector; it is only used by callers to fetch blocks.)
	shortMoreWork := SelectBestChain(
		big.NewInt(7), []byte{0x01}, // candidate: work 7
		big.NewInt(6), []byte{0x02}, // current:   work 6
	)
	if !shortMoreWork {
		t.Error("chain with greater chainwork should win regardless of height")
	}
	longLessWork := SelectBestChain(
		big.NewInt(6), []byte{0x02}, // current:   work 6
		big.NewInt(7), []byte{0x01}, // candidate: work 7
	)
	if longLessWork {
		t.Error("chain with greater chainwork should win even when shorter")
	}
}

func TestSelectBestChainLexicographicTipTiebreak(t *testing.T) {
	// Equal chainwork resolves by the greater block_hash (lexicographic on bytes),
	// never by height.
	aWins := SelectBestChain(
		big.NewInt(7), []byte{0x02}, // tip 0x02 > 0x01
		big.NewInt(7), []byte{0x01},
	)
	if !aWins {
		t.Error("greater tip hash should win on equal chainwork")
	}
	bWins := SelectBestChain(
		big.NewInt(7), []byte{0x01},
		big.NewInt(7), []byte{0x02},
	)
	if bWins {
		t.Error("greater tip hash should win on equal chainwork")
	}
}

func TestSelectBestChainEqualIsNotStrictlyBetter(t *testing.T) {
	// Identical (work, tip) is not strictly better for either side.
	if SelectBestChain(big.NewInt(7), []byte{0x01}, big.NewInt(7), []byte{0x01}) {
		t.Error("identical chains should not be strictly better")
	}
}
