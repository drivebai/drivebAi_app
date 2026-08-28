package models

import "testing"

// The payout split: fee floors, owner takes the remainder, and the
// invariant fee + owner == kept holds for every input — no cent is ever
// lost or invented.
func TestComputePayoutSplit(t *testing.T) {
	cases := []struct {
		name      string
		kept      int64
		bps       int
		wantFee   int64
		wantOwner int64
	}{
		{"even split", 10000, 1000, 1000, 9000},
		{"floor gives remainder cent to owner", 9999, 1000, 999, 9000},
		{"one cent, tiny fee floors to zero", 1, 1, 0, 1},
		{"sub-cent fee floors to zero", 99, 100, 0, 99},
		{"exact prod config 10% of $300", 30000, 1000, 3000, 27000},
		{"5% default of $299.99", 29999, 500, 1499, 28500},
		{"zero kept", 0, 1000, 0, 0},
		{"negative kept clamps", -500, 1000, 0, 0},
		{"zero bps — owner gets all", 4242, 0, 0, 4242},
		{"negative bps clamps to zero", 4242, -50, 0, 4242},
		{"full bps — platform keeps all", 4242, 10000, 4242, 0},
		{"absurd bps clamps to full", 4242, 20000, 4242, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fee, owner := ComputePayoutSplit(tc.kept, tc.bps)
			if fee != tc.wantFee || owner != tc.wantOwner {
				t.Errorf("ComputePayoutSplit(%d, %d) = (%d, %d), want (%d, %d)",
					tc.kept, tc.bps, fee, owner, tc.wantFee, tc.wantOwner)
			}
		})
	}
}

// Brute-force the invariant across a dense range of amounts and fees.
func TestComputePayoutSplit_Invariant(t *testing.T) {
	for kept := int64(1); kept <= 3000; kept++ {
		for _, bps := range []int{1, 250, 333, 500, 999, 1000, 2500, 9999} {
			fee, owner := ComputePayoutSplit(kept, bps)
			if fee+owner != kept {
				t.Fatalf("invariant broken: kept=%d bps=%d fee=%d owner=%d (sum %d)",
					kept, bps, fee, owner, fee+owner)
			}
			if fee < 0 || owner < 0 {
				t.Fatalf("negative leg: kept=%d bps=%d fee=%d owner=%d", kept, bps, fee, owner)
			}
		}
	}
}
