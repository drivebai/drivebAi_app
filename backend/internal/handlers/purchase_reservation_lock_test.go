package handlers

import (
	"os"
	"strings"
	"testing"
)

// Source-level wiring locks for the C4 reservation guard + payment_failed
// handling, in the same style as TestSoldGuard_WiredIntoAllOwnerWritePaths:
// removing the guard from either entrance, or dropping the webhook case,
// fails the build tests.

// TestReservationGuard_WiredIntoCreateAndAccept: both entrances to a
// concurrent second sale — a new offer (Create) and the seller accepting a
// second offer (Accept) — must consult HasBlockingPurchase and reject with
// the shared CAR_SALE_IN_PROGRESS envelope.
func TestReservationGuard_WiredIntoCreateAndAccept(t *testing.T) {
	src, err := os.ReadFile("purchase_request.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	s := string(src)
	for _, fn := range []string{
		"func (h *PurchaseRequestHandler) Create(",
		"func (h *PurchaseRequestHandler) Accept(",
	} {
		body := extractFunc(t, s, fn)
		if !strings.Contains(body, "HasBlockingPurchase(") {
			t.Errorf("%s must consult repo.HasBlockingPurchase", fn)
		}
		if !strings.Contains(body, "ErrCarSaleInProgress") {
			t.Errorf("%s must reject with models.ErrCarSaleInProgress", fn)
		}
	}
}

// TestPaymentFailed_WiredIntoStripeEvents: the purchase-side Stripe branch
// must handle payment_intent.payment_failed (it was silently dropped before
// C4) — record it and notify the buyer.
func TestPaymentFailed_WiredIntoStripeEvents(t *testing.T) {
	src, err := os.ReadFile("purchase_request.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := extractFunc(t, string(src), "func (h *PurchaseRequestHandler) HandleStripeEvent(")
	if !strings.Contains(body, `case "payment_intent.payment_failed":`) {
		t.Error("HandleStripeEvent must handle payment_intent.payment_failed")
	}
	if !strings.Contains(body, "MarkPaymentFailed(") {
		t.Error("payment_failed branch must record via repo.MarkPaymentFailed")
	}
}

// TestLicenceGate_WiredIntoLeaseCreate (F2a): a rejected/missing licence must
// block NEW lease requests — and only there (locked decision: never rip a
// driver out of an active rental).
func TestLicenceGate_WiredIntoLeaseCreate(t *testing.T) {
	src, err := os.ReadFile("lease_request.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := extractFunc(t, string(src), "func (h *LeaseRequestHandler) CreateLeaseRequest(")
	if !strings.Contains(body, "HasRequiredDocuments(") {
		t.Error("CreateLeaseRequest must gate on docRepo.HasRequiredDocuments")
	}
	if !strings.Contains(body, "DRIVER_LICENSE_INVALID") {
		t.Error("CreateLeaseRequest must reject with DRIVER_LICENSE_INVALID naming the re-upload path")
	}
}
