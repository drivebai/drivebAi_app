package handlers

// Batch 1 of the recurring-billing plan — closes audit finding M2. Card
// disputes and out-of-band refunds become visible and actionable.
//
// Transactional discipline (post-review):
//   - Money-signal lookups FAIL CLOSED: a transient DB/Stripe error returns
//     false → 500 → Stripe redelivers (H2 rule). Only definitive
//     not-ours/malformed events are ACKed unprocessed.
//   - Closure side effects are RE-RUNNABLE and gated on the durable
//     outcome_settled marker, not on the Close claim: a crash after Close
//     leaves outcome_settled=false and the next delivery re-runs
//     everything. Every step in between is idempotent (status-scoped
//     updates, stable Stripe idempotency keys, targeted ticket resolve).
//   - Before any WON release, the charge's refunded amount is re-read from
//     Stripe: a closure whose charge was refunded (inquiry ended by a
//     dashboard refund; statuses warning_closed / charge_refunded) must
//     never release payouts as if the platform kept the money.
//   - Reversals are proportional to the DISPUTED amount, target the row by
//     source_charge_id (stamped by MarkPaid) with a lease-scoped fallback
//     for legacy rows, and replay safely under a stable idempotency key.

import (
	"context"
	"fmt"
	"net/http"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	"github.com/google/uuid"
)

// SetDisputeDependencies wires the dispute mirror + payout collaborators.
func (h *LeaseRequestHandler) SetDisputeDependencies(disputeRepo *repository.ChargeDisputeRepository, payoutRepo *repository.PayoutRepository) {
	h.disputeRepo = disputeRepo
	h.payoutRepo = payoutRepo
}

// SetReturnRepositoryForDisputes wires the vehicle-return lookup used for
// refund recognition.
func (h *LeaseRequestHandler) SetReturnRepositoryForDisputes(r *repository.VehicleReturnRepository) {
	h.returnRepoForDisputes = r
}

// handleChargeDispute processes charge.dispute.created/updated/closed.
// Returns false whenever redelivery could complete unfinished work.
func (h *LeaseRequestHandler) handleChargeDispute(r *http.Request, eventType string, obj map[string]interface{}) bool {
	ctx := r.Context()
	if h.disputeRepo == nil || h.payoutRepo == nil {
		// Wiring bug: refuse the event so Stripe keeps redelivering until
		// the deploy is fixed — an ACK here would swallow money signals.
		h.logger.Error("dispute webhook: dependencies not wired — refusing event")
		return false
	}

	disputeID, _ := obj["id"].(string)
	chargeID, _ := obj["charge"].(string)
	intentID, _ := obj["payment_intent"].(string)
	status, _ := obj["status"].(string)
	reason, _ := obj["reason"].(string)
	amount := int64(0)
	if a, ok := obj["amount"].(float64); ok {
		amount = int64(a)
	}
	currency, _ := obj["currency"].(string)
	if currency == "" {
		currency = "usd"
	}
	if disputeID == "" || chargeID == "" {
		h.logger.Error("dispute webhook: event missing id/charge", "type", eventType)
		return true // malformed — nothing a retry fixes
	}

	// Resolve the lease through the payment intent. FAIL CLOSED on errors;
	// nil results (purchase-era / unknown charges) proceed lease-less.
	var leaseRef *uuid.UUID
	var lr *models.LeaseRequest
	if intentID != "" {
		payment, perr := h.leaseRepo.GetPaymentByIntentID(ctx, intentID)
		if perr != nil {
			if apiErr := models.GetAPIError(perr); apiErr == nil || apiErr.Code != models.ErrCodePaymentNotFound {
				h.logger.Error("dispute webhook: payment lookup", "error", perr, "intent_id", intentID)
				return false
			}
		}
		if payment != nil {
			cur, gerr := h.leaseRepo.GetByID(ctx, payment.LeaseRequestID)
			if gerr != nil || cur == nil {
				h.logger.Error("dispute webhook: lease lookup", "error", gerr, "lease_request_id", payment.LeaseRequestID)
				return false
			}
			lr = cur
			leaseRef = &cur.ID
		} else if h.billingRepo != nil {
			// Rolling cycle charges live in billing_cycles, not payments
			// (review H2: cycle disputes were lease-blind).
			var cycleLease uuid.UUID
			ferr := h.dbLookupCycleLease(ctx, intentID, &cycleLease)
			if ferr != nil {
				h.logger.Error("dispute webhook: cycle lease lookup", "error", ferr, "intent_id", intentID)
				return false
			}
			if cycleLease != uuid.Nil {
				cur, gerr := h.leaseRepo.GetByID(ctx, cycleLease)
				if gerr != nil || cur == nil {
					return false
				}
				lr = cur
				leaseRef = &cur.ID
			}
		}
	}

	var reasonPtr, intentPtr *string
	if reason != "" {
		reasonPtr = &reason
	}
	if intentID != "" {
		intentPtr = &intentID
	}
	d, createdNow, err := h.disputeRepo.Upsert(ctx, &models.ChargeDispute{
		StripeDisputeID: disputeID,
		StripeChargeID:  chargeID,
		PaymentIntentID: intentPtr,
		LeaseRequestID:  leaseRef,
		AmountCents:     amount,
		Currency:        currency,
		Reason:          reasonPtr,
		Status:          status,
	})
	if err != nil {
		h.logger.Error("dispute webhook: upsert", "error", err, "dispute_id", disputeID)
		return false
	}
	// The upsert backfills lease linkage on redelivery; prefer the stored
	// linkage from here on (first delivery may have raced the payment row).
	if d.LeaseRequestID != nil && lr == nil {
		if cur, gerr := h.leaseRepo.GetByID(ctx, *d.LeaseRequestID); gerr == nil && cur != nil {
			lr = cur
		}
	}

	h.logger.Warn("card dispute event",
		"type", eventType, "dispute_id", disputeID, "charge_id", chargeID,
		"amount_cents", amount, "status", status, "created_now", createdNow,
		"lease_request_id", d.LeaseRequestID)

	if !isDisputeClosedStatus(status) {
		return h.disputeOpenSideEffects(ctx, d, lr)
	}
	return h.disputeClosureSideEffects(ctx, d, lr, status)
}

