package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
)

type LeaseRequestRepository struct {
	db *database.DB
}

func NewLeaseRequestRepository(db *database.DB) *LeaseRequestRepository {
	return &LeaseRequestRepository{db: db}
}

// CreateLeaseRequest creates a lease request, auto-creating a chat if needed.
// Returns the lease request with chat_id set.
func (r *LeaseRequestRepository) CreateLeaseRequest(ctx context.Context, lr *models.LeaseRequest) (*models.LeaseRequest, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	now := time.Now().UTC()

	// Find or create chat for (listing, driver, owner)
	chatID := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO chats (id, car_id, driver_id, owner_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5)
		ON CONFLICT (car_id, driver_id, owner_id) DO NOTHING
	`, chatID, lr.ListingID, lr.DriverID, lr.OwnerID, now)
	if err != nil {
		return nil, fmt.Errorf("upsert chat: %w", err)
	}

	// Get the actual chat ID (may have existed)
	err = tx.QueryRow(ctx, `
		SELECT id FROM chats WHERE car_id = $1 AND driver_id = $2 AND owner_id = $3
	`, lr.ListingID, lr.DriverID, lr.OwnerID).Scan(&lr.ChatID)
	if err != nil {
		return nil, fmt.Errorf("select chat: %w", err)
	}

	// Ensure participants exist
	for _, uid := range []uuid.UUID{lr.DriverID, lr.OwnerID} {
		_, err = tx.Exec(ctx, `
			INSERT INTO chat_participants (id, chat_id, user_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $4)
			ON CONFLICT (chat_id, user_id) DO NOTHING
		`, uuid.New(), lr.ChatID, uid, now)
		if err != nil {
			return nil, fmt.Errorf("upsert participant %s: %w", uid, err)
		}
	}

	// Insert lease request
	lr.ID = uuid.New()
	lr.Status = models.LeaseStatusRequested
	lr.CreatedAt = now
	lr.UpdatedAt = now
	lr.ExpiresAt = now.Add(24 * time.Hour)

	err = tx.QueryRow(ctx, `
		INSERT INTO lease_requests (id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, currency, weeks, message, expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12)
		RETURNING id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, offered_weekly_price, offered_price_updated_at, currency, weeks, message, expires_at, created_at, updated_at,
		          price_change_pending, previous_offered_weekly_price, price_change_acted_at
	`, lr.ID, lr.ChatID, lr.ListingID, lr.OwnerID, lr.DriverID, lr.Status,
		lr.WeeklyPrice, lr.Currency, lr.Weeks, lr.Message, lr.ExpiresAt, now,
	).Scan(
		&lr.ID, &lr.ChatID, &lr.ListingID, &lr.OwnerID, &lr.DriverID,
		&lr.Status, &lr.WeeklyPrice, &lr.OfferedWeeklyPrice, &lr.OfferedPriceUpdatedAt, &lr.Currency, &lr.Weeks, &lr.Message,
		&lr.ExpiresAt, &lr.CreatedAt, &lr.UpdatedAt,
		&lr.PriceChangePending, &lr.PreviousOfferedWeeklyPrice, &lr.PriceChangeActedAt,
	)
	if err != nil {
		// Check for unique constraint violation (duplicate active request)
		if isDuplicateKeyError(err) {
			return nil, models.ErrDuplicateLeaseReq
		}
		return nil, fmt.Errorf("insert lease request: %w", err)
	}

	// Create a system message in the chat
	_, err = tx.Exec(ctx, `
		INSERT INTO messages (id, chat_id, sender_id, type, body, created_at)
		VALUES ($1, $2, $3, 'system', $4, $5)
	`, uuid.New(), lr.ChatID, lr.DriverID,
		fmt.Sprintf("New lease request: %d week(s) at %s %.2f/week", lr.Weeks, lr.Currency, lr.WeeklyPrice),
		now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert system message: %w", err)
	}

	// Update chat timestamps
	_, err = tx.Exec(ctx, `
		UPDATE chats SET last_message_at = $2, last_request_at = $2, updated_at = $2 WHERE id = $1
	`, lr.ChatID, now)
	if err != nil {
		return nil, fmt.Errorf("update chat timestamps: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return lr, nil
}

// GetByID returns a lease request by its ID, including migration-000024
// pickup-deadline + refund fields and the migration-000028 price-review
// fields so callers can render the pickup countdown, refund status, AND
// the "owner changed the price, driver must review" state.
func (r *LeaseRequestRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.LeaseRequest, error) {
	var lr models.LeaseRequest
	err := r.db.Pool.QueryRow(ctx, `
		SELECT id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, offered_weekly_price, offered_price_updated_at, currency, weeks, message, expires_at, created_at, updated_at,
		       pickup_deadline_at, pickup_confirmed_at, refund_id, refunded_at, refund_status,
		       pickup_extension_total_minutes, pickup_extension_count, pickup_last_extended_at,
		       price_change_pending, previous_offered_weekly_price, price_change_acted_at
		FROM lease_requests WHERE id = $1
	`, id).Scan(
		&lr.ID, &lr.ChatID, &lr.ListingID, &lr.OwnerID, &lr.DriverID,
		&lr.Status, &lr.WeeklyPrice, &lr.OfferedWeeklyPrice, &lr.OfferedPriceUpdatedAt, &lr.Currency, &lr.Weeks, &lr.Message,
		&lr.ExpiresAt, &lr.CreatedAt, &lr.UpdatedAt,
		&lr.PickupDeadlineAt, &lr.PickupConfirmedAt, &lr.RefundID, &lr.RefundedAt, &lr.RefundStatus,
		&lr.PickupExtensionTotalMinutes, &lr.PickupExtensionCount, &lr.PickupLastExtendedAt,
		&lr.PriceChangePending, &lr.PreviousOfferedWeeklyPrice, &lr.PriceChangeActedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, models.ErrLeaseRequestNotFound
	}
	if err != nil {
		return nil, err
	}
	return &lr, nil
}

// ListForChat returns all lease requests for a given chat, most recent first.
func (r *LeaseRequestRepository) ListForChat(ctx context.Context, chatID uuid.UUID) ([]models.LeaseRequestResponse, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT
			lr.id, lr.chat_id, lr.listing_id, lr.owner_id, lr.driver_id, lr.status,
			lr.weekly_price, lr.offered_weekly_price, lr.currency, lr.weeks, lr.message, lr.expires_at, lr.created_at, lr.updated_at,
			lr.price_change_pending, lr.previous_offered_weekly_price, lr.price_change_acted_at,
			lr.pickup_deadline_at, lr.pickup_confirmed_at,
			lr.refund_id, lr.refunded_at, lr.refund_status,
			lr.pickup_extension_total_minutes, lr.pickup_extension_count, lr.pickup_last_extended_at,
			(SELECT first_name || ' ' || last_name FROM users WHERE id = lr.driver_id) AS driver_name,
			(SELECT first_name || ' ' || last_name FROM users WHERE id = lr.owner_id) AS owner_name,
			(SELECT title FROM cars WHERE id = lr.listing_id) AS car_title,
			p.id AS payment_id, p.payment_intent_id, p.amount AS payment_amount,
			p.platform_fee_amount, p.currency AS payment_currency, p.status AS payment_status
		FROM lease_requests lr
		LEFT JOIN payments p ON p.lease_request_id = lr.id
		WHERE lr.chat_id = $1
		ORDER BY lr.created_at DESC
	`, chatID)
	if err != nil {
		return nil, fmt.Errorf("list lease requests: %w", err)
	}
	defer rows.Close()

	results := make([]models.LeaseRequestResponse, 0)
	for rows.Next() {
		var resp models.LeaseRequestResponse
		var expiresAt, createdAt, updatedAt time.Time
		var message *string
		var priceChangeActedAt *time.Time
		// Pickup / refund / extension fields (added by migrations 24/25/28).
		// All nullable on the DB side — only populated once the lease moves
		// into the paid + pickup window. Without these in the response the
		// chat-card "I returned the car" CTA never appears even when the
		// lease is fully eligible, because iOS gates on pickup_confirmed_at.
		var pickupDeadlineAt, pickupConfirmedAt *time.Time
		var refundID *string
		var refundedAt *time.Time
		var refundStatus *string
		var pickupExtTotal, pickupExtCount int
		var pickupLastExtendedAt *time.Time
		// Payment fields (nullable from LEFT JOIN)
		var paymentID *uuid.UUID
		var paymentIntentID *string
		var paymentAmount *int64
		var platformFee *int64
		var paymentCurrency *string
		var paymentStatus *models.PaymentStatus

		err := rows.Scan(
			&resp.ID, &resp.ChatID, &resp.ListingID, &resp.OwnerID, &resp.DriverID, &resp.Status,
			&resp.WeeklyPrice, &resp.OfferedWeeklyPrice, &resp.Currency, &resp.Weeks, &message, &expiresAt, &createdAt, &updatedAt,
			&resp.PriceChangePending, &resp.PreviousOfferedWeeklyPrice, &priceChangeActedAt,
			&pickupDeadlineAt, &pickupConfirmedAt,
			&refundID, &refundedAt, &refundStatus,
			&pickupExtTotal, &pickupExtCount, &pickupLastExtendedAt,
			&resp.DriverName, &resp.OwnerName, &resp.CarTitle,
			&paymentID, &paymentIntentID, &paymentAmount,
			&platformFee, &paymentCurrency, &paymentStatus,
		)
		if err != nil {
			return nil, fmt.Errorf("scan lease request: %w", err)
		}
		resp.Message = message
		if priceChangeActedAt != nil {
			t := models.RFC3339Time(*priceChangeActedAt)
			resp.PriceChangeActedAt = &t
		}
		if pickupDeadlineAt != nil {
			t := models.RFC3339Time(*pickupDeadlineAt)
			resp.PickupDeadlineAt = &t
		}
		if pickupConfirmedAt != nil {
			t := models.RFC3339Time(*pickupConfirmedAt)
			resp.PickupConfirmedAt = &t
		}
		resp.RefundID = refundID
		if refundedAt != nil {
			t := models.RFC3339Time(*refundedAt)
			resp.RefundedAt = &t
		}
		resp.RefundStatus = refundStatus
		resp.PickupExtensionTotalMinutes = pickupExtTotal
		resp.PickupExtensionCount = pickupExtCount
		remaining := models.PickupMaxExtensionMinutes - pickupExtTotal
		if remaining < 0 {
			remaining = 0
		}
		resp.PickupExtensionRemainingMin = remaining
		if pickupLastExtendedAt != nil {
			t := models.RFC3339Time(*pickupLastExtendedAt)
			resp.PickupLastExtendedAt = &t
		}
		effectivePrice := resp.WeeklyPrice
		if resp.OfferedWeeklyPrice != nil {
			effectivePrice = *resp.OfferedWeeklyPrice
		}
		resp.TotalAmount = effectivePrice * float64(resp.Weeks)
		resp.ExpiresAt = models.RFC3339Time(expiresAt)
		resp.CreatedAt = models.RFC3339Time(createdAt)
		resp.UpdatedAt = models.RFC3339Time(updatedAt)

		if paymentID != nil {
			resp.Payment = &models.PaymentSummary{
				ID:                *paymentID,
				PaymentIntentID:   paymentIntentID,
				Amount:            *paymentAmount,
				PlatformFeeAmount: *platformFee,
				Currency:          *paymentCurrency,
				Status:            *paymentStatus,
			}
		}

		results = append(results, resp)
	}

	return results, nil
}

