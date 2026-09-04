package handlers

// Audit P0 batch (M3/H2/H7): the machinery that makes one invariant hold —
// a captured lease charge is NEVER silently kept.
//
//   - neutralizePaymentIntentSvc: the ONE way a lease-killing path decides
//     "no money moved". Cancel first; on any ambiguous failure ask Stripe
//     what the intent actually is. Unknown = hands off this tick.
//   - adoptSucceededPayment: the reconciliation half — a charge that won a
//     race is adopted (lease → paid, full side effects), not skipped.
//   - refundOrphanedPayment: the refund half — a charge whose lease is
//     already terminal is refunded in full under a stable idempotency key,
//     claimed-once, with the driver told the truth.
//   - runOrphanedPaymentSweep: the backstop — no payments row may sit
//     'succeeded' against a dead lease, even if every inline path missed.
//   - markLeaseRefundUnrecoverable: H7's port of the vehicle-return
//     'unrecoverable' exit to the pickup-expiry refund path.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/drivebai/backend/internal/models"
	stripeService "github.com/drivebai/backend/internal/stripe"
	"github.com/drivebai/backend/internal/ws"
	"github.com/google/uuid"
)

type piOutcome int

const (
	// piNeutralized: the intent is cancelled (by us, or it already was) —
	// it can never be confirmed, so killing the lease is safe.
	piNeutralized piOutcome = iota
	// piMoneyMoved: succeeded / processing / requires_capture — the payment
	// won. The lease must NOT be killed; adopt or back off.
	piMoneyMoved
	// piUnknown: Stripe couldn't tell us (network, 5xx). Do nothing this
	// tick — an unverified kill is exactly the M3 bug.
	piUnknown
)

// neutralizePaymentIntentSvc is shared with the account-deletion handler,
// which has no LeaseRequestHandler — hence a package function over the
// service rather than a method.
func neutralizePaymentIntentSvc(s *stripeService.Service, intentID string) piOutcome {
	cerr := s.CancelPaymentIntent(intentID)
	if cerr == nil {
		return piNeutralized
	}
	es := cerr.Error()
	if strings.Contains(es, "succeeded") || strings.Contains(es, "processing") {
		return piMoneyMoved
	}
	// Ambiguous: could be "already canceled" (benign) or a network failure
	// with the intent still live and confirmable. The old code treated this
	// as benign and killed the lease anyway — that fall-through was M3(b).
	pi, rerr := s.RetrievePaymentIntent(intentID)
	if rerr != nil {
		return piUnknown
	}
	switch pi.Status {
	case "canceled":
		return piNeutralized
	case "succeeded", "processing", "requires_capture":
		return piMoneyMoved
	}
	// Intent is live (requires_payment_method/_confirmation/_action) and the
	// first cancel failed for a non-status reason. One more attempt; if that
	// also fails, leave the row alone until the next tick.
	if s.CancelPaymentIntent(intentID) == nil {
		return piNeutralized
	}
	return piUnknown
}

func (h *LeaseRequestHandler) neutralizePaymentIntent(intentID string) piOutcome {
	return neutralizePaymentIntentSvc(h.stripe, intentID)
}

// adoptSucceededPayment reconciles a charge Stripe reports as succeeded with
// a lease our DB never flipped — the webhook was lost, or a sweep's cancel
// bounced off a winning payment. Mirrors the webhook's success path; if the
// lease meanwhile reached a terminal state, falls through to the refund half.
func (h *LeaseRequestHandler) adoptSucceededPayment(ctx context.Context, payment *models.Payment) {
	if payment.Status != models.PaymentStatusSucceeded {
		if err := h.leaseRepo.UpdatePaymentStatus(ctx, payment.ID, models.PaymentStatusSucceeded); err != nil {
			h.logger.Error("adopt payment: update status", "error", err, "payment_id", payment.ID)
			return
		}
		payment.Status = models.PaymentStatusSucceeded
	}

	lr, err := h.leaseRepo.SetPaid(ctx, payment.LeaseRequestID)
	if err == nil {
		h.logger.Info("adopted succeeded payment — lease paid",
			"lease_request_id", lr.ID, "payment_id", payment.ID)
		h.applyPaidSideEffectsCtx(ctx, lr)
		return
	}

	cur, gerr := h.leaseRepo.GetByID(ctx, payment.LeaseRequestID)
	if gerr != nil || cur == nil {
		h.logger.Error("adopt payment: reload lease", "error", gerr, "lease_request_id", payment.LeaseRequestID)
		return
	}
	switch cur.Status {
	case models.LeaseStatusPaid:
		return // the webhook or sync won the race — nothing left to do
	case models.LeaseStatusExpired, models.LeaseStatusCancelled, models.LeaseStatusDeclined:
		h.refundOrphanedPayment(ctx, cur, payment)
	default:
		// expired_refunded etc. — states that own their refund elsewhere.
		h.logger.Error("adopt payment: lease in unexpected state, leaving to its own pipeline",
			"lease_request_id", cur.ID, "status", cur.Status, "payment_id", payment.ID)
	}
}

