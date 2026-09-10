package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// The booking block, and the positive proof that it leaves fixed-term rentals
// alone. The app has told drivers "new bookings are paused until this is
// resolved" since the rolling engine shipped; nothing enforced it until now.
//
//	TEST_DATABASE_URL="postgres://…/scratch?sslmode=disable" \
//	  go test ./internal/handlers/ -run TestDebtBooking -v
func debtBlockRequest(t *testing.T, driverID, carID uuid.UUID, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/v1/cars/%s/lease-requests", carID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("listingId", carID.String())
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, driverID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

func TestDebtBookingBlock(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	debtRepo := repository.NewDriverDebtRepository(e.db)
	e.leaseH.SetDebtDependencies(debtRepo, true)

	ownerID := e.seedUser(t, "car_owner", "debtblock_owner_"+uuid.NewString()+"@example.com")
	driverID := e.seedUser(t, "driver", "debtblock_driver_"+uuid.NewString()+"@example.com")
	e.seedLicense(t, driverID)
	carID := e.seedCar(t, ownerID, "available", true, false)

	t.Cleanup(func() {
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM driver_debt_entries WHERE debt_id IN (SELECT id FROM driver_debts WHERE driver_id=$1)`, driverID)
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM driver_debts WHERE driver_id=$1`, driverID)
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM billing_cycles WHERE lease_request_id IN (SELECT id FROM lease_requests WHERE driver_id=$1)`, driverID)
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM lease_requests WHERE driver_id=$1`, driverID)
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM chats WHERE driver_id=$1`, driverID)
	})

	// A driver who owes NOTHING books exactly as before. This is the
	// fixed-term proof: the block is inert for everyone without a debt.
	rr := httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr, debtBlockRequest(t, driverID, carID, `{"weeks":1}`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("clean driver could not book: %d %s", rr.Code, rr.Body.String())
	}
	var created struct {
		LeaseRequest struct {
			ID          uuid.UUID `json:"id"`
			BillingMode string    `json:"billing_mode"`
		} `json:"lease_request"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.LeaseRequest.BillingMode != string(models.BillingModeFixedTerm) {
		t.Errorf("billing_mode = %q, want fixed_term", created.LeaseRequest.BillingMode)
	}
	leaseID := created.LeaseRequest.ID

	// Now give that driver a debt and try again.
	var cycleID uuid.UUID
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO billing_cycles (lease_request_id, cycle_number, period_start, period_end, amount_cents, status)
		VALUES ($1, 1, NOW() - INTERVAL '7 days', NOW(), 6426, 'arrears_due') RETURNING id`, leaseID).Scan(&cycleID); err != nil {
		t.Fatalf("seed cycle: %v", err)
	}
	if _, _, err := debtRepo.OpenForCycle(ctx, driverID, leaseID, cycleID, 6426, "USD", models.DriverDebtSnapshot{}); err != nil {
		t.Fatalf("open debt: %v", err)
	}

	rr2 := httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr2, debtBlockRequest(t, driverID, carID, `{"weeks":1}`))
	if rr2.Code != http.StatusConflict {
		t.Fatalf("indebted driver was NOT blocked: %d %s", rr2.Code, rr2.Body.String())
	}
	var errBody struct {
		Error struct {
			Code    string                 `json:"code"`
			Message string                 `json:"message"`
			Details map[string]interface{} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rr2.Body.Bytes(), &errBody)
	if errBody.Error.Code != "OUTSTANDING_BALANCE" {
		t.Errorf("error code = %q, want OUTSTANDING_BALANCE (body %s)", errBody.Error.Code, rr2.Body.String())
	}
	if !strings.Contains(errBody.Error.Message, "64.26") {
		t.Errorf("message does not state the amount owed: %q", errBody.Error.Message)
	}
	if errBody.Error.Details["outstanding_cents"] != float64(6426) {
		t.Errorf("details.outstanding_cents = %v, want 6426", errBody.Error.Details["outstanding_cents"])
	}

	// Paying it off restores booking — the block must have an exit.
	debt, _ := debtRepo.GetByCycle(ctx, cycleID)
	if _, _, err := debtRepo.ApplyPayment(ctx, debt.ID, 6426, "pi_block_clear", "driver"); err != nil {
		t.Fatalf("pay debt: %v", err)
	}
	rr3 := httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr3, debtBlockRequest(t, driverID, carID, `{"weeks":1}`))
	if rr3.Code == http.StatusConflict {
		var still struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(rr3.Body.Bytes(), &still)
		if still.Error.Code == "OUTSTANDING_BALANCE" {
			t.Error("driver still blocked after clearing the balance — the block has no exit")
		}
	}
}

// With enforcement off the block cannot fire, whatever is owed. This is the
// kill switch the deploy rule requires.
func TestDebtBookingBlockRespectsSwitch(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	debtRepo := repository.NewDriverDebtRepository(e.db)
	e.leaseH.SetDebtDependencies(debtRepo, false)

	ownerID := e.seedUser(t, "car_owner", "debtoff_owner_"+uuid.NewString()+"@example.com")
	driverID := e.seedUser(t, "driver", "debtoff_driver_"+uuid.NewString()+"@example.com")
	e.seedLicense(t, driverID)
	carID := e.seedCar(t, ownerID, "available", true, false)
	t.Cleanup(func() {
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM driver_debt_entries WHERE debt_id IN (SELECT id FROM driver_debts WHERE driver_id=$1)`, driverID)
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM driver_debts WHERE driver_id=$1`, driverID)
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM billing_cycles WHERE lease_request_id IN (SELECT id FROM lease_requests WHERE driver_id=$1)`, driverID)
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM lease_requests WHERE driver_id=$1`, driverID)
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM chats WHERE driver_id=$1`, driverID)
	})

	rr := httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr, debtBlockRequest(t, driverID, carID, `{"weeks":1}`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed booking failed: %d %s", rr.Code, rr.Body.String())
	}
	var created struct {
		LeaseRequest struct {
			ID uuid.UUID `json:"id"`
		} `json:"lease_request"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	var cycleID uuid.UUID
	_ = e.db.Pool.QueryRow(ctx, `
		INSERT INTO billing_cycles (lease_request_id, cycle_number, period_start, period_end, amount_cents, status)
		VALUES ($1, 1, NOW() - INTERVAL '7 days', NOW(), 9999, 'arrears_due') RETURNING id`, created.LeaseRequest.ID).Scan(&cycleID)
	if _, _, err := debtRepo.OpenForCycle(ctx, driverID, created.LeaseRequest.ID, cycleID, 9999, "USD", models.DriverDebtSnapshot{}); err != nil {
		t.Fatalf("open debt: %v", err)
	}

	rr2 := httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr2, debtBlockRequest(t, driverID, carID, `{"weeks":1}`))
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rr2.Body.Bytes(), &body)
	if body.Error.Code == "OUTSTANDING_BALANCE" {
		t.Error("block fired with enforcement switched off")
	}
}
