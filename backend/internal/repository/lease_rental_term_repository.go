package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
)

// Rental-term queries (lifecycle batch, defects 1–3). All methods live on
// LeaseRequestRepository; they are split into this file because they form one
// coherent concern — the paid term's clock — and none of them touch the
// shared full-struct scan lists (rental_ends_at and the notified-at flags are
// selected only here, mirroring how vehicle_returned_at is handled).

// TermLease is the slim row the term scanner and the Today card work from —
// the lease's identifiers plus everything needed to notify and render
// without a second lookup.
type TermLease struct {
	ID           uuid.UUID
	ChatID       uuid.UUID
	ListingID    uuid.UUID
	OwnerID      uuid.UUID
	DriverID     uuid.UUID
	Weeks        int
	RentalEndsAt time.Time
	CarTitle     string
	DriverName   string
	OwnerName    string
}

const termLeaseUpdateReturning = `
	RETURNING lr.id, lr.chat_id, lr.listing_id, lr.owner_id, lr.driver_id, lr.weeks, lr.rental_ends_at,
	          (SELECT title FROM cars WHERE id = lr.listing_id),
	          (SELECT COALESCE(first_name || ' ' || last_name, '') FROM users WHERE id = lr.driver_id),
	          (SELECT COALESCE(first_name || ' ' || last_name, '') FROM users WHERE id = lr.owner_id)`