// applyPaidSideEffectsCtx is the webhook success path's tail for callers
// that have no *http.Request (sweeps): broadcast, notify both parties,
// create the key-handover, arm the pickup deadline, decline siblings.
// Every step is idempotent against a concurrent webhook retry.
func (h *LeaseRequestHandler) applyPaidSideEffectsCtx(ctx context.Context, lr *models.LeaseRequest) {
	resp := h.buildLeaseRequestResponseCtx(ctx, lr, nil)
	h.wsHub.Broadcast(&ws.Event{
		Type:          "lease_request_updated",
		Payload:       resp,
		TargetUserIDs: []uuid.UUID{lr.DriverID, lr.OwnerID},
	})

	chatID := lr.ChatID
	leaseID := lr.ID
	driverName := resp.DriverName
	if driverName == "" {
		driverName = "The driver"
	}
	carTitle := resp.CarTitle
	if carTitle == "" {
		carTitle = "your listing"
	}
	go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypePayment,
		"Payment received",
		fmt.Sprintf("%s paid for %d week(s) of %s — coordinate pickup in chat", driverName, lr.Weeks, carTitle),
		&chatID, &leaseID)
	go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
		"Payment confirmed",
		fmt.Sprintf("Payment confirmed for %s — wait for pickup instructions from the owner", carTitle),
		&chatID, &leaseID)

	h.ensureKeyHandoverCtx(ctx, lr)
	h.armPickupDeadline(ctx, lr)
	h.declineSiblingsOfPaidLease(ctx, lr)
}

// refundOrphanedPayment returns a captured charge whose lease is terminal.
// Stable idempotency key per payment row; MarkPaymentRefunded is the
// claimed-once gate on the notification. A transient Stripe failure leaves
// the row 'succeeded' so runOrphanedPaymentSweep retries it forever rather
// than ever losing it.
func (h *LeaseRequestHandler) refundOrphanedPayment(ctx context.Context, lr *models.LeaseRequest, payment *models.Payment) {
	if payment.PaymentIntentID == nil {
		h.parkOrphanedPayment(ctx, lr, payment, "charge recorded succeeded but no payment intent is stored")
		return
	}

	idemKey := fmt.Sprintf("orphan-refund-%s", payment.ID)
	refund, err := h.stripe.CreateRefund(*payment.PaymentIntentID, idemKey, "requested_by_customer", 0)
	if err != nil {
		if strings.Contains(err.Error(), "resource_missing") {
			h.parkOrphanedPayment(ctx, lr, payment, "payment intent no longer exists at Stripe: "+err.Error())
			return
		}
		h.logger.Error("orphaned payment: refund failed, sweep will retry",
			"error", err, "lease_request_id", lr.ID, "payment_id", payment.ID)
		return
	}

	claimed, merr := h.leaseRepo.MarkPaymentRefunded(ctx, payment.ID)
	if merr != nil {
		// The refund exists at Stripe; the stable key makes the replay safe.
		h.logger.Error("orphaned payment: mark refunded", "error", merr, "payment_id", payment.ID)
		return
	}
	if !claimed {
		return // another worker finished first and owns the notification
	}

	h.logger.Warn("orphaned payment refunded",
		"lease_request_id", lr.ID, "lease_status", lr.Status,
		"payment_id", payment.ID, "refund_id", refund.ID, "amount_cents", payment.Amount)

	chatID := lr.ChatID
	leaseID := lr.ID
	go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
		"Payment refunded",
		fmt.Sprintf("Your rental request had already closed when the payment went through, so the full $%.2f was refunded to your card.", float64(payment.Amount)/100),
		&chatID, &leaseID)
}