// HasAcceptedLeaseForChat reports whether the given driver has at least one
// lease request in this chat that the owner has already accepted (or that has
// progressed further into the payment / paid flow). Used as the visibility
// gate for vehicle documents on the driver side — car docs (registration,
// insurance, …) must not be revealed to a driver who has only created a
// request that the owner has not yet acted on.
//
// Gating statuses (driver may see vehicle docs):
//
//	accepted, payment_pending, paid
//
// Excluded by design:
//
//   - requested: the owner has not yet acted; this is exactly the case the
//     bug report is about.
//   - declined / cancelled: rental never started; access to docs ends with
//     the request.
//   - expired / expired_refunded: terminal states after a missed pickup or a
//     deadline lapse; per the safest-default rule we drop access once the
//     rental is no longer active.
func (r *LeaseRequestRepository) HasAcceptedLeaseForChat(ctx context.Context, chatID, driverID uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM lease_requests
			WHERE chat_id = $1
			  AND driver_id = $2
			  AND status IN ('accepted', 'payment_pending', 'paid')
		)
	`, chatID, driverID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check accepted lease for chat: %w", err)
	}
	return exists, nil
}

// AcceptLeaseRequest transitions a lease request from requested → accepted AND
// atomically reserves the car listing so it disappears from discovery for
// other drivers. Only the car owner may call. Failure to reserve (e.g., the
// car is already reserved by another concurrent accept) rolls back the entire
// transition, so the lease stays 'requested' rather than getting orphaned.
func (r *LeaseRequestRepository) AcceptLeaseRequest(ctx context.Context, id, ownerID uuid.UUID) (*models.LeaseRequest, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Lock the row, validate actor + current status.
	var lr models.LeaseRequest
	err = tx.QueryRow(ctx, `
		SELECT id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, offered_weekly_price, offered_price_updated_at, currency, weeks, message, expires_at, created_at, updated_at,
		       price_change_pending, previous_offered_weekly_price, price_change_acted_at
		FROM lease_requests WHERE id = $1 FOR UPDATE
	`, id).Scan(
		&lr.ID, &lr.ChatID, &lr.ListingID, &lr.OwnerID, &lr.DriverID,
		&lr.Status, &lr.WeeklyPrice, &lr.OfferedWeeklyPrice, &lr.OfferedPriceUpdatedAt, &lr.Currency, &lr.Weeks, &lr.Message,
		&lr.ExpiresAt, &lr.CreatedAt, &lr.UpdatedAt,
		&lr.PriceChangePending, &lr.PreviousOfferedWeeklyPrice, &lr.PriceChangeActedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, models.ErrLeaseRequestNotFound
	}
	if err != nil {
		return nil, err
	}
	if ownerID != lr.OwnerID {
		return nil, models.NewAPIError(models.ErrCodeInvalidLeaseAction, "Only the owner can perform this action")
	}
	if lr.Status != models.LeaseStatusRequested {
		return nil, models.ErrInvalidLeaseAction
	}

	now := time.Now().UTC()

	// 1. Transition lease status to 'accepted'.
	err = tx.QueryRow(ctx, `
		UPDATE lease_requests SET status = $2, updated_at = $3
		WHERE id = $1
		RETURNING id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, offered_weekly_price, offered_price_updated_at, currency, weeks, message, expires_at, created_at, updated_at,
		          price_change_pending, previous_offered_weekly_price, price_change_acted_at
	`, id, models.LeaseStatusAccepted, now).Scan(
		&lr.ID, &lr.ChatID, &lr.ListingID, &lr.OwnerID, &lr.DriverID,
		&lr.Status, &lr.WeeklyPrice, &lr.OfferedWeeklyPrice, &lr.OfferedPriceUpdatedAt, &lr.Currency, &lr.Weeks, &lr.Message,
		&lr.ExpiresAt, &lr.CreatedAt, &lr.UpdatedAt,
		&lr.PriceChangePending, &lr.PreviousOfferedWeeklyPrice, &lr.PriceChangeActedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("update lease status: %w", err)
	}

	// 2. Reserve the car listing — guarded so a concurrent accept of a
	//    different lease for the same car cannot double-reserve.
	tag, err := tx.Exec(ctx, `
		UPDATE cars SET reserved_by_lease_request_id = $1
		WHERE id = $2 AND reserved_by_lease_request_id IS NULL
	`, lr.ID, lr.ListingID)
	if err != nil {
		return nil, fmt.Errorf("reserve car: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, models.NewAPIError(models.ErrCodeInvalidLeaseAction,
			"This car was already reserved by another driver while you were accepting.")
	}

	// 3. System message + chat timestamp (same shape updateStatus uses).
	if _, err := tx.Exec(ctx, `
		INSERT INTO messages (id, chat_id, sender_id, type, body, created_at)
		VALUES ($1, $2, $3, 'system', 'Lease request accepted', $4)
	`, uuid.New(), lr.ChatID, ownerID, now); err != nil {
		return nil, fmt.Errorf("insert system message: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE chats SET last_message_at = $2, updated_at = $2 WHERE id = $1
	`, lr.ChatID, now); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &lr, nil
}

// DeclineLeaseRequest transitions a lease request from requested → declined (owner only).
// Also best-effort unreserves the car if this lease had reserved it (defensive
// — today's flow only allows decline from 'requested' so the car was never
// reserved by this lease, but future flexibility may allow decline-after-accept).
func (r *LeaseRequestRepository) DeclineLeaseRequest(ctx context.Context, id, ownerID uuid.UUID) (*models.LeaseRequest, error) {
	lr, err := r.updateStatus(ctx, id, ownerID, models.LeaseStatusRequested, models.LeaseStatusDeclined, "owner")
	if err != nil {
		return nil, err
	}
	r.unreserveCarIfHeldBy(ctx, lr.ID)
	return lr, nil
}

