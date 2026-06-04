package models

import (
	"testing"
	"time"
)

func TestKeyHandover_IsActive(t *testing.T) {
	cases := map[KeyHandoverStatus]bool{
		KeyHandoverPending:        true,
		KeyHandoverOwnerConfirmed: true,
		KeyHandoverCompleted:      false,
		KeyHandoverExpired:        false,
	}
	for status, want := range cases {
		kh := &KeyHandover{Status: status}
		if got := kh.IsActive(); got != want {
			t.Errorf("status %q: IsActive() = %v, want %v", status, got, want)
		}
	}
}

func TestNewRFC3339TimePtr(t *testing.T) {
	if NewRFC3339TimePtr(nil) != nil {
		t.Error("expected nil for nil input")
	}
	now := time.Now()
	got := NewRFC3339TimePtr(&now)
	if got == nil {
		t.Fatal("expected non-nil for non-nil input")
	}
	if got.Time().Unix() != now.Unix() {
		t.Errorf("time mismatch: got %v, want %v", got.Time(), now)
	}
}

func TestKeyHandoverConfirmWindow(t *testing.T) {
	if KeyHandoverConfirmWindow != 15*time.Minute {
		t.Errorf("expected 15m window, got %v", KeyHandoverConfirmWindow)
	}
}
