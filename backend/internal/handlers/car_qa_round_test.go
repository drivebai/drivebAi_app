package handlers

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// ── UpdateCar ignore rules (QA pt-7 deposit, pt-9 pause bypass) ─────────────

// strPtr lives in admin_update_profile_test.go (same package).
func f64Ptr(f float64) *float64                                    { return &f }
func boolPtr(b bool) *bool                                         { return &b }
func statusPtr(s models.CarListingStatus) *models.CarListingStatus { return &s }

// A full-car autosave PATCH carrying status/is_paused/deposit_amount must
// not be able to clobber pause state (D2) or resurrect deposits (D8) —
// UpdateCar routes pause exclusively through POST /cars/{id}/pause.
func TestApplyCarUpdateRequest_IgnoresStatusPauseAndDeposit(t *testing.T) {
	car := &models.Car{
		Title:         "2019 Toyota Corolla",
		Status:        models.CarStatusRented,
		IsPaused:      false,
		DepositAmount: 0,
	}

	applyCarUpdateRequest(car, &models.UpdateCarRequest{
		Title:         strPtr("Renamed"),
		Status:        statusPtr(models.CarStatusAvailable),
		IsPaused:      boolPtr(true),
		DepositAmount: f64Ptr(800),
	})

	if car.Title != "Renamed" {
		t.Errorf("legit field not applied: title = %q", car.Title)
	}
	if car.Status != models.CarStatusRented {
		t.Errorf("client-sent status must be ignored — got %q", car.Status)
	}
	if car.IsPaused {
		t.Error("client-sent is_paused must be ignored")
	}
	if car.DepositAmount != 0 {
		t.Errorf("client-sent deposit_amount must be ignored — got %v", car.DepositAmount)
	}
}

func TestApplyCarUpdateRequest_NormalizesVIN(t *testing.T) {
	car := &models.Car{}
	applyCarUpdateRequest(car, &models.UpdateCarRequest{VIN: strPtr("  1hgcm82633a004352 ")})
	if !car.VIN.Valid || car.VIN.String != "1HGCM82633A004352" {
		t.Fatalf("VIN not normalized: %+v", car.VIN)
	}

	// Empty-after-normalize clears the VIN entirely.
	applyCarUpdateRequest(car, &models.UpdateCarRequest{VIN: strPtr("   ")})
	if car.VIN.Valid {
		t.Fatalf("blank VIN should clear the field, got %+v", car.VIN)
	}
}

// ── Sale readiness (QA pt-8 / D4) ────────────────────────────────────────────

func TestSaleRequirementsMissing(t *testing.T) {
	titleDoc := models.CarDocument{DocumentType: models.CarDocTitle}
	insuranceDoc := models.CarDocument{DocumentType: models.CarDocInsurance}

	cases := []struct {
		name string
		car  *models.Car
		docs []models.CarDocument
		want []string
	}{
		{
			name: "no price (NULL), no title",
			car:  &models.Car{IsForSale: true},
			docs: nil,
			want: []string{"sale_price_min", "title_document"},
		},
		{
			// Floor relaxed: any positive amount is accepted. $500 + title = ready.
			name: "price 500 with title — ready (no $1,000 floor)",
			car:  &models.Car{IsForSale: true, SalePrice: sql.NullFloat64{Float64: 500, Valid: true}},
			docs: []models.CarDocument{titleDoc},
			want: []string{},
		},
		{
			name: "price zero rejected",
			car:  &models.Car{IsForSale: true, SalePrice: sql.NullFloat64{Float64: 0, Valid: true}},
			docs: []models.CarDocument{titleDoc},
			want: []string{"sale_price_min"},
		},
		{
			name: "negative price rejected",
			car:  &models.Car{IsForSale: true, SalePrice: sql.NullFloat64{Float64: -100, Valid: true}},
			docs: []models.CarDocument{titleDoc},
			want: []string{"sale_price_min"},
		},
		{
			// The nil-guard: is_for_sale with a NULL sale price must be rejected.
			name: "NULL price rejected even with title",
			car:  &models.Car{IsForSale: true, SalePrice: sql.NullFloat64{Valid: false}},
			docs: []models.CarDocument{titleDoc},
			want: []string{"sale_price_min"},
		},
		{
			name: "price ok, title missing (other docs present)",
			car:  &models.Car{IsForSale: true, SalePrice: sql.NullFloat64{Float64: 12000, Valid: true}},
			docs: []models.CarDocument{insuranceDoc},
			want: []string{"title_document"},
		},
		{
			name: "price 1 cent-equivalent positive with title — ready",
			car:  &models.Car{IsForSale: true, SalePrice: sql.NullFloat64{Float64: 1, Valid: true}},
			docs: []models.CarDocument{titleDoc, insuranceDoc},
			want: []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := saleRequirementsMissing(tc.car, tc.docs)
			if len(got) != len(tc.want) {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("want %v, got %v", tc.want, got)
				}
			}
		})
	}
}

