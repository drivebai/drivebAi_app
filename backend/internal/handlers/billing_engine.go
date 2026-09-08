package handlers

// The rolling-billing engine (batch 2, design §4): mints one cycle per
// lease-week at T−24h, charges it off-session under the ACTIVE CONSENT ROW
// (the only source of amount + payment method), retries by re-confirming
// the SAME PaymentIntent, and — on success — advances paid-through in ONE
// transaction with the paid claim. Arrears payouts accrue at charge time
// and promote when the week is consumed.
//
// Arrears cannot stack structurally: cycle N+1 is minted only at
// (paid-through − 24h) and paid-through advances only when the current
// cycle pays — at most ONE unpaid cycle exists per lease, ever.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drivebai/backend/internal/httputil"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	stripeService "github.com/drivebai/backend/internal/stripe"
	"github.com/google/uuid"
)

// SetBillingDependencies wires the rolling engine (house setter pattern).
func (h *LeaseRequestHandler) SetBillingDependencies(billingRepo *repository.BillingRepository, feeBPS int, rollingEnabled bool) {
	h.billingRepo = billingRepo
	h.billingFeeBPS = feeBPS
	h.rollingEnabled = rollingEnabled
}

// runBillingSweep is the engine tick (rides the pickup-expiry scanner's
// 60s ticker, after the lease sweeps).
func (h *LeaseRequestHandler) runBillingSweep(ctx context.Context) {
	if h.billingRepo == nil {
		return
	}
	now := time.Now().UTC()
	h.billingBootstrapPhase(ctx, now)
	h.billingPostReturnRefundPhase(ctx, now)
	h.billingNoticePhase(ctx, now)
	h.billingMintPhase(ctx, now)
	h.billingRetryPhase(ctx, now)
	h.runStuckChargingPhase(ctx, now)
	h.billingNeedsActionPhase(ctx, now)
	h.billingReturnedLeaseCloserPhase(ctx, now)
	h.billingAmendmentExpiryPhase(ctx, now)
	h.billingPromotePhase(ctx, now)
}

// Phase 1 — T−48h renewal notice (recurring claimed-once: stamped with the
// CURRENT paid-through value; the next advance re-arms it automatically).
func (h *LeaseRequestHandler) billingNoticePhase(ctx context.Context, now time.Time) {
	due, err := h.leaseRepo.ListRollingDueForBilling(ctx, now.Add(models.BillingNoticeLead), 50)
	if err != nil {
		h.logger.Error("billing notice: list", "error", err)
		return
	}
	for i := range due {
		lr := &due[i]
		consent, _ := h.billingRepo.GetActiveConsent(ctx, lr.ID)
		if consent == nil || !consent.Active() {
			continue // the mint phase halts these with the right copy
		}
		claimed, cerr := h.leaseRepo.ClaimRenewalNotice(ctx, lr.ID)
		if cerr != nil || !claimed {
			continue
		}
		amount := consent.AmountCents
		chatID := lr.ChatID
		leaseID := lr.ID
		chargeAt := lr.RentalEndsAt.Add(-models.BillingChargeLead)
		go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
			"Your rental renews soon",
			fmt.Sprintf("$%.2f will be charged to your saved card on %s. Return the car before then to stop.",
				float64(amount)/100, chargeAt.Format("Mon, Jan 2 15:04 MST")),
			&chatID, &leaseID)
	}
}

// Phase 2 — T−24h: mint the next cycle and make the first charge attempt.
func (h *LeaseRequestHandler) billingMintPhase(ctx context.Context, now time.Time) {
	due, err := h.leaseRepo.ListRollingDueForBilling(ctx, now.Add(models.BillingChargeLead), 50)
	if err != nil {
		h.logger.Error("billing mint: list", "error", err)
		return
	}
	for i := range due {
		lr := &due[i]
		consent, cerr := h.billingRepo.GetActiveConsent(ctx, lr.ID)
		if cerr != nil {
			h.logger.Error("billing mint: consent lookup", "error", cerr, "lease_request_id", lr.ID)
			continue
		}
		if consent == nil || !consent.Active() {
			// No chargeable consent: halt renewals with a reason that has
			// a party exit (card update re-activates), notify both, and
			// let paid-through lapse into the term scanner's machinery.
			if halted, herr := h.leaseRepo.HaltRenewals(ctx, lr.ID, "consent_revoked"); herr == nil && halted {
				chatID := lr.ChatID
				leaseID := lr.ID
				go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
					"Action needed to keep your rental",
					"We can't charge your saved card anymore. Update your payment method to continue the rental — otherwise it ends when your paid time runs out.",
					&chatID, &leaseID)
				go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypePayment,
					"Rental billing paused",
					"The driver's payment method needs updating. If they don't fix it, the rental ends when their paid time runs out — you'll be kept posted.",
					&chatID, &leaseID)
				h.logger.Warn("billing: renewals halted — no active consent", "lease_request_id", lr.ID)
			}
			continue
		}

		next, nerr := h.billingRepo.NextCycleNumber(ctx, lr.ID)
		if nerr != nil {
			h.logger.Error("billing mint: next cycle number", "error", nerr, "lease_request_id", lr.ID)
			continue
		}
		periodStart := *lr.RentalEndsAt
		cycle, merr := h.billingRepo.MintCycle(ctx, lr.ID, next, periodStart,
			periodStart.Add(models.BillingIntervalLength(consent.BillingInterval)), consent.AmountCents, now)
		if merr != nil || cycle == nil {
			h.logger.Error("billing mint: mint", "error", merr, "lease_request_id", lr.ID)
			continue
		}
		if cycle.Status == models.CycleScheduled {
			h.attemptCycleCharge(ctx, lr, cycle, consent)
		}
	}
}

// Phase 3 — retries of due cycles (same PI, per-attempt confirm keys).
func (h *LeaseRequestHandler) billingRetryPhase(ctx context.Context, now time.Time) {
	due, err := h.billingRepo.ListDueCycles(ctx, now, 50)
	if err != nil {
		h.logger.Error("billing retry: list", "error", err)
		return
	}
	for i := range due {
		c := &due[i]
		lr, gerr := h.leaseRepo.GetByID(ctx, c.LeaseRequestID)
		if gerr != nil || lr == nil {
			continue
		}
		// A return in flight, a halt, or a STOP pauses the ladder (review
		// H3: a stopped/terminated rental must never be charged again).
		if lr.RenewalHaltedReason != nil || lr.VehicleReturnedAt != nil || lr.RenewalStoppedAt != nil {
			continue
		}
		// Belt-and-braces live-return shield (verify pass: the revive path
		// can leave the halt unset — never charge mid-handshake).
		if h.returnRepoForDisputes != nil {
			if ret, rerr := h.returnRepoForDisputes.GetByLeaseRequestID(ctx, c.LeaseRequestID); rerr == nil && ret != nil &&
				ret.Status != models.VehicleReturnCompleted && ret.Status != models.VehicleReturnCancelled {
				continue
			}
		}
		consent, cerr := h.billingRepo.GetActiveConsent(ctx, c.LeaseRequestID)
		if cerr != nil || consent == nil || !consent.Active() {
			continue
		}
		h.attemptCycleCharge(ctx, lr, c, consent)
	}
}

