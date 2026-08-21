package handlers

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	"github.com/drivebai/backend/internal/ws"
)

// Rental-term scanner (lifecycle batch, defect 2). Before this file existed
// nothing in the system ever compared "now" against the end of a paid term —
// a driver who kept the car and never tapped "I returned the car" held the
// listing as 'rented' forever, silently.
//
// Design decision, stated for the record: a passed deadline NEVER
// auto-completes the rental or releases the car. The car may genuinely still
// be with the driver, and releasing it would let the owner double-rent a
// vehicle that is physically gone. Instead every threshold produces a
// VISIBLE state change plus notifications, and sustained silence escalates
// to a human:
//
//	T-24h  → both parties notified the term is ending (phase 1)
//	T      → rental flagged overdue, both parties notified; car stays
//	         rented (phase 2)
//	T+72h  → support ticket opened (reporter: the owner — the party the
//	         platform owes an answer to), both parties told support is
//	         involved (phase 3)
//
// Idempotency: each phase's repo claim flips its own *_notified/escalated
// flag under an IS NULL guard (migration 000046), so a re-run, a crash
// replay, or a second instance can never notify twice. Mirrors
// StartPickupExpiryScanner's shape and registration.

// StartRentalTermScanner runs the term sweep on the shared scanner cadence.
func (h *LeaseRequestHandler) StartRentalTermScanner(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Self-heal the deploy window: an old app instance confirming a pickup
	// after migration 000046 ran (but before machines swapped) stamps
	// pickup_confirmed_at without rental_ends_at, leaving that rental
	// invisible to every sweep below. Idempotent re-backfill on boot.
	if n, err := h.leaseRepo.BackfillMissingTermEnds(ctx); err != nil {
		h.logger.Error("rental term scanner: backfill missing term ends", "error", err)
	} else if n > 0 {
		h.logger.Info("rental term scanner: backfilled missing term ends", "count", n)
	}

	h.logger.Info("rental term scanner started", "interval", interval.String())

	for {
		select {
		case <-ctx.Done():
			h.logger.Info("rental term scanner stopped")
			return
		case <-ticker.C:
			h.runTermSweep(ctx)
		}
	}
}

func (h *LeaseRequestHandler) runTermSweep(ctx context.Context) {
	now := time.Now().UTC()

	// Phase 1: T-24h — the return is coming up.
	ending, err := h.leaseRepo.ClaimTermEndingSoon(ctx, now, 50)
	if err != nil {
		h.logger.Error("term sweep: claim ending-soon", "error", err)
	}
	for _, t := range ending {
		endsLocal := t.RentalEndsAt.Format("Mon, Jan 2")
		h.notifyTermPhase(t,
			"Rental ends tomorrow",
			fmt.Sprintf("Your rental of %s ends %s. Arrange the return with %s — tap \"I returned the car\" once you've handed it back.", carTitleOr(t.CarTitle), endsLocal, nameOr(t.OwnerName, "the owner")),
			"Rental ends tomorrow",
			fmt.Sprintf("%s's rental of %s ends %s. Agree on the return handover in chat.", nameOr(t.DriverName, "The driver"), carTitleOr(t.CarTitle), endsLocal))
	}

	// Phase 2: T — the term is over; the car is now overdue. The car stays
	// 'rented' (see the header comment) but the state change is visible on
	// every surface via the WS refetch.
	overdue, err := h.leaseRepo.ClaimTermOverdue(ctx, now, 50)
	if err != nil {
		h.logger.Error("term sweep: claim overdue", "error", err)
	}
	for _, t := range overdue {
		// No refund promise here: past the term end, used days equal paid
		// days and ComputeReturnRefund yields $0 — promising money in the
		// one state where none is possible would be its own defect.
		h.notifyTermPhase(t,
			"Rental period ended",
			fmt.Sprintf("Your rental of %s has ended and the car isn't marked returned. Hand it back and tap \"I returned the car\" to close out the rental.", carTitleOr(t.CarTitle)),
			"Rental period ended",
			fmt.Sprintf("The paid term on %s is over and %s hasn't marked it returned yet. If the handover is arranged, nothing to do; otherwise reach out in chat.", carTitleOr(t.CarTitle), nameOr(t.DriverName, "the driver")))
	}

	// Phase 3: T+72h — sustained silence gets a human. Ticket-first,
	// flag-after: the insert is idempotent against a live ticket (partial
	// unique index), so a crash or a transient error between the steps
	// leaves the lease claimable and the next sweep retries — a claim-first
	// shape would commit the flag and lose the ticket forever.
	if h.ticketRepo == nil {
		return
	}
	escalate, err := h.leaseRepo.ListTermEscalationCandidates(ctx, now, 50)
	if err != nil {
		h.logger.Error("term sweep: list escalation candidates", "error", err)
		return
	}
	for _, t := range escalate {
		leaseRef := t.ID
		subject := fmt.Sprintf("Overdue rental — %s", carTitleOr(t.CarTitle))
		description := fmt.Sprintf(
			"Rental past its paid term by more than %d hours with no return started.\n\nCar: %s\nDriver: %s\nOwner: %s\nTerm ended: %s\nLease request: %s\n\nNeither party has moved the return forward; please contact both.",
			int(models.RentalOverdueEscalateAfter.Hours()), carTitleOr(t.CarTitle),
			nameOr(t.DriverName, "unknown"), nameOr(t.OwnerName, "unknown"),
			t.RentalEndsAt.Format(time.RFC1123), t.ID)
		created, terr := h.ticketRepo.CreateSystemTicket(ctx, t.OwnerID,
			models.TicketCategoryRenting, subject, description, &leaseRef, nil)
		if terr != nil {
			// Leave the flag unset — the next sweep retries this lease.
			h.logger.Error("term sweep: escalation ticket create failed", "error", terr, "lease_request_id", t.ID)
			continue
		}
		if created != nil {
			// This instance opened the ticket → it owns the notifications.
			h.notifyTermPhase(t,
				"Overdue rental — support involved",
				fmt.Sprintf("%s is three days past its rental end. A support case has been opened; our team will contact you. Returning the car and tapping \"I returned the car\" closes it out.", carTitleOr(t.CarTitle)),
				"Overdue rental — support involved",
				fmt.Sprintf("%s is three days past its rental end with no return started. A support case has been opened on your behalf; our team will contact you both.", carTitleOr(t.CarTitle)))
		}
		if merr := h.leaseRepo.MarkTermEscalated(ctx, t.ID); merr != nil {
			h.logger.Error("term sweep: mark escalated", "error", merr, "lease_request_id", t.ID)
		}
	}
}