func isDisputeClosedStatus(status string) bool {
	switch status {
	case "won", "lost", "warning_closed", "charge_refunded":
		return true
	}
	return false
}

func (h *LeaseRequestHandler) disputeNoteTag(d *models.ChargeDispute) string {
	return "dispute " + d.StripeDisputeID
}

// disputeOpenSideEffects: withhold FIRST (idempotent), claim AFTER, so a
// withhold failure stays retryable and the ticket never lies about what
// happened (review: open-path ordering).
func (h *LeaseRequestHandler) disputeOpenSideEffects(ctx context.Context, d *models.ChargeDispute, lr *models.LeaseRequest) bool {
	if d.PayoutsWithheld {
		return true // side effects already completed for this dispute
	}

	if d.LeaseRequestID != nil {
		note := h.disputeNoteTag(d) + ": withheld pending outcome"
		n, werr := h.payoutRepo.WithholdAllUnpaidForLease(ctx, *d.LeaseRequestID, note)
		if werr != nil {
			h.logger.Error("dispute open: withhold payouts", "error", werr, "dispute_id", d.StripeDisputeID)
			return false // retryable; claim not yet taken
		}
		if n > 0 {
			h.logger.Warn("dispute open: unpaid payout rows withheld", "count", n, "lease_request_id", *d.LeaseRequestID)
		}
	}

	claimed, cerr := h.disputeRepo.ClaimPayoutsWithheld(ctx, d.ID)
	if cerr != nil {
		h.logger.Error("dispute open: claim", "error", cerr, "dispute_id", d.StripeDisputeID)
		return false
	}
	if !claimed {
		return true // another delivery finished first
	}

	// Ticket: filed under the OWNER (their money is on hold; drivers must
	// never see dispute workflow — review: the disputing party could read
	// evidence strategy from their own ticket list). Copy is neutral;
	// evidence instructions live in logs + the Stripe dashboard.
	if h.ticketRepo != nil && lr != nil {
		subject := fmt.Sprintf("Card dispute opened — $%.2f", float64(d.AmountCents)/100)
		desc := fmt.Sprintf(
			"Stripe dispute %s on charge %s ($%.2f). Unpaid owner payouts for lease %s are withheld pending the outcome; nothing was clawed back. See the Stripe dashboard for the response deadline.",
			d.StripeDisputeID, d.StripeChargeID, float64(d.AmountCents)/100, lr.ID)
		if t, terr := h.ticketRepo.CreateSystemTicket(ctx, lr.OwnerID, models.TicketCategoryPayments,
			subject, desc, d.LeaseRequestID, nil); terr != nil {
			h.logger.Error("dispute open: ticket", "error", terr, "dispute_id", d.StripeDisputeID)
		} else if t != nil {
			_ = h.disputeRepo.SetTicket(ctx, d.ID, t.ID)
		} else {
			h.logger.Warn("dispute open: ticket deduped against an existing live lease ticket — dispute is queued there",
				"dispute_id", d.StripeDisputeID, "lease_request_id", *d.LeaseRequestID)
		}
	}

	// Rolling leases: pause renewals while the dispute is open (cleared on
	// a won closure; a lost/refunded closure leaves the halt for the term
	// machinery to drive the return).
	if lr != nil {
		if _, herr := h.leaseRepo.HaltRenewals(ctx, lr.ID, "dispute"); herr != nil {
			h.logger.Warn("dispute open: halt renewals", "error", herr, "lease_request_id", lr.ID)
		}
	}

	if lr != nil {
		chatID := lr.ChatID
		leaseID := lr.ID
		go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypePayment,
			"Payment under review",
			"The card payment for one of your rentals is being disputed by the cardholder's bank. Your payout for it is on hold while we respond — most disputes are resolved in the platform's favor. Nothing is needed from you.",
			&chatID, &leaseID)
	}
	return true
}