// attemptCycleCharge makes exactly one attempt: first attempt creates the
// cycle's ONE intent (stable create key) confirmed off-session; retries
// re-confirm it. Outcomes route to paid / needs_action / retry ladder.
func (h *LeaseRequestHandler) attemptCycleCharge(ctx context.Context, lr *models.LeaseRequest, c *models.BillingCycle, consent *models.BillingConsent) {
	if h.stripe == nil {
		return
	}
	// The consent row is the ONLY amount authority (design §2).
	if c.AmountCents != consent.AmountCents {
		h.logger.Error("billing charge: cycle amount disagrees with consent — refusing",
			"cycle_id", c.ID, "cycle_cents", c.AmountCents, "consent_cents", consent.AmountCents)
		return
	}

	claimed, aerr := h.billingRepo.ClaimAttempt(ctx, c.ID)
	if aerr != nil || !claimed {
		return // another worker owns this attempt
	}
	attempt := c.AttemptCount + 1

	storedIntent := ""
	if c.StripePaymentIntentID != nil {
		storedIntent = *c.StripePaymentIntentID
	}
	// Crash-window reconciliation (review C2): a create whose response was
	// lost leaves the stored id empty while the intent — and possibly the
	// CHARGE — exists at Stripe. The create key expires after ~24h, so on
	// any attempt past the first we search by our cycle metadata before
	// ever creating again; a found intent is adopted, never duplicated.
	if storedIntent == "" && attempt > 1 {
		if found, ferr := h.stripe.FindPaymentIntentByCycle(c.ID.String()); ferr != nil {
			h.logger.Warn("billing charge: intent search failed, deferring", "error", ferr, "cycle_id", c.ID)
			h.deferAttempt(ctx, c)
			return
		} else if found != nil {
			storedIntent = found.ID
			if serr := h.billingRepo.StampIntent(ctx, c.ID, found.ID); serr != nil {
				h.logger.Error("billing charge: stamp reconciled intent", "error", serr, "cycle_id", c.ID)
			}
			if found.Status == "succeeded" {
				h.handleCyclePaid(ctx, c.ID, found.ID)
				return
			}
		}
	}

	var piID, piStatus string
	if storedIntent == "" {
		pi, err := h.stripe.CreatePaymentIntentWithOptions(c.AmountCents, lr.Currency, h.cycleCustomerID(ctx, lr),
			h.stripe.PlatformFee(c.AmountCents), fmt.Sprintf("cycle-%s", c.ID),
			stripePIOptions(consent, c, lr))
		if err != nil {
			h.recordCycleOutcomeError(ctx, lr, c, attempt, err)
			return
		}
		piID, piStatus = pi.ID, pi.Status
		if serr := h.billingRepo.StampIntent(ctx, c.ID, piID); serr != nil {
			// The intent exists; losing the stamp re-enters reconciliation
			// next attempt. Loud, not fatal.
			h.logger.Error("billing charge: stamp intent", "error", serr, "cycle_id", c.ID)
		}
	} else {
		pi, err := h.stripe.ConfirmPaymentIntent(storedIntent, strOrEmpty(consent.StripePaymentMethodID),
			fmt.Sprintf("cycle-%s-confirm-%d", c.ID, attempt))
		if err != nil {
			h.recordCycleOutcomeError(ctx, lr, c, attempt, err)
			return
		}
		piID, piStatus = pi.ID, pi.Status
	}

	switch piStatus {
	case "succeeded":
		h.handleCyclePaid(ctx, c.ID, piID)
	case "requires_action":
		if ok, _ := h.billingRepo.MarkNeedsAction(ctx, c.ID); ok {
			chatID := lr.ChatID
			leaseID := lr.ID
			go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
				"Action needed to keep your rental",
				"Your bank needs a quick verification for this week's rental payment. Open the app to approve it.",
				&chatID, &leaseID)
		}
	case "processing":
		// Not final: leave the row charging; the webhook decides.
	default:
		h.recordCycleOutcomeError(ctx, lr, c, attempt, fmt.Errorf("unexpected intent status %q", piStatus))
	}
}

// cycleCustomerID resolves the driver's bound customer (H6 binding).
func (h *LeaseRequestHandler) cycleCustomerID(ctx context.Context, lr *models.LeaseRequest) string {
	if cid, err := h.userRepo.GetStripeCustomerID(ctx, lr.DriverID); err == nil && cid != nil {
		return *cid
	}
	return ""
}

func stripePIOptions(consent *models.BillingConsent, c *models.BillingCycle, lr *models.LeaseRequest) stripeService.PaymentIntentOptions {
	return stripeService.PaymentIntentOptions{
		OffSessionConfirm: true,
		PaymentMethod:     strOrEmpty(consent.StripePaymentMethodID),
		Metadata: map[string]string{
			"kind":             "cycle",
			"billing_cycle_id": c.ID.String(),
			"lease_request_id": lr.ID.String(),
			"consent_id":       consent.ID.String(),
		},
	}
}

// hardDeclineCodes must never be retried (network rules).
var hardDeclineCodes = map[string]bool{
	"stolen_card": true, "lost_card": true, "pickup_card": true,
	"fraudulent": true, "invalid_account": true, "merchant_blacklist": true,
	"do_not_honor_forever": true,
}

