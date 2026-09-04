package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
)

// Self-service account deletion (App Review 5.1.1(v)). The DELETION itself
// reuses AdminRepository.SoftDeleteUser — one tombstone path, never two.
// This file holds the orchestration queries around it: what BLOCKS deletion
// (money captured or a car physically out), what deletion AUTO-RESOLVES
// (open lease requests that hold no captured money), and the data the user
// takes with them (their listings leave the marketplace, their identity
// documents are removed).

// ListAccountDeletionBlockers returns every in-flight transaction that must
// finish before the account may be deleted. The rule: deletion never strands
// a counterparty's money or a physical car. Everything listed here has a
// self-service exit inside the app (verified against the lifecycle state
// machine — every state has an exit or a scanner).
func (r *UserRepository) ListAccountDeletionBlockers(ctx context.Context, userID uuid.UUID) ([]models.AccountDeletionBlocker, error) {
	out := []models.AccountDeletionBlocker{}

	// Paid, unreturned leases — money captured, possibly a car out.
	rows, err := r.db.Pool.Query(ctx, `
		SELECT c.title,
		       lr.driver_id = $1 AS is_driver,
		       lr.pickup_confirmed_at IS NOT NULL AS picked_up,
		       lr.pickup_deadline_at
		FROM lease_requests lr
		JOIN cars c ON c.id = lr.listing_id
		WHERE (lr.driver_id = $1 OR lr.owner_id = $1)
		  AND lr.status = 'paid'
		  AND lr.vehicle_returned_at IS NULL`, userID)
	if err != nil {
		return nil, fmt.Errorf("list deletion blockers (leases): %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var title string
		var isDriver, pickedUp bool
		var deadline *time.Time
		if err := rows.Scan(&title, &isDriver, &pickedUp, &deadline); err != nil {
			return nil, err
		}
		b := models.AccountDeletionBlocker{CarTitle: title}
		switch {
		case isDriver && !pickedUp:
			b.Kind = "awaiting_pickup"
			b.Detail = fmt.Sprintf("Your paid rental of %s hasn't started. It refunds automatically if you don't confirm pickup", title)
			if deadline != nil {
				b.Detail += fmt.Sprintf(" by %s", deadline.Format("Jan 2, 15:04 MST"))
			}
			b.Detail += " — after the refund you can delete your account."
		case isDriver:
			b.Kind = "active_rental_driver"
			b.Detail = fmt.Sprintf("You're in an active rental of %s. Return the car (tap \"I returned the car\") and finish the handover first.", title)
		default:
			b.Kind = "active_rental_owner"
			b.Detail = fmt.Sprintf("Your car %s is in an active rental. Complete the return handshake with the driver first.", title)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Open payment windows (M3c): a payment_pending lease may have a
	// confirmable — or already-confirmed-but-unreported — PaymentIntent
	// behind it. Deletion used to cancel these with a best-effort Stripe
	// call and no succeeded-check, which could tombstone an account around
	// a captured charge. Both parties have a one-tap in-app exit, so this
	// is a blocker, not an auto-resolve.
	pprows, err := r.db.Pool.Query(ctx, `
		SELECT c.title, lr.driver_id = $1 AS is_driver
		FROM lease_requests lr
		JOIN cars c ON c.id = lr.listing_id
		WHERE (lr.driver_id = $1 OR lr.owner_id = $1)
		  AND lr.status = 'payment_pending'`, userID)
	if err != nil {
		return nil, fmt.Errorf("list deletion blockers (payment windows): %w", err)
	}
	defer pprows.Close()
	for pprows.Next() {
		var title string
		var isDriver bool
		if err := pprows.Scan(&title, &isDriver); err != nil {
			return nil, err
		}
		b := models.AccountDeletionBlocker{Kind: "payment_in_flight", CarTitle: title}
		if isDriver {
			b.Detail = fmt.Sprintf("Your payment for %s is still open. Cancel the request (or finish paying and complete the rental) first.", title)
		} else {
			b.Detail = fmt.Sprintf("A driver's payment for %s is in progress. Decline the request to release it first.", title)
		}
		out = append(out, b)
	}
	if err := pprows.Err(); err != nil {
		return nil, err
	}

	// In-flight purchases — authorized or captured money on either side.
	// Same status set the availability guards use (one definition).
	prows, err := r.db.Pool.Query(ctx, `
		SELECT c.title, pr.buyer_id = $1 AS is_buyer
		FROM purchase_requests pr
		JOIN cars c ON c.id = pr.car_id
		WHERE (pr.buyer_id = $1 OR pr.seller_id = $1)
		  AND pr.status IN `+BlockingPurchaseStatusesSQL, userID)
	if err != nil {
		return nil, fmt.Errorf("list deletion blockers (purchases): %w", err)
	}
	defer prows.Close()
	for prows.Next() {
		var title string
		var isBuyer bool
		if err := prows.Scan(&title, &isBuyer); err != nil {
			return nil, err
		}
		role := "sale"
		if isBuyer {
			role = "purchase"
		}
		out = append(out, models.AccountDeletionBlocker{
			Kind:     "purchase_in_flight",
			CarTitle: title,
			Detail:   fmt.Sprintf("Your %s of %s is in progress. Complete or cancel it from the chat first.", role, title),
		})
	}
	return out, prows.Err()
}

// CancelledOpenLease is one auto-resolved lease: enough to notify the
// counterparty and cancel any dangling Stripe intent.
type CancelledOpenLease struct {
	ID              uuid.UUID
	ChatID          uuid.UUID
	DriverID        uuid.UUID
	OwnerID         uuid.UUID
	CarTitle        string
	WasDriver       bool
	PaymentIntentID *string
}

// CancelOpenLeasesForUser auto-resolves the deleting user's open lease
// requests — requested / accepted / payment_pending, both roles. These hold
// NO captured money (paid is blocked upstream), so deletion may close them
// the way a manual cancel/decline would: status flipped, car reservation
// released, chat told. Runs in one transaction; the caller notifies
// counterparties and cancels dangling Stripe intents best-effort.
func (r *LeaseRequestRepository) CancelOpenLeasesForUser(ctx context.Context, userID uuid.UUID) ([]CancelledOpenLease, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		UPDATE lease_requests lr
		SET status = CASE WHEN lr.driver_id = $1 THEN 'cancelled' ELSE 'declined' END,
		    updated_at = NOW()
		FROM (
			SELECT id FROM lease_requests
			WHERE (driver_id = $1 OR owner_id = $1)
			  AND status IN ('requested', 'accepted', 'payment_pending')
			FOR UPDATE
		) picked
		WHERE lr.id = picked.id
		RETURNING lr.id, lr.chat_id, lr.driver_id, lr.owner_id, lr.driver_id = $1,
		          (SELECT title FROM cars WHERE id = lr.listing_id),
		          (SELECT p.payment_intent_id FROM payments p WHERE p.lease_request_id = lr.id)`, userID)
	if err != nil {
		return nil, fmt.Errorf("cancel open leases for user: %w", err)
	}
	var out []CancelledOpenLease
	for rows.Next() {
		var c CancelledOpenLease
		if err := rows.Scan(&c.ID, &c.ChatID, &c.DriverID, &c.OwnerID, &c.WasDriver, &c.CarTitle, &c.PaymentIntentID); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, c := range out {
		// Release any reservation these leases held (accepted/payment_pending).
		if _, err := tx.Exec(ctx, `
			UPDATE cars SET reserved_by_lease_request_id = NULL, updated_at = NOW()
			WHERE reserved_by_lease_request_id = $1`, c.ID); err != nil {
			return nil, fmt.Errorf("unreserve on account deletion: %w", err)
		}
		// System message + chat bump, house pattern. Sender is the deleting
		// user (the actor) — the row must exist before the tombstone rename,
		// which it does: SoftDeleteUser runs after this and keeps the row.
		if _, err := tx.Exec(ctx, `
			INSERT INTO messages (id, chat_id, sender_id, type, body, created_at)
			VALUES ($1, $2, $3, 'system', 'Request closed — the account was deleted', NOW())`,
			uuid.New(), c.ChatID, userID); err != nil {
			return nil, fmt.Errorf("account deletion system message: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE chats SET last_message_at = NOW(), updated_at = NOW() WHERE id = $1`, c.ChatID); err != nil {
			return nil, fmt.Errorf("account deletion chat bump: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// ArchiveCarsForOwner soft-archives every live listing the deleting owner
// has. Their cars already vanish from Discovery via the is_blocked join the
// moment the tombstone lands; archiving makes the removal EXPLICIT (admin
// lists, VIN release for future relisting) and matches what owner-initiated
// listing deletion does. Rented cars are excluded by the upstream blockers;
// sold cars are already archived by the capture path.
func (r *CarRepository) ArchiveCarsForOwner(ctx context.Context, ownerID uuid.UUID) (int64, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE cars
		SET archived_at = NOW(), is_paused = TRUE, updated_at = NOW()
		WHERE owner_id = $1 AND archived_at IS NULL AND status <> 'rented'`, ownerID)
	if err != nil {
		return 0, fmt.Errorf("archive cars for deleted owner: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteAllForUser removes every personal document ROW for the user and
// returns the on-disk file paths for the caller to unlink. Identity
// documents are the most sensitive thing we hold and serve no counterparty
// history (lease shares cascade away with the rows — ON DELETE CASCADE,
// migration 000014); a deleted account keeps nothing of them.
func (r *DocumentRepository) DeleteAllForUser(ctx context.Context, userID uuid.UUID) ([]string, error) {
	rows, err := r.db.Pool.Query(ctx, `
		DELETE FROM documents WHERE user_id = $1 RETURNING file_path`, userID)
	if err != nil {
		return nil, fmt.Errorf("delete documents for user: %w", err)
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// ListOpenLeasePaymentIntents returns the Stripe intent IDs attached to the
// user's still-open lease requests (M3c). The deletion handler must prove
// each one neutralized BEFORE CancelOpenLeasesForUser kills the rows — a
// cancel-after-the-fact left a window where a captured charge survived its
// lease. payment_pending is a hard blocker upstream, but the crash window
// between intent creation and the payment_pending transition can leave an
// intent on an 'accepted' row, so the predicate covers all open statuses.
func (r *LeaseRequestRepository) ListOpenLeasePaymentIntents(ctx context.Context, userID uuid.UUID) ([]string, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT p.payment_intent_id
		FROM payments p
		JOIN lease_requests lr ON lr.id = p.lease_request_id
		WHERE (lr.driver_id = $1 OR lr.owner_id = $1)
		  AND lr.status IN ('requested', 'accepted', 'payment_pending')
		  AND p.payment_intent_id IS NOT NULL
		  AND p.status NOT IN ('canceled', 'refunded', 'refund_unrecoverable')
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
