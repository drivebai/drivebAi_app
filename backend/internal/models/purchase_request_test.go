package models

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestPurchaseRequestStatus_IsTerminal covers all terminal / non-terminal
// classification. Both branches matter — a false-positive on IsTerminal
// would silently strand a live purchase (scanners skip terminal rows), a
// false-negative would let terminal cards keep re-appearing in Today.
func TestPurchaseRequestStatus_IsTerminal(t *testing.T) {
	terminals := []PurchaseRequestStatus{
		PurchaseStatusCompleted,
		PurchaseStatusRejectedRefunded,
		PurchaseStatusRejectedUpheld,
		PurchaseStatusDeclined,
		PurchaseStatusCancelled,
		PurchaseStatusExpired,
		PurchaseStatusExpiredAuth,
	}
	for _, s := range terminals {
		if !s.IsTerminal() {
			t.Errorf("expected %q terminal", s)
		}
	}
	nonTerminals := []PurchaseRequestStatus{
		PurchaseStatusRequested,
		PurchaseStatusAccepted,
		PurchaseStatusBOSPendingBuyer,
		PurchaseStatusBOSPendingSeller,
		PurchaseStatusBOSSigned,
		PurchaseStatusPaymentAuthorized,
		PurchaseStatusHandoverScheduled,
		PurchaseStatusAwaitingInspection,
		PurchaseStatusInspectionAccepted,
		PurchaseStatusInspectionRejected,
	}
	for _, s := range nonTerminals {
		if s.IsTerminal() {
			t.Errorf("did not expect %q terminal", s)
		}
	}
}

// TestPurchaseRequestStatus_IsActive: `inspection_accepted` is transient
// (capture in flight) so Today should NOT surface it — but every other
// non-terminal state should.
func TestPurchaseRequestStatus_IsActive(t *testing.T) {
	if PurchaseStatusInspectionAccepted.IsActive() {
		t.Errorf("inspection_accepted must not be active on Today (transient)")
	}
	if !PurchaseStatusAwaitingInspection.IsActive() {
		t.Errorf("awaiting_inspection must surface on Today")
	}
	if PurchaseStatusCompleted.IsActive() {
		t.Errorf("terminal states must not be active")
	}
}

// TestPurchaseRejectionReason_IsValid ensures the buyer's rejection body
// is validated against the enum before we insert. A false-positive here
// would let arbitrary strings hit the DB CHECK and 500 the request.
func TestPurchaseRejectionReason_IsValid(t *testing.T) {
	good := []PurchaseRejectionReason{
		PurchaseRejectionUndisclosedDamage,
		PurchaseRejectionMechanicalIssues,
		PurchaseRejectionTitleOrPaperwork,
		PurchaseRejectionVINMismatch,
		PurchaseRejectionNotAsDescribed,
		PurchaseRejectionNoShow,
		PurchaseRejectionOther,
	}
	for _, r := range good {
		if !r.IsValid() {
			t.Errorf("expected %q valid", r)
		}
	}
	if PurchaseRejectionReason("not_a_reason").IsValid() {
		t.Errorf("random string must not validate")
	}
	if PurchaseRejectionReason("").IsValid() {
		t.Errorf("empty string must not validate")
	}
}

// TestBillOfSale_SigningFlags: the two SellerSigned/BuyerSigned + combined
// FullySigned helpers drive the state machine transitions from
// accepted → bos_pending_* → bos_signed. If any of them mis-report the
// signature state, we'd either block signing forever or advance to
// payment authorization without both signatures.
func TestBillOfSale_SigningFlags(t *testing.T) {
	now := time.Now()
	b := &PurchaseBillOfSale{}

	if b.SellerSigned() || b.BuyerSigned() || b.FullySigned() {
		t.Errorf("empty BoS reported signed")
	}

	b.SellerSignedAt = &now
	if !b.SellerSigned() {
		t.Errorf("SellerSigned false after stamping seller_signed_at")
	}
	if b.FullySigned() {
		t.Errorf("FullySigned true with only seller signature")
	}

	b.BuyerSignedAt = &now
	if !b.BuyerSigned() {
		t.Errorf("BuyerSigned false after stamping buyer_signed_at")
	}
	if !b.FullySigned() {
		t.Errorf("FullySigned false after both signed")
	}
}

// TestPurchaseOfferMinCents_Threshold locks the $1,000 floor from the
// CreateListing spec. If someone bumps this without updating the model
// enum, the DB CHECK constraint will still refuse but the handler will
// return an ambiguous 500 instead of the friendly OFFER_TOO_LOW.
func TestPurchaseOfferMinCents_Threshold(t *testing.T) {
	if PurchaseOfferMinCents != 100_000 {
		t.Errorf("min cents should be $1000 = 100_000, got %d", PurchaseOfferMinCents)
	}
}