// recordCycleOutcomeError classifies a failed attempt and walks the ladder:
// attempts at T−24h, T0, +24h, +48h; delinquent stamped at the 3rd failure;
// failed_final + halt at the 4th (or immediately on a hard decline).
func (h *LeaseRequestHandler) recordCycleOutcomeError(ctx context.Context, lr *models.LeaseRequest, c *models.BillingCycle, attempt int, chargeErr error) {
	es := chargeErr.Error()
	declineCode := extractDeclineCode(es)

	if strings.Contains(es, "authentication_required") {
		if ok, _ := h.billingRepo.MarkNeedsAction(ctx, c.ID); ok {
			chatID := lr.ChatID
			leaseID := lr.ID
			go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
				"Action needed to keep your rental",
				"Your bank needs a quick verification for this week's rental payment. Open the app to approve it.",
				&chatID, &leaseID)
		}
		return
	}

	terminal := attempt >= models.BillingMaxAttempts || hardDeclineCodes[declineCode]
	var nextAt *time.Time
	if !terminal {
		// Attempt 1 fired at T−24h; attempt 2 lands at T0 (period start);
		// later ones space 24h apart.
		var n time.Time
		if attempt == 1 {
			n = c.PeriodStart
		} else {
			n = time.Now().UTC().Add(models.BillingRetrySpacing)
		}
		nextAt = &n
	}
	updated, rerr := h.billingRepo.RecordFailure(ctx, c.ID, declineCode, nextAt, terminal)
	if rerr != nil || updated == nil {
		return
	}
	h.logger.Warn("billing charge failed", "cycle_id", c.ID, "attempt", attempt,
		"decline_code", declineCode, "terminal", terminal, "lease_request_id", lr.ID)

	chatID := lr.ChatID
	leaseID := lr.ID
	if claimed, _ := h.billingRepo.ClaimFailureNotice(ctx, c.ID); claimed {
		go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
			"Payment failed",
			"This week's rental payment didn't go through. Update your card or retry in the app — your rental continues while we retry.",
			&chatID, &leaseID)
	}

	// 3rd failure (T+24h): the lease is formally delinquent. Re-read the
	// cycle FIRST (batch-4 verification HIGH): a driver's on-session
	// pay-now can succeed between the ladder's confirm failure and this
	// write — branding a fully-paid lease delinquent would halt it with no
	// exit (the admin waive finds nothing to waive on a paid cycle).
	if fresh, ferr := h.billingRepo.GetCycle(ctx, c.ID); ferr != nil || fresh == nil || fresh.Status == models.CyclePaid {
		return
	}
	if attempt >= 3 {
		if marked, _ := h.leaseRepo.MarkDelinquent(ctx, lr.ID); marked {
			if claimed, _ := h.billingRepo.ClaimDelinquentNotice(ctx, c.ID); claimed {
				go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
					"Rental payment overdue",
					"Your weekly payment has failed repeatedly. Pay in the app or return the car — new bookings are paused until this is resolved.",
					&chatID, &leaseID)
				go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypePayment,
					"Weekly payment failed",
					"This week's payment for your rental couldn't be collected. The rental is ending unless the driver fixes it — you can also end it now from the rental card.",
					&chatID, &leaseID)
			}
		}
	}
	if terminal {
		if halted, _ := h.leaseRepo.HaltRenewals(ctx, lr.ID, "delinquent"); halted {
			h.logger.Warn("billing: renewals halted — dunning exhausted", "lease_request_id", lr.ID)
		}
	}
}

// extractDeclineCode pulls Stripe's decline_code/code out of a raw error body.
func extractDeclineCode(errBody string) string {
	for _, key := range []string{`"decline_code": "`, `"decline_code":"`, `"code": "`, `"code":"`} {
		if i := strings.Index(errBody, key); i >= 0 {
			rest := errBody[i+len(key):]
			if j := strings.IndexByte(rest, '"'); j > 0 {
				return rest[:j]
			}
		}
	}
	return ""
}

// Phase 4 — needs_action TTL: past 72h the rescue window closes.
func (h *LeaseRequestHandler) billingNeedsActionPhase(ctx context.Context, now time.Time) {
	expired, err := h.billingRepo.ListNeedsActionExpired(ctx, now.Add(-models.BillingNeedsActionTTL), 50)
	if err != nil {
		h.logger.Error("billing needs-action: list", "error", err)
		return
	}
	for i := range expired {
		c := &expired[i]
		lr, gerr := h.leaseRepo.GetByID(ctx, c.LeaseRequestID)
		if gerr != nil || lr == nil {
			continue
		}
		// Return-aware TTL (batch 3): the car is already back — marking a
		// finished rental delinquent (with "the rental is ending" copy) is
		// wrong on every axis. Settle what's actually owed instead: the
		// used days pro-rata as arrears, or a waive when the week was
		// never entered (an overshoot's verification lapsing).
		if lr.VehicleReturnedAt != nil {
			// Neutralize the live 3DS intent FIRST (belt; the webhook's
			// late-charge backstop is the braces): an intent that succeeds
			// after the write-off must not exist to succeed. Not proven
			// neutralized → leave the cycle for the next tick; the TTL
			// urgency is gone once the car is back. piMoneyMoved means the
			// charge landed — the webhook/stuck-charging machinery settles
			// it (needs_action is still in the paid-claim scope).
			if c.StripePaymentIntentID != nil && *c.StripePaymentIntentID != "" && h.stripe != nil {
				if neutralizePaymentIntentSvc(h.stripe, *c.StripePaymentIntentID) != piNeutralized {
					continue
				}
			}
			returnedAt := *lr.VehicleReturnedAt
			if !returnedAt.After(c.PeriodStart) {
				// The week began at/after the return — never entered,
				// nothing owed (the refund formula's 1-day floor must not
				// invent debt here — batch-3 review).
				_, _ = h.billingRepo.WaiveUnpaidCycle(ctx, c.ID,
					"waived: verification lapsed on a week that began after the vehicle was returned")
				continue
			}
			owed := c.AmountCents - models.ComputeReturnRefund(c.AmountCents, 1, c.PeriodStart, returnedAt).RefundAmountCents
			if owed > 0 {
				if settled, serr := h.billingRepo.SettleArrearsProRata(ctx, c.ID, owed); serr == nil && settled {
					h.openArrearsTicket(ctx, lr, c, owed)
					chatID := lr.ChatID
					leaseID := lr.ID
					go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
						"Balance due on your rental",
						fmt.Sprintf("The bank verification for your final rental week wasn't completed. $%.2f for the days you used is still owed — our support team will follow up to arrange payment.",
							float64(owed)/100),
						&chatID, &leaseID)
				}
			} else {
				_, _ = h.billingRepo.WaiveUnpaidCycle(ctx, c.ID,
					"waived: verification lapsed with nothing owed for the returned week")
			}
			continue
		}
		if updated, _ := h.billingRepo.RecordFailure(ctx, c.ID, "authentication_timeout", nil, true); updated != nil {
			_, _ = h.leaseRepo.MarkDelinquent(ctx, lr.ID)
			_, _ = h.leaseRepo.HaltRenewals(ctx, lr.ID, "delinquent")
			chatID := lr.ChatID
			leaseID := lr.ID
			go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
				"Verification window closed",
				"The bank verification for this week's payment wasn't completed, so the rental is ending. Pay in the app or return the car.",
				&chatID, &leaseID)
		}
	}
}

// Phase 5 — arrears promotion: consumed weeks become payable (guards live
// in the repository query); the existing payout sweep transfers them.
func (h *LeaseRequestHandler) billingPromotePhase(ctx context.Context, now time.Time) {
	if h.payoutRepo == nil {
		return
	}
	n, err := h.payoutRepo.PromoteConsumedCycles(ctx, now, 50)
	if err != nil {
		h.logger.Error("billing promote: promote", "error", err)
		return
	}
	if n > 0 {
		h.logger.Info("billing promote: cycles consumed → payable", "count", n)
	}
}

