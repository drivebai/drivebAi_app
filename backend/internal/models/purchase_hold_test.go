package models

import (
	"testing"
	"time"
)

// The latest handover leaves the whole inspection window AND the capture
// margin inside the auth hold. Expectations are hand-computed literals.
func TestLatestHandoverForLeavesWindowAndMarginInsideTheHold(t *testing.T) {
	holdEnds := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	got := LatestHandoverFor(holdEnds)
	want := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC) // 48h window + 24h margin = 72h earlier
	if !got.Equal(want) {
		t.Fatalf("latest handover = %v, want %v", got, want)
	}
	if !got.Add(PurchaseInspectionWindow).Add(PurchaseCaptureSafetyMargin).Equal(holdEnds) {
		t.Error("window + margin from the latest handover does not land exactly on the hold's end")
	}
	// Under a full 7-day hold, that is four days from authorization.
	if d := PurchaseAuthTTL - (PurchaseInspectionWindow + PurchaseCaptureSafetyMargin); d != 4*24*time.Hour {
		t.Errorf("handover budget under a fresh hold = %v, want 96h", d)
	}
}