// claimTermPhase is the shared body of the three term-scanner claims. Each
// phase flips its own claimed-once flag under an IS NULL guard inside the
// row-selecting subquery, so the claim itself is the idempotency barrier —
// a concurrent scanner instance (FOR UPDATE SKIP LOCKED) or a repeated sweep
// can never act on the same lease twice. Mirrors ClaimForExpiry's shape.
func (r *LeaseRequestRepository) claimTermPhase(ctx context.Context, flagColumn, endPredicate string, now time.Time, limit int) ([]TermLease, error) {
	if limit <= 0 {
		limit = 50
	}
	// The NOT EXISTS is load-bearing: a return sitting at driver_initiated /
	// owner_confirmed / disputed means the parties ARE moving the rental
	// forward — nagging "the car isn't marked returned" at a driver who
	// already tapped the button (or opening an "unresponsive parties"
	// ticket over an active dispute) would be the exact false-copy disease
	// this batch exists to cure. Cancelled returns don't shield the lease.
	q := fmt.Sprintf(`
		UPDATE lease_requests lr
		SET %[1]s = NOW(), updated_at = NOW()
		FROM (
			SELECT id FROM lease_requests
			WHERE status = 'paid'
			  AND pickup_confirmed_at IS NOT NULL
			  AND vehicle_returned_at IS NULL
			  AND rental_ends_at IS NOT NULL
			  AND %[2]s
			  AND %[1]s IS NULL
			  AND NOT EXISTS (
			        SELECT 1 FROM vehicle_returns vr
			        WHERE vr.lease_request_id = lease_requests.id
			          AND vr.status IN ('driver_initiated', 'owner_confirmed', 'disputed')
			      )
			ORDER BY rental_ends_at ASC
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		) picked
		WHERE lr.id = picked.id`+termLeaseUpdateReturning, flagColumn, endPredicate)

	rows, err := r.db.Pool.Query(ctx, q, now, limit)
	if err != nil {
		return nil, fmt.Errorf("claim term phase %s: %w", flagColumn, err)
	}
	defer rows.Close()
	var out []TermLease
	for rows.Next() {
		var t TermLease
		if err := rows.Scan(&t.ID, &t.ChatID, &t.ListingID, &t.OwnerID, &t.DriverID, &t.Weeks,
			&t.RentalEndsAt, &t.CarTitle, &t.DriverName, &t.OwnerName); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// The explicit ::timestamptz casts in every predicate are load-bearing:
// where $1's FIRST occurrence sits inside interval arithmetic, Postgres
// deduces the parameter as `interval` and the comparison fails with
// "operator does not exist: timestamp with time zone <= interval".

// ClaimTermEndingSoon claims rentals inside the T-24h window (end still
// ahead) that haven't had their ending-soon notice yet.
func (r *LeaseRequestRepository) ClaimTermEndingSoon(ctx context.Context, now time.Time, limit int) ([]TermLease, error) {
	return r.claimTermPhase(ctx, "term_ending_notified_at",
		"rental_ends_at > $1::timestamptz AND rental_ends_at <= $1::timestamptz + INTERVAL '24 hours'", now, limit)
}

// ClaimTermOverdue claims rentals whose end has passed (inclusive — an end
// exactly on the scan tick counts) that haven't had their overdue notice.
func (r *LeaseRequestRepository) ClaimTermOverdue(ctx context.Context, now time.Time, limit int) ([]TermLease, error) {
	return r.claimTermPhase(ctx, "overdue_notified_at",
		"rental_ends_at <= $1::timestamptz", now, limit)
}

// ListTermEscalationCandidates SELECTs (without claiming) rentals
// sustained-overdue past the escalation threshold that haven't been
// escalated yet. Deliberately claimless — the sweep creates the ticket
// FIRST (idempotent via the live-ticket unique index) and stamps the flag
// only after success via MarkTermEscalated, so a crash or transient insert
// failure between the two leaves the lease claimable and the next sweep
// retries. A claim-first shape would commit the flag and lose the ticket
// forever.
func (r *LeaseRequestRepository) ListTermEscalationCandidates(ctx context.Context, now time.Time, limit int) ([]TermLease, error) {
	if limit <= 0 {
		limit = 50
	}
	q := fmt.Sprintf(`
		SELECT lr.id, lr.chat_id, lr.listing_id, lr.owner_id, lr.driver_id, lr.weeks, lr.rental_ends_at,
		       (SELECT title FROM cars WHERE id = lr.listing_id),
		       (SELECT COALESCE(first_name || ' ' || last_name, '') FROM users WHERE id = lr.driver_id),
		       (SELECT COALESCE(first_name || ' ' || last_name, '') FROM users WHERE id = lr.owner_id)
		FROM lease_requests lr
		WHERE lr.status = 'paid'
		  AND lr.pickup_confirmed_at IS NOT NULL
		  AND lr.vehicle_returned_at IS NULL
		  AND lr.rental_ends_at IS NOT NULL
		  AND lr.rental_ends_at <= $1::timestamptz - INTERVAL '%d hours'
		  AND lr.overdue_escalated_at IS NULL
		  AND NOT EXISTS (
		        SELECT 1 FROM vehicle_returns vr
		        WHERE vr.lease_request_id = lr.id
		          AND vr.status IN ('driver_initiated', 'owner_confirmed', 'disputed')
		      )
		ORDER BY lr.rental_ends_at ASC
		LIMIT $2`, int(models.RentalOverdueEscalateAfter.Hours()))
	rows, err := r.db.Pool.Query(ctx, q, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list term escalation candidates: %w", err)
	}
	defer rows.Close()
	var out []TermLease
	for rows.Next() {
		var t TermLease
		if err := rows.Scan(&t.ID, &t.ChatID, &t.ListingID, &t.OwnerID, &t.DriverID, &t.Weeks,
			&t.RentalEndsAt, &t.CarTitle, &t.DriverName, &t.OwnerName); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// MarkTermEscalated stamps the escalated flag once the ticket verifiably
// exists. Guarded on IS NULL so a concurrent sweep stamping first wins and
// the second is a no-op.
func (r *LeaseRequestRepository) MarkTermEscalated(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE lease_requests
		SET overdue_escalated_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND overdue_escalated_at IS NULL`, id)
	return err
}

// BackfillMissingTermEnds re-runs migration 000046's backfill for any
// paid+picked-up row missing rental_ends_at. Live rows can slip through the
// deploy window (an old app instance confirming a pickup after the
// release_command migrated but before machines swapped stamps only
// pickup_confirmed_at) — run once at scanner start so no rental is ever
// invisible to the term sweep. Idempotent by construction.
func (r *LeaseRequestRepository) BackfillMissingTermEnds(ctx context.Context) (int64, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE lease_requests
		SET rental_ends_at = pickup_confirmed_at + (GREATEST(weeks, 1) * INTERVAL '7 days')
		WHERE status = 'paid'
		  AND pickup_confirmed_at IS NOT NULL
		  AND rental_ends_at IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("backfill missing term ends: %w", err)
	}
	return tag.RowsAffected(), nil
}

// SiblingDecline is one auto-declined competitor row — enough to notify its
// driver and post the chat system message.
type SiblingDecline struct {
	ID       uuid.UUID
	ChatID   uuid.UUID
	DriverID uuid.UUID
	OwnerID  uuid.UUID
	CarTitle string
}

// DeclineSiblingRequests auto-declines every other still-'requested' lease on
// the same listing the moment one lease is PAID (defect 1): a request that
// can no longer possibly succeed must not sit in a driver's Today list until
// its 24h TTL lazily expires. Only 'requested' rows are touched — an
// 'accepted' sibling is impossible while the paid lease holds the car's
// reservation, and declining anything holding state of its own would need
// its own cleanup. Chat system messages ride in the same transaction,
// mirroring updateStatus. Naturally idempotent: a re-run matches zero rows.
func (r *LeaseRequestRepository) DeclineSiblingRequests(ctx context.Context, listingID, paidLeaseID uuid.UUID) ([]SiblingDecline, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Plain FOR UPDATE (no SKIP LOCKED): an owner's in-flight Accept of a
	// sibling holds that row's lock but is doomed to roll back against the
	// paid lease's reservation — waiting it out lets this sweep decline
	// the row after the rollback, where SKIP LOCKED would silently leave
	// it 'requested' forever (its one decline shot is here).
	rows, err := tx.Query(ctx, `
		UPDATE lease_requests lr
		SET status = 'declined', updated_at = NOW()
		FROM (
			SELECT id FROM lease_requests
			WHERE listing_id = $1 AND id <> $2 AND status = 'requested'
			FOR UPDATE
		) picked
		WHERE lr.id = picked.id
		RETURNING lr.id, lr.chat_id, lr.driver_id, lr.owner_id,
		          (SELECT title FROM cars WHERE id = lr.listing_id)`, listingID, paidLeaseID)
	if err != nil {
		return nil, fmt.Errorf("decline sibling requests: %w", err)
	}
	var out []SiblingDecline
	for rows.Next() {
		var s SiblingDecline
		if err := rows.Scan(&s.ID, &s.ChatID, &s.DriverID, &s.OwnerID, &s.CarTitle); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, s := range out {
		// Sender is the OWNER (the party effectively declining) so the
		// declined driver's unread count lights up; every system-message
		// insert in this repo also bumps the chat clocks — same here.
		if _, err := tx.Exec(ctx, `
			INSERT INTO messages (id, chat_id, sender_id, type, body, created_at)
			VALUES ($1, $2, $3, 'system', 'This car is no longer available — another rental was completed first', NOW())
		`, uuid.New(), s.ChatID, s.OwnerID); err != nil {
			return nil, fmt.Errorf("sibling decline system message: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE chats SET last_message_at = NOW(), updated_at = NOW() WHERE id = $1
		`, s.ChatID); err != nil {
			return nil, fmt.Errorf("sibling decline chat bump: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// DriverActiveRentalRow is the raw scan target for ListActiveRentalsForDriver.
type DriverActiveRentalRow struct {
	LeaseRequestID    uuid.UUID
	ListingID         uuid.UUID
	CarTitle          string
	CoverPhotoPath    *string
	OwnerID           uuid.UUID
	OwnerName         string
	ChatID            *uuid.UUID
	PickupConfirmedAt time.Time
	RentalEndsAt      time.Time
	Weeks             int
	WeeklyPriceDollar float64
	PaidAmountCents   *int64
	PickupArea        *string
	PickupLat         *float64
	PickupLng         *float64
	ListingArea       *string
	ListingLat        *float64
	ListingLng        *float64
}

// ListActiveRentalsForDriver returns the driver's rentals in progress (paid,
// picked up, not returned) with everything the Today card renders — car
// title/cover, owner name, chat, term end, the paid amount, and the pickup
// snapshot from key_handovers (the return-location default) with the car's
// listed location as fallback. COALESCE on rental_ends_at covers any row
// that predates migration 000046's backfill.
func (r *LeaseRequestRepository) ListActiveRentalsForDriver(ctx context.Context, driverID uuid.UUID) ([]DriverActiveRentalRow, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT lr.id, lr.listing_id, c.title,
		       (SELECT file_url FROM car_photos WHERE car_id = c.id AND slot_type = 'cover_front' ORDER BY created_at DESC LIMIT 1),
		       lr.owner_id, COALESCE(o.first_name || ' ' || o.last_name, ''),
		       lr.chat_id,
		       lr.pickup_confirmed_at,
		       COALESCE(lr.rental_ends_at, lr.pickup_confirmed_at + (GREATEST(lr.weeks, 1) * INTERVAL '7 days')),
		       lr.weeks,
		       COALESCE(lr.offered_weekly_price, lr.weekly_price),
		       p.amount,
		       kh.pickup_area, kh.pickup_latitude, kh.pickup_longitude,
		       c.area, c.latitude, c.longitude
		FROM lease_requests lr
		JOIN cars c ON c.id = lr.listing_id
		JOIN users o ON o.id = lr.owner_id
		LEFT JOIN payments p ON p.lease_request_id = lr.id
		LEFT JOIN key_handovers kh ON kh.lease_request_id = lr.id
		WHERE lr.driver_id = $1
		  AND lr.status = 'paid'
		  AND lr.pickup_confirmed_at IS NOT NULL
		  AND lr.vehicle_returned_at IS NULL
		ORDER BY lr.pickup_confirmed_at DESC`, driverID)
	if err != nil {
		return nil, fmt.Errorf("list active rentals for driver: %w", err)
	}
	defer rows.Close()
	var out []DriverActiveRentalRow
	for rows.Next() {
		var d DriverActiveRentalRow
		if err := rows.Scan(&d.LeaseRequestID, &d.ListingID, &d.CarTitle, &d.CoverPhotoPath,
			&d.OwnerID, &d.OwnerName, &d.ChatID, &d.PickupConfirmedAt, &d.RentalEndsAt,
			&d.Weeks, &d.WeeklyPriceDollar, &d.PaidAmountCents,
			&d.PickupArea, &d.PickupLat, &d.PickupLng,
			&d.ListingArea, &d.ListingLat, &d.ListingLng); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