// handleCyclePaid is the ONE success path (called from the direct charge
// outcome and from the webhook route — both idempotent through the
// AdvanceOnCyclePaid claim): cycle→paid + paid-through advance atomically,
// then payout accrual and notifications.
func (h *LeaseRequestHandler) handleCyclePaid(ctx context.Context, cycleID uuid.UUID, intentID string) bool {
	cycle, advanced, err := h.billingRepo.AdvanceOnCyclePaid(ctx, cycleID)
	if err != nil {
		h.logger.Error("cycle paid: advance", "error", err, "cycle_id", cycleID)
		return false // webhook 500s; redelivery re-runs the whole TX
	}
	if cycle == nil {
		// The claim matched no row. Either already paid (redelivery — a
		// crash may have died between the advance and the accrual, review
		// H1), or the cycle sits in a status the claim scope refuses.
		existing, gerr := h.billingRepo.GetCycle(ctx, cycleID)
		if gerr != nil {
			return false // H2: unreadable ≠ settled; 500 → redelivery
		}
		if existing == nil {
			return true // intent metadata points at a deleted cycle — nothing to do
		}
		switch existing.Status {
		case models.CycleRefunded, models.CyclePartiallyRefunded:
			return true // money already settled through the cycle's own record
		case models.CycleArrearsDue, models.CycleWaived:
			// A charge SUCCEEDED for a week that was already written off or
			// cut to pro-rata arrears (late 3DS completion past the TTL
			// flip; an admin waive racing the confirm). ACKing here would
			// silently keep the driver's full week (batch-3 review
			// CRITICAL) — refund it instead. The status stays put: arrears
			// keeps its used-days debt for on-session collection.
			return h.refundLateChargeOnSettledCycle(ctx, existing, intentID)
		case models.CyclePaid:
			// Identity check first (batch-4 review): a paid cycle whose
			// recorded intent DIFFERS from this event's is a duplicate
			// charge — e.g. the original cycle intent succeeding late after
			// an arrears settlement overwrote the stamp. Refund it; only a
			// true redelivery of the recorded intent repairs the accrual.
			if intentID != "" && existing.StripePaymentIntentID != nil &&
				*existing.StripePaymentIntentID != "" && intentID != *existing.StripePaymentIntentID {
				return h.refundLateChargeOnSettledCycle(ctx, existing, intentID)
			}
			// fall through to the accrual repair below
		default:
			// A claimable status read back after a nil claim = a concurrent
			// worker owns the transition. 500 → redelivery re-checks.
			return false
		}
		if row, perr := h.payoutRepo.GetByBillingCycleID(ctx, cycleID); perr != nil {
			return false
		} else if row != nil {
			return true // fully settled — benign redelivery
		}
		cycle = existing
		// The durable discriminator decides which crash window this is
		// (verify-pass CRITICAL): refund_pending → the advance was REFUSED
		// and the refund must be retried; otherwise the advance happened
		// and only the accrual is missing.
		advanced = existing.AdminNote == nil || !strings.HasPrefix(*existing.AdminNote, "refund_pending")
	}
	lr, gerr := h.leaseRepo.GetByID(ctx, cycle.LeaseRequestID)
	if gerr != nil || lr == nil {
		h.logger.Error("cycle paid: load lease", "error", gerr, "lease_request_id", cycle.LeaseRequestID)
		return false
	}

	if !advanced {
		// The charge landed on a lease that is no longer occupied (review
		// H4: returned/terminal). Refund it in full — never keep, never
		// extend. Stable idem key; claimed-once on the cycle row.
		if h.stripe == nil || intentID == "" {
			h.logger.Error("cycle paid on dead lease: cannot refund (no stripe/intent)", "cycle_id", cycle.ID)
			return false
		}
		refund, rerr := h.stripe.CreateRefund(intentID, "cycle-refund-"+cycle.ID.String(), "requested_by_customer", 0)
		if rerr != nil {
			h.logger.Error("cycle paid on dead lease: refund failed — redelivery retries", "error", rerr, "cycle_id", cycle.ID)
			return false
		}
		if claimed, merr := h.billingRepo.RefundCycleClaim(ctx, cycle.ID, refund.ID, cycle.AmountCents); merr != nil {
			return false
		} else if claimed {
			chatID := lr.ChatID
			leaseID := lr.ID
			go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
				"Charge refunded",
				fmt.Sprintf("A weekly charge of $%.2f went through after your rental ended — it has been refunded in full.", float64(cycle.AmountCents)/100),
				&chatID, &leaseID)
			h.logger.Warn("cycle charge on ended lease refunded", "cycle_id", cycle.ID, "refund_id", refund.ID)
		}
		return true
	}

	// Accrue the owner's share (arrears; promoted at period_end). The
	// charge id makes the row dispute-addressable from birth.
	sourceCharge := ""
	if h.stripe != nil && intentID != "" {
		if chID, cerr := h.stripe.GetLatestChargeID(intentID); cerr == nil {
			sourceCharge = chID
		}
	}
	fee, ownerShare := models.ComputePayoutSplit(cycle.AmountCents, h.billingFeeBPS)
	var chargePtr *string
	if sourceCharge != "" {
		chargePtr = &sourceCharge
	}
	cycleRef := cycle.ID
	ps := cycle.PeriodStart
	pe := cycle.PeriodEnd
	if _, _, aerr := h.payoutRepo.CreateCycleAccruing(ctx, &models.OwnerPayout{
		LeaseRequestID:   cycle.LeaseRequestID,
		OwnerID:          lr.OwnerID,
		GrossKeptCents:   cycle.AmountCents,
		FeeBPS:           h.billingFeeBPS,
		FeeCents:         fee,
		OwnerAmountCents: ownerShare,
		Currency:         "USD",
		SourceChargeID:   chargePtr,
		BillingCycleID:   &cycleRef,
		PeriodStart:      &ps,
		PeriodEnd:        &pe,
	}); aerr != nil {
		h.logger.Error("cycle paid: accrue payout", "error", aerr, "cycle_id", cycle.ID)
		return false
	}

	h.logger.Info("cycle paid — paid-through advanced",
		"lease_request_id", lr.ID, "cycle", cycle.CycleNumber, "amount_cents", cycle.AmountCents)
	if updated, uerr := h.leaseRepo.GetByID(ctx, lr.ID); uerr == nil && updated != nil {
		h.broadcastLeaseUpdate(ctx, updated)
		chatID := updated.ChatID
		leaseID := updated.ID
		paidThrough := ""
		if updated.RentalEndsAt != nil {
			paidThrough = updated.RentalEndsAt.Format("Mon, Jan 2")
		}
		go h.notifHandler.Notify(updated.DriverID, models.NotificationTypePayment,
			fmt.Sprintf("Week %d paid", cycle.CycleNumber),
			fmt.Sprintf("$%.2f charged — you're paid through %s.", float64(cycle.AmountCents)/100, paidThrough),
			&chatID, &leaseID)
		go h.notifHandler.Notify(updated.OwnerID, models.NotificationTypePayment,
			fmt.Sprintf("$%.2f earned for week %d", float64(ownerShare)/100, cycle.CycleNumber),
			fmt.Sprintf("The week's rent was collected. Your $%.2f transfers when the week completes (%s).", float64(ownerShare)/100, paidThrough),
			&chatID, &leaseID)
	}
	return true
}

