package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	"github.com/drivebai/backend/internal/ws"
)

// Sales gate + auth-hold guards. Stripe is nil throughout: the gate refuses
// before any money, and the hold guards read a stamped auth_expires_at.

type salesGateEnv struct {
	*payoutEnv
	purchaseH    *PurchaseRequestHandler
	purchaseRepo *repository.PurchaseRequestRepository
}

func newSalesGateEnv(t *testing.T) *salesGateEnv {
	t.Helper()
	e := newPayoutEnv(t)
	logger := discardLogger()
	purchaseRepo := repository.NewPurchaseRequestRepository(e.db)
	notifH := NewNotificationHandler(
		repository.NewNotificationRepository(e.db),
		repository.NewDeviceTokenRepository(e.db),
		ws.NewHub(logger), nil, logger)
	ph := NewPurchaseRequestHandler(purchaseRepo, e.carRepo,
		repository.NewUserRepository(e.db), repository.NewChatRepository(e.db), e.leaseRepo,
		nil, ws.NewHub(logger), notifH, nil, t.TempDir(), logger)
	return &salesGateEnv{payoutEnv: e, purchaseH: ph, purchaseRepo: purchaseRepo}
}

func purchaseReq(t *testing.T, userID, purchaseID uuid.UUID, action, body string) *http.Request {
	t.Helper()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", purchaseID.String())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/purchase-requests/"+purchaseID.String()+"/"+action, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, userID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

func purchaseCreateReq(t *testing.T, userID, carID uuid.UUID, body string) *http.Request {
	t.Helper()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("carId", carID.String())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/cars/"+carID.String()+"/purchase-requests", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, userID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

// errDetailsOf digs the error's details map out of either envelope shape.
func errDetailsOf(t *testing.T, rr *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var raw map[string]interface{}
	_ = json.Unmarshal(rr.Body.Bytes(), &raw)
	if d, ok := raw["details"].(map[string]interface{}); ok {
		return d
	}
	if e, ok := raw["error"].(map[string]interface{}); ok {
		if d, ok := e["details"].(map[string]interface{}); ok {
			return d
		}
	}
	return nil
}

func (e *salesGateEnv) listForSale(t *testing.T, carID uuid.UUID, priceDollars int64) {
	t.Helper()
	if _, err := e.db.Pool.Exec(context.Background(),
		`UPDATE cars SET is_for_sale = TRUE, sale_price = $2 WHERE id = $1`, carID, priceDollars); err != nil {
		t.Fatalf("list for sale: %v", err)
	}
}

// seedPurchase inserts a sale at a given status with the auth hold stamped
// the way MarkAuthorized stamps it, plus the chat it hangs off.
func (e *salesGateEnv) seedPurchase(t *testing.T, carID, sellerID, buyerID uuid.UUID, status string, holdRemaining time.Duration) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var chatID uuid.UUID
	if err := e.db.Pool.QueryRow(ctx,
		`INSERT INTO chats (car_id, driver_id, owner_id) VALUES ($1,$2,$3) RETURNING id`,
		carID, buyerID, sellerID).Scan(&chatID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	var pid uuid.UUID
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO purchase_requests (car_id, seller_id, buyer_id, chat_id, offer_amount_cents, currency, status,
			expires_at, payment_status, payment_intent_id, auth_expires_at)
		VALUES ($1,$2,$3,$4, 900000, 'USD', $5, NOW() + INTERVAL '30 days', 'requires_capture', $6, NOW() + $7::interval)
		RETURNING id`,
		carID, sellerID, buyerID, chatID, status, "pi_gate_"+uuid.NewString()[:8],
		fmt.Sprintf("%d seconds", int(holdRemaining.Seconds()))).Scan(&pid); err != nil {
		t.Fatalf("seed purchase: %v", err)
	}
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `UPDATE cars SET reserved_by_purchase_request_id = NULL WHERE id = $1`, carID)
		e.db.Pool.Exec(ctx, `DELETE FROM purchase_requests WHERE id = $1`, pid)
		e.db.Pool.Exec(ctx, `DELETE FROM chats WHERE id = $1`, chatID)
	})
	return pid
}

func (e *salesGateEnv) purchaseStatus(t *testing.T, pid uuid.UUID) string {
	t.Helper()
	var st string
	if err := e.db.Pool.QueryRow(context.Background(), `SELECT status FROM purchase_requests WHERE id = $1`, pid).Scan(&st); err != nil {
		t.Fatalf("status: %v", err)
	}
	return st
}

// The switch is a public kill switch; the allowlist is the pilot behind it.
// BOTH parties must be listed, or the sale strands at the seller's first
// refused step.
func TestSalesAllowlist_BothPartiesMustBeListed(t *testing.T) {
	e := newSalesGateEnv(t)
	ctx := context.Background()
	run := uuid.NewString()[:8]
	seller := e.seedUser(t, "car_owner", "sg_seller_"+run+"@example.com")
	buyer := e.seedUser(t, "driver", "sg_buyer_"+run+"@example.com")
	e.seedLicense(t, buyer)
	car := e.seedCar(t, seller, "available", true, false)
	e.listForSale(t, car, 9000)
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `UPDATE cars SET reserved_by_purchase_request_id = NULL WHERE id = $1`, car)
		e.db.Pool.Exec(ctx, `DELETE FROM purchase_requests WHERE car_id = $1`, car)
	})
	e.purchaseH.SetSalesDisabled(true)
	offer := `{"offer_amount_cents": 900000}`

	// Nobody listed: refused at the door.
	rr := httptest.NewRecorder()
	e.purchaseH.Create(rr, purchaseCreateReq(t, buyer, car, offer))
	if rr.Code != http.StatusServiceUnavailable || errCodeOf(t, rr) != "SALES_PAUSED" {
		t.Fatalf("switch on, nobody listed: %d %s, want 503 SALES_PAUSED", rr.Code, rr.Body.String())
	}
	// Buyer listed, seller not: still refused — this seller could never
	// schedule the handover.
	e.purchaseH.SetSalesAllowlist([]uuid.UUID{buyer})
	rr = httptest.NewRecorder()
	e.purchaseH.Create(rr, purchaseCreateReq(t, buyer, car, offer))
	if rr.Code != http.StatusServiceUnavailable || errCodeOf(t, rr) != "SALES_PAUSED" {
		t.Fatalf("buyer listed, seller not: %d %s, want 503 SALES_PAUSED", rr.Code, rr.Body.String())
	}
	// Both listed: the offer goes through.
	e.purchaseH.SetSalesAllowlist([]uuid.UUID{buyer, seller})
	rr = httptest.NewRecorder()
	e.purchaseH.Create(rr, purchaseCreateReq(t, buyer, car, offer))
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("both listed: %d %s, want the offer created", rr.Code, rr.Body.String())
	}
	// The downstream commit points read the same list: an unlisted seller
	// is refused at schedule-handover even on a sale that exists.
	var pid uuid.UUID
	if err := e.db.Pool.QueryRow(ctx, `SELECT id FROM purchase_requests WHERE car_id = $1`, car).Scan(&pid); err != nil {
		t.Fatalf("created offer not found: %v", err)
	}
	e.purchaseH.SetSalesAllowlist([]uuid.UUID{buyer})
	rr = httptest.NewRecorder()
	e.purchaseH.ScheduleHandover(rr, purchaseReq(t, seller, pid, "schedule-handover",
		`{"handover_scheduled_at":"2030-01-01T10:00:00Z","handover_location":"Lot A"}`))
	if rr.Code != http.StatusServiceUnavailable || errCodeOf(t, rr) != "SALES_PAUSED" {
		t.Fatalf("unlisted seller at schedule-handover: %d %s, want 503 SALES_PAUSED", rr.Code, rr.Body.String())
	}
	// Switch off: open to everyone, whatever the list says.
	e.purchaseH.SetSalesDisabled(false)
	e.purchaseH.SetSalesAllowlist(nil)
	if !e.purchaseH.salesOpenFor(uuid.New()) {
		t.Error("switch off must open the flow to everyone")
	}
}

// The auth hold is ~7 days. Keys change hands at the handover, the buyer
// then has 48h, and the capture that ends the window must still land inside
// the hold with margin — otherwise the seller has given up the car for money
// that can no longer be taken.
func TestScheduleHandover_RefusesDatesThatOutrunTheHold(t *testing.T) {
	e := newSalesGateEnv(t)
	run := uuid.NewString()[:8]
	seller := e.seedUser(t, "car_owner", "sh_seller_"+run+"@example.com")
	buyer := e.seedUser(t, "driver", "sh_buyer_"+run+"@example.com")
	e.seedLicense(t, buyer)
	car := e.seedCar(t, seller, "available", true, false)
	e.listForSale(t, car, 9000)
	pid := e.seedPurchase(t, car, seller, buyer, "payment_authorized", 7*24*time.Hour)

	// Five days out: 48h window + 24h margin would end after the hold.
	tooLate := time.Now().Add(5 * 24 * time.Hour)
	rr := httptest.NewRecorder()
	e.purchaseH.ScheduleHandover(rr, purchaseReq(t, seller, pid, "schedule-handover",
		fmt.Sprintf(`{"handover_scheduled_at":%q,"handover_location":"Lot A"}`, tooLate.UTC().Format(time.RFC3339))))
	if rr.Code != http.StatusConflict || errCodeOf(t, rr) != "HANDOVER_TOO_LATE" {
		t.Fatalf("5-day handover under a 7-day hold: %d %s, want 409 HANDOVER_TOO_LATE", rr.Code, rr.Body.String())
	}
	details := errDetailsOf(t, rr)
	latestRaw, _ := details["latest_handover_at"].(string)
	latest, perr := time.Parse(time.RFC3339, latestRaw)
	if perr != nil {
		t.Fatalf("latest_handover_at missing or unparseable in %s", rr.Body.String())
	}
	// Latest = hold end − 48h − 24h = ~4 days from now.
	if d := time.Until(latest); d < 4*24*time.Hour-time.Minute || d > 4*24*time.Hour+time.Minute {
		t.Errorf("latest_handover_at is %v out, want ~4 days (hold − window − margin)", d.Round(time.Minute))
	}
	if st := e.purchaseStatus(t, pid); st != "payment_authorized" {
		t.Fatalf("refused schedule changed status to %s", st)
	}

	// Three days out fits: window ends at +5d, margin to +6d, hold at +7d.
	inTime := time.Now().Add(3 * 24 * time.Hour)
	rr = httptest.NewRecorder()
	e.purchaseH.ScheduleHandover(rr, purchaseReq(t, seller, pid, "schedule-handover",
		fmt.Sprintf(`{"handover_scheduled_at":%q,"handover_location":"Lot A"}`, inTime.UTC().Format(time.RFC3339))))
	if rr.Code != http.StatusOK {
		t.Fatalf("3-day handover under a 7-day hold: %d %s, want 200", rr.Code, rr.Body.String())
	}
	if st := e.purchaseStatus(t, pid); st != "handover_scheduled" {
		t.Fatalf("status = %s, want handover_scheduled", st)
	}
}

// The same arithmetic at the moment that matters most: once the keys are
// gone the window is running. Handler and repository both refuse.
func TestKeysHandedOver_RefusesWhenTheHoldIsTooShort(t *testing.T) {
	e := newSalesGateEnv(t)
	ctx := context.Background()
	run := uuid.NewString()[:8]
	seller := e.seedUser(t, "car_owner", "kh_seller_"+run+"@example.com")
	buyer := e.seedUser(t, "driver", "kh_buyer_"+run+"@example.com")
	e.seedLicense(t, buyer)
	car := e.seedCar(t, seller, "available", true, false)
	e.listForSale(t, car, 9000)
	// 60h of hold left: the 48h window + 24h margin does not fit.
	pid := e.seedPurchase(t, car, seller, buyer, "handover_scheduled", 60*time.Hour)

	rr := httptest.NewRecorder()
	e.purchaseH.KeysHandedOver(rr, purchaseReq(t, seller, pid, "keys-handed-over", `{}`))
	if rr.Code != http.StatusConflict || errCodeOf(t, rr) != "AUTH_EXPIRING" {
		t.Fatalf("keys with 60h of hold left: %d %s, want 409 AUTH_EXPIRING", rr.Code, rr.Body.String())
	}
	if st := e.purchaseStatus(t, pid); st != "handover_scheduled" {
		t.Fatalf("refused handover changed status to %s", st)
	}
	var reserved *string
	_ = e.db.Pool.QueryRow(ctx, `SELECT reserved_by_purchase_request_id::text FROM cars WHERE id=$1`, car).Scan(&reserved)
	if reserved != nil {
		t.Error("refused handover still reserved the car")
	}
	// The repository backstop holds on its own, with the handler bypassed.
	if _, err := e.purchaseRepo.KeysHandedOver(ctx, pid, seller); err == nil {
		t.Fatal("repository accepted keys-handed-over with 60h of hold left — the SQL backstop is missing")
	}
	if st := e.purchaseStatus(t, pid); st != "handover_scheduled" {
		t.Fatalf("repository refusal changed status to %s", st)
	}

	// Four days of hold: window to +2d, margin to +3d, fits.
	if _, err := e.db.Pool.Exec(ctx, `UPDATE purchase_requests SET auth_expires_at = NOW() + INTERVAL '4 days' WHERE id = $1`, pid); err != nil {
		t.Fatalf("extend hold: %v", err)
	}
	rr = httptest.NewRecorder()
	e.purchaseH.KeysHandedOver(rr, purchaseReq(t, seller, pid, "keys-handed-over", `{}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("keys with 4 days of hold left: %d %s, want 200", rr.Code, rr.Body.String())
	}
	if st := e.purchaseStatus(t, pid); st != "awaiting_inspection" {
		t.Fatalf("status = %s, want awaiting_inspection", st)
	}
	var deadline *time.Time
	_ = e.db.Pool.QueryRow(ctx, `SELECT inspection_deadline_at FROM purchase_requests WHERE id=$1`, pid).Scan(&deadline)
	if deadline == nil {
		t.Error("inspection window did not start")
	}
	_ = e.db.Pool.QueryRow(ctx, `SELECT reserved_by_purchase_request_id::text FROM cars WHERE id=$1`, car).Scan(&reserved)
	if reserved == nil || *reserved != pid.String() {
		t.Error("keys handed over but the car is not reserved for this sale")
	}
}

// Discovery advertises Buy only where an offer would be accepted.
func TestCarHandler_AdvertisesSaleOnlyToPilotPairs(t *testing.T) {
	h := &CarHandler{}
	viewer, owner, stranger := uuid.New(), uuid.New(), uuid.New()

	h.SetSalesDisabled(false)
	if !h.advertiseSale(stranger, owner) {
		t.Error("switch off: everyone sees Buy")
	}
	h.SetSalesDisabled(true)
	h.SetSalesAllowlist(nil)
	if h.advertiseSale(viewer, owner) {
		t.Error("switch on, empty list: nobody sees Buy")
	}
	h.SetSalesAllowlist([]uuid.UUID{viewer})
	if h.advertiseSale(viewer, owner) {
		t.Error("listed viewer on an unlisted seller's car must not see Buy — the offer would be refused")
	}
	h.SetSalesAllowlist([]uuid.UUID{viewer, owner})
	if !h.advertiseSale(viewer, owner) {
		t.Error("listed viewer on a listed seller's car must see Buy")
	}
	if h.advertiseSale(stranger, owner) {
		t.Error("unlisted viewer must not see Buy on a pilot car")
	}
	if h.advertiseSale(viewer, stranger) {
		t.Error("listed viewer must not see Buy on a non-pilot car")
	}
	if h.advertiseSale(uuid.Nil, owner) {
		t.Error("anonymous viewer must never see Buy while the switch is on")
	}
	_ = models.PurchaseInspectionWindow
}
