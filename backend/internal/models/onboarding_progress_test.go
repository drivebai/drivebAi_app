package models

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTourStatus_IsValid(t *testing.T) {
	for _, s := range []TourStatus{TourStatusInProgress, TourStatusCompleted, TourStatusSkipped} {
		if !s.IsValid() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []TourStatus{"", "done", "finished", "COMPLETED"} {
		if TourStatus(s).IsValid() {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestUpsertTourProgressBody_Validate(t *testing.T) {
	intPtr := func(i int) *int { return &i }

	t.Run("empty entries rejected", func(t *testing.T) {
		b := UpsertTourProgressBody{}
		if b.Validate() == nil {
			t.Errorf("empty entries should be rejected")
		}
	})

	t.Run("too many entries rejected", func(t *testing.T) {
		entries := make([]UpsertTourProgressEntry, TourProgressMaxBatch+1)
		for i := range entries {
			entries[i] = UpsertTourProgressEntry{TourKey: "k", Status: TourStatusCompleted}
		}
		b := UpsertTourProgressBody{Entries: entries}
		if b.Validate() == nil {
			t.Errorf("over-limit batch should be rejected")
		}
	})

	t.Run("blank tour_key rejected", func(t *testing.T) {
		b := UpsertTourProgressBody{Entries: []UpsertTourProgressEntry{{TourKey: "   ", Status: TourStatusCompleted}}}
		if b.Validate() == nil {
			t.Errorf("blank tour_key should be rejected")
		}
	})

	t.Run("overlong tour_key rejected", func(t *testing.T) {
		long := make([]byte, TourKeyMaxLen+1)
		for i := range long {
			long[i] = 'a'
		}
		b := UpsertTourProgressBody{Entries: []UpsertTourProgressEntry{{TourKey: string(long)}}}
		if b.Validate() == nil {
			t.Errorf("overlong tour_key should be rejected")
		}
	})

	t.Run("invalid status rejected", func(t *testing.T) {
		b := UpsertTourProgressBody{Entries: []UpsertTourProgressEntry{{TourKey: "k", Status: "bogus"}}}
		if b.Validate() == nil {
			t.Errorf("bogus status should be rejected")
		}
	})

	t.Run("negative step rejected", func(t *testing.T) {
		b := UpsertTourProgressBody{Entries: []UpsertTourProgressEntry{{TourKey: "k", Status: TourStatusInProgress, Step: intPtr(-1)}}}
		if b.Validate() == nil {
			t.Errorf("negative step should be rejected")
		}
	})

	t.Run("valid: trims key and defaults status", func(t *testing.T) {
		b := UpsertTourProgressBody{Entries: []UpsertTourProgressEntry{{TourKey: "  driverTabs  "}}}
		if err := b.Validate(); err != nil {
			t.Fatalf("valid entry rejected: %v", err)
		}
		if b.Entries[0].TourKey != "driverTabs" {
			t.Errorf("tour_key not trimmed: %q", b.Entries[0].TourKey)
		}
		if b.Entries[0].Status != TourStatusCompleted {
			t.Errorf("omitted status should default to completed, got %q", b.Entries[0].Status)
		}
	})
}

// TestUpsertTourProgress_NoUserIDSpoofVector locks the authorization
// invariant: the request types carry NO user-identifier field, so a client
// (user A) cannot name another user (B) in the body. The server always binds
// the row owner from the JWT.
func TestUpsertTourProgress_NoUserIDSpoofVector(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(UpsertTourProgressBody{}),
		reflect.TypeOf(UpsertTourProgressEntry{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			name := typ.Field(i).Name
			jsonTag := typ.Field(i).Tag.Get("json")
			if name == "UserID" || name == "UserId" || jsonTag == "user_id" || jsonTag == "user_id,omitempty" {
				t.Errorf("%s must not expose a user-id field (found %q, tag %q)", typ.Name(), name, jsonTag)
			}
		}
	}

	// A body carrying a stray user_id decodes cleanly and validates — the
	// field is simply dropped, so there is no spoof surface.
	var b UpsertTourProgressBody
	raw := `{"user_id":"11111111-1111-1111-1111-111111111111","entries":[{"tour_key":"welcome","status":"completed"}]}`
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(b.Entries) != 1 || b.Entries[0].TourKey != "welcome" {
		t.Fatalf("unexpected decode: %+v", b.Entries)
	}
}