// StopRenewal — POST /api/v1/lease-requests/{id}/stop-renewal (driver).
// Halts future charges; every hour already paid is kept; the term scanner
// owns the tail. Claimed-once.
func (h *LeaseRequestHandler) StopRenewal(w http.ResponseWriter, r *http.Request) {
	h.renewalStopEndpoint(w, r, "driver")
}

// TerminateRenewal — POST /api/v1/lease-requests/{id}/terminate-renewal
// (owner). Same rule from the other side: no further renewals, driver
// returns by paid-through (floor 24h by construction).
func (h *LeaseRequestHandler) TerminateRenewal(w http.ResponseWriter, r *http.Request) {
	h.renewalStopEndpoint(w, r, "owner")
}

func (h *LeaseRequestHandler) renewalStopEndpoint(w http.ResponseWriter, r *http.Request, role string) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	leaseID, err := uuid.Parse(chiURLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid lease request ID"))
		return
	}
	var claimed bool
	var cerr error
	if role == "driver" {
		claimed, cerr = h.leaseRepo.StopRenewal(r.Context(), leaseID, userID)
	} else {
		claimed, cerr = h.leaseRepo.TerminateRenewal(r.Context(), leaseID, userID)
	}
	if cerr != nil {
		h.logger.Error("renewal stop", "error", cerr, "lease_request_id", leaseID, "role", role)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if !claimed {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("RENEWAL_STOP_REFUSED",
			"Auto-renew is already stopped, or this isn't your active weekly rental"))
		return
	}
	// An open unpaid cycle (charge fired at T−24h but the driver stops
	// before it resolves) must not keep the ladder alive: neutralize its
	// intent (proven-neutralized rule) and waive it. A cycle that already
	// PAID stays paid — the money settles at the return per the design.
	if oc, ocerr := h.billingRepo.GetOpenCycleForLease(r.Context(), leaseID); ocerr == nil && oc != nil {
		// Waive only on PROOF (verify-pass HIGH: an in-flight create with
		// no stored id must not be waived over — the charge may land):
		//   scheduled          → no attempt ever claimed, safe.
		//   stored intent      → proven-neutralized rule.
		//   no id, attempted   → search Stripe by metadata; unknown = leave
		//                        it (the ladder skips stopped leases; the
		//                        stuck-charging phase reconciles).
		intent := ""
		if oc.StripePaymentIntentID != nil {
			intent = *oc.StripePaymentIntentID
		}
		if intent == "" && oc.Status != models.CycleScheduled && h.stripe != nil {
			if found, ferr := h.stripe.FindPaymentIntentByCycle(oc.ID.String()); ferr == nil && found != nil {
				intent = found.ID
				_ = h.billingRepo.StampIntent(r.Context(), oc.ID, found.ID)
			} else if ferr != nil {
				intent = "unknown"
			}
		}
		waive := false
		switch {
		case oc.Status == models.CycleScheduled && intent == "":
			waive = true
		case intent != "" && intent != "unknown" && h.stripe != nil:
			waive = neutralizePaymentIntentSvc(h.stripe, intent) == piNeutralized
		}
		if waive {
			if _, werr := h.billingRepo.WaiveUnpaidCycle(r.Context(), oc.ID, "auto: renewal stopped by "+role); werr != nil {
				h.logger.Error("renewal stop: waive open cycle", "error", werr, "cycle_id", oc.ID)
			}
		} else {
			h.logger.Warn("renewal stop: open cycle left for reconciliation", "cycle_id", oc.ID, "status", oc.Status)
		}
	}

	lr, gerr := h.leaseRepo.GetByID(r.Context(), leaseID)
	if gerr != nil || lr == nil {
		httputil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	h.broadcastLeaseUpdate(r.Context(), lr)
	chatID := lr.ChatID
	ref := lr.ID
	endsCopy := "when the paid time runs out"
	if lr.RentalEndsAt != nil {
		endsCopy = "by " + lr.RentalEndsAt.Format("Mon, Jan 2 15:04 MST")
	}
	if role == "driver" {
		go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypeLeaseRequest,
			"Driver ended auto-renew",
			fmt.Sprintf("The weekly rental stops renewing — the car comes back %s.", endsCopy),
			&chatID, &ref)
	} else {
		go h.notifHandler.Notify(lr.DriverID, models.NotificationTypeLeaseRequest,
			"Owner ended the rental",
			fmt.Sprintf("No more weekly charges. Please return the car %s — every hour you've paid for is yours.", endsCopy),
			&chatID, &ref)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "return_by": lr.RentalEndsAt})
}

// chiURLParam avoids importing chi in this file twice across the package.
func chiURLParam(r *http.Request, key string) string {
	return chi.URLParam(r, key)
}

// deferAttempt returns a claimed attempt to the ladder unchanged (the
// attempt didn't reach Stripe): back to retrying with a short delay,
// attempt count NOT decremented (conservative — a stuck search shouldn't
// extend the ladder).
func (h *LeaseRequestHandler) deferAttempt(ctx context.Context, c *models.BillingCycle) {
	next := time.Now().UTC().Add(10 * time.Minute)
	if _, err := h.billingRepo.RecordFailure(ctx, c.ID, "reconciliation_deferred", &next, false); err != nil {
		h.logger.Error("billing charge: defer", "error", err, "cycle_id", c.ID)
	}
}