// disputeClosureSideEffects drives EVERY closure delivery until the durable
// outcome_settled marker claims. Steps are idempotent; any failure returns
// false so Stripe redelivers and finishes the job (review R1).
func (h *LeaseRequestHandler) disputeClosureSideEffects(ctx context.Context, d *models.ChargeDispute, lr *models.LeaseRequest, status string) bool {
	if d.OutcomeSettled {
		return true // fully handled — redelivery is benign
	}

	// The money question first, from the source of truth: did the charge's
	// money go back to the cardholder? Covers 'lost' (chargeback debit),
	// 'charge_refunded'/'warning_closed'-via-refund (dashboard refund), and
	// makes the won/release decision immune to status-string ambiguity.
	refundedCents := int64(0)
	if h.stripe != nil {
		rc, rerr := h.stripe.GetChargeRefundedAmount(d.StripeChargeID)
		if rerr != nil {
			h.logger.Error("dispute close: charge refund check failed — deferring to redelivery", "error", rerr, "dispute_id", d.StripeDisputeID)
			return false
		}
		refundedCents = rc
	}
	// charge_refunded is definitionally money-gone even without the Stripe
	// read (the status exists only when the charge was refunded).
	moneyGone := status == "lost" || status == "charge_refunded" || refundedCents > 0

	outcome := "won"
	if status == "lost" {
		outcome = "lost"
	} else if moneyGone {
		outcome = "refunded"
	}

	// Stamp the outcome (first writer wins; the settled marker below is
	// what gates completion, so a crash here is fully recoverable).
	if d.ClosedAt == nil {
		if _, cerr := h.disputeRepo.Close(ctx, d.ID, outcome); cerr != nil {
			h.logger.Error("dispute close: stamp", "error", cerr, "dispute_id", d.StripeDisputeID)
			return false
		}
	}

	if moneyGone {
		if !h.disputeMoneyGoneEffects(ctx, d, lr, outcome, refundedCents) {
			return false
		}
	} else {
		if !h.disputeWonEffects(ctx, d, lr) {
			return false
		}
	}

	settledNow, serr := h.disputeRepo.MarkOutcomeSettled(ctx, d.ID)
	if serr != nil {
		h.logger.Error("dispute close: mark settled", "error", serr, "dispute_id", d.StripeDisputeID)
		return false
	}
	if settledNow && lr != nil {
		h.notifyDisputeOutcome(lr, outcome)
	}
	return true
}