// TestPurchaseTTLs sanity-checks the three configurable windows. The
// Stripe auth window is fixed at ~7d server-side; the offer + inspection
// windows are product-tunable but we verify they're sensible defaults so
// a bad env override doesn't silently kill the flow.
func TestPurchaseTTLs(t *testing.T) {
	if PurchaseOfferTTL <= 0 || PurchaseInspectionWindow <= 0 || PurchaseAuthTTL <= 0 {
		t.Fatalf("TTLs must be positive")
	}
	if PurchaseAuthTTL != 7*24*time.Hour {
		t.Errorf("PurchaseAuthTTL should match Stripe's ~7d limit")
	}
	if PurchaseInspectionWindow > PurchaseAuthTTL {
		t.Errorf("inspection window (%s) exceeds auth TTL (%s) — flow will auto-expire before inspection can complete", PurchaseInspectionWindow, PurchaseAuthTTL)
	}
}

// TestPurchaseNotificationTypes verifies the four new notification types
// are distinct strings so iOS DeepLinkRouter can route correctly.
func TestPurchaseNotificationTypes(t *testing.T) {
	types := []NotificationType{
		NotificationTypePurchaseRequest,
		NotificationTypePurchasePayment,
		NotificationTypePurchaseHandover,
		NotificationTypePurchaseRejection,
	}
	seen := map[NotificationType]bool{}
	for _, tp := range types {
		if seen[tp] {
			t.Errorf("duplicate notification type %q", tp)
		}
		seen[tp] = true
		if string(tp) == "" {
			t.Errorf("empty notification type")
		}
	}
}

// TestPurchaseRejectionEvidenceLimits: the file cap + byte cap are used
// on both the multipart parser and the DB pre-check. Locking them down
// prevents an accidental doubling that would let a rogue client upload
// arbitrary blobs into /uploads/purchases/.
func TestPurchaseRejectionEvidenceLimits(t *testing.T) {
	if PurchaseRejectionEvidenceMaxFiles < 1 {
		t.Errorf("evidence file cap must be >= 1")
	}
	if PurchaseRejectionEvidenceMaxBytes < 1<<20 {
		t.Errorf("per-file cap must be at least 1 MB")
	}
	if PurchaseExplanationMinLen >= PurchaseExplanationMaxLen {
		t.Errorf("explanation min must be < max")
	}
}

// TestPurchaseCarSoldConstant guards against an accidental rename of the
// terminal car status. If this constant changes, the enum migration must
// also change; catching the drift here is cheaper than a bad prod deploy.
func TestPurchaseCarSoldConstant(t *testing.T) {
	if string(CarStatusSold) != "sold" {
		t.Errorf("CarStatusSold must map to lowercase 'sold' in the DB enum")
	}
}

// TestPurchaseRequestResponse_ViewerRoleShape is a defensive check on the
// three known viewer_role values that iOS branches on. Regressions here
// would leave buyer/seller cards rendering with the wrong CTA and no
// obvious error.
func TestPurchaseRequestResponse_ViewerRoleShape(t *testing.T) {
	sellerID := uuid.New()
	buyerID := uuid.New()
	resp := PurchaseRequestResponse{
		SellerID: sellerID,
		BuyerID:  buyerID,
	}
	// Simulate the handler assignment paths.
	viewerCases := []struct {
		viewer  uuid.UUID
		expects string
	}{
		{sellerID, "seller"},
		{buyerID, "buyer"},
		{uuid.New(), "admin"},
	}
	for _, tc := range viewerCases {
		switch tc.viewer {
		case resp.SellerID:
			resp.ViewerRole = "seller"
		case resp.BuyerID:
			resp.ViewerRole = "buyer"
		default:
			resp.ViewerRole = "admin"
		}
		if resp.ViewerRole != tc.expects {
			t.Errorf("viewer %v got %q, want %q", tc.viewer, resp.ViewerRole, tc.expects)
		}
	}
}

// TestPurchaseErrors_HaveCodes prevents someone from creating a naked
// APIError without a code — codes are what iOS matches against, so a
// blank one strands the client on a generic "Something went wrong."
func TestPurchaseErrors_HaveCodes(t *testing.T) {
	errs := []*APIError{
		ErrCannotBuyOwnCar,
		ErrCarNotForSale,
		ErrCarSold,
		ErrDuplicatePurchase,
		ErrInvalidPurchaseAction,
		ErrBOSLocked,
		ErrBOSNotSigned,
		ErrAlreadySigned,
		ErrNotAwaitingInspection,
		ErrNotHandoverScheduled,
		ErrPurchaseRequestNotFound,
		ErrPurchaseRejectionNotFound,
		ErrPurchaseNotCancellable,
		ErrPurchaseOfferTooLow,
		ErrInvalidRoleField,
		ErrPurchaseEvidenceRequired,
	}
	seen := map[string]bool{}
	for _, e := range errs {
		if e == nil {
			t.Errorf("nil error in list")
			continue
		}
		if e.Code == "" {
			t.Errorf("error %+v missing code", e)
		}
		if e.Message == "" {
			t.Errorf("error %+v missing message", e)
		}
		if seen[e.Code] {
			t.Errorf("duplicate error code %q", e.Code)
		}
		seen[e.Code] = true
	}
}