// runStuckChargingPhase (review M2): a 'charging' row older than the grace
// means the process died mid-attempt — re-read the intent's true state and
// route it like any other outcome. Runs inside runBillingSweep.
func (h *LeaseRequestHandler) runStuckChargingPhase(ctx context.Context, now time.Time) {
	stuck, err := h.billingRepo.ListStuckCharging(ctx, now.Add(-5*time.Minute), 50)
	if err != nil {
		h.logger.Error("billing stuck-charging: list", "error", err)
		return
	}
	for i := range stuck {
		c := &stuck[i]
		lr, gerr := h.leaseRepo.GetByID(ctx, c.LeaseRequestID)
		if gerr != nil || lr == nil {
			continue
		}
		intent := ""
		if c.StripePaymentIntentID != nil && *c.StripePaymentIntentID != "" {
			intent = *c.StripePaymentIntentID
		} else if h.stripe != nil {
			if found, ferr := h.stripe.FindPaymentIntentByCycle(c.ID.String()); ferr == nil && found != nil {
				intent = found.ID
				_ = h.billingRepo.StampIntent(ctx, c.ID, found.ID)
			}
		}
		if intent == "" || h.stripe == nil {
			// No intent ever landed: the attempt is void — back to the ladder.
			h.deferAttempt(ctx, c)
			continue
		}
		pi, rerr := h.stripe.RetrievePaymentIntent(intent)
		if rerr != nil {
			continue // transient; next tick
		}
		switch pi.Status {
		case "succeeded":
			h.handleCyclePaid(ctx, c.ID, intent)
		case "canceled":
			h.recordCycleOutcomeError(ctx, lr, c, c.AttemptCount, fmt.Errorf("intent canceled"))
		case "requires_action":
			if ok, _ := h.billingRepo.MarkNeedsAction(ctx, c.ID); ok {
				h.logger.Info("stuck charging → needs_action (TTL armed)", "cycle_id", c.ID)
			}
		case "processing":
			// Still in flight — the webhook owns it.
		default:
			h.deferAttempt(ctx, c)
		}
	}
}

// billingBootstrapPhase (batch 3): week 1 becomes a real cycle row shortly
// after pickup — paid, period [pickup, pickup+7d], intent from the week-1
// payments row — with its payout accrued on the SAME weekly cadence as
// every later cycle. Without this, week 1's owner share rode the legacy
// settle-at-return path: correct money, wrong cadence for open-ended.
func (h *LeaseRequestHandler) billingBootstrapPhase(ctx context.Context, now time.Time) {
	if h.payoutRepo == nil {
		return // accrual has nowhere to land — wait for full wiring
	}
	ids, err := h.billingRepo.ListRollingNeedingCycleOne(ctx, 50)
	if err != nil {
		h.logger.Error("billing bootstrap: list", "error", err)
		return
	}
	for _, leaseID := range ids {
		lr, gerr := h.leaseRepo.GetByID(ctx, leaseID)
		if gerr != nil || lr == nil || lr.PickupConfirmedAt == nil {
			continue
		}
		payment, perr := h.leaseRepo.GetPaymentByLeaseRequestID(ctx, leaseID)
		if perr != nil || payment == nil || payment.Status != models.PaymentStatusSucceeded {
			continue // week 1 not (yet) truly paid — wait
		}
		intent := ""
		if payment.PaymentIntentID != nil {
			intent = *payment.PaymentIntentID
		}
		periodStart := *lr.PickupConfirmedAt
		cycle, created, merr := h.billingRepo.MintCycleOnePaid(ctx, leaseID,
			periodStart, periodStart.Add(models.BillingCycleLength), payment.Amount, intent)
		if merr != nil || cycle == nil {
			h.logger.Error("billing bootstrap: mint cycle 1", "error", merr, "lease_request_id", leaseID)
			continue
		}
		// !created ⇒ the row already existed — usually a crash between the
		// mint and the accrual below (review H: `continue` here would lose
		// the owner's week-1 share forever, since no other path backfills
		// it). Fall through: CreateCycleAccruing is idempotent per cycle.
		// Only a still-cleanly-paid cycle accrues; a refunded/settled week
		// must not resurrect a payout.
		if !created && (cycle.Status != models.CyclePaid || cycle.RefundID != nil) {
			continue
		}
		_ = created
		// Accrue week 1 (idempotent per cycle); the promotion sweep pays it
		// at pickup+7d — the arrears cadence from day one.
		sourceCharge := ""
		if h.stripe != nil && intent != "" {
			if chID, cerr := h.stripe.GetLatestChargeID(intent); cerr == nil {
				sourceCharge = chID
			}
		}
		fee, ownerShare := models.ComputePayoutSplit(cycle.AmountCents, h.billingFeeBPS)
		var chargePtr *string
		if sourceCharge != "" {
			chargePtr = &sourceCharge
		}
		cycleRef := cycle.ID
		ps := cycle.PeriodStart
		pe := cycle.PeriodEnd
		if _, _, aerr := h.payoutRepo.CreateCycleAccruing(ctx, &models.OwnerPayout{
			LeaseRequestID:   leaseID,
			OwnerID:          lr.OwnerID,
			GrossKeptCents:   cycle.AmountCents,
			FeeBPS:           h.billingFeeBPS,
			FeeCents:         fee,
			OwnerAmountCents: ownerShare,
			Currency:         "USD",
			SourceChargeID:   chargePtr,
			BillingCycleID:   &cycleRef,
			PeriodStart:      &ps,
			PeriodEnd:        &pe,
		}); aerr != nil {
			h.logger.Error("billing bootstrap: accrue week 1", "error", aerr, "lease_request_id", leaseID)
			continue
		}
		h.logger.Info("billing bootstrap: week 1 cycled + accrued", "lease_request_id", leaseID)
	}
}

// RollingReturnSettlement describes what a rolling return settles: the
// final (current) cycle pro-rata, plus a fully-unconsumed overshoot cycle
// when the return lands inside the charge-lead window.
type RollingReturnSettlement struct {
	CurrentCycle *models.BillingCycle
	// CurrentRefundCents is the pro-rata refund IF the current cycle is
	// cleanly paid and not yet refunded; 0 otherwise (an unpaid week
	// refunds nothing — nothing was collected; an already-refunded week
	// carries its own record on the cycle row).
	CurrentRefundCents int64
	CurrentUsedDays    int
	OvershootCycle     *models.BillingCycle // charged for a week never entered; fully refundable when paid
}