// CancelLeaseRequest transitions a lease request from requested → cancelled (driver only).
func (r *LeaseRequestRepository) CancelLeaseRequest(ctx context.Context, id, driverID uuid.UUID) (*models.LeaseRequest, error) {
	lr, err := r.updateStatus(ctx, id, driverID, models.LeaseStatusRequested, models.LeaseStatusCancelled, "driver")
	if err != nil {
		return nil, err
	}
	r.unreserveCarIfHeldBy(ctx, lr.ID)
	return lr, nil
}

// RescindAccept transitions accepted → cancelled (owner only) and releases the
// car back to discovery in the same flow. This is the "undo accept" path for
// owners who tapped Accept by mistake; it MUST refuse once a payment is in
// flight (status moved past 'accepted') because we never want to drop a paid
// lease without going through the refund path.
//
// The updateStatus helper underneath enforces:
//   - actor must equal owner_id (owner-only)
//   - current status must equal 'accepted' (refused with INVALID_LEASE_ACTION
//     for any other state, including payment_pending and paid)
//
// Calling Rescind twice in succession returns INVALID_LEASE_ACTION on the
// second call — the row is already cancelled. The handler maps that to
// a clean 409 so a double-tap doesn't look like a 500.
func (r *LeaseRequestRepository) RescindAccept(ctx context.Context, id, ownerID uuid.UUID) (*models.LeaseRequest, error) {
	lr, err := r.updateStatus(ctx, id, ownerID, models.LeaseStatusAccepted, models.LeaseStatusCancelled, "owner")
	if err != nil {
		return nil, err
	}
	r.unreserveCarIfHeldBy(ctx, lr.ID)
	return lr, nil
}

// unreserveCarIfHeldBy clears cars.reserved_by_lease_request_id when (and only
// when) it matches this lease — so this lease's transition out of a
// reservation-holding state releases the listing back to discovery, while
// leaving unrelated reservations untouched. Safe to call when no car is
// reserved (no-op).
func (r *LeaseRequestRepository) unreserveCarIfHeldBy(ctx context.Context, leaseRequestID uuid.UUID) {
	_, _ = r.db.Pool.Exec(ctx, `
		UPDATE cars SET reserved_by_lease_request_id = NULL
		WHERE reserved_by_lease_request_id = $1
	`, leaseRequestID)
}

// SetPaymentPending transitions accepted → payment_pending.
func (r *LeaseRequestRepository) SetPaymentPending(ctx context.Context, id uuid.UUID) (*models.LeaseRequest, error) {
	var lr models.LeaseRequest
	err := r.db.Pool.QueryRow(ctx, `
		UPDATE lease_requests SET status = $2, updated_at = NOW()
		WHERE id = $1 AND status = 'accepted'
		RETURNING id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, offered_weekly_price, offered_price_updated_at, currency, weeks, message, expires_at, created_at, updated_at,
		          price_change_pending, previous_offered_weekly_price, price_change_acted_at
	`, id, models.LeaseStatusPaymentPending).Scan(
		&lr.ID, &lr.ChatID, &lr.ListingID, &lr.OwnerID, &lr.DriverID,
		&lr.Status, &lr.WeeklyPrice, &lr.OfferedWeeklyPrice, &lr.OfferedPriceUpdatedAt, &lr.Currency, &lr.Weeks, &lr.Message,
		&lr.ExpiresAt, &lr.CreatedAt, &lr.UpdatedAt,
		&lr.PriceChangePending, &lr.PreviousOfferedWeeklyPrice, &lr.PriceChangeActedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, models.ErrInvalidLeaseAction
	}
	if err != nil {
		return nil, err
	}
	return &lr, nil
}

// SetPaid transitions accepted/payment_pending → paid.
// Accepts both statuses because the webhook may arrive before SetPaymentPending completes.
func (r *LeaseRequestRepository) SetPaid(ctx context.Context, id uuid.UUID) (*models.LeaseRequest, error) {
	var lr models.LeaseRequest
	err := r.db.Pool.QueryRow(ctx, `
		UPDATE lease_requests SET status = $2, updated_at = NOW()
		WHERE id = $1 AND status IN ('accepted', 'payment_pending')
		RETURNING id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, offered_weekly_price, offered_price_updated_at, currency, weeks, message, expires_at, created_at, updated_at,
		          price_change_pending, previous_offered_weekly_price, price_change_acted_at
	`, id, models.LeaseStatusPaid).Scan(
		&lr.ID, &lr.ChatID, &lr.ListingID, &lr.OwnerID, &lr.DriverID,
		&lr.Status, &lr.WeeklyPrice, &lr.OfferedWeeklyPrice, &lr.OfferedPriceUpdatedAt, &lr.Currency, &lr.Weeks, &lr.Message,
		&lr.ExpiresAt, &lr.CreatedAt, &lr.UpdatedAt,
		&lr.PriceChangePending, &lr.PreviousOfferedWeeklyPrice, &lr.PriceChangeActedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, models.ErrInvalidLeaseAction
	}
	if err != nil {
		return nil, err
	}
	return &lr, nil
}

// updateStatus is a generic state transition helper.
func (r *LeaseRequestRepository) updateStatus(ctx context.Context, id, actorID uuid.UUID, fromStatus, toStatus models.LeaseRequestStatus, role string) (*models.LeaseRequest, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Lock and validate
	var lr models.LeaseRequest
	err = tx.QueryRow(ctx, `
		SELECT id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, offered_weekly_price, offered_price_updated_at, currency, weeks, message, expires_at, created_at, updated_at,
		       price_change_pending, previous_offered_weekly_price, price_change_acted_at
		FROM lease_requests WHERE id = $1 FOR UPDATE
	`, id).Scan(
		&lr.ID, &lr.ChatID, &lr.ListingID, &lr.OwnerID, &lr.DriverID,
		&lr.Status, &lr.WeeklyPrice, &lr.OfferedWeeklyPrice, &lr.OfferedPriceUpdatedAt, &lr.Currency, &lr.Weeks, &lr.Message,
		&lr.ExpiresAt, &lr.CreatedAt, &lr.UpdatedAt,
		&lr.PriceChangePending, &lr.PreviousOfferedWeeklyPrice, &lr.PriceChangeActedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, models.ErrLeaseRequestNotFound
	}
	if err != nil {
		return nil, err
	}

	// Validate actor
	switch role {
	case "owner":
		if actorID != lr.OwnerID {
			return nil, models.NewAPIError(models.ErrCodeInvalidLeaseAction, "Only the owner can perform this action")
		}
	case "driver":
		if actorID != lr.DriverID {
			return nil, models.NewAPIError(models.ErrCodeInvalidLeaseAction, "Only the driver can perform this action")
		}
	}

	// Validate status transition
	if lr.Status != fromStatus {
		return nil, models.ErrInvalidLeaseAction
	}

	now := time.Now().UTC()
	err = tx.QueryRow(ctx, `
		UPDATE lease_requests SET status = $2, updated_at = $3
		WHERE id = $1
		RETURNING id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, offered_weekly_price, offered_price_updated_at, currency, weeks, message, expires_at, created_at, updated_at,
		          price_change_pending, previous_offered_weekly_price, price_change_acted_at
	`, id, toStatus, now).Scan(
		&lr.ID, &lr.ChatID, &lr.ListingID, &lr.OwnerID, &lr.DriverID,
		&lr.Status, &lr.WeeklyPrice, &lr.OfferedWeeklyPrice, &lr.OfferedPriceUpdatedAt, &lr.Currency, &lr.Weeks, &lr.Message,
		&lr.ExpiresAt, &lr.CreatedAt, &lr.UpdatedAt,
		&lr.PriceChangePending, &lr.PreviousOfferedWeeklyPrice, &lr.PriceChangeActedAt,
	)
	if err != nil {
		return nil, err
	}

	// System message
	sysMsg := fmt.Sprintf("Lease request %s", string(toStatus))
	_, err = tx.Exec(ctx, `
		INSERT INTO messages (id, chat_id, sender_id, type, body, created_at)
		VALUES ($1, $2, $3, 'system', $4, $5)
	`, uuid.New(), lr.ChatID, actorID, sysMsg, now)
	if err != nil {
		return nil, fmt.Errorf("insert system message: %w", err)
	}

	_, err = tx.Exec(ctx, `UPDATE chats SET last_message_at = $2, updated_at = $2 WHERE id = $1`, lr.ChatID, now)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &lr, nil
}

