package handlers

// Batch 1 of the recurring-billing plan — also closes audit finding M2 for
// the EXISTING product: card disputes and out-of-band refunds were
// previously invisible (no handler, platform debited silently, owner
// payouts kept settling against money Stripe had already pulled back).
//
// Policy (design §5, client-approved Sep 7):
//   dispute OPEN   → mirror row + withhold the lease's UNPAID payout rows
//                    (claimed-once) + support ticket + notify. NEVER claw
//                    back on an open dispute.
//   dispute LOST   → partial Transfer Reversal of exactly that charge's
//                    paid payout row (stable idem key), row → 'reversed',
//                    owner notified honestly.
//   dispute WON    → release the withheld rows back to the sweep; notify.
//   charge.refunded (out-of-band, e.g. dashboard) → if the refund matches
//                    nothing we issued, withhold unpaid payout rows + ticket
//                    so no payout settles against refunded money.

import (
	"context"
	"fmt"
	"net/http"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	"github.com/google/uuid"
)

// SetDisputeDependencies wires the dispute mirror + payout collaborators
// (house setter pattern; keeps the constructor signature stable).
func (h *LeaseRequestHandler) SetDisputeDependencies(disputeRepo *repository.ChargeDisputeRepository, payoutRepo *repository.PayoutRepository) {
	h.disputeRepo = disputeRepo
	h.payoutRepo = payoutRepo
}

