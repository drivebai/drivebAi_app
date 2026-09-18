package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// A rolling lease that reaches 'paid' WITHOUT a webhook must still have its
// mandate bound. Before the recovery path existed, SyncPaymentStatus and the
// reconciliation sweep left activated_at NULL: the lease was paid, the owner
// was told "renewing until they return it", and the engine then halted
// renewals telling the driver we could not charge a card that had never been
// attached (observed in the T-C/C3 run, 2026-09-18).
func TestRecoveredConsentBindsTheCardThatPaid(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)

	owner := e.seedUser(t, "car_owner", "cr_o_"+uuid.NewString()[:8]+"@example.com")
	driver := e.seedUser(t, "driver", "cr_d_"+uuid.NewString()[:8]+"@example.com")
	e.seedLicense(t, driver)
	leaseID, _ := e.seedActiveRental(t, owner, driver)

	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE lease_requests SET billing_mode='rolling', billing_interval='weekly' WHERE id=$1`, leaseID); err != nil {
		t.Fatalf("make rolling: %v", err)
	}
	if _, err := billingRepo.CreateConsent(ctx, &models.BillingConsent{
		LeaseRequestID: leaseID, DriverID: driver, AmountCents: 15000,
		TermsVersion: models.TermsVersionRolling, DisclosureText: "test disclosure",
	}); err != nil {
		t.Fatalf("create consent: %v", err)
	}

	lr, err := e.leaseRepo.GetByID(ctx, leaseID)
	if err != nil || lr == nil {
		t.Fatalf("load lease: %v", err)
	}

	// The card that paid is passed straight in — exactly what SyncPaymentStatus
	// has in pi.PaymentMethod. No Stripe call is needed for the binding itself.
	e.leaseH.activateRecoveredConsent(ctx, lr, "", "pm_recovered")

	consent, cerr := billingRepo.GetActiveConsent(ctx, leaseID)
	if cerr != nil || consent == nil {
		t.Fatalf("read consent: %v", cerr)
	}
	if consent.ActivatedAt == nil {
		t.Fatal("recovered rolling lease left the mandate unactivated — renewals would halt and blame the driver's card")
	}
	if consent.StripePaymentMethodID == nil || *consent.StripePaymentMethodID != "pm_recovered" {
		t.Errorf("mandate bound to %v, want pm_recovered", consent.StripePaymentMethodID)
	}
	if consent.AmountCents != 15000 {
		t.Errorf("recovery changed the consented amount to %d — it must never touch the amount", consent.AmountCents)
	}

	// Re-entry must not rebind: ActivateConsent is claimed-once, so a racing
	// webhook arriving after recovery cannot overwrite the bound card.
	e.leaseH.activateRecoveredConsent(ctx, lr, "", "pm_second")
	again, _ := billingRepo.GetActiveConsent(ctx, leaseID)
	if again == nil || again.StripePaymentMethodID == nil || *again.StripePaymentMethodID != "pm_recovered" {
		t.Errorf("second recovery rebound the mandate to %v — it must stay on the card that paid", again.StripePaymentMethodID)
	}
}

// The fixed-term path must not acquire a billing dependency it never had: a
// billing-table outage cannot be allowed to touch the legacy payment path.
func TestRecoveredConsentIgnoresFixedTerm(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)

	owner := e.seedUser(t, "car_owner", "cf_o_"+uuid.NewString()[:8]+"@example.com")
	driver := e.seedUser(t, "driver", "cf_d_"+uuid.NewString()[:8]+"@example.com")
	e.seedLicense(t, driver)
	leaseID, _ := e.seedActiveRental(t, owner, driver)

	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE lease_requests SET billing_mode='fixed_term' WHERE id=$1`, leaseID); err != nil {
		t.Fatalf("make fixed-term: %v", err)
	}
	// A consent row should not exist for fixed-term, but seed one anyway: if
	// the guard ever regresses, this is what it would wrongly activate.
	if _, err := billingRepo.CreateConsent(ctx, &models.BillingConsent{
		LeaseRequestID: leaseID, DriverID: driver, AmountCents: 15000,
		TermsVersion: models.TermsVersionRolling, DisclosureText: "must not be activated",
	}); err != nil {
		t.Fatalf("create consent: %v", err)
	}

	lr, err := e.leaseRepo.GetByID(ctx, leaseID)
	if err != nil || lr == nil {
		t.Fatalf("load lease: %v", err)
	}
	e.leaseH.activateRecoveredConsent(ctx, lr, "", "pm_should_not_bind")

	consent, _ := billingRepo.GetActiveConsent(ctx, leaseID)
	if consent == nil {
		t.Fatal("consent row vanished")
	}
	if consent.ActivatedAt != nil {
		t.Error("fixed-term lease activated a mandate — the recurring machinery must not reach the legacy path")
	}
	if consent.StripePaymentMethodID != nil {
		t.Errorf("fixed-term lease bound a card (%v)", consent.StripePaymentMethodID)
	}
}