// UpdateOfferedPrice sets the owner's offered price and flips the lease
// into the "price changed, driver must review" state. Allowed at any
// non-terminal pre-payment status (requested / accepted / payment_pending)
// — basically anywhere the driver hasn't paid yet.
//
// Behaviour:
//   - snapshots the current offered_weekly_price (or weekly_price if there's
//     no prior offer) into previous_offered_weekly_price so the UI can show
//     old → new,
//   - sets price_change_pending=TRUE (driver must accept or decline before
//     Pay Now reappears — enforced by LeaseRequest.IsPayable() + the
//     handler payment gate),
//   - clears price_change_acted_at (last decision is now stale),
//   - inserts a gray "Owner updated the weekly price" system message,
//   - returns the existing payment_intent_id if any so the handler can
//     cancel the stale Stripe PaymentIntent (price is going to change,
//     the saved payment sheet must not be able to submit the old amount).
//
// Refuses ErrPriceLocked once the lease has actually been paid (status
// `paid`, `expired`, `expired_refunded`, `declined`, `cancelled`) — after
// payment we'd need a refund flow, which is out of scope.
func (r *LeaseRequestRepository) UpdateOfferedPrice(ctx context.Context, id, ownerID uuid.UUID, offeredPrice float64) (*models.LeaseRequest, string, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(ctx)

	var lr models.LeaseRequest
	err = tx.QueryRow(ctx, `
		SELECT id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, offered_weekly_price, offered_price_updated_at, currency, weeks, message, expires_at, created_at, updated_at,
		       price_change_pending, previous_offered_weekly_price, price_change_acted_at
		FROM lease_requests WHERE id = $1 FOR UPDATE
	`, id).Scan(
		&lr.ID, &lr.ChatID, &lr.ListingID, &lr.OwnerID, &lr.DriverID,
		&lr.Status, &lr.WeeklyPrice, &lr.OfferedWeeklyPrice, &lr.OfferedPriceUpdatedAt, &lr.Currency, &lr.Weeks, &lr.Message,
		&lr.ExpiresAt, &lr.CreatedAt, &lr.UpdatedAt,
		&lr.PriceChangePending, &lr.PreviousOfferedWeeklyPrice, &lr.PriceChangeActedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, "", models.ErrLeaseRequestNotFound
	}
	if err != nil {
		return nil, "", err
	}

	if ownerID != lr.OwnerID {
		return nil, "", models.NewAPIError(models.ErrCodeInvalidLeaseAction, "Only the owner can adjust the price")
	}

	switch lr.Status {
	case models.LeaseStatusRequested, models.LeaseStatusAccepted, models.LeaseStatusPaymentPending:
		// OK — price adjustable while no payment has actually succeeded.
	default:
		return nil, "", models.ErrPriceLocked
	}

	// Capture the prior offered price so the UI can render old vs new. If
	// there was no previous offer (first adjust), fall back to the base
	// listing price so the comparison still has something to show.
	prev := lr.WeeklyPrice
	if lr.OfferedWeeklyPrice != nil {
		prev = *lr.OfferedWeeklyPrice
	}

	now := time.Now().UTC()
	err = tx.QueryRow(ctx, `
		UPDATE lease_requests
		SET offered_weekly_price          = $2,
		    offered_price_updated_at      = $3,
		    previous_offered_weekly_price = $4,
		    price_change_pending          = TRUE,
		    price_change_acted_at         = NULL,
		    updated_at                    = $3
		WHERE id = $1
		RETURNING id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, offered_weekly_price, offered_price_updated_at, currency, weeks, message, expires_at, created_at, updated_at,
		          price_change_pending, previous_offered_weekly_price, price_change_acted_at
	`, id, offeredPrice, now, prev).Scan(
		&lr.ID, &lr.ChatID, &lr.ListingID, &lr.OwnerID, &lr.DriverID,
		&lr.Status, &lr.WeeklyPrice, &lr.OfferedWeeklyPrice, &lr.OfferedPriceUpdatedAt, &lr.Currency, &lr.Weeks, &lr.Message,
		&lr.ExpiresAt, &lr.CreatedAt, &lr.UpdatedAt,
		&lr.PriceChangePending, &lr.PreviousOfferedWeeklyPrice, &lr.PriceChangeActedAt,
	)
	if err != nil {
		return nil, "", fmt.Errorf("update offered price: %w", err)
	}

	sysMsg := fmt.Sprintf("Owner updated the weekly price to %s %.2f/wk — driver must review before paying.", lr.Currency, offeredPrice)
	_, err = tx.Exec(ctx, `
		INSERT INTO messages (id, chat_id, sender_id, type, body, created_at)
		VALUES ($1, $2, $3, 'system', $4, $5)
	`, uuid.New(), lr.ChatID, ownerID, sysMsg, now)
	if err != nil {
		return nil, "", fmt.Errorf("insert system message: %w", err)
	}

	_, err = tx.Exec(ctx, `UPDATE chats SET last_message_at = $2, updated_at = $2 WHERE id = $1`, lr.ChatID, now)
	if err != nil {
		return nil, "", err
	}

	// Pull the still-active PaymentIntent (if any) so the caller can cancel
	// it on Stripe. We do this inside the same transaction so a concurrent
	// CreatePaymentIntent racing with this update either commits first
	// (we get its intent ID and cancel it) or rolls back behind our row
	// lock.
	var intentID string
	err = tx.QueryRow(ctx, `
		SELECT payment_intent_id FROM payments
		WHERE lease_request_id = $1
		  AND payment_intent_id IS NOT NULL
		  AND status <> 'succeeded'
		ORDER BY created_at DESC
		LIMIT 1
	`, id).Scan(&intentID)
	if err != nil && err != pgx.ErrNoRows {
		return nil, "", fmt.Errorf("look up pending intent: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	return &lr, intentID, nil
}

// AcceptPriceChange clears the price-review flag so Pay Now becomes
// available again. Driver-only, only allowed when price_change_pending=TRUE.
// Inserts a gray system message announcing the acceptance.
func (r *LeaseRequestRepository) AcceptPriceChange(ctx context.Context, id, driverID uuid.UUID) (*models.LeaseRequest, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var lr models.LeaseRequest
	err = tx.QueryRow(ctx, `
		SELECT id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, offered_weekly_price, offered_price_updated_at, currency, weeks, message, expires_at, created_at, updated_at,
		       price_change_pending, previous_offered_weekly_price, price_change_acted_at
		FROM lease_requests WHERE id = $1 FOR UPDATE
	`, id).Scan(
		&lr.ID, &lr.ChatID, &lr.ListingID, &lr.OwnerID, &lr.DriverID,
		&lr.Status, &lr.WeeklyPrice, &lr.OfferedWeeklyPrice, &lr.OfferedPriceUpdatedAt, &lr.Currency, &lr.Weeks, &lr.Message,
		&lr.ExpiresAt, &lr.CreatedAt, &lr.UpdatedAt,
		&lr.PriceChangePending, &lr.PreviousOfferedWeeklyPrice, &lr.PriceChangeActedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, models.ErrLeaseRequestNotFound
	}
	if err != nil {
		return nil, err
	}

	if driverID != lr.DriverID {
		return nil, models.NewAPIError(models.ErrCodeInvalidLeaseAction, "Only the driver can review the price change")
	}
	if !lr.PriceChangePending {
		return nil, models.ErrNoPriceChangePending
	}

	now := time.Now().UTC()
	err = tx.QueryRow(ctx, `
		UPDATE lease_requests
		SET price_change_pending  = FALSE,
		    price_change_acted_at = $2,
		    updated_at            = $2
		WHERE id = $1
		RETURNING id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, offered_weekly_price, offered_price_updated_at, currency, weeks, message, expires_at, created_at, updated_at,
		          price_change_pending, previous_offered_weekly_price, price_change_acted_at
	`, id, now).Scan(
		&lr.ID, &lr.ChatID, &lr.ListingID, &lr.OwnerID, &lr.DriverID,
		&lr.Status, &lr.WeeklyPrice, &lr.OfferedWeeklyPrice, &lr.OfferedPriceUpdatedAt, &lr.Currency, &lr.Weeks, &lr.Message,
		&lr.ExpiresAt, &lr.CreatedAt, &lr.UpdatedAt,
		&lr.PriceChangePending, &lr.PreviousOfferedWeeklyPrice, &lr.PriceChangeActedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("accept price change: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO messages (id, chat_id, sender_id, type, body, created_at)
		VALUES ($1, $2, $3, 'system', 'Driver accepted the new price', $4)
	`, uuid.New(), lr.ChatID, driverID, now); err != nil {
		return nil, fmt.Errorf("insert system message: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE chats SET last_message_at = $2, updated_at = $2 WHERE id = $1`, lr.ChatID, now); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &lr, nil
}

// DeclinePriceChange cancels the lease — same terminal effect as the
// regular driver-cancel path. Driver-only, only allowed when
// price_change_pending=TRUE. Unreserves the car so the listing returns
// to Discovery and inserts a tailored system message that names the
// trigger (price decline) rather than the generic "cancelled".
//
// The caller is responsible for cancelling any unpaid Stripe
// PaymentIntent — we return its ID alongside the updated lease so the
// handler can do that best-effort outside the DB transaction.
func (r *LeaseRequestRepository) DeclinePriceChange(ctx context.Context, id, driverID uuid.UUID) (*models.LeaseRequest, string, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(ctx)

	var lr models.LeaseRequest
	err = tx.QueryRow(ctx, `
		SELECT id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, offered_weekly_price, offered_price_updated_at, currency, weeks, message, expires_at, created_at, updated_at,
		       price_change_pending, previous_offered_weekly_price, price_change_acted_at
		FROM lease_requests WHERE id = $1 FOR UPDATE
	`, id).Scan(
		&lr.ID, &lr.ChatID, &lr.ListingID, &lr.OwnerID, &lr.DriverID,
		&lr.Status, &lr.WeeklyPrice, &lr.OfferedWeeklyPrice, &lr.OfferedPriceUpdatedAt, &lr.Currency, &lr.Weeks, &lr.Message,
		&lr.ExpiresAt, &lr.CreatedAt, &lr.UpdatedAt,
		&lr.PriceChangePending, &lr.PreviousOfferedWeeklyPrice, &lr.PriceChangeActedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, "", models.ErrLeaseRequestNotFound
	}
	if err != nil {
		return nil, "", err
	}

	if driverID != lr.DriverID {
		return nil, "", models.NewAPIError(models.ErrCodeInvalidLeaseAction, "Only the driver can review the price change")
	}
	if !lr.PriceChangePending {
		return nil, "", models.ErrNoPriceChangePending
	}

	now := time.Now().UTC()
	err = tx.QueryRow(ctx, `
		UPDATE lease_requests
		SET status                = 'cancelled',
		    price_change_pending  = FALSE,
		    price_change_acted_at = $2,
		    updated_at            = $2
		WHERE id = $1
		RETURNING id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, offered_weekly_price, offered_price_updated_at, currency, weeks, message, expires_at, created_at, updated_at,
		          price_change_pending, previous_offered_weekly_price, price_change_acted_at
	`, id, now).Scan(
		&lr.ID, &lr.ChatID, &lr.ListingID, &lr.OwnerID, &lr.DriverID,
		&lr.Status, &lr.WeeklyPrice, &lr.OfferedWeeklyPrice, &lr.OfferedPriceUpdatedAt, &lr.Currency, &lr.Weeks, &lr.Message,
		&lr.ExpiresAt, &lr.CreatedAt, &lr.UpdatedAt,
		&lr.PriceChangePending, &lr.PreviousOfferedWeeklyPrice, &lr.PriceChangeActedAt,
	)
	if err != nil {
		return nil, "", fmt.Errorf("decline price change: %w", err)
	}

	// Unreserve the car if THIS lease was the one holding the reservation.
	// Safe inside the same transaction — uses the same predicate as the
	// regular cancel path.
	if _, err := tx.Exec(ctx, `
		UPDATE cars SET reserved_by_lease_request_id = NULL
		WHERE reserved_by_lease_request_id = $1
	`, lr.ID); err != nil {
		return nil, "", fmt.Errorf("unreserve car: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO messages (id, chat_id, sender_id, type, body, created_at)
		VALUES ($1, $2, $3, 'system', 'Driver declined the new price — request cancelled', $4)
	`, uuid.New(), lr.ChatID, driverID, now); err != nil {
		return nil, "", fmt.Errorf("insert system message: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE chats SET last_message_at = $2, updated_at = $2 WHERE id = $1`, lr.ChatID, now); err != nil {
		return nil, "", err
	}

	// Grab the still-pending PaymentIntent (if any) so the caller can
	// cancel it on Stripe — same pattern as UpdateOfferedPrice.
	var intentID string
	err = tx.QueryRow(ctx, `
		SELECT payment_intent_id FROM payments
		WHERE lease_request_id = $1
		  AND payment_intent_id IS NOT NULL
		  AND status <> 'succeeded'
		ORDER BY created_at DESC
		LIMIT 1
	`, id).Scan(&intentID)
	if err != nil && err != pgx.ErrNoRows {
		return nil, "", fmt.Errorf("look up pending intent: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	return &lr, intentID, nil
}

// --- Today Actions ---

// ListTodayActionsForOwner returns pending lease requests where the user is the owner.
// Also lazy-expires overdue requests. Results ordered by expires_at ASC (most urgent first).
func (r *LeaseRequestRepository) ListTodayActionsForOwner(ctx context.Context, ownerID uuid.UUID) ([]models.TodayAction, error) {
	// Lazy-expire overdue lease requests
	_, _ = r.db.Pool.Exec(ctx, `
		UPDATE lease_requests SET status = 'expired', updated_at = NOW()
		WHERE owner_id = $1 AND status = 'requested' AND expires_at <= NOW()
	`, ownerID)

	rows, err := r.db.Pool.Query(ctx, `
		SELECT
			lr.id,
			lr.weekly_price, lr.currency, lr.weeks,
			c.car_id,
			(SELECT title FROM cars WHERE id = c.car_id) AS car_title,
			lr.chat_id,
			lr.driver_id,
			(SELECT first_name || ' ' || last_name FROM users WHERE id = lr.driver_id) AS driver_name,
			lr.status,
			lr.created_at,
			lr.expires_at
		FROM lease_requests lr
		JOIN chats c ON c.id = lr.chat_id
		WHERE lr.owner_id = $1
			AND lr.status = 'requested'
			AND lr.expires_at > NOW()
		ORDER BY lr.expires_at ASC
	`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list today actions: %w", err)
	}
	defer rows.Close()

	actions := make([]models.TodayAction, 0)
	for rows.Next() {
		var a models.TodayAction
		var weeklyPrice float64
		var currency string
		var weeks int
		var createdAt, expiresAt time.Time

		err := rows.Scan(
			&a.ID,
			&weeklyPrice, &currency, &weeks,
			&a.CarID, &a.CarTitle,
			&a.ChatID,
			&a.CounterpartyID, &a.CounterpartyName,
			&a.Status,
			&createdAt, &expiresAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan today action: %w", err)
		}

		a.Type = models.TodayActionLeaseRequest
		a.Title = "Approve lease request"
		a.Body = fmt.Sprintf("%s wants to rent your %s for %d week(s) at %s %.0f/week",
			a.CounterpartyName, a.CarTitle, weeks, currency, weeklyPrice)
		a.PrimaryAction = "approve"
		a.SecondaryAction = "decline"
		a.CreatedAt = models.RFC3339Time(createdAt)
		a.ExpiresAt = models.RFC3339Time(expiresAt)

		actions = append(actions, a)
	}

	return actions, nil
}

// HasUnreadActions checks if any actions were created after lastSeenAt.
func (r *LeaseRequestRepository) HasUnreadActions(ctx context.Context, ownerID uuid.UUID, lastSeenAt time.Time) (bool, error) {
	var exists bool
	err := r.db.Pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM lease_requests
			WHERE owner_id = $1 AND status = 'requested' AND expires_at > NOW() AND created_at > $2
		)
	`, ownerID, lastSeenAt).Scan(&exists)
	return exists, err
}

// ListTodayActionsForDriver returns the driver's Today feed. Two kinds of
// card are produced, both backed by the lease_requests table:
//
//   - lease_price_review (priority, listed first): the owner just adjusted
//     the offered price and the driver hasn't accepted or declined yet.
//     Pay Now is held until they review, so this card is the only path
//     forward — pull it to the top of the feed.
//   - lease_payment: the owner accepted (or driver is mid-retry) and no
//     successful payment exists yet. Same shape as before.
//
// Status=paid is excluded — once paid, the key-handover card takes over.
// We exclude price-review rows from the lease_payment query so a single
// lease never produces two cards simultaneously.
func (r *LeaseRequestRepository) ListTodayActionsForDriver(ctx context.Context, driverID uuid.UUID) ([]models.TodayAction, error) {
	actions := make([]models.TodayAction, 0)

	// --- 1. Price-review cards (priority). ---
	prRows, err := r.db.Pool.Query(ctx, `
		SELECT
			lr.id,
			lr.weekly_price, lr.offered_weekly_price, lr.previous_offered_weekly_price,
			lr.currency, lr.weeks,
			c.car_id,
			(SELECT title FROM cars WHERE id = c.car_id) AS car_title,
			lr.chat_id,
			lr.owner_id,
			(SELECT first_name || ' ' || last_name FROM users WHERE id = lr.owner_id) AS owner_name,
			lr.status,
			lr.created_at,
			lr.expires_at
		FROM lease_requests lr
		JOIN chats c ON c.id = lr.chat_id
		WHERE lr.driver_id = $1
			AND lr.price_change_pending = TRUE
			AND lr.status IN ('requested', 'accepted', 'payment_pending')
		ORDER BY lr.updated_at DESC
	`, driverID)
	if err != nil {
		return nil, fmt.Errorf("list driver price-review actions: %w", err)
	}
	defer prRows.Close()
	for prRows.Next() {
		var a models.TodayAction
		var weeklyPrice float64
		var offered, prev *float64
		var currency string
		var weeks int
		var createdAt, expiresAt time.Time

		if err := prRows.Scan(
			&a.ID,
			&weeklyPrice, &offered, &prev,
			&currency, &weeks,
			&a.CarID, &a.CarTitle,
			&a.ChatID,
			&a.CounterpartyID, &a.CounterpartyName,
			&a.Status,
			&createdAt, &expiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan driver price-review action: %w", err)
		}

		newPrice := weeklyPrice
		if offered != nil {
			newPrice = *offered
		}
		a.Type = models.TodayActionLeasePriceReview
		a.Title = "Price updated"
		if prev != nil {
			a.Body = fmt.Sprintf("%s changed the weekly price for %s — was %s %.0f, now %s %.0f. Review before paying.",
				a.CounterpartyName, a.CarTitle, currency, *prev, currency, newPrice)
		} else {
			a.Body = fmt.Sprintf("%s changed the price for %s to %s %.0f/week. Review before paying.",
				a.CounterpartyName, a.CarTitle, currency, newPrice)
		}
		a.PrimaryAction = "go_to_requests"
		a.SecondaryAction = ""
		a.CreatedAt = models.RFC3339Time(createdAt)
		a.ExpiresAt = models.RFC3339Time(expiresAt)
		actions = append(actions, a)
	}
	if err := prRows.Err(); err != nil {
		return nil, err
	}

	// --- 2. Payment cards. ---
	rows, err := r.db.Pool.Query(ctx, `
		SELECT
			lr.id,
			lr.weekly_price, lr.currency, lr.weeks,
			c.car_id,
			(SELECT title FROM cars WHERE id = c.car_id) AS car_title,
			lr.chat_id,
			lr.owner_id,
			(SELECT first_name || ' ' || last_name FROM users WHERE id = lr.owner_id) AS owner_name,
			lr.status,
			lr.created_at,
			lr.expires_at
		FROM lease_requests lr
		JOIN chats c ON c.id = lr.chat_id
		WHERE lr.driver_id = $1
			AND lr.status IN ('accepted', 'payment_pending')
			AND lr.price_change_pending = FALSE
			AND NOT EXISTS (
				SELECT 1 FROM payments p
				WHERE p.lease_request_id = lr.id AND p.status = 'succeeded'
			)
		ORDER BY lr.updated_at DESC
	`, driverID)
	if err != nil {
		return nil, fmt.Errorf("list driver today actions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var a models.TodayAction
		var weeklyPrice float64
		var currency string
		var weeks int
		var createdAt, expiresAt time.Time

		err := rows.Scan(
			&a.ID,
			&weeklyPrice, &currency, &weeks,
			&a.CarID, &a.CarTitle,
			&a.ChatID,
			&a.CounterpartyID, &a.CounterpartyName,
			&a.Status,
			&createdAt, &expiresAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan driver today action: %w", err)
		}

		a.Type = models.TodayActionLeasePayment
		a.Title = "Request accepted"
		a.Body = fmt.Sprintf("%s accepted your request for %s — %d week(s) at %s %.0f/week. Go to requests to complete payment.",
			a.CounterpartyName, a.CarTitle, weeks, currency, weeklyPrice)
		// One-CTA card; iOS collapses the layout to a single button labelled
		// "Go to requests" when the type is lease_payment.
		a.PrimaryAction = "go_to_requests"
		a.SecondaryAction = ""
		a.CreatedAt = models.RFC3339Time(createdAt)
		a.ExpiresAt = models.RFC3339Time(expiresAt)

		actions = append(actions, a)
	}

	return actions, nil
}

// HasUnreadActionsForDriver mirrors HasUnreadActions for the driver-side
// card; we want the bell badge to light up when an owner just accepted OR
// when the owner just adjusted the price (price-review pending).
func (r *LeaseRequestRepository) HasUnreadActionsForDriver(ctx context.Context, driverID uuid.UUID, lastSeenAt time.Time) (bool, error) {
	var exists bool
	err := r.db.Pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM lease_requests
			WHERE driver_id = $1
			  AND updated_at > $2
			  AND (
			    -- Standard "accepted, awaiting payment" path
			    (status IN ('accepted', 'payment_pending')
			     AND price_change_pending = FALSE
			     AND NOT EXISTS (
				     SELECT 1 FROM payments p
				     WHERE p.lease_request_id = lease_requests.id AND p.status = 'succeeded'
			     ))
			    OR
			    -- New: owner just changed the price, driver must review
			    (status IN ('requested', 'accepted', 'payment_pending')
			     AND price_change_pending = TRUE)
			  )
		)
	`, driverID, lastSeenAt).Scan(&exists)
	return exists, err
}

// --- Payment repository methods ---

// CreatePayment creates a payment record for a lease request.
func (r *LeaseRequestRepository) CreatePayment(ctx context.Context, p *models.Payment) (*models.Payment, error) {
	p.ID = uuid.New()
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now

	err := r.db.Pool.QueryRow(ctx, `
		INSERT INTO payments (id, lease_request_id, provider, stripe_customer_id, payment_intent_id, payment_intent_client_secret, amount, currency, platform_fee_amount, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)
		RETURNING id, lease_request_id, provider, stripe_customer_id, payment_intent_id, payment_intent_client_secret, amount, currency, platform_fee_amount, status, created_at, updated_at
	`, p.ID, p.LeaseRequestID, p.Provider, p.StripeCustomerID, p.PaymentIntentID, p.ClientSecret,
		p.Amount, p.Currency, p.PlatformFeeAmount, p.Status, now,
	).Scan(
		&p.ID, &p.LeaseRequestID, &p.Provider, &p.StripeCustomerID, &p.PaymentIntentID, &p.ClientSecret,
		&p.Amount, &p.Currency, &p.PlatformFeeAmount, &p.Status, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if isDuplicateKeyError(err) {
			return nil, models.ErrPaymentAlreadyExists
		}
		return nil, fmt.Errorf("insert payment: %w", err)
	}
	return p, nil
}

// GetPaymentByLeaseRequestID returns the payment for a lease request.
func (r *LeaseRequestRepository) GetPaymentByLeaseRequestID(ctx context.Context, leaseRequestID uuid.UUID) (*models.Payment, error) {
	var p models.Payment
	err := r.db.Pool.QueryRow(ctx, `
		SELECT id, lease_request_id, provider, stripe_customer_id, payment_intent_id, payment_intent_client_secret, amount, currency, platform_fee_amount, status, created_at, updated_at
		FROM payments WHERE lease_request_id = $1
	`, leaseRequestID).Scan(
		&p.ID, &p.LeaseRequestID, &p.Provider, &p.StripeCustomerID, &p.PaymentIntentID, &p.ClientSecret,
		&p.Amount, &p.Currency, &p.PlatformFeeAmount, &p.Status, &p.CreatedAt, &p.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil // No payment yet — not an error
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetPaymentByIntentID returns a payment by its Stripe PaymentIntent ID (for webhook handling).
func (r *LeaseRequestRepository) GetPaymentByIntentID(ctx context.Context, intentID string) (*models.Payment, error) {
	var p models.Payment
	err := r.db.Pool.QueryRow(ctx, `
		SELECT id, lease_request_id, provider, stripe_customer_id, payment_intent_id, payment_intent_client_secret, amount, currency, platform_fee_amount, status, created_at, updated_at
		FROM payments WHERE payment_intent_id = $1
	`, intentID).Scan(
		&p.ID, &p.LeaseRequestID, &p.Provider, &p.StripeCustomerID, &p.PaymentIntentID, &p.ClientSecret,
		&p.Amount, &p.Currency, &p.PlatformFeeAmount, &p.Status, &p.CreatedAt, &p.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, models.ErrPaymentNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// UpdatePaymentStatus updates a payment's status (idempotent).
func (r *LeaseRequestRepository) UpdatePaymentStatus(ctx context.Context, paymentID uuid.UUID, status models.PaymentStatus) error {
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE payments SET status = $2, updated_at = NOW() WHERE id = $1
	`, paymentID, status)
	return err
}

// isDuplicateKeyError checks for Postgres unique constraint violations.
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	return fmt.Sprintf("%v", err) != "" &&
		(contains(err.Error(), "duplicate key") || contains(err.Error(), "23505"))
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ────────────────────────────────────────────────────────────────────────────
// Pickup deadline + refund (migration 000024)
// ────────────────────────────────────────────────────────────────────────────

// SetPickupDeadline stamps the lease with a pickup_deadline_at value. Guarded
// so it only fires while the lease is in 'paid' state and the deadline has
// not already been set — repeated webhook/sync calls are no-ops, idempotently.
func (r *LeaseRequestRepository) SetPickupDeadline(ctx context.Context, id uuid.UUID, deadline time.Time) error {
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE lease_requests
		SET pickup_deadline_at = $2, updated_at = NOW()
		WHERE id = $1 AND status = 'paid' AND pickup_deadline_at IS NULL
	`, id, deadline)
	if err != nil {
		return fmt.Errorf("set pickup deadline: %w", err)
	}
	return nil
}

// ConfirmPickup marks pickup_confirmed_at = NOW(). Only the driver may call.
// Idempotent: if already confirmed, returns the current row without a second
// timestamp update. If the deadline has passed and the row is already in
// expired_refunded, returns ErrPickupDeadlinePassed.
func (r *LeaseRequestRepository) ConfirmPickup(ctx context.Context, id, driverID uuid.UUID) (*models.LeaseRequest, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	lr, err := scanFullLeaseLocked(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if lr.DriverID != driverID {
		return nil, models.NewAPIError(models.ErrCodeInvalidLeaseAction, "Only the driver can confirm pickup")
	}

	// Idempotent: already confirmed → return as-is.
	if lr.PickupConfirmedAt != nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return lr, nil
	}

	if lr.Status == models.LeaseStatusExpiredRefunded {
		return nil, models.ErrPickupDeadlinePassed
	}
	if lr.Status != models.LeaseStatusPaid {
		return nil, models.ErrInvalidLeaseAction
	}

	now := time.Now().UTC()
	if lr.PickupDeadlineAt != nil && now.After(*lr.PickupDeadlineAt) {
		// Deadline has technically passed — let the scanner refund instead of
		// confirming. We return the pre-existing 'paid' row so the caller can
		// surface a clear "deadline passed" message; the scanner's atomic
		// UPDATE will flip it on its next tick.
		return nil, models.ErrPickupDeadlinePassed
	}

	if _, err := tx.Exec(ctx, `
		UPDATE lease_requests
		SET pickup_confirmed_at = $2, updated_at = $2
		WHERE id = $1
	`, id, now); err != nil {
		return nil, fmt.Errorf("confirm pickup: %w", err)
	}
	lr.PickupConfirmedAt = &now
	lr.UpdatedAt = now

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return lr, nil
}

// ListStuckRefunds returns leases that were claimed for expiry (status moved
// to expired_refunded) but whose Stripe refund never finalized — typically
// because the worker crashed or the Stripe call timed out between
// ClaimForExpiry and FinalizeRefund. Rows older than `staleAfter` are
// surfaced so the next sweep can replay CreateRefund with the same stable
// idempotency key (`refund-<leaseID>`), which Stripe dedupes server-side.
//
// The query intentionally allows refund_status = 'failed' or NULL in
// addition to 'pending' so a transient Stripe 5xx from a previous attempt
// doesn't permanently park the row.
func (r *LeaseRequestRepository) ListStuckRefunds(ctx context.Context, staleAfter time.Time, limit int) ([]models.LeaseRequest, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.Pool.Query(ctx, `
		SELECT id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, offered_weekly_price, offered_price_updated_at, currency, weeks, message, expires_at, created_at, updated_at,
		       pickup_deadline_at, pickup_confirmed_at, refund_id, refunded_at, refund_status,
		       pickup_extension_total_minutes, pickup_extension_count, pickup_last_extended_at,
		       price_change_pending, previous_offered_weekly_price, price_change_acted_at
		FROM lease_requests
		WHERE status = 'expired_refunded'
		  AND refund_id   IS NULL
		  AND refunded_at IS NULL
		  AND (refund_status IS NULL OR refund_status IN ('pending', 'failed'))
		  AND updated_at <= $1
		ORDER BY updated_at ASC
		LIMIT $2
	`, staleAfter, limit)
	if err != nil {
		return nil, fmt.Errorf("list stuck refunds: %w", err)
	}
	defer rows.Close()

	out := []models.LeaseRequest{}
	for rows.Next() {
		var lr models.LeaseRequest
		if err := rows.Scan(
			&lr.ID, &lr.ChatID, &lr.ListingID, &lr.OwnerID, &lr.DriverID,
			&lr.Status, &lr.WeeklyPrice, &lr.OfferedWeeklyPrice, &lr.OfferedPriceUpdatedAt, &lr.Currency, &lr.Weeks, &lr.Message,
			&lr.ExpiresAt, &lr.CreatedAt, &lr.UpdatedAt,
			&lr.PickupDeadlineAt, &lr.PickupConfirmedAt, &lr.RefundID, &lr.RefundedAt, &lr.RefundStatus,
			&lr.PickupExtensionTotalMinutes, &lr.PickupExtensionCount, &lr.PickupLastExtendedAt,
			&lr.PriceChangePending, &lr.PreviousOfferedWeeklyPrice, &lr.PriceChangeActedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, lr)
	}
	return out, nil
}

// ListExpiredAwaitingPickup returns leases whose pickup deadline has passed
// without a driver confirmation. Used by the background expiry worker.
func (r *LeaseRequestRepository) ListExpiredAwaitingPickup(ctx context.Context, now time.Time, limit int) ([]models.LeaseRequest, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.Pool.Query(ctx, `
		SELECT id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, offered_weekly_price, offered_price_updated_at, currency, weeks, message, expires_at, created_at, updated_at,
		       pickup_deadline_at, pickup_confirmed_at, refund_id, refunded_at, refund_status,
		       pickup_extension_total_minutes, pickup_extension_count, pickup_last_extended_at,
		       price_change_pending, previous_offered_weekly_price, price_change_acted_at
		FROM lease_requests
		WHERE status = 'paid'
		  AND pickup_confirmed_at IS NULL
		  AND pickup_deadline_at IS NOT NULL
		  AND pickup_deadline_at <= $1
		ORDER BY pickup_deadline_at ASC
		LIMIT $2
	`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list expired awaiting pickup: %w", err)
	}
	defer rows.Close()

	out := []models.LeaseRequest{}
	for rows.Next() {
		var lr models.LeaseRequest
		if err := rows.Scan(
			&lr.ID, &lr.ChatID, &lr.ListingID, &lr.OwnerID, &lr.DriverID,
			&lr.Status, &lr.WeeklyPrice, &lr.OfferedWeeklyPrice, &lr.OfferedPriceUpdatedAt, &lr.Currency, &lr.Weeks, &lr.Message,
			&lr.ExpiresAt, &lr.CreatedAt, &lr.UpdatedAt,
			&lr.PickupDeadlineAt, &lr.PickupConfirmedAt, &lr.RefundID, &lr.RefundedAt, &lr.RefundStatus,
			&lr.PickupExtensionTotalMinutes, &lr.PickupExtensionCount, &lr.PickupLastExtendedAt,
			&lr.PriceChangePending, &lr.PreviousOfferedWeeklyPrice, &lr.PriceChangeActedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, lr)
	}
	return out, nil
}

// ClaimForExpiry atomically transitions a single lease from paid →
// expired_refunded so two workers cannot double-refund. Returns the updated
// row on success or pgx.ErrNoRows if someone else already claimed it (or the
// driver just confirmed). Caller then performs the Stripe refund and persists
// the result via FinalizeRefund.
func (r *LeaseRequestRepository) ClaimForExpiry(ctx context.Context, id uuid.UUID) (*models.LeaseRequest, error) {
	var lr models.LeaseRequest
	err := r.db.Pool.QueryRow(ctx, `
		UPDATE lease_requests
		SET status = $2, refund_status = 'pending', updated_at = NOW()
		WHERE id = $1
		  AND status = 'paid'
		  AND pickup_confirmed_at IS NULL
		  AND pickup_deadline_at IS NOT NULL
		  AND pickup_deadline_at <= NOW()
		RETURNING id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, offered_weekly_price, offered_price_updated_at, currency, weeks, message, expires_at, created_at, updated_at,
		          pickup_deadline_at, pickup_confirmed_at, refund_id, refunded_at, refund_status,
		          pickup_extension_total_minutes, pickup_extension_count, pickup_last_extended_at,
		          price_change_pending, previous_offered_weekly_price, price_change_acted_at
	`, id, models.LeaseStatusExpiredRefunded).Scan(
		&lr.ID, &lr.ChatID, &lr.ListingID, &lr.OwnerID, &lr.DriverID,
		&lr.Status, &lr.WeeklyPrice, &lr.OfferedWeeklyPrice, &lr.OfferedPriceUpdatedAt, &lr.Currency, &lr.Weeks, &lr.Message,
		&lr.ExpiresAt, &lr.CreatedAt, &lr.UpdatedAt,
		&lr.PickupDeadlineAt, &lr.PickupConfirmedAt, &lr.RefundID, &lr.RefundedAt, &lr.RefundStatus,
		&lr.PickupExtensionTotalMinutes, &lr.PickupExtensionCount, &lr.PickupLastExtendedAt,
		&lr.PriceChangePending, &lr.PreviousOfferedWeeklyPrice, &lr.PriceChangeActedAt,
	)
	if err != nil {
		return nil, err
	}
	// Release the car listing in the same lifecycle step — the claim has
	// committed regardless of this side-effect; doing it now keeps discovery
	// in sync even if the subsequent FinalizeRefund call fails (we still
	// don't want other drivers blocked by a deadline-busted reservation).
	r.unreserveCarIfHeldBy(ctx, lr.ID)
	return &lr, nil
}

// FinalizeRefund persists the outcome of the Stripe Refund API call for a
// lease that was previously claimed via ClaimForExpiry. status='succeeded'
// records the refund_id; status='failed' leaves refund_id NULL so the worker
// can retry on a future tick.
func (r *LeaseRequestRepository) FinalizeRefund(ctx context.Context, id uuid.UUID, refundID string, status models.RefundStatus) error {
	now := time.Now().UTC()
	if status == models.RefundStatusSucceeded {
		_, err := r.db.Pool.Exec(ctx, `
			UPDATE lease_requests
			SET refund_id = $2, refunded_at = $3, refund_status = $4, updated_at = $3
			WHERE id = $1
		`, id, refundID, now, string(status))
		return err
	}
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE lease_requests
		SET refund_status = $2, updated_at = $3
		WHERE id = $1
	`, id, string(status), now)
	return err
}

// ExtendPickupDeadline is the owner-initiated "Add more time" action.
//
// The whole operation is a single guarded UPDATE that is the serialization
// point against the expiry scanner: it only succeeds when the row is still
// in a pre-claim state (status='paid', not refunded, not confirmed) and the
// existing deadline has not yet elapsed. Because ClaimForExpiry uses the
// same predicate (status='paid' AND pickup_confirmed_at IS NULL AND
// pickup_deadline_at <= NOW()), at most one of {extend, claim} can change
// the row — whoever updates first commits, the other sees zero affected
// rows. The total-minutes cap is enforced inline so we don't need a
// separate read-then-write.
//
// On zero rows affected we do one follow-up read to classify the failure
// (refunded? confirmed? cap reached?) and surface a clean API error.
func (r *LeaseRequestRepository) ExtendPickupDeadline(
	ctx context.Context,
	id, ownerID uuid.UUID,
	minutes int,
) (*models.LeaseRequest, error) {
	if !models.IsAllowedPickupExtensionMinutes(minutes) {
		return nil, models.ErrInvalidExtensionMin
	}

	now := time.Now().UTC()
	var lr models.LeaseRequest
	err := r.db.Pool.QueryRow(ctx, `
		UPDATE lease_requests
		SET pickup_deadline_at              = pickup_deadline_at + (make_interval(mins => $3)),
		    pickup_extension_total_minutes  = pickup_extension_total_minutes + $3,
		    pickup_extension_count          = pickup_extension_count + 1,
		    pickup_last_extended_at         = $4,
		    updated_at                      = $4
		WHERE id = $1
		  AND owner_id = $2
		  AND status = 'paid'
		  AND pickup_confirmed_at IS NULL
		  AND pickup_deadline_at IS NOT NULL
		  AND pickup_deadline_at > $4
		  AND refund_status IS NULL
		  AND refunded_at   IS NULL
		  AND pickup_extension_total_minutes + $3 <= $5
		RETURNING id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, offered_weekly_price, offered_price_updated_at, currency, weeks, message, expires_at, created_at, updated_at,
		          pickup_deadline_at, pickup_confirmed_at, refund_id, refunded_at, refund_status,
		          pickup_extension_total_minutes, pickup_extension_count, pickup_last_extended_at,
		          price_change_pending, previous_offered_weekly_price, price_change_acted_at
	`, id, ownerID, minutes, now, models.PickupMaxExtensionMinutes).Scan(
		&lr.ID, &lr.ChatID, &lr.ListingID, &lr.OwnerID, &lr.DriverID,
		&lr.Status, &lr.WeeklyPrice, &lr.OfferedWeeklyPrice, &lr.OfferedPriceUpdatedAt, &lr.Currency, &lr.Weeks, &lr.Message,
		&lr.ExpiresAt, &lr.CreatedAt, &lr.UpdatedAt,
		&lr.PickupDeadlineAt, &lr.PickupConfirmedAt, &lr.RefundID, &lr.RefundedAt, &lr.RefundStatus,
		&lr.PickupExtensionTotalMinutes, &lr.PickupExtensionCount, &lr.PickupLastExtendedAt,
		&lr.PriceChangePending, &lr.PreviousOfferedWeeklyPrice, &lr.PriceChangeActedAt,
	)
	if err == nil {
		return &lr, nil
	}
	if err != pgx.ErrNoRows {
		return nil, fmt.Errorf("extend pickup deadline: %w", err)
	}
	return nil, r.classifyExtendFailure(ctx, id, ownerID, minutes)
}

// classifyExtendFailure runs after a zero-row UPDATE to turn the silent
// guard-failure into a precise API error.
func (r *LeaseRequestRepository) classifyExtendFailure(
	ctx context.Context,
	id, ownerID uuid.UUID,
	minutes int,
) error {
	existing, err := r.GetByID(ctx, id)
	if err != nil {
		// Not found is already wrapped as ErrLeaseRequestNotFound.
		return err
	}
	if existing.OwnerID != ownerID {
		return models.NewAPIError(models.ErrCodeInvalidLeaseAction,
			"Only the car owner can extend the pickup deadline")
	}
	if existing.Status == models.LeaseStatusExpiredRefunded ||
		existing.RefundedAt != nil ||
		existing.RefundStatus != nil {
		return models.ErrPickupDeadlinePassed
	}
	if existing.PickupConfirmedAt != nil {
		return models.NewAPIError(models.ErrCodeInvalidLeaseAction,
			"Pickup is already confirmed; nothing to extend")
	}
	if existing.Status != models.LeaseStatusPaid || existing.PickupDeadlineAt == nil {
		return models.ErrInvalidLeaseAction
	}
	now := time.Now().UTC()
	if !existing.PickupDeadlineAt.After(now) {
		// Deadline elapsed — the scanner will (or already did) claim it.
		return models.ErrPickupDeadlinePassed
	}
	if existing.PickupExtensionTotalMinutes+minutes > models.PickupMaxExtensionMinutes {
		return models.ErrPickupExtensionCap
	}
	// Shouldn't reach here, but fall back to a generic guard rejection so the
	// caller still gets an APIError shape instead of a raw 500.
	return models.ErrInvalidLeaseAction
}

// scanFullLeaseLocked SELECT ... FOR UPDATE with the full lease columns
// (including the migration-000024 pickup + refund fields AND the
// migration-000028 price-review fields). Used by ConfirmPickup;
// ErrLeaseRequestNotFound on miss.
func scanFullLeaseLocked(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*models.LeaseRequest, error) {
	var lr models.LeaseRequest
	err := tx.QueryRow(ctx, `
		SELECT id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, offered_weekly_price, offered_price_updated_at, currency, weeks, message, expires_at, created_at, updated_at,
		       pickup_deadline_at, pickup_confirmed_at, refund_id, refunded_at, refund_status,
		       pickup_extension_total_minutes, pickup_extension_count, pickup_last_extended_at,
		       price_change_pending, previous_offered_weekly_price, price_change_acted_at
		FROM lease_requests
		WHERE id = $1
		FOR UPDATE
	`, id).Scan(
		&lr.ID, &lr.ChatID, &lr.ListingID, &lr.OwnerID, &lr.DriverID,
		&lr.Status, &lr.WeeklyPrice, &lr.OfferedWeeklyPrice, &lr.OfferedPriceUpdatedAt, &lr.Currency, &lr.Weeks, &lr.Message,
		&lr.ExpiresAt, &lr.CreatedAt, &lr.UpdatedAt,
		&lr.PickupDeadlineAt, &lr.PickupConfirmedAt, &lr.RefundID, &lr.RefundedAt, &lr.RefundStatus,
		&lr.PickupExtensionTotalMinutes, &lr.PickupExtensionCount, &lr.PickupLastExtendedAt,
		&lr.PriceChangePending, &lr.PreviousOfferedWeeklyPrice, &lr.PriceChangeActedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, models.ErrLeaseRequestNotFound
	}
	if err != nil {
		return nil, err
	}
	return &lr, nil
}
