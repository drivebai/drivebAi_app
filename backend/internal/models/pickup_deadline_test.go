package models

import "testing"

// These tests pin the wire values for migration 000024 enums so a future
// rename can't silently break the iOS client or the OpenAPI consumers.

func TestLeaseStatus_ExpiredRefunded_WireValue(t *testing.T) {
	if got, want := string(LeaseStatusExpiredRefunded), "expired_refunded"; got != want {
		t.Fatalf("LeaseStatusExpiredRefunded wire value: got %q, want %q", got, want)
	}
}

func TestRefundStatus_WireValues(t *testing.T) {
	cases := []struct {
		got  string
		want string
	}{
		{string(RefundStatusPending), "pending"},
		{string(RefundStatusSucceeded), "succeeded"},
		{string(RefundStatusFailed), "failed"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("RefundStatus wire value: got %q, want %q", c.got, c.want)
		}
	}
}

func TestErrPickupDeadlinePassed_Shape(t *testing.T) {
	if ErrPickupDeadlinePassed == nil {
		t.Fatal("ErrPickupDeadlinePassed should not be nil")
	}
	if got, want := ErrPickupDeadlinePassed.Code, "PICKUP_DEADLINE_PASSED"; got != want {
		t.Errorf("error code: got %q, want %q", got, want)
	}
	if ErrPickupDeadlinePassed.Message == "" {
		t.Error("error message should not be empty (client renders this verbatim)")
	}
}

// Pickup-extension policy (migration 000025): the iOS client mirrors these
// constants verbatim. A drift here means the preset buttons stop matching
// what the server accepts.

func TestPickupMaxExtensionMinutes(t *testing.T) {
	if PickupMaxExtensionMinutes != 120 {
		t.Errorf("PickupMaxExtensionMinutes drift: got %d, want 120", PickupMaxExtensionMinutes)
	}
}

func TestAllowedPickupExtensionMinutes_Presets(t *testing.T) {
	want := []int{15, 30, 60}
	if len(AllowedPickupExtensionMinutes) != len(want) {
		t.Fatalf("presets length drift: got %v want %v", AllowedPickupExtensionMinutes, want)
	}
	for i, v := range want {
		if AllowedPickupExtensionMinutes[i] != v {
			t.Errorf("preset[%d]: got %d want %d", i, AllowedPickupExtensionMinutes[i], v)
		}
	}
}

func TestIsAllowedPickupExtensionMinutes(t *testing.T) {
	cases := []struct {
		minutes int
		want    bool
	}{
		{15, true}, {30, true}, {60, true},
		{0, false}, {-15, false}, {45, false}, {61, false}, {120, false},
	}
	for _, c := range cases {
		if got := IsAllowedPickupExtensionMinutes(c.minutes); got != c.want {
			t.Errorf("IsAllowedPickupExtensionMinutes(%d): got %v want %v", c.minutes, got, c.want)
		}
	}
}

func TestRemainingExtensionMinutes(t *testing.T) {
	cases := []struct {
		used int
		want int
	}{
		{0, 120}, {15, 105}, {120, 0}, {130, 0}, // clamps to >=0 if cap exceeded
	}
	for _, c := range cases {
		lr := &LeaseRequest{PickupExtensionTotalMinutes: c.used}
		if got := lr.RemainingExtensionMinutes(); got != c.want {
			t.Errorf("Remaining(used=%d): got %d want %d", c.used, got, c.want)
		}
	}
}

func TestErrPickupExtensionCap_Shape(t *testing.T) {
	if ErrPickupExtensionCap.Code != "PICKUP_EXTENSION_CAP_REACHED" {
		t.Errorf("got %q", ErrPickupExtensionCap.Code)
	}
	if ErrInvalidExtensionMin.Code != "INVALID_EXTENSION_MINUTES" {
		t.Errorf("got %q", ErrInvalidExtensionMin.Code)
	}
}