// handleChargeDispute processes charge.dispute.created/updated/closed.
// Returns false only for failures a Stripe redelivery can fix (H2 rule).
func (h *LeaseRequestHandler) handleChargeDispute(r *http.Request, eventType string, obj map[string]interface{}) bool {
	ctx := r.Context()
	if h.disputeRepo == nil || h.payoutRepo == nil {
		h.logger.Error("dispute webhook: dependencies not wired")
		return true // config error; redelivery won't fix it — log loudly, ACK
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

	// Resolve the lease through the payment intent (nil for purchases /
	// unknown charges — those still get a mirror row and a ticket).
	var leaseRef *uuid.UUID
	var lr *models.LeaseRequest
	if intentID != "" {
		if payment, err := h.leaseRepo.GetPaymentByIntentID(ctx, intentID); err == nil && payment != nil {
			if cur, gerr := h.leaseRepo.GetByID(ctx, payment.LeaseRequestID); gerr == nil && cur != nil {
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
		return false // DB failure — redelivery helps
	}

	h.logger.Warn("card dispute event",
		"type", eventType, "dispute_id", disputeID, "charge_id", chargeID,
		"amount_cents", amount, "status", status, "created_now", createdNow,
		"lease_request_id", leaseRef)

	// OPEN side effects — claimed-once regardless of which event type
	// carried the first sighting (created may be lost; updated must be
	// able to trigger the same protection).
	if !isDisputeClosedStatus(status) {
		if err := h.disputeOpenSideEffects(ctx, d, lr); err != nil {
			return false
		}
		return true
	}

	// CLOSED: won / lost / warning_closed (charge_refunded counts as closed).
	outcome := "won"
	if status == "lost" {
		outcome = "lost"
	}
	closed, cerr := h.disputeRepo.Close(ctx, d.ID, outcome)
	if cerr != nil {
		h.logger.Error("dispute webhook: close", "error", cerr, "dispute_id", disputeID)
		return false
	}
	if closed == nil {
		return true // already closed — redelivery, benign
	}
	if outcome == "lost" {
		return h.disputeLostSideEffects(ctx, closed, lr)
	}
	return h.disputeWonSideEffects(ctx, closed, lr)
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

func (h *LeaseRequestHandler) disputeOpenSideEffects(ctx context.Context, d *models.ChargeDispute, lr *models.LeaseRequest) error {
	claimed, err := h.disputeRepo.ClaimPayoutsWithheld(ctx, d.ID)
	if err != nil {
		return err
	}
	if !claimed {
		return nil // side effects already ran for this dispute
	}

	if d.LeaseRequestID != nil {
		note := h.disputeNoteTag(d) + ": withheld pending outcome"
		n, werr := h.payoutRepo.WithholdAllUnpaidForLease(ctx, *d.LeaseRequestID, note)
		if werr != nil {
			h.logger.Error("dispute open: withhold payouts", "error", werr, "dispute_id", d.StripeDisputeID)
			// claimed flag already set; the ticket below still surfaces it —
			// admin can withhold manually. Log, don't 500 (money hasn't moved).
		} else if n > 0 {
			h.logger.Warn("dispute open: unpaid payout rows withheld", "count", n, "lease_request_id", *d.LeaseRequestID)
		}
	}

	// Ticket — deduped by the live-scoped unique index on lease_request_id.
	// Tickets require a real reporter user; a dispute we cannot map to a
	// lease (purchase-era or unknown charge) is mirrored + logged loudly
	// and lives in the Stripe dashboard queue instead.
	if h.ticketRepo != nil && lr != nil {
		subject := fmt.Sprintf("Card dispute opened — $%.2f", float64(d.AmountCents)/100)
		desc := fmt.Sprintf(
			"Stripe dispute %s on charge %s ($%.2f, reason: %s).\nStripe has debited the platform balance pending the outcome.\nUnpaid owner payouts for the lease are withheld; nothing was clawed back.\nRespond with evidence in the Stripe dashboard before the deadline shown there.",
			d.StripeDisputeID, d.StripeChargeID, float64(d.AmountCents)/100, strOrEmpty(d.Reason))
		if t, terr := h.ticketRepo.CreateSystemTicket(ctx, lr.DriverID, models.TicketCategoryPayments,
			subject, desc, d.LeaseRequestID, nil); terr != nil {
			h.logger.Error("dispute open: ticket", "error", terr, "dispute_id", d.StripeDisputeID)
		} else if t != nil {
			_ = h.disputeRepo.SetTicket(ctx, d.ID, t.ID)
		}
	}

	// Owner notice — honest, no panic: money is on hold, nothing is taken.
	if lr != nil {
		chatID := lr.ChatID
		leaseID := lr.ID
		go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypePayment,
			"Payment under review",
			"The card payment for one of your rentals is being disputed by the cardholder's bank. Your payout for it is on hold while we respond — most disputes are resolved in the platform's favor. Nothing is needed from you.",
			&chatID, &leaseID)
	}
	return nil
}

func (h *LeaseRequestHandler) disputeLostSideEffects(ctx context.Context, d *models.ChargeDispute, lr *models.LeaseRequest) bool {
	claimed, err := h.disputeRepo.ClaimReversalDone(ctx, d.ID)
	if err != nil {
		h.logger.Error("dispute lost: claim reversal", "error", err, "dispute_id", d.StripeDisputeID)
		return false
	}
	if !claimed {
		return true
	}

	// Reverse exactly the transfer funded by the disputed charge, if one
	// was ever paid. Reversal amount = the owner share of that row, never
	// more (partial reversal; platform absorbs fee + dispute fee).
	paidRow, perr := h.payoutRepo.GetPaidByChargeID(ctx, d.StripeChargeID)
	if perr != nil {
		h.logger.Error("dispute lost: find paid payout", "error", perr, "dispute_id", d.StripeDisputeID)
		return false
	}
	if paidRow != nil && paidRow.StripeTransferID != nil && h.stripe != nil {
		idemKey := "dispute-reversal-" + d.StripeDisputeID
		rev, rerr := h.stripe.CreateTransferReversal(*paidRow.StripeTransferID, idemKey, paidRow.OwnerAmountCents)
		if rerr != nil {
			// Transient Stripe failure: surface loudly; the claimed flag
			// stops automated retries (reversals against people's balances
			// must not be retried blind) — the ticket owns recovery.
			h.logger.Error("dispute lost: transfer reversal FAILED — manual action required",
				"error", rerr, "dispute_id", d.StripeDisputeID, "transfer_id", *paidRow.StripeTransferID)
		} else {
			if ok, merr := h.payoutRepo.MarkReversed(ctx, paidRow.ID, rev.ID, rev.Amount, "dispute_lost"); merr != nil || !ok {
				h.logger.Error("dispute lost: mark reversed", "error", merr, "claimed", ok, "payout_id", paidRow.ID)
			}
			h.logger.Warn("dispute lost: owner transfer reversed",
				"dispute_id", d.StripeDisputeID, "reversal_id", rev.ID, "amount_cents", rev.Amount)
		}
	}

	if h.ticketRepo != nil && d.TicketID == nil && lr != nil {
		if t, terr := h.ticketRepo.CreateSystemTicket(ctx, lr.DriverID, models.TicketCategoryPayments,
			fmt.Sprintf("Card dispute LOST — $%.2f", float64(d.AmountCents)/100),
			fmt.Sprintf("Dispute %s on charge %s was lost. Reconcile the reversal and the platform absorption in the Stripe dashboard.", d.StripeDisputeID, d.StripeChargeID),
			d.LeaseRequestID, nil); terr == nil && t != nil {
			_ = h.disputeRepo.SetTicket(ctx, d.ID, t.ID)
		}
	}

	if lr != nil {
		chatID := lr.ChatID
		leaseID := lr.ID
		go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypePayment,
			"Disputed payment reversed",
			"The cardholder's bank decided a disputed rental payment against the platform, and the payout funded by that payment was reversed. Our team reviews every lost dispute — reach out via Support with any questions.",
			&chatID, &leaseID)
	}
	return true
}

func (h *LeaseRequestHandler) disputeWonSideEffects(ctx context.Context, d *models.ChargeDispute, lr *models.LeaseRequest) bool {
	if d.LeaseRequestID != nil {
		n, err := h.payoutRepo.ReleaseDisputeWithheld(ctx, *d.LeaseRequestID, h.disputeNoteTag(d))
		if err != nil {
			h.logger.Error("dispute won: release withheld", "error", err, "dispute_id", d.StripeDisputeID)
			return false
		}
		if n > 0 {
			h.logger.Info("dispute won: withheld payouts released", "count", n, "lease_request_id", *d.LeaseRequestID)
		}
	}
	if h.ticketRepo != nil && d.LeaseRequestID != nil {
		_ = h.ticketRepo.ResolveForLeaseRequest(ctx, *d.LeaseRequestID)
	}
	if lr != nil {
		chatID := lr.ChatID
		leaseID := lr.ID
		go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypePayment,
			"Dispute resolved in your favor",
			"A disputed rental payment was decided in the platform's favor. The payout that was on hold is released and will transfer on the normal schedule.",
			&chatID, &leaseID)
	}
	return true
}

// handleChargeRefunded processes charge.refunded. Refunds WE issued are
// already recorded (lease refund_id, vehicle_returns refund_id, orphan
// 'refunded' payments, purchase refunds) — those arrive here as recognized
// and need nothing. An UNRECOGNIZED refund (issued from the Stripe
// dashboard, or by Stripe itself) must stop payouts settling against money
// no longer held, and put a human in the loop.
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
	if err != nil || payment == nil {
		// Purchases and unknown charges: purchases record their own refunds;
		// log for reconciliation, nothing to protect (no lease payout rows).
		h.logger.Info("charge.refunded: no lease payment for intent", "intent_id", intentID, "amount_refunded", amountRefunded)
		return true
	}
	lr, err := h.leaseRepo.GetByID(ctx, payment.LeaseRequestID)
	if err != nil || lr == nil {
		h.logger.Error("charge.refunded: load lease", "error", err, "lease_request_id", payment.LeaseRequestID)
		return false
	}

	if h.refundIsRecognized(ctx, lr, payment) {
		h.logger.Info("charge.refunded: matches a refund we issued (recorded elsewhere)", "lease_request_id", lr.ID)
		return true
	}

	// Out-of-band refund: withhold unpaid payouts + ticket, claimed-once
	// via the live-scoped ticket index (a second delivery finds the live
	// ticket and no-ops; withholding is idempotent by status scope).
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
			"Charge %s (lease %s) shows $%.2f refunded at Stripe, but no refund of record matches it — likely issued from the dashboard.\nUnpaid owner payouts are withheld. Reconcile the lease/return state, then resolve.",
			chargeID, lr.ID, float64(amountRefunded)/100)
		if _, terr := h.ticketRepo.CreateSystemTicket(ctx, lr.DriverID, models.TicketCategoryPayments,
			"External refund needs reconciliation", desc, &lr.ID, nil); terr != nil {
			h.logger.Error("charge.refunded: ticket", "error", terr, "lease_request_id", lr.ID)
		}
	}
	h.logger.Warn("charge.refunded: EXTERNAL refund detected",
		"lease_request_id", lr.ID, "charge_id", chargeID, "amount_refunded_cents", amountRefunded)
	return true
}

// refundIsRecognized reports whether the refund on this charge corresponds
// to one our own pipelines issued and recorded.
func (h *LeaseRequestHandler) refundIsRecognized(ctx context.Context, lr *models.LeaseRequest, payment *models.Payment) bool {
	if lr.RefundID != nil && *lr.RefundID != "" {
		return true // pickup-expiry refund recorded on the lease
	}
	if payment.Status == models.PaymentStatusRefunded || payment.Status == models.PaymentStatusRefundUnrecoverable {
		return true // orphaned-payment reconciliation
	}
	if h.returnRepoForDisputes != nil {
		if ret, err := h.returnRepoForDisputes.GetByLeaseRequestID(ctx, lr.ID); err == nil && ret != nil && ret.RefundID != nil && *ret.RefundID != "" {
			return true // vehicle-return refund
		}
	}
	return false
}

// SetReturnRepositoryForDisputes wires the vehicle-return lookup used only
// for refund recognition (avoids a handler-level import cycle).
func (h *LeaseRequestHandler) SetReturnRepositoryForDisputes(r *repository.VehicleReturnRepository) {
	h.returnRepoForDisputes = r
}
