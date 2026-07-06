package models

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ── Required-documents gate (QA pt-10 / D5) ─────────────────────────────────

func TestRequiredCarDocumentTypes(t *testing.T) {
	rent := RequiredCarDocumentTypes(false)
	if !reflect.DeepEqual(rent, []CarDocumentType{CarDocRegistration, CarDocInspection, CarDocInsurance}) {
		t.Fatalf("rental-only required set wrong: %v", rent)
	}
	sale := RequiredCarDocumentTypes(true)
	if len(sale) != 4 || sale[3] != CarDocTitle {
		t.Fatalf("for-sale must additionally require title: %v", sale)
	}
}

func TestMissingRequiredCarDocuments(t *testing.T) {
	cases := []struct {
		name      string
		isForSale bool
		onFile    []string
		want      []string
	}{
		{
			name:   "2 of 3 on file",
			onFile: []string{"registration", "inspection"},
			want:   []string{"insurance"},
		},
		{
			name:   "all 3 on file, not for sale",
			onFile: []string{"registration", "inspection", "insurance"},
			want:   []string{},
		},
		{
			name:      "all 3 on file but for sale without title",
			isForSale: true,
			onFile:    []string{"registration", "inspection", "insurance"},
			want:      []string{"title"},
		},
		{
			name:      "for sale, everything on file",
			isForSale: true,
			onFile:    []string{"registration", "inspection", "insurance", "title", "permit"},
			want:      []string{},
		},
		{
			name:   "nothing on file",
			onFile: nil,
			want:   []string{"registration", "inspection", "insurance"},
		},
		{
			name:   "permit alone does not satisfy anything",
			onFile: []string{"permit"},
			want:   []string{"registration", "inspection", "insurance"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MissingRequiredCarDocuments(tc.isForSale, tc.onFile)
			if got == nil {
				t.Fatal("must return non-nil slice so JSON encodes [] not null")
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
}

// ── Soft archive (QA pt-9 / D3) ──────────────────────────────────────────────

func TestCarIsArchived(t *testing.T) {
	c := &Car{}
	if c.IsArchived() {
		t.Fatal("fresh car must not be archived")
	}
	c.ArchivedAt = sql.NullTime{Time: time.Now(), Valid: true}
	if !c.IsArchived() {
		t.Fatal("car with archived_at set must report archived")
	}
}

// ── Deposit wire compatibility (QA pt-7 / D8) ────────────────────────────────

// Shipped iOS builds decode requirements.deposit_amount as a NON-optional
// Double, so the key must stay on the wire (now always 0) until a forced
// update. This pins that the key is present even at its zero value.
func TestCarResponse_DepositAmountKeyRetained(t *testing.T) {
	car := &Car{
		Title:         "2019 Toyota Corolla",
		DepositAmount: 0,
		Currency:      "USD",
	}
	raw, err := json.Marshal(car.ToResponse(nil, nil, nil, false))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"deposit_amount":0`) {
		t.Fatalf(`old builds require "deposit_amount":0 in requirements; got %s`, raw)
	}
}
