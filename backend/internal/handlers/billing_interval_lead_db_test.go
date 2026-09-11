package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// The listers are widened to the longest lead any interval needs (monthly,
// 72h). Each lease must still be acted on at ITS OWN lead: a weekly driver
// is told at T-48h and charged at T-24h, exactly as before the widening.
func TestBillingPhasesHoldEachLeaseToItsOwnLead(t *testing.T) {
	e := newPayoutEnv(t)
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	ctx := context.Background()

	mk := func(tag string, hoursOut int) uuid.UUID {
		t.Helper()
		run := uuid.NewString()[:8]
		owner := e.seedUser(t, "car_owner", "lead_o_"+tag+"_"+run+"@example.com")
		driver := e.seedUser(t, "driver", "lead_d_"+tag+"_"+run+"@example.com")
		e.seedLicense(t, driver)
		leaseID, _ := e.seedActiveRental(t, owner, driver)
		if _, err := e.db.Pool.Exec(ctx, `
			UPDATE lease_requests SET billing_mode='rolling', weeks=1,
			    rental_ends_at = NOW() + ($2::int || ' hours')::interval WHERE id=$1`, leaseID, hoursOut); err != nil {
			t.Fatalf("to rolling: %v", err)
		}
		text, ver := models.RollingDisclosureFor("weekly", 30000)
		if _, err := billingRepo.CreateConsent(ctx, &models.BillingConsent{
			LeaseRequestID: leaseID, DriverID: driver, AmountCents: 30000, TermsVersion: ver, DisclosureText: text,
		}); err != nil {
			t.Fatalf("consent: %v", err)
		}
		if ok, err := billingRepo.ActivateConsent(ctx, leaseID, "pm_lead_"+run, "visa", "4242", "fp_lead_"+run); err != nil || !ok {
			t.Fatalf("activate: ok=%v err=%v", ok, err)
		}
		t.Cleanup(func() {
			e.db.Pool.Exec(ctx, `DELETE FROM billing_cycles WHERE lease_request_id=$1`, leaseID)
			e.db.Pool.Exec(ctx, `DELETE FROM lease_billing_consents WHERE lease_request_id=$1`, leaseID)
		})
		return leaseID
	}
	notified := func(id uuid.UUID) bool {
		var v *time.Time
		_ = e.db.Pool.QueryRow(ctx, `SELECT renewal_notified_for FROM lease_requests WHERE id=$1`, id).Scan(&v)
		return v != nil
	}
	cycles := func(id uuid.UUID) int {
		var n int
		_ = e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM billing_cycles WHERE lease_request_id=$1`, id).Scan(&n)
		return n
	}

	now := time.Now()
	// Notice: the weekly lead is 48h. A lease 60h out is inside the widened
	// 72h list but must NOT be told yet; one 40h out must be.
	far, near := mk("far", 60), mk("near", 40)
	e.leaseH.billingNoticePhase(ctx, now)
	if notified(far) {
		t.Error("weekly lease 60h out was notified — it inherited the monthly lead")
	}
	if !notified(near) {
		t.Error("weekly lease 40h out was not notified — the weekly lead regressed")
	}

	// Mint: the weekly charge lead is 24h. 30h out must not mint; 20h must.
	mintFar, mintNear := mk("mintfar", 30), mk("mintnear", 20)
	e.leaseH.billingMintPhase(ctx, now)
	if n := cycles(mintFar); n != 0 {
		t.Errorf("weekly lease 30h out minted %d cycle(s) — it would be charged early", n)
	}
	if n := cycles(mintNear); n != 1 {
		t.Errorf("weekly lease 20h out minted %d cycle(s), want 1", n)
	}
	if n := cycles(far) + cycles(near); n != 0 {
		t.Errorf("notice-phase leases (40h/60h out) minted %d cycle(s)", n)
	}
}