// notifyTermPhase sends the two-party notifications for one term event and
// broadcasts lease_request_updated so open Today feeds refetch and flip the
// rental card's state without a manual refresh.
func (h *LeaseRequestHandler) notifyTermPhase(t repository.TermLease, driverTitle, driverBody, ownerTitle, ownerBody string) {
	chatID := t.ChatID
	leaseID := t.ID
	go h.notifHandler.Notify(t.DriverID, models.NotificationTypeLeaseRequest,
		driverTitle, driverBody, &chatID, &leaseID)
	go h.notifHandler.Notify(t.OwnerID, models.NotificationTypeLeaseRequest,
		ownerTitle, ownerBody, &chatID, &leaseID)
	h.wsHub.Broadcast(&ws.Event{
		Type: "lease_request_updated",
		Payload: map[string]any{
			"id":             t.ID,
			"rental_ends_at": t.RentalEndsAt,
		},
		TargetUserIDs: []uuid.UUID{t.DriverID, t.OwnerID},
	})
}

// declineSiblingsOfPaidLease closes out every still-'requested' lease on the
// same listing once one lease is paid (defect 1): a request that can no
// longer possibly succeed must not sit in a driver's Today list until its
// 24h TTL lazily expires. Called from both paid transitions (webhook +
// sync); naturally idempotent — a retry matches zero rows.
func (h *LeaseRequestHandler) declineSiblingsOfPaidLease(ctx context.Context, lr *models.LeaseRequest) {
	declined, err := h.leaseRepo.DeclineSiblingRequests(ctx, lr.ListingID, lr.ID)
	if err != nil {
		h.logger.Error("decline siblings of paid lease", "error", err, "listing_id", lr.ListingID)
		return
	}
	for _, s := range declined {
		chatID := s.ChatID
		siblingID := s.ID
		go h.notifHandler.Notify(s.DriverID, models.NotificationTypeLeaseRequest,
			"Car no longer available",
			fmt.Sprintf("%s was just rented by another driver, so your request was closed. Similar cars are in Discover.", carTitleOr(s.CarTitle)),
			&chatID, &siblingID)
		h.wsHub.Broadcast(&ws.Event{
			Type: "lease_request_updated",
			Payload: map[string]any{
				"id":     s.ID,
				"status": models.LeaseStatusDeclined,
			},
			TargetUserIDs: []uuid.UUID{s.DriverID},
		})
	}
	if len(declined) > 0 {
		h.logger.Info("auto-declined sibling lease requests", "count", len(declined), "listing_id", lr.ListingID, "paid_lease_id", lr.ID)
	}
}