// computeRollingSettlement maps returnedAt onto the lease's cycle ledger.
// The latest money-bearing cycle ends at the paid-through mark: when
// returnedAt falls inside its period it is the final (current) cycle; when
// returnedAt strictly precedes its period (the T−24h charge already bought
// the NEXT week) that cycle is a full-refund overshoot and its predecessor
// is current. CurrentCycle nil ⇒ nothing is cycled yet (pre-bootstrap
// week 1) and the caller decides between legacy math and deferral.
func computeRollingSettlement(ctx context.Context, repo *repository.BillingRepository, lr *models.LeaseRequest, returnedAt time.Time) (*RollingReturnSettlement, error) {
	out := &RollingReturnSettlement{}
	latest, err := repo.GetOpenOrLatestPaidCycle(ctx, lr.ID)
	if err != nil {
		return nil, err
	}
	if latest == nil {
		return out, nil // nothing cycled yet (pre-bootstrap) — caller decides
	}
	current := latest
	if returnedAt.Before(latest.PeriodStart) {
		// The latest cycle is an unconsumed overshoot.
		out.OvershootCycle = latest
		prev, perr := repo.GetCycleByNumber(ctx, lr.ID, latest.CycleNumber-1)
		if perr != nil {
			return nil, perr
		}
		if prev == nil {
			// Cycle N exists without N−1 — a bootstrap gap. Fail closed so
			// the caller defers; the sweep's bootstrap phase fills the hole.
			return nil, fmt.Errorf("rolling settlement: cycle %d missing for lease %s", latest.CycleNumber-1, lr.ID)
		}
		current = prev
	}
	out.CurrentCycle = current
	calc := models.ComputeReturnRefund(current.AmountCents, 1, current.PeriodStart, returnedAt)
	out.CurrentUsedDays = calc.UsedDays
	if current.Status == models.CyclePaid && current.RefundID == nil {
		out.CurrentRefundCents = calc.RefundAmountCents
	}
	return out, nil
}

// billingPostReturnRefundPhase (batch 3 reconciler): a cycle charge that
// was in flight while a return settled can land AFTER the settlement read
// the ledger — paid money for a week that starts after the car came back,
// invisible to every other path (promotion refuses it: vehicle_returned_at
// precedes period_end; the settlement already finished). Refund it in
// full. Strict '>' in the lister keeps the boundary week with the
// settlement's pro-rata instead. Key reuse with the settlement path
// ("cycle-overshoot-refund-<id>") is deliberate — whichever actor runs
// first, Stripe dedupes, and a same-charge race with the webhook's
// occupancy-ended branch can only error harmlessly (a second full refund
// exceeds the charge and Stripe rejects it).
func (h *LeaseRequestHandler) billingPostReturnRefundPhase(ctx context.Context, now time.Time) {
	if h.stripe == nil || h.payoutRepo == nil {
		return // refunds and their ledger writes both need full wiring
	}
	cycles, err := h.billingRepo.ListPaidCyclesAfterReturn(ctx, 20)
	if err != nil {
		h.logger.Error("billing post-return: list", "error", err)
		return
	}
	for _, bc := range cycles {
		intent := ""
		if bc.StripePaymentIntentID != nil {
			intent = *bc.StripePaymentIntentID
		}
		if intent == "" {
			h.logger.Error("billing post-return: paid cycle with no intent", "cycle_id", bc.ID)
			continue
		}
		// Void the accrual BEFORE the refund claim (batch-3 review: the
		// claim flips status/refund_id, which drops the cycle from this
		// lister — a void failure after it would never be retried and the
		// accruing row would sit exitless forever). Void-first is safe:
		// while the refund hasn't claimed, the cycle stays listed and every
		// step here is idempotent.
		if _, verr := h.payoutRepo.VoidAccruingCycle(ctx, bc.ID,
			"voided: charge landed after the vehicle was returned — refunded in full"); verr != nil {
			h.logger.Error("billing post-return: void accrual", "error", verr, "cycle_id", bc.ID)
			continue // next tick retries the whole unit
		}
		refund, rerr := h.stripe.CreateRefund(intent,
			"cycle-overshoot-refund-"+bc.ID.String(), "requested_by_customer", bc.AmountCents)
		if rerr != nil {
			h.logger.Error("billing post-return: refund", "error", rerr, "cycle_id", bc.ID)
			continue // next tick retries with the same key
		}
		if refund.Status != "succeeded" && refund.Status != "pending" {
			h.logger.Error("billing post-return: refund unhealthy", "cycle_id", bc.ID, "stripe_status", refund.Status)
			continue
		}
		claimed, cerr := h.billingRepo.RefundCycleClaim(ctx, bc.ID, refund.ID, bc.AmountCents)
		if cerr != nil {
			h.logger.Error("billing post-return: refund claim", "error", cerr, "cycle_id", bc.ID)
			continue
		}
		if !claimed {
			continue // another worker (or the settlement replay) owns the notify
		}
		h.logger.Info("billing post-return: refunded post-return cycle",
			"cycle_id", bc.ID, "lease_request_id", bc.LeaseRequestID, "amount_cents", bc.AmountCents)
		if lr, gerr := h.leaseRepo.GetByID(ctx, bc.LeaseRequestID); gerr == nil && lr != nil {
			chatID := lr.ChatID
			leaseRef := lr.ID
			go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
				"Refund issued",
				fmt.Sprintf("Your card was charged $%.2f for a rental week after your return — we've refunded it in full.",
					float64(bc.AmountCents)/100),
				&chatID, &leaseRef)
		}
	}
}

// refundLateChargeOnSettledCycle processes a paid-signal for a cycle whose
// status was already written off (arrears_due / waived) when the charge
// landed — the batch-3 review CRITICAL. Full refund with the SAME stable
// key as the occupancy-ended branch ("cycle-refund-<id>", amount 0=full),
// so whichever path reaches Stripe first wins and the other dedupes.
// Returns false (webhook 500 → redelivery) until both the refund and its
// cycle stamp are durable.
func (h *LeaseRequestHandler) refundLateChargeOnSettledCycle(ctx context.Context, c *models.BillingCycle, intentID string) bool {
	stored := ""
	if c.StripePaymentIntentID != nil {
		stored = *c.StripePaymentIntentID
	}
	intent := intentID
	if intent == "" {
		intent = stored
	}
	// The row-level refund record only proves the FIRST charge was
	// returned. A SECOND, distinct intent landing on the same cycle (a
	// stale arrears sheet confirmed after the debt settled; the original
	// cycle intent succeeding after an arrears overwrite) must get its own
	// refund — replaying `return true` off refund_id would silently keep
	// it (batch-4 review). Per-intent stable key makes each charge's
	// refund replay-safe independently. This benign-replay ACK needs no
	// Stripe, so it precedes the availability guard.
	if c.RefundID != nil && (intentID == "" || intentID == stored) {
		return true // recorded replay of the same charge — benign
	}
	if h.stripe == nil || intent == "" {
		h.logger.Error("late charge on settled cycle: cannot refund (no stripe/intent)", "cycle_id", c.ID)
		return false
	}
	refund, rerr := h.stripe.CreateRefund(intent, "cycle-late-refund-"+intent, "requested_by_customer", 0)
	if rerr != nil {
		if strings.Contains(rerr.Error(), "already been refunded") {
			return true // fully returned earlier under another path's key
		}
		h.logger.Error("late charge on settled cycle: refund failed — redelivery retries", "error", rerr, "cycle_id", c.ID)
		return false
	}
	if refund.Status != "succeeded" && refund.Status != "pending" {
		h.logger.Error("late charge on settled cycle: refund unhealthy", "cycle_id", c.ID, "stripe_status", refund.Status)
		return false
	}
	// Record the first refund on the row (claimed-once, written-off
	// statuses only); later duplicates keep their Stripe trail + the warn
	// log below as the audit record.
	if c.RefundID == nil {
		if _, serr := h.billingRepo.RecordLateChargeRefund(ctx, c.ID, refund.ID, refund.Amount); serr != nil {
			h.logger.Error("late charge on settled cycle: record", "error", serr, "cycle_id", c.ID)
			return false
		}
	}
	h.logger.Warn("late charge on written-off cycle refunded",
		"cycle_id", c.ID, "cycle_status", c.Status, "refund_id", refund.ID, "amount_cents", refund.Amount)
	if lr, gerr := h.leaseRepo.GetByID(ctx, c.LeaseRequestID); gerr == nil && lr != nil {
		chatID := lr.ChatID
		leaseID := lr.ID
		go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
			"Charge refunded",
			fmt.Sprintf("A payment of $%.2f went through after this rental week was closed out — it has been refunded in full.",
				float64(refund.Amount)/100),
			&chatID, &leaseID)
	}
	return true
}

