package handlers

import (
	"database/sql"
	"testing"

	"github.com/drivebai/backend/internal/models"
)

// Exercises the exact predicate CreateCar calls before insert — the same rule
// UpdateCar's readiness gate uses, so the two write paths cannot drift apart.
func TestCreateCar_SalePriceGate(t *testing.T) {
	price := func(v float64) sql.NullFloat64 {
		return sql.NullFloat64{Float64: v, Valid: true}
	}

	cases := []struct {
		name        string
		car         *models.Car
		wantInvalid bool
	}{
		{
			name:        "rent-only car needs no sale price",
			car:         &models.Car{IsForRent: true},
			wantInvalid: false,
		},
		{
			name:        "for sale at $500 is allowed (no $1,000 floor)",
			car:         &models.Car{IsForSale: true, SalePrice: price(500)},
			wantInvalid: false,
		},
		{
			name:        "for sale at one cent is allowed",
			car:         &models.Car{IsForSale: true, SalePrice: price(0.01)},
			wantInvalid: false,
		},
		{
			name:        "for sale with NULL price is rejected",
			car:         &models.Car{IsForSale: true},
			wantInvalid: true,
		},
		{
			name:        "for sale at zero is rejected",
			car:         &models.Car{IsForSale: true, SalePrice: price(0)},
			wantInvalid: true,
		},
		{
			name:        "for sale at a negative price is rejected",
			car:         &models.Car{IsForSale: true, SalePrice: price(-1)},
			wantInvalid: true,
		},
		{
			// A price on a not-for-sale car is harmless: the gate only fires
			// when the listing actually claims to be for sale.
			name:        "negative price on a rent-only car is not gated here",
			car:         &models.Car{IsForRent: true, SalePrice: price(-1)},
			wantInvalid: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := salePriceMissingOrNonPositive(tc.car); got != tc.wantInvalid {
				t.Errorf("invalid = %v, want %v", got, tc.wantInvalid)
			}
		})
	}
}