// ── Pause guard (QA pt-9 / D2) ───────────────────────────────────────────────

func TestPauseConflictError(t *testing.T) {
	if err := pauseConflictError(&models.Car{Status: models.CarStatusRented}); err == nil {
		t.Fatal("rented car must 409 on pause")
	} else if err.Code != models.ErrCodeCarCurrentlyRented {
		t.Fatalf("want code %s, got %s", models.ErrCodeCarCurrentlyRented, err.Code)
	}

	for _, s := range []models.CarListingStatus{
		models.CarStatusAvailable, models.CarStatusPaused,
		models.CarStatusPending, models.CarStatusSold,
	} {
		if err := pauseConflictError(&models.Car{Status: s}); err != nil {
			t.Fatalf("status %q must be pausable/unpausable, got %v", s, err)
		}
	}
}

// ── Photo slots + document types (QA pt-4 / pt-8) ────────────────────────────

func TestValidPhotoSlots_GuidedCaptureSet(t *testing.T) {
	for _, slot := range []models.PhotoSlotType{
		models.PhotoSlotCoverFront, models.PhotoSlotRight, models.PhotoSlotLeft,
		models.PhotoSlotBack, models.PhotoSlotDashboard,
		models.PhotoSlotFrontLeft34, models.PhotoSlotRearRight34, models.PhotoSlotInterior,
	} {
		if !validPhotoSlots[slot] {
			t.Errorf("slot %q must be accepted", slot)
		}
	}
	if len(validPhotoSlots) != 8 {
		t.Errorf("want exactly 8 slots (lockstep with car_photos_slot_type_check), got %d", len(validPhotoSlots))
	}
	if validPhotoSlots[models.PhotoSlotType("bogus_slot")] {
		t.Error("bogus slot must be rejected")
	}
}

func TestValidCarDocumentTypes_IncludesTitle(t *testing.T) {
	for _, dt := range []models.CarDocumentType{
		models.CarDocInspection, models.CarDocRegistration,
		models.CarDocPermit, models.CarDocInsurance, models.CarDocTitle,
	} {
		if !validCarDocumentTypes[dt] {
			t.Errorf("document type %q must be accepted", dt)
		}
	}
	if validCarDocumentTypes[models.CarDocumentType("passport")] {
		t.Error("unknown document type must be rejected")
	}
}

// ── Active rental summary: chat_id passthrough (QA pt-6) ────────────────────

func TestBuildActiveRentalSummary_ChatIDPassthrough(t *testing.T) {
	chatID := uuid.New()
	row := &repository.OwnerCarActiveRental{
		LeaseRequestID:            uuid.New(),
		DriverID:                  uuid.New(),
		DriverName:                "Jamie Rivera",
		Weeks:                     4,
		EffectiveWeeklyPriceCents: 18000,
		PickupConfirmedAt:         time.Now().Add(-8 * 24 * time.Hour),
		ChatID:                    &chatID,
	}

	summary := buildActiveRentalSummary(row)
	if summary.ChatID == nil || *summary.ChatID != chatID {
		t.Fatalf("chat id not passed through: %v", summary.ChatID)
	}

	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"chat_id":"`+chatID.String()+`"`) {
		t.Fatalf("chat_id missing from wire shape: %s", raw)
	}

	// No chat → key omitted so old clients see no change.
	row.ChatID = nil
	raw, err = json.Marshal(buildActiveRentalSummary(row))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "chat_id") {
		t.Fatalf("chat_id must be omitted when nil: %s", raw)
	}
}