// parkOrphanedPayment is the permanent-failure exit: claimed-once state
// flip, one support ticket (deduped by the live-scoped unique index on
// lease_request_id), one honest driver notification.
func (h *LeaseRequestHandler) parkOrphanedPayment(ctx context.Context, lr *models.LeaseRequest, payment *models.Payment, reason string) {
	claimed, err := h.leaseRepo.MarkPaymentRefundUnrecoverable(ctx, payment.ID)
	if err != nil {
		h.logger.Error("orphaned payment: mark unrecoverable", "error", err, "payment_id", payment.ID)
		return
	}
	if !claimed {
		return
	}

	h.logger.Error("orphaned payment UNRECOVERABLE — manual refund required",
		"lease_request_id", lr.ID, "payment_id", payment.ID, "reason", reason)

	if h.ticketRepo != nil {
		leaseRef := lr.ID
		desc := fmt.Sprintf(
			"Lease request %s is %s but its payment of $%.2f captured and cannot be refunded automatically.\nReason: %s\nRefund the charge from the Stripe dashboard, then resolve this ticket.",
			lr.ID, lr.Status, float64(payment.Amount)/100, reason)
		if _, terr := h.ticketRepo.CreateSystemTicket(ctx, lr.DriverID, models.TicketCategoryPayments,
			"Captured payment on a closed request needs a manual refund", desc, &leaseRef, nil); terr != nil {
			h.logger.Error("orphaned payment: create ticket", "error", terr, "lease_request_id", lr.ID)
		}
	}

	chatID := lr.ChatID
	leaseID := lr.ID
	go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
		"We're on your refund",
		"Your request had already closed when the payment went through, and the refund needs a manual step. Our team has it — nothing is needed from you.",
		&chatID, &leaseID)
}

// orphanedPaymentStaleAfter keeps the sweep off rows an inline handler
// (webhook, adopt path) touched moments ago.
const orphanedPaymentStaleAfter = 2 * time.Minute

// runOrphanedPaymentSweep is M3's backstop: succeeded payments on terminal
// leases get refunded no matter which inline path missed them.
func (h *LeaseRequestHandler) runOrphanedPaymentSweep(ctx context.Context) {
	rows, err := h.leaseRepo.ListOrphanedSucceededPayments(ctx, time.Now().UTC().Add(-orphanedPaymentStaleAfter), 25)
	if err != nil {
		h.logger.Error("orphaned-payment sweep: list", "error", err)
		return
	}
	for i := range rows {
		o := &rows[i]
		lr, gerr := h.leaseRepo.GetByID(ctx, o.Payment.LeaseRequestID)
		if gerr != nil || lr == nil {
			h.logger.Error("orphaned-payment sweep: load lease", "error", gerr, "lease_request_id", o.Payment.LeaseRequestID)
			continue
		}
		h.refundOrphanedPayment(ctx, lr, &o.Payment)
	}
}

// markLeaseRefundUnrecoverable is H7's permanent-failure exit for the
// pickup-expiry refund pipeline (mirror of vehicle_return.go's
// markRefundUnrecoverable): claimed-once flip out of the retry sweep, one
// ticket, one honest driver notification.
func (h *LeaseRequestHandler) markLeaseRefundUnrecoverable(ctx context.Context, lr *models.LeaseRequest, amountCents int64, reason string) {
	claimed, err := h.leaseRepo.MarkLeaseRefundUnrecoverable(ctx, lr.ID)
	if err != nil {
		h.logger.Error("expiry refund: mark unrecoverable", "error", err, "lease_request_id", lr.ID)
		return
	}
	if !claimed {
		return
	}

	h.logger.Error("expiry refund UNRECOVERABLE — manual refund required",
		"lease_request_id", lr.ID, "reason", reason)

	if h.ticketRepo != nil {
		leaseRef := lr.ID
		desc := fmt.Sprintf(
			"Lease request %s expired unpaid-pickup and its refund of $%.2f cannot be processed automatically.\nReason: %s\nRefund the charge from the Stripe dashboard, then resolve this ticket.",
			lr.ID, float64(amountCents)/100, reason)
		if _, terr := h.ticketRepo.CreateSystemTicket(ctx, lr.DriverID, models.TicketCategoryPayments,
			"Pickup-expiry refund needs a manual step", desc, &leaseRef, nil); terr != nil {
			h.logger.Error("expiry refund: create ticket", "error", terr, "lease_request_id", lr.ID)
		}
	}

	chatID := lr.ChatID
	leaseID := lr.ID
	go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
		"We're on your refund",
		"Your refund for the missed pickup needs a manual step. Our team has it — nothing is needed from you.",
		&chatID, &leaseID)
}