// disputeMoneyGoneEffects — LOST or refunded-closure: the platform no
// longer holds the charge's money. Withheld rows STAY withheld (their note
// explains why); a PAID transfer funded by this charge is reversed
// proportionally to the disputed amount.
func (h *LeaseRequestHandler) disputeMoneyGoneEffects(ctx context.Context, d *models.ChargeDispute, lr *models.LeaseRequest, outcome string, refundedCents int64) bool {
	if d.ReversalDone {
		return true
	}

	// Target: charge-stamped row first; lease-scoped fallback for legacy
	// rows created before MarkPaid stamped source_charge_id (review
	// CRITICAL — without the fallback the clawback is dead code for every
	// existing row, including the one real payout).
	paidRow, perr := h.payoutRepo.GetPaidByChargeID(ctx, d.StripeChargeID)
	if perr != nil {
		h.logger.Error("dispute money-gone: charge-keyed lookup", "error", perr, "dispute_id", d.StripeDisputeID)
		return false
	}
	if paidRow == nil && d.LeaseRequestID != nil {
		paidRow, perr = h.payoutRepo.GetPaidByLeaseID(ctx, *d.LeaseRequestID)
		if perr != nil {
			h.logger.Error("dispute money-gone: lease-keyed lookup", "error", perr, "dispute_id", d.StripeDisputeID)
			return false
		}
	}

	if paidRow != nil && paidRow.StripeTransferID != nil && h.stripe != nil {
		// Proportional: claw back the owner share of the DISPUTED/refunded
		// cents, never more than the row holds (review: a $30 partial
		// dispute must not reverse a $135 transfer).
		contested := d.AmountCents
		if outcome == "refunded" && refundedCents > 0 && refundedCents < contested {
			contested = refundedCents
		}
		_, ownerShare := models.ComputePayoutSplit(contested, paidRow.FeeBPS)
		if ownerShare > paidRow.OwnerAmountCents {
			ownerShare = paidRow.OwnerAmountCents
		}
		if ownerShare > 0 {
			idemKey := "dispute-reversal-" + d.StripeDisputeID
			rev, rerr := h.stripe.CreateTransferReversal(*paidRow.StripeTransferID, idemKey, ownerShare)
			if rerr != nil {
				// Stable idempotency key ⇒ the retry is exactly-once at
				// Stripe; let redelivery drive it.
				h.logger.Error("dispute money-gone: transfer reversal failed — will retry via redelivery",
					"error", rerr, "dispute_id", d.StripeDisputeID, "transfer_id", *paidRow.StripeTransferID)
				return false
			}
			if ok, merr := h.payoutRepo.MarkReversed(ctx, paidRow.ID, rev.ID, rev.Amount, "dispute_"+outcome); merr != nil {
				h.logger.Error("dispute money-gone: mark reversed", "error", merr, "payout_id", paidRow.ID)
				return false
			} else if !ok {
				h.logger.Warn("dispute money-gone: row already reversed", "payout_id", paidRow.ID)
			}
			h.logger.Warn("dispute money-gone: owner transfer reversed",
				"dispute_id", d.StripeDisputeID, "reversal_id", rev.ID, "amount_cents", rev.Amount, "outcome", outcome)
		}
	} else {
		h.logger.Warn("dispute money-gone: no paid transfer found — platform absorbs",
			"dispute_id", d.StripeDisputeID, "charge_id", d.StripeChargeID, "outcome", outcome)
	}

	// Rows withheld for this lease's disputes are now permanently parked —
	// the money is with the cardholder; no later WON sibling may free them.
	if d.LeaseRequestID != nil {
		if n, rerr := h.payoutRepo.RetagDisputeWithheldUnreleasable(ctx, *d.LeaseRequestID, h.disputeNoteTag(d)); rerr != nil {
			h.logger.Error("dispute money-gone: retag withheld", "error", rerr, "dispute_id", d.StripeDisputeID)
			return false
		} else if n > 0 {
			h.logger.Warn("dispute money-gone: withheld rows parked unreleasable", "count", n)
		}
	}

	if claimed, cerr := h.disputeRepo.ClaimReversalDone(ctx, d.ID); cerr != nil {
		h.logger.Error("dispute money-gone: claim reversal done", "error", cerr, "dispute_id", d.StripeDisputeID)
		return false
	} else if !claimed {
		return true
	}

	// Keep a human in the loop: the outcome ticket is the reconciliation
	// record (targeted; never bulk-resolved).
	if h.ticketRepo != nil && lr != nil {
		subject := fmt.Sprintf("Card dispute %s — $%.2f", outcome, float64(d.AmountCents)/100)
		desc := fmt.Sprintf(
			"Dispute %s on charge %s closed '%s'. Money returned to the cardholder; any paid owner transfer was reversed proportionally (see payout ledger). Reconcile in the Stripe dashboard, then resolve.",
			d.StripeDisputeID, d.StripeChargeID, outcome)
		if t, terr := h.ticketRepo.CreateSystemTicket(ctx, lr.OwnerID, models.TicketCategoryPayments,
			subject, desc, d.LeaseRequestID, nil); terr == nil && t != nil && d.TicketID == nil {
			_ = h.disputeRepo.SetTicket(ctx, d.ID, t.ID)
		}
	}
	return true
}