// billingReturnedLeaseCloserPhase (batch-3 review): closes cycles left
// open after the rental factually ended. 'retrying' has NO other closer —
// the retry phase skips returned leases forever; a 'scheduled' with a
// stamped intent is past the settlement's provably-safe waive; and a
// 'failed_final' the settlement missed (crash window, or an overshoot the
// settlement's current-cycle arm never touches) would park. Any live
// intent is proven-neutralized first (belt; the webhook's late-charge
// backstop is the braces), then the used days settle as pro-rata arrears
// (ticket + notice) or the week waives when it began after the return.
func (h *LeaseRequestHandler) billingReturnedLeaseCloserPhase(ctx context.Context, now time.Time) {
	cycles, err := h.billingRepo.ListOpenCyclesOnReturnedLeases(ctx, 20)
	if err != nil {
		h.logger.Error("billing returned-closer: list", "error", err)
		return
	}
	for _, c := range cycles {
		lr, gerr := h.leaseRepo.GetByID(ctx, c.LeaseRequestID)
		if gerr != nil || lr == nil || lr.VehicleReturnedAt == nil {
			continue
		}
		intent := ""
		if c.StripePaymentIntentID != nil {
			intent = *c.StripePaymentIntentID
		}
		if intent != "" {
			if h.stripe == nil {
				continue
			}
			if neutralizePaymentIntentSvc(h.stripe, intent) != piNeutralized {
				// Live or unknown — retry next tick. piMoneyMoved routes
				// through the webhook/stuck-charging machinery instead.
				continue
			}
		}
		returnedAt := *lr.VehicleReturnedAt
		if !returnedAt.After(c.PeriodStart) {
			_, _ = h.billingRepo.WaiveUnpaidCycle(ctx, c.ID,
				"waived: week began after the vehicle was returned")
			continue
		}
		owed := c.AmountCents - models.ComputeReturnRefund(c.AmountCents, 1, c.PeriodStart, returnedAt).RefundAmountCents
		if owed <= 0 {
			_, _ = h.billingRepo.WaiveUnpaidCycle(ctx, c.ID,
				"waived: nothing owed for the returned week")
			continue
		}
		if settled, serr := h.billingRepo.SettleArrearsProRata(ctx, c.ID, owed); serr == nil && settled {
			h.openArrearsTicket(ctx, lr, c, owed)
			chatID := lr.ChatID
			leaseID := lr.ID
			go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
				"Balance due on your rental",
				fmt.Sprintf("$%.2f for the days used in your final rental week couldn't be collected — our support team will follow up to arrange payment.",
					float64(owed)/100),
				&chatID, &leaseID)
		}
	}
}

// openArrearsTicket gives every arrears_due debt a live actor (batch-3
// review: 'support will follow up' with no ticket is the defect-4 pattern
// this codebase already treats as a bug). Callers gate on the claimed-once
// SettleArrearsProRata, so each cycle opens at most one ticket.
func (h *LeaseRequestHandler) openArrearsTicket(ctx context.Context, lr *models.LeaseRequest, c *models.BillingCycle, owedCents int64) {
	if h.ticketRepo == nil {
		return
	}
	leaseRef := lr.ID
	subject := "Uncollected rental week — collection needed"
	desc := fmt.Sprintf(
		"A rolling rental ended with %s still owed for the used days of its final week (cycle %d, %s – %s).\n\nCollection is ON-SESSION ONLY (never charge the saved card silently). Arrange payment with the driver, or write the debt off via Admin → Rents → Billing cycles → Waive — uncollected days are borne by the owner per the rolling terms.\n\nLease request: %s",
		formatMoney(owedCents), c.CycleNumber,
		c.PeriodStart.Format("Jan 2"), c.PeriodEnd.Format("Jan 2, 2006"), lr.ID)
	if _, terr := h.ticketRepo.CreateSystemTicket(ctx, lr.DriverID, models.TicketCategoryPayments, subject, desc, &leaseRef, nil); terr != nil {
		h.logger.Error("arrears ticket failed", "error", terr, "cycle_id", c.ID)
	}
}

// billingAmendmentExpiryPhase closes lapsed offers and tells both sides
// plainly what continues: the rental, at the terms already agreed.
func (h *LeaseRequestHandler) billingAmendmentExpiryPhase(ctx context.Context, now time.Time) {
	expired, err := h.billingRepo.ExpireAmendments(ctx, 20)
	if err != nil {
		h.logger.Error("amendment expiry: sweep", "error", err)
		return
	}
	for _, a := range expired {
		lr, gerr := h.leaseRepo.GetByID(ctx, a.LeaseRequestID)
		if gerr != nil || lr == nil {
			continue
		}
		consent, _ := h.billingRepo.GetActiveConsent(ctx, lr.ID)
		cur := ""
		if consent != nil {
			cur = fmt.Sprintf(" at $%.2f", float64(consent.AmountCents)/100)
		}
		chatID := lr.ChatID
		leaseRef := lr.ID
		go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypeLeaseRequest,
			"Price proposal expired",
			fmt.Sprintf("The driver didn't respond to your proposed $%.2f — the rental continues%s. You can propose again, or end auto-renew from the rental card.",
				float64(a.NewAmountCents)/100, cur),
			&chatID, &leaseRef)
		go h.notifHandler.Notify(lr.DriverID, models.NotificationTypeLeaseRequest,
			"Price proposal expired",
			fmt.Sprintf("The owner's proposed $%.2f lapsed — your rental continues unchanged%s.", float64(a.NewAmountCents)/100, cur),
			&chatID, &leaseRef)
	}
}