// disputeWonEffects — genuinely won, money stayed: release ONLY what this
// dispute withheld, and only when no sibling money signal still needs the
// rows parked (review R5). The dispute's own ticket resolves by id — never
// a lease-wide bulk resolve (review: unrelated live tickets).
func (h *LeaseRequestHandler) disputeWonEffects(ctx context.Context, d *models.ChargeDispute, lr *models.LeaseRequest) bool {
	if d.LeaseRequestID != nil {
		if _, herr := h.leaseRepo.ClearRenewalHalt(ctx, *d.LeaseRequestID, "dispute"); herr != nil {
			h.logger.Warn("dispute won: clear renewal halt", "error", herr, "lease_request_id", *d.LeaseRequestID)
		}
		others, oerr := h.disputeRepo.CountOtherOpenForLease(ctx, *d.LeaseRequestID, d.ID)
		if oerr != nil {
			h.logger.Error("dispute won: sibling check", "error", oerr, "dispute_id", d.StripeDisputeID)
			return false
		}
		if others > 0 {
			h.logger.Warn("dispute won: sibling dispute still open — leaving rows withheld",
				"dispute_id", d.StripeDisputeID, "open_siblings", others)
		} else {
			n, rerr := h.payoutRepo.ReleaseDisputeWithheld(ctx, *d.LeaseRequestID)
			if rerr != nil {
				h.logger.Error("dispute won: release withheld", "error", rerr, "dispute_id", d.StripeDisputeID)
				return false
			}
			if n > 0 {
				h.logger.Info("dispute won: withheld payouts released", "count", n, "lease_request_id", *d.LeaseRequestID)
			}
		}
	}
	if h.ticketRepo != nil && d.TicketID != nil {
		if terr := h.ticketRepo.ResolveTicketByID(ctx, *d.TicketID); terr != nil {
			h.logger.Error("dispute won: resolve ticket", "error", terr, "ticket_id", *d.TicketID)
			return false
		}
	}
	return true
}

func (h *LeaseRequestHandler) notifyDisputeOutcome(lr *models.LeaseRequest, outcome string) {
	chatID := lr.ChatID
	leaseID := lr.ID
	switch outcome {
	case "won":
		go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypePayment,
			"Dispute resolved in your favor",
			"A disputed rental payment was decided in the platform's favor. The payout that was on hold is released and will transfer on the normal schedule.",
			&chatID, &leaseID)
	default: // lost / refunded — honest, no euphemism
		go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypePayment,
			"Disputed payment returned to the cardholder",
			"A disputed rental payment was returned to the cardholder, and the payout tied to it was adjusted. Our team reviews every such case — reach out via Support with any questions.",
			&chatID, &leaseID)
	}
}

// handleChargeRefunded processes charge.refunded. Recognized refunds (ours,
// with matching amounts) no-op; anything beyond what we issued withholds
// unpaid payouts and opens a reconciliation ticket. FAIL CLOSED on lookups.
func (h *LeaseRequestHandler) handleChargeRefunded(r *http.Request, obj map[string]interface{}) bool {
	ctx := r.Context()
	intentID, _ := obj["payment_intent"].(string)
	chargeID, _ := obj["id"].(string)
	amountRefunded := int64(0)
	if a, ok := obj["amount_refunded"].(float64); ok {
		amountRefunded = int64(a)
	}
	if intentID == "" {
		h.logger.Info("charge.refunded: no payment_intent on charge — ignoring", "charge_id", chargeID)
		return true
	}

	payment, err := h.leaseRepo.GetPaymentByIntentID(ctx, intentID)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil && apiErr.Code == models.ErrCodePaymentNotFound {
			h.logger.Info("charge.refunded: not a lease charge", "intent_id", intentID)
			return true
		}
		h.logger.Error("charge.refunded: payment lookup", "error", err, "intent_id", intentID)
		return false
	}
	if payment == nil {
		h.logger.Info("charge.refunded: not a lease charge", "intent_id", intentID)
		return true
	}
	lr, err := h.leaseRepo.GetByID(ctx, payment.LeaseRequestID)
	if err != nil || lr == nil {
		h.logger.Error("charge.refunded: load lease", "error", err, "lease_request_id", payment.LeaseRequestID)
		return false
	}

	recognized, rerr := h.recognizedRefundCents(ctx, lr, payment)
	if rerr != nil {
		h.logger.Error("charge.refunded: recognition lookup", "error", rerr, "lease_request_id", lr.ID)
		return false
	}
	if recognized >= amountRefunded {
		h.logger.Info("charge.refunded: fully matches refunds we issued",
			"lease_request_id", lr.ID, "recognized_cents", recognized, "amount_refunded_cents", amountRefunded)
		return true
	}

	// External (or larger-than-recorded) refund: park unpaid payouts,
	// ticket for reconciliation, tell the owner the truth.
	if h.payoutRepo != nil {
		note := "external refund " + chargeID + ": withheld pending reconciliation"
		if n, werr := h.payoutRepo.WithholdAllUnpaidForLease(ctx, lr.ID, note); werr != nil {
			h.logger.Error("charge.refunded: withhold payouts", "error", werr, "lease_request_id", lr.ID)
			return false
		} else if n > 0 {
			h.logger.Warn("charge.refunded: unpaid payouts withheld for external refund", "count", n, "lease_request_id", lr.ID)
		}
	}
	if h.ticketRepo != nil {
		desc := fmt.Sprintf(
			"Charge %s (lease %s) shows $%.2f refunded at Stripe but only $%.2f is on record — likely a dashboard refund.\nUnpaid owner payouts are withheld. Reconcile the lease/return state, then resolve.",
			chargeID, lr.ID, float64(amountRefunded)/100, float64(recognized)/100)
		if _, terr := h.ticketRepo.CreateSystemTicket(ctx, lr.OwnerID, models.TicketCategoryPayments,
			"External refund needs reconciliation", desc, &lr.ID, nil); terr != nil {
			h.logger.Error("charge.refunded: ticket", "error", terr, "lease_request_id", lr.ID)
		}
	}
	chatID := lr.ChatID
	leaseID := lr.ID
	go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypePayment,
		"Payout on hold",
		"A payment on one of your rentals was refunded outside the normal flow, so its payout is on hold while our team reconciles it. We'll follow up — nothing is needed from you.",
		&chatID, &leaseID)
	h.logger.Warn("charge.refunded: EXTERNAL refund detected",
		"lease_request_id", lr.ID, "charge_id", chargeID,
		"amount_refunded_cents", amountRefunded, "recognized_cents", recognized)
	return true
}

// recognizedRefundCents totals the refunds OUR pipelines issued for this
// lease's charge, so recognition is amount-aware (review: one recorded
// refund must not blind the lease to later external refunds).
func (h *LeaseRequestHandler) recognizedRefundCents(ctx context.Context, lr *models.LeaseRequest, payment *models.Payment) (int64, error) {
	total := int64(0)
	if lr.RefundID != nil && *lr.RefundID != "" {
		total += payment.Amount // pickup-expiry refunds are always full
	}
	if payment.Status == models.PaymentStatusRefunded || payment.Status == models.PaymentStatusRefundUnrecoverable {
		total += payment.Amount // orphan reconciliation refunds in full
	}
	if h.returnRepoForDisputes != nil {
		ret, err := h.returnRepoForDisputes.GetByLeaseRequestID(ctx, lr.ID)
		if err != nil {
			if apiErr := models.GetAPIError(err); apiErr == nil || apiErr.Code != models.ErrCodeVehicleReturnNotFound {
				return 0, err
			}
		} else if ret != nil && ret.RefundID != nil && *ret.RefundID != "" {
			total += ret.RefundAmountCents
		}
	}
	if total > payment.Amount {
		total = payment.Amount
	}
	return total, nil
}

// dbLookupCycleLease resolves a payment intent to its rolling lease via the
// billing_cycles table (uuid.Nil when not a cycle charge).
func (h *LeaseRequestHandler) dbLookupCycleLease(ctx context.Context, intentID string, out *uuid.UUID) error {
	cycle, err := h.billingRepo.GetCycleByIntent(ctx, intentID)
	if err != nil {
		return err
	}
	if cycle != nil {
		*out = cycle.LeaseRequestID
	}
	return nil
}
