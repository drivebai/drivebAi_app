package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
)

// PurchaseRequestRepository persists the buy-the-car state machine, the
// Bill of Sale satellite, and the buyer-rejection + evidence satellites.
// Every mutating helper follows the lease/vehicle-return pattern:
// SELECT ... FOR UPDATE inside a TX (or a guarded WHERE clause) so
// double-taps and retries are idempotent (see DESIGN SPEC §1.5).
type PurchaseRequestRepository struct {
	db *database.DB
}

func NewPurchaseRequestRepository(db *database.DB) *PurchaseRequestRepository {
	return &PurchaseRequestRepository{db: db}
}

const purchaseRequestColumns = `
	id, car_id, seller_id, buyer_id, chat_id,
	offer_amount_cents, currency, buyer_message,
	status, expires_at, auth_expires_at,
	handover_location, handover_latitude, handover_longitude,
	handover_scheduled_at, keys_handed_over_at, inspection_deadline_at, inspection_accepted_at, completed_at,
	payment_intent_id, payment_status, refund_status, refund_id, refunded_at, refund_failure_reason,
	cancellation_reason,
	created_at, updated_at`

func scanPurchaseRequest(row scanRow) (*models.PurchaseRequest, error) {
	var p models.PurchaseRequest
	var paymentStatus sql.NullString
	var refundStatus sql.NullString
	err := row.Scan(
		&p.ID, &p.CarID, &p.SellerID, &p.BuyerID, &p.ChatID,
		&p.OfferAmountCents, &p.Currency, &p.BuyerMessage,
		&p.Status, &p.ExpiresAt, &p.AuthExpiresAt,
		&p.HandoverLocation, &p.HandoverLatitude, &p.HandoverLongitude,
		&p.HandoverScheduledAt, &p.KeysHandedOverAt, &p.InspectionDeadlineAt, &p.InspectionAcceptedAt, &p.CompletedAt,
		&p.PaymentIntentID, &paymentStatus, &refundStatus, &p.RefundID, &p.RefundedAt, &p.RefundFailureReason,
		&p.CancellationReason,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if paymentStatus.Valid {
		ps := models.PaymentStatus(paymentStatus.String)
		p.PaymentStatus = &ps
	}
	if refundStatus.Valid {
		rs := models.VehicleReturnRefundStatus(refundStatus.String)
		p.RefundStatus = &rs
	}
	return &p, nil
}

// ─── Create ─────────────────────────────────────────────────────────────────

// CreatePurchaseRequestParams is the immutable input the handler passes to
// CreateForCar. Keeps the constructor honest about which fields are required.
type CreatePurchaseRequestParams struct {
	CarID            uuid.UUID
	SellerID         uuid.UUID
	BuyerID          uuid.UUID
	ChatID           uuid.UUID
	OfferAmountCents int64
	Currency         string
	BuyerMessage     *string
	ExpiresAt        time.Time
}

// CreateForCar inserts a fresh `requested` purchase row. The unique partial
// index on (car_id, buyer_id) enforces "one non-terminal per buyer" — a
// duplicate raises 23505 which the handler maps to
// ErrDuplicatePurchase.
func (r *PurchaseRequestRepository) CreateForCar(ctx context.Context, p CreatePurchaseRequestParams) (*models.PurchaseRequest, error) {
	row := r.db.Pool.QueryRow(ctx, `
		INSERT INTO purchase_requests
			(id, car_id, seller_id, buyer_id, chat_id,
			 offer_amount_cents, currency, buyer_message,
			 status, expires_at,
			 created_at, updated_at)
		VALUES
			(gen_random_uuid(), $1, $2, $3, $4,
			 $5, $6, $7,
			 'requested', $8,
			 NOW(), NOW())
		RETURNING `+purchaseRequestColumns,
		p.CarID, p.SellerID, p.BuyerID, p.ChatID,
		p.OfferAmountCents, p.Currency, p.BuyerMessage,
		p.ExpiresAt,
	)
	created, err := scanPurchaseRequest(row)
	if err != nil {
		if isDuplicateKeyError(err) {
			return nil, models.ErrDuplicatePurchase
		}
		return nil, fmt.Errorf("insert purchase request: %w", err)
	}
	return created, nil
}

// ─── Simple reads ───────────────────────────────────────────────────────────

// GetByID returns any purchase row by id.
func (r *PurchaseRequestRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.PurchaseRequest, error) {
	row := r.db.Pool.QueryRow(ctx,
		`SELECT `+purchaseRequestColumns+` FROM purchase_requests WHERE id = $1`, id)
	p, err := scanPurchaseRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrPurchaseRequestNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// GetByIDForUser returns the row only when the user is a participant
// (seller or buyer). Non-participants get NotFound to avoid leaking row
// existence.
func (r *PurchaseRequestRepository) GetByIDForUser(ctx context.Context, id, userID uuid.UUID) (*models.PurchaseRequest, error) {
	row := r.db.Pool.QueryRow(ctx,
		`SELECT `+purchaseRequestColumns+` FROM purchase_requests
		  WHERE id = $1 AND (seller_id = $2 OR buyer_id = $2)`, id, userID)
	p, err := scanPurchaseRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrPurchaseRequestNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// GetActiveByCarAndBuyer returns the buyer's non-terminal offer for the
// given car, if any. Used pre-create for a friendly DUPLICATE_ACTIVE_REQUEST
// error before the unique-index 23505 fires.
func (r *PurchaseRequestRepository) GetActiveByCarAndBuyer(ctx context.Context, carID, buyerID uuid.UUID) (*models.PurchaseRequest, error) {
	row := r.db.Pool.QueryRow(ctx, `
		SELECT `+purchaseRequestColumns+`
		FROM purchase_requests
		WHERE car_id = $1 AND buyer_id = $2
		  AND status IN (
		    'requested','accepted','bos_pending_seller','bos_pending_buyer','bos_signed',
		    'payment_authorized','handover_scheduled','awaiting_inspection',
		    'inspection_accepted','inspection_rejected'
		  )
		LIMIT 1`, carID, buyerID)
	p, err := scanPurchaseRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// GetByPaymentIntentID returns the row whose Stripe intent id matches.
// Used by the webhook handler.
func (r *PurchaseRequestRepository) GetByPaymentIntentID(ctx context.Context, intentID string) (*models.PurchaseRequest, error) {
	row := r.db.Pool.QueryRow(ctx,
		`SELECT `+purchaseRequestColumns+` FROM purchase_requests WHERE payment_intent_id = $1`, intentID)
	p, err := scanPurchaseRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrPurchaseRequestNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ListForChat returns every purchase for a chat, newest first.
func (r *PurchaseRequestRepository) ListForChat(ctx context.Context, chatID uuid.UUID) ([]models.PurchaseRequest, error) {
	rows, err := r.db.Pool.Query(ctx,
		`SELECT `+purchaseRequestColumns+`
		 FROM purchase_requests
		 WHERE chat_id = $1
		 ORDER BY created_at DESC`, chatID)
	if err != nil {
		return nil, fmt.Errorf("list purchase requests for chat: %w", err)
	}
	defer rows.Close()

	out := []models.PurchaseRequest{}
	for rows.Next() {
		p, err := scanPurchaseRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, nil
}

// ListActiveForUser returns non-terminal (plus a 15-minute grace on terminal)
// purchase rows where the user is a participant. Fuels Today aggregation
// for both roles.
func (r *PurchaseRequestRepository) ListActiveForUser(ctx context.Context, userID uuid.UUID) ([]models.PurchaseRequest, error) {
	rows, err := r.db.Pool.Query(ctx,
		`SELECT `+purchaseRequestColumns+`
		 FROM purchase_requests
		 WHERE (seller_id = $1 OR buyer_id = $1)
		   AND (
		         status NOT IN ('completed','rejected_refunded','rejected_upheld','declined','cancelled','expired','expired_auth')
		      OR (status IN ('completed','rejected_refunded','rejected_upheld') AND completed_at >= NOW() - INTERVAL '15 minutes')
		   )
		 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list active purchases: %w", err)
	}
	defer rows.Close()

	out := []models.PurchaseRequest{}
	for rows.Next() {
		p, err := scanPurchaseRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, nil
}

// ─── State transitions ──────────────────────────────────────────────────────

// updateStatus is the guarded, TX-scoped one-shot used by every simple
// transition. Returns ErrInvalidPurchaseAction when no row matched the
// pre-condition (either wrong actor or unexpected state).
func (r *PurchaseRequestRepository) updateStatus(ctx context.Context, id uuid.UUID, fromStatuses []models.PurchaseRequestStatus, toStatus models.PurchaseRequestStatus, extraSet string, extraArgs []interface{}) (*models.PurchaseRequest, error) {
	if len(fromStatuses) == 0 {
		return nil, fmt.Errorf("purchase updateStatus: from-status required")
	}
	fromArgs := make([]string, 0, len(fromStatuses))
	args := []interface{}{id, string(toStatus)}
	for i, s := range fromStatuses {
		fromArgs = append(fromArgs, fmt.Sprintf("$%d", 3+i))
		args = append(args, string(s))
	}
	setExtra := ""
	if extraSet != "" {
		setExtra = ", " + extraSet
	}
	// Renumber extra args to continue after the fromStatus placeholders.
	baseArg := len(args) + 1
	_ = baseArg // extraArgs are already numbered by the caller via %s templating below
	q := fmt.Sprintf(`
		UPDATE purchase_requests
		SET status = $2%s, updated_at = NOW()
		WHERE id = $1 AND status IN (%s)
		RETURNING `+purchaseRequestColumns,
		setExtra, joinComma(fromArgs))

	allArgs := append(args, extraArgs...)
	row := r.db.Pool.QueryRow(ctx, q, allArgs...)
	p, err := scanPurchaseRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrInvalidPurchaseAction
	}
	if err != nil {
		return nil, fmt.Errorf("update purchase status: %w", err)
	}
	return p, nil
}

func joinComma(items []string) string {
	out := ""
	for i, it := range items {
		if i > 0 {
			out += ", "
		}
		out += it
	}
	return out
}

// AcceptOffer: seller accepts. Also inserts the initial Bill of Sale row
// pre-filled from the car and offer amount so both parties can start
// editing/signing immediately. Guarded to `requested` only.
func (r *PurchaseRequestRepository) AcceptOffer(ctx context.Context, id, sellerID uuid.UUID, bosSeed BillOfSaleSeed) (*models.PurchaseRequest, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		UPDATE purchase_requests
		SET status = 'accepted', updated_at = NOW()
		WHERE id = $1 AND seller_id = $2 AND status = 'requested'
		RETURNING `+purchaseRequestColumns, id, sellerID)
	p, err := scanPurchaseRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrInvalidPurchaseAction
	}
	if err != nil {
		return nil, fmt.Errorf("accept purchase offer: %w", err)
	}

	// Insert the BoS row idempotently. If one already exists (retry path)
	// we leave it alone — sellers only get one shot at pre-filling.
	//
	// terms_conditions is inserted explicitly (not relying on the migration
	// column default) so that any drift between DB default and Go constant
	// is impossible — the Review step always renders a concrete value.
	terms := bosSeed.TermsConditions
	if strings.TrimSpace(terms) == "" {
		terms = models.DefaultBOSTerms
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO purchase_bill_of_sales
			(id, purchase_request_id,
			 vehicle_year, vehicle_make, vehicle_model, vin,
			 sale_amount_cents, currency, terms_conditions,
			 seller_name, seller_address,
			 buyer_name, buyer_address,
			 created_at, updated_at)
		VALUES
			(gen_random_uuid(), $1,
			 $2, $3, $4, $5,
			 $6, $7, $8,
			 $9, $10,
			 $11, $12,
			 NOW(), NOW())
		ON CONFLICT (purchase_request_id) DO NOTHING
	`, p.ID,
		bosSeed.VehicleYear, bosSeed.VehicleMake, bosSeed.VehicleModel, bosSeed.VIN,
		bosSeed.SaleAmountCents, bosSeed.Currency, terms,
		bosSeed.SellerName, bosSeed.SellerAddress,
		bosSeed.BuyerName, bosSeed.BuyerAddress,
	); err != nil {
		return nil, fmt.Errorf("seed bos: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

// BillOfSaleSeed carries the pre-fill values written on `accept`.
//
// TermsConditions is populated by the handler with models.DefaultBOSTerms
// so that the seeded row always has a concrete disclaimer string — no
// silent fallback to the DB column default and no `—` in the wizard's
// Review step.
type BillOfSaleSeed struct {
	VehicleYear     int
	VehicleMake     string
	VehicleModel    string
	VIN             string
	SaleAmountCents int64
	Currency        string
	TermsConditions string
	SellerName      string
	SellerAddress   string
	BuyerName       string
	BuyerAddress    string
}

// DeclineOffer: seller declines a `requested` row.
func (r *PurchaseRequestRepository) DeclineOffer(ctx context.Context, id, sellerID uuid.UUID, reason *string) (*models.PurchaseRequest, error) {
	row := r.db.Pool.QueryRow(ctx, `
		UPDATE purchase_requests
		SET status = 'declined', cancellation_reason = $3, updated_at = NOW()
		WHERE id = $1 AND seller_id = $2 AND status = 'requested'
		RETURNING `+purchaseRequestColumns, id, sellerID, reason)
	p, err := scanPurchaseRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrInvalidPurchaseAction
	}
	if err != nil {
		return nil, fmt.Errorf("decline purchase: %w", err)
	}
	return p, nil
}

// CancelOffer: buyer withdraws before payment authorization. Allowed pre-BoS,
// pre-payment (mirrors ChoosePaymentOption UX where "Cancel" is only visible
// while status ∈ {requested, accepted, bos_pending_*, bos_signed}).
func (r *PurchaseRequestRepository) CancelOffer(ctx context.Context, id, buyerID uuid.UUID) (*models.PurchaseRequest, error) {
	row := r.db.Pool.QueryRow(ctx, `
		UPDATE purchase_requests
		SET status = 'cancelled', updated_at = NOW()
		WHERE id = $1 AND buyer_id = $2
		  AND status IN ('requested','accepted','bos_pending_seller','bos_pending_buyer','bos_signed')
		RETURNING `+purchaseRequestColumns, id, buyerID)
	p, err := scanPurchaseRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrPurchaseNotCancellable
	}
	if err != nil {
		return nil, fmt.Errorf("cancel purchase: %w", err)
	}
	return p, nil
}

// ExpireIfStale flips a `requested` row past its ExpiresAt to `expired`.
// Called by the lazy-expiry scanner. Returns ErrInvalidPurchaseAction when
// nothing was ready to expire (idempotent no-op).
func (r *PurchaseRequestRepository) ExpireIfStale(ctx context.Context, id uuid.UUID) (*models.PurchaseRequest, error) {
	row := r.db.Pool.QueryRow(ctx, `
		UPDATE purchase_requests
		SET status = 'expired', updated_at = NOW()
		WHERE id = $1 AND status = 'requested' AND expires_at <= NOW()
		RETURNING `+purchaseRequestColumns, id)
	p, err := scanPurchaseRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrInvalidPurchaseAction
	}
	if err != nil {
		return nil, fmt.Errorf("expire purchase: %w", err)
	}
	return p, nil
}

// ─── Bill of Sale ────────────────────────────────────────────────────────

// GetBillOfSale returns the BoS row for a purchase, if any.
func (r *PurchaseRequestRepository) GetBillOfSale(ctx context.Context, purchaseID uuid.UUID) (*models.PurchaseBillOfSale, error) {
	row := r.db.Pool.QueryRow(ctx, `
		SELECT id, purchase_request_id,
		       vehicle_year, vehicle_make, vehicle_model, vin,
		       sale_amount_cents, currency,
		       terms_conditions,
		       seller_name, seller_address, seller_signature_url, seller_signed_at,
		       buyer_name, buyer_address, buyer_signature_url, buyer_signed_at,
		       finalized_pdf_url, finalized_at,
		       created_at, updated_at
		FROM purchase_bill_of_sales
		WHERE purchase_request_id = $1`, purchaseID)
	var b models.PurchaseBillOfSale
	if err := row.Scan(
		&b.ID, &b.PurchaseRequestID,
		&b.VehicleYear, &b.VehicleMake, &b.VehicleModel, &b.VIN,
		&b.SaleAmountCents, &b.Currency,
		&b.TermsConditions,
		&b.SellerName, &b.SellerAddress, &b.SellerSignatureURL, &b.SellerSignedAt,
		&b.BuyerName, &b.BuyerAddress, &b.BuyerSignatureURL, &b.BuyerSignedAt,
		&b.FinalizedPDFURL, &b.FinalizedAt,
		&b.CreatedAt, &b.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &b, nil
}

// SetFinalizedPDF stamps the finalized Bill-of-Sale PDF path onto the row,
// but ONLY when finalized_pdf_url is still NULL. This is the idempotency
// guard for the finalize flow: whether the write comes from the async
// SignBOS goroutine or a manual retry, whoever wins the race sets the
// column; every later caller is a no-op (rowsAffected == 0). The path is
// deterministic (one file per purchase), so any concurrent identical file
// write overwrites the same bytes at the same path harmlessly.
//
// The stored value MUST be the BARE relative /uploads/... path — never a
// signed URL. buildBOSResponse signs it on the way out.
//
// Returns true when this call performed the write (was the winner).
func (r *PurchaseRequestRepository) SetFinalizedPDF(ctx context.Context, purchaseID uuid.UUID, relativeURL string) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE purchase_bill_of_sales
		SET finalized_pdf_url = $2, finalized_at = NOW(), updated_at = NOW()
		WHERE purchase_request_id = $1 AND finalized_pdf_url IS NULL
	`, purchaseID, relativeURL)
	if err != nil {
		return false, fmt.Errorf("set finalized pdf: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// validateNonBlankPatch returns a 400 APIError when the caller sent an
// explicit empty (or all-whitespace) string for a field that must remain
// populated. nil pointers (field omitted from JSON) are always allowed.
//
// This is the last line of defence against the iOS "send-all-fields-every-
// time" pattern — if the wizard @State happens to be empty when the user
// taps Save (e.g. a fresh open before rehydration lands), the API rejects
// it instead of silently clobbering the seeded value with `''`.
func validateNonBlankPatch(field string, p *string) *models.APIError {
	if p == nil {
		return nil
	}
	if strings.TrimSpace(*p) == "" {
		return models.NewValidationError(field + " cannot be blank")
	}
	return nil
}

// UpdateBillOfSaleFields is the seller-side PATCH for the "vehicle" and
// seller identity fields. Guarded so it fails with BOS_LOCKED (self-locked
// variant) once the SELLER has signed — buyer's signature does NOT block
// seller edits, because the seller's block is still editable until they
// sign it themselves. The UPDATE SQL touches only columns owned by the
// seller/vehicle side; no implicit clearing of buyer fields.
func (r *PurchaseRequestRepository) UpdateBillOfSaleFields(ctx context.Context, purchaseID uuid.UUID, patch models.UpdateBOSBody) (*models.PurchaseBillOfSale, error) {
	// Reject explicit-empty patches for required strings before we hit the
	// DB. Addresses may be `""` (user intentionally clears); name/make/
	// model/vin/terms are load-bearing on the printed BoS.
	if apiErr := validateNonBlankPatch("vehicle_make", patch.VehicleMake); apiErr != nil {
		return nil, apiErr
	}
	if apiErr := validateNonBlankPatch("vehicle_model", patch.VehicleModel); apiErr != nil {
		return nil, apiErr
	}
	if apiErr := validateNonBlankPatch("vin", patch.VIN); apiErr != nil {
		return nil, apiErr
	}
	if apiErr := validateNonBlankPatch("seller_name", patch.SellerName); apiErr != nil {
		return nil, apiErr
	}
	if apiErr := validateNonBlankPatch("terms_conditions", patch.TermsConditions); apiErr != nil {
		return nil, apiErr
	}

	// Fetch first to check lock. Cheap (indexed).
	b, err := r.GetBillOfSale(ctx, purchaseID)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, models.ErrInvalidPurchaseAction
	}
	// Seller-fields lock: only the seller's own signature blocks this
	// endpoint. The buyer signing MUST NOT lock the seller out — before
	// this fix the OR-guard cross-blocked the wrong party.
	if b.SellerSigned() {
		return nil, models.ErrBOSSelfLocked
	}

	// Build a dynamic SET clause so we only write the columns the caller
	// actually patched. This prevents a stale @State from re-writing a
	// column the user hasn't touched, and keeps the UPDATE role-scoped
	// (buyer columns are never mentioned by this endpoint).
	sets := []string{}
	args := []interface{}{purchaseID}
	next := 2
	if patch.VehicleYear != nil {
		sets = append(sets, fmt.Sprintf("vehicle_year = $%d", next))
		args = append(args, *patch.VehicleYear)
		next++
	}
	if patch.VehicleMake != nil {
		sets = append(sets, fmt.Sprintf("vehicle_make = $%d", next))
		args = append(args, *patch.VehicleMake)
		next++
	}
	if patch.VehicleModel != nil {
		sets = append(sets, fmt.Sprintf("vehicle_model = $%d", next))
		args = append(args, *patch.VehicleModel)
		next++
	}
	if patch.VIN != nil {
		sets = append(sets, fmt.Sprintf("vin = $%d", next))
		args = append(args, *patch.VIN)
		next++
	}
	// sale_amount_cents intentionally NOT patchable — it's seeded from
	// purchase_requests.offer_amount_cents and must never diverge from
	// the amount CreatePaymentIntent actually charges.
	if patch.TermsConditions != nil {
		sets = append(sets, fmt.Sprintf("terms_conditions = $%d", next))
		args = append(args, *patch.TermsConditions)
		next++
	}
	if patch.SellerName != nil {
		sets = append(sets, fmt.Sprintf("seller_name = $%d", next))
		args = append(args, *patch.SellerName)
		next++
	}
	if patch.SellerAddress != nil {
		sets = append(sets, fmt.Sprintf("seller_address = $%d", next))
		args = append(args, *patch.SellerAddress)
		next++
	}
	if len(sets) == 0 {
		// Nothing to write — return the current row as a friendly no-op.
		return b, nil
	}
	q := "UPDATE purchase_bill_of_sales SET " + strings.Join(sets, ", ") +
		", updated_at = NOW() WHERE purchase_request_id = $1"
	if _, err := r.db.Pool.Exec(ctx, q, args...); err != nil {
		return nil, fmt.Errorf("update bos: %w", err)
	}
	return r.GetBillOfSale(ctx, purchaseID)
}

// UpdateBillOfSaleBuyerFields is the buyer-owned identity PATCH.
//
// Symmetric to UpdateBillOfSaleFields: only the BUYER's own signature
// blocks this endpoint. The seller signing MUST NOT lock the buyer out —
// pre-signature identity edits are a per-role concern.
// The UPDATE SQL touches only buyer_* columns; the vehicle / seller
// block is untouched.
func (r *PurchaseRequestRepository) UpdateBillOfSaleBuyerFields(ctx context.Context, purchaseID uuid.UUID, patch models.UpdateBOSBuyerFieldsBody) (*models.PurchaseBillOfSale, error) {
	if apiErr := validateNonBlankPatch("buyer_name", patch.BuyerName); apiErr != nil {
		return nil, apiErr
	}

	b, err := r.GetBillOfSale(ctx, purchaseID)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, models.ErrInvalidPurchaseAction
	}
	if b.BuyerSigned() {
		return nil, models.ErrBOSSelfLocked
	}

	sets := []string{}
	args := []interface{}{purchaseID}
	next := 2
	if patch.BuyerName != nil {
		sets = append(sets, fmt.Sprintf("buyer_name = $%d", next))
		args = append(args, *patch.BuyerName)
		next++
	}
	if patch.BuyerAddress != nil {
		sets = append(sets, fmt.Sprintf("buyer_address = $%d", next))
		args = append(args, *patch.BuyerAddress)
		next++
	}
	if len(sets) == 0 {
		return b, nil
	}
	q := "UPDATE purchase_bill_of_sales SET " + strings.Join(sets, ", ") +
		", updated_at = NOW() WHERE purchase_request_id = $1"
	if _, err := r.db.Pool.Exec(ctx, q, args...); err != nil {
		return nil, fmt.Errorf("update bos buyer fields: %w", err)
	}
	return r.GetBillOfSale(ctx, purchaseID)
}

// MarkSignature records the signature URL for a role and, when both are
// present, transitions the purchase_requests row appropriately:
//
//	first signer:  accepted → bos_pending_{other}
//	second signer: bos_pending_* → bos_signed
//
// Returns (bos, purchase, alreadySigned, err).  alreadySigned=true when
// this role's signature was already on file (repeat call), letting the
// handler return 200 with the existing state instead of surfacing a 409.
func (r *PurchaseRequestRepository) MarkSignature(ctx context.Context, purchaseID uuid.UUID, role string, signatureURL string) (*models.PurchaseBillOfSale, *models.PurchaseRequest, bool, error) {
	if role != "seller" && role != "buyer" {
		return nil, nil, false, models.ErrInvalidRoleField
	}
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, false, err
	}
	defer tx.Rollback(ctx)

	// Lock the purchase row so the two sign endpoints don't race.
	var pStatus models.PurchaseRequestStatus
	if err := tx.QueryRow(ctx, `SELECT status FROM purchase_requests WHERE id = $1 FOR UPDATE`, purchaseID).Scan(&pStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, false, models.ErrPurchaseRequestNotFound
		}
		return nil, nil, false, err
	}
	// Only allow signing while the offer is accepted or half-signed.
	switch pStatus {
	case models.PurchaseStatusAccepted,
		models.PurchaseStatusBOSPendingSeller,
		models.PurchaseStatusBOSPendingBuyer:
	default:
		return nil, nil, false, models.ErrInvalidPurchaseAction
	}

	// Load the BoS row (must exist — accept creates it).
	var (
		sellerSignedAt *time.Time
		buyerSignedAt  *time.Time
	)
	if err := tx.QueryRow(ctx, `
		SELECT seller_signed_at, buyer_signed_at FROM purchase_bill_of_sales
		WHERE purchase_request_id = $1 FOR UPDATE
	`, purchaseID).Scan(&sellerSignedAt, &buyerSignedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, false, models.ErrInvalidPurchaseAction
		}
		return nil, nil, false, err
	}

	alreadySigned := false
	if role == "seller" && sellerSignedAt != nil {
		alreadySigned = true
	}
	if role == "buyer" && buyerSignedAt != nil {
		alreadySigned = true
	}

	if !alreadySigned {
		var setClause string
		if role == "seller" {
			setClause = "seller_signature_url = $2, seller_signed_at = NOW()"
		} else {
			setClause = "buyer_signature_url = $2, buyer_signed_at = NOW()"
		}
		if _, err := tx.Exec(ctx, `
			UPDATE purchase_bill_of_sales
			SET `+setClause+`, updated_at = NOW()
			WHERE purchase_request_id = $1
		`, purchaseID, signatureURL); err != nil {
			return nil, nil, false, fmt.Errorf("mark signature: %w", err)
		}
	}

	// Re-read to compute the fresh state.
	var b models.PurchaseBillOfSale
	if err := tx.QueryRow(ctx, `
		SELECT id, purchase_request_id,
		       vehicle_year, vehicle_make, vehicle_model, vin,
		       sale_amount_cents, currency,
		       terms_conditions,
		       seller_name, seller_address, seller_signature_url, seller_signed_at,
		       buyer_name, buyer_address, buyer_signature_url, buyer_signed_at,
		       finalized_pdf_url, finalized_at,
		       created_at, updated_at
		FROM purchase_bill_of_sales
		WHERE purchase_request_id = $1
	`, purchaseID).Scan(
		&b.ID, &b.PurchaseRequestID,
		&b.VehicleYear, &b.VehicleMake, &b.VehicleModel, &b.VIN,
		&b.SaleAmountCents, &b.Currency,
		&b.TermsConditions,
		&b.SellerName, &b.SellerAddress, &b.SellerSignatureURL, &b.SellerSignedAt,
		&b.BuyerName, &b.BuyerAddress, &b.BuyerSignatureURL, &b.BuyerSignedAt,
		&b.FinalizedPDFURL, &b.FinalizedAt,
		&b.CreatedAt, &b.UpdatedAt,
	); err != nil {
		return nil, nil, false, err
	}

	// Advance purchase status.
	next := pStatus
	switch {
	case b.SellerSigned() && b.BuyerSigned():
		next = models.PurchaseStatusBOSSigned
	case b.SellerSigned() && !b.BuyerSigned():
		next = models.PurchaseStatusBOSPendingBuyer
	case !b.SellerSigned() && b.BuyerSigned():
		next = models.PurchaseStatusBOSPendingSeller
	}
	if next != pStatus {
		if _, err := tx.Exec(ctx, `
			UPDATE purchase_requests SET status = $2, updated_at = NOW() WHERE id = $1
		`, purchaseID, string(next)); err != nil {
			return nil, nil, false, fmt.Errorf("advance status: %w", err)
		}
	}

	// Re-read the purchase row.
	row := tx.QueryRow(ctx, `SELECT `+purchaseRequestColumns+` FROM purchase_requests WHERE id = $1`, purchaseID)
	p, err := scanPurchaseRequest(row)
	if err != nil {
		return nil, nil, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, false, err
	}
	return &b, p, alreadySigned, nil
}

// ─── Payment ────────────────────────────────────────────────────────────────

// RecordPaymentIntent stamps the Stripe intent id + expected AuthExpiresAt
// onto the row. Called immediately after the intent is created so retries
// return the same intent.
func (r *PurchaseRequestRepository) RecordPaymentIntent(ctx context.Context, id uuid.UUID, intentID string) (*models.PurchaseRequest, error) {
	// Idempotent: if the intent id is already set we leave it. UPDATE
	// only when NULL.
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE purchase_requests
		SET payment_intent_id = COALESCE(payment_intent_id, $2), updated_at = NOW()
		WHERE id = $1
	`, id, intentID)
	if err != nil {
		return nil, fmt.Errorf("record payment intent: %w", err)
	}
	return r.GetByID(ctx, id)
}

// MarkAuthorized transitions bos_signed → payment_authorized and stamps
// auth_expires_at. Idempotent: repeat calls are no-ops.
func (r *PurchaseRequestRepository) MarkAuthorized(ctx context.Context, intentID string) (*models.PurchaseRequest, error) {
	row := r.db.Pool.QueryRow(ctx, `
		UPDATE purchase_requests
		SET status = 'payment_authorized',
		    payment_status = 'requires_capture',
		    auth_expires_at = COALESCE(auth_expires_at, NOW() + $2::interval),
		    updated_at = NOW()
		WHERE payment_intent_id = $1
		  AND status IN ('bos_signed','payment_authorized')
		RETURNING `+purchaseRequestColumns, intentID,
		fmt.Sprintf("%d seconds", int(models.PurchaseAuthTTL.Seconds())))
	p, err := scanPurchaseRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrInvalidPurchaseAction
	}
	if err != nil {
		return nil, fmt.Errorf("mark authorized: %w", err)
	}
	return p, nil
}

// MarkCaptured transitions inspection_accepted → completed after Stripe
// capture succeeds. Same helper also serves the `rejected_upheld → completed`
// admin path via the fromStatuses arg.
//
// Concurrency guard: locks the car row inside the same transaction and
// aborts if the car is already sold OR is reserved for a different
// purchase. Two buyers who race to inspection_accepted could otherwise
// both Capture — the seller collects on both, and one buyer pays for
// a car they can never own. Returns ErrCarSold so the caller can back
// out of Stripe capture cleanly.
func (r *PurchaseRequestRepository) MarkCaptured(ctx context.Context, id uuid.UUID) (*models.PurchaseRequest, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		UPDATE purchase_requests
		SET status = 'completed',
		    payment_status = 'succeeded',
		    completed_at = COALESCE(completed_at, NOW()),
		    updated_at = NOW()
		WHERE id = $1 AND status IN ('inspection_accepted','rejected_upheld','completed')
		RETURNING `+purchaseRequestColumns, id)
	p, err := scanPurchaseRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrInvalidPurchaseAction
	}
	if err != nil {
		return nil, fmt.Errorf("mark captured: %w", err)
	}

	// Lock car + verify it hasn't already been sold to a different
	// purchase. Reservation set at KeysHandedOver blocks concurrent
	// purchases from progressing, but we defend at the capture point
	// too in case the reservation guard is bypassed or a race squeaks
	// through.
	var carStatus string
	var reservedBy *uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT status, reserved_by_purchase_request_id
		  FROM cars WHERE id = $1 FOR UPDATE
	`, p.CarID).Scan(&carStatus, &reservedBy); err != nil {
		return nil, fmt.Errorf("lock car row: %w", err)
	}
	if carStatus == "sold" || (reservedBy != nil && *reservedBy != p.ID) {
		return nil, models.ErrCarSold
	}

	// Terminal side-effects: mark the car as sold, unreserve.
	if _, err := tx.Exec(ctx, `
		UPDATE cars
		SET status = 'sold', is_paused = TRUE, is_for_sale = FALSE, is_for_rent = FALSE,
		    reserved_by_purchase_request_id = NULL,
		    updated_at = NOW()
		WHERE id = $1
	`, p.CarID); err != nil {
		return nil, fmt.Errorf("mark car sold: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

// MarkAuthCancelled records a Stripe cancel (auth released) on a
// non-terminal row and pushes it to the terminal state indicated by the
// caller (`rejected_refunded` for admin-accepted rejections,
// `expired_auth` for scanner-driven auth expiries).
func (r *PurchaseRequestRepository) MarkAuthCancelled(ctx context.Context, id uuid.UUID, terminal models.PurchaseRequestStatus) (*models.PurchaseRequest, error) {
	if terminal != models.PurchaseStatusRejectedRefunded && terminal != models.PurchaseStatusExpiredAuth {
		return nil, fmt.Errorf("mark auth cancelled: invalid terminal %s", terminal)
	}
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		UPDATE purchase_requests
		SET status = $2,
		    payment_status = 'canceled',
		    refund_status = 'not_applicable',
		    completed_at = COALESCE(completed_at, NOW()),
		    updated_at = NOW()
		WHERE id = $1
		  AND status IN ('payment_authorized','handover_scheduled','awaiting_inspection','inspection_rejected')
		RETURNING `+purchaseRequestColumns, id, string(terminal))
	p, err := scanPurchaseRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrInvalidPurchaseAction
	}
	if err != nil {
		return nil, fmt.Errorf("mark auth cancelled: %w", err)
	}

	// Release the car reservation so it re-enters discovery.
	if _, err := tx.Exec(ctx, `
		UPDATE cars
		SET reserved_by_purchase_request_id = NULL, updated_at = NOW()
		WHERE reserved_by_purchase_request_id = $1
	`, p.ID); err != nil {
		return nil, fmt.Errorf("unreserve car: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

// RecordRefund persists the Stripe refund id + status on the row.
func (r *PurchaseRequestRepository) RecordRefund(ctx context.Context, id uuid.UUID, refundID string, status models.VehicleReturnRefundStatus) (*models.PurchaseRequest, error) {
	row := r.db.Pool.QueryRow(ctx, `
		UPDATE purchase_requests
		SET refund_id = $2,
		    refund_status = $3,
		    refunded_at = CASE WHEN $3 = 'succeeded' THEN NOW() ELSE refunded_at END,
		    updated_at = NOW()
		WHERE id = $1
		RETURNING `+purchaseRequestColumns, id, refundID, string(status))
	p, err := scanPurchaseRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrPurchaseRequestNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("record refund: %w", err)
	}
	return p, nil
}

// ─── Handover / Inspection ──────────────────────────────────────────────────

// ScheduleHandover: seller sets time+location. Guarded to payment_authorized.
func (r *PurchaseRequestRepository) ScheduleHandover(ctx context.Context, id, sellerID uuid.UUID, body models.ScheduleHandoverBody) (*models.PurchaseRequest, error) {
	row := r.db.Pool.QueryRow(ctx, `
		UPDATE purchase_requests
		SET status = 'handover_scheduled',
		    handover_scheduled_at = $3,
		    handover_location = $4,
		    handover_latitude = $5,
		    handover_longitude = $6,
		    updated_at = NOW()
		WHERE id = $1 AND seller_id = $2
		  AND status IN ('payment_authorized','handover_scheduled')
		RETURNING `+purchaseRequestColumns,
		id, sellerID, body.HandoverScheduledAt, body.HandoverLocation, body.HandoverLatitude, body.HandoverLongitude)
	p, err := scanPurchaseRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrInvalidPurchaseAction
	}
	if err != nil {
		return nil, fmt.Errorf("schedule handover: %w", err)
	}
	return p, nil
}

// KeysHandedOver: seller confirms in-person. Sets inspection_deadline_at
// and reserves the car so it drops out of discovery. Guarded to
// handover_scheduled.
func (r *PurchaseRequestRepository) KeysHandedOver(ctx context.Context, id, sellerID uuid.UUID) (*models.PurchaseRequest, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		UPDATE purchase_requests
		SET status = 'awaiting_inspection',
		    keys_handed_over_at = NOW(),
		    inspection_deadline_at = NOW() + $3::interval,
		    updated_at = NOW()
		WHERE id = $1 AND seller_id = $2 AND status = 'handover_scheduled'
		RETURNING `+purchaseRequestColumns,
		id, sellerID, fmt.Sprintf("%d seconds", int(models.PurchaseInspectionWindow.Seconds())))
	p, err := scanPurchaseRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrInvalidPurchaseAction
	}
	if err != nil {
		return nil, fmt.Errorf("keys handed over: %w", err)
	}

	// Atomically reserve the car. Guarded so a concurrent purchase that
	// beat us to reservation (or a sale that already completed) makes
	// this UPDATE affect zero rows — we then roll back and surface
	// ErrCarSold so the caller flips the purchase back to a safe state
	// instead of proceeding into inspection under the illusion of
	// exclusive control.
	tag, err := tx.Exec(ctx, `
		UPDATE cars SET reserved_by_purchase_request_id = $2, updated_at = NOW()
		WHERE id = $1
		  AND status <> 'sold'
		  AND (reserved_by_purchase_request_id IS NULL
		       OR reserved_by_purchase_request_id = $2)
	`, p.CarID, p.ID)
	if err != nil {
		return nil, fmt.Errorf("reserve car: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, models.ErrCarSold
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

// InspectionAccept: buyer accepts. Transitions to `inspection_accepted`.
// The handler then follows up with a Stripe Capture and MarkCaptured to
// reach `completed`.
func (r *PurchaseRequestRepository) InspectionAccept(ctx context.Context, id, buyerID uuid.UUID) (*models.PurchaseRequest, error) {
	row := r.db.Pool.QueryRow(ctx, `
		UPDATE purchase_requests
		SET status = 'inspection_accepted',
		    inspection_accepted_at = NOW(),
		    updated_at = NOW()
		WHERE id = $1 AND buyer_id = $2 AND status = 'awaiting_inspection'
		RETURNING `+purchaseRequestColumns, id, buyerID)
	p, err := scanPurchaseRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrNotAwaitingInspection
	}
	if err != nil {
		return nil, fmt.Errorf("inspection accept: %w", err)
	}
	return p, nil
}

// ─── Rejections ─────────────────────────────────────────────────────────────

// CreateEvidence writes a single evidence row not yet linked to any rejection.
// The rejection is created separately once the buyer submits, at which
// point we backfill purchase_rejection_id via LinkEvidenceToRejection.
// In this iteration we take a simpler two-step: evidence rows carry
// purchase_rejection_id from the start via a placeholder "pending" row.
//
// To keep the DB schema honest with the FK, we instead create a
// "placeholder" rejection lazily on the first evidence upload and reuse
// it on submit. Simpler flow: the handler creates the rejection row when
// the buyer taps "Reject" (with status='submitted'), and evidence uploads
// take a rejection id. We match that shape by having the handler call
// CreateOrGetPendingRejection first, then this function.
func (r *PurchaseRequestRepository) CreateEvidence(ctx context.Context, purchaseRejectionID uuid.UUID, e models.PurchaseRejectionEvidence) (*models.PurchaseRejectionEvidence, error) {
	row := r.db.Pool.QueryRow(ctx, `
		INSERT INTO purchase_rejection_evidence
			(id, purchase_rejection_id, file_url, file_path, filename, mime_type, size_bytes, created_at)
		VALUES
			(gen_random_uuid(), $1, $2, $3, $4, $5, $6, NOW())
		RETURNING id, purchase_rejection_id, file_url, file_path, filename, mime_type, size_bytes, created_at
	`, purchaseRejectionID, e.FileURL, e.FilePath, e.Filename, e.MimeType, e.SizeBytes)
	var out models.PurchaseRejectionEvidence
	if err := row.Scan(&out.ID, &out.PurchaseRejectionID, &out.FileURL, &out.FilePath, &out.Filename, &out.MimeType, &out.SizeBytes, &out.CreatedAt); err != nil {
		return nil, fmt.Errorf("create evidence: %w", err)
	}
	return &out, nil
}

// ListEvidence returns all evidence for a rejection.
func (r *PurchaseRequestRepository) ListEvidence(ctx context.Context, rejectionID uuid.UUID) ([]models.PurchaseRejectionEvidence, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT id, purchase_rejection_id, file_url, file_path, filename, mime_type, size_bytes, created_at
		FROM purchase_rejection_evidence WHERE purchase_rejection_id = $1
		ORDER BY created_at ASC
	`, rejectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.PurchaseRejectionEvidence{}
	for rows.Next() {
		var e models.PurchaseRejectionEvidence
		if err := rows.Scan(&e.ID, &e.PurchaseRejectionID, &e.FileURL, &e.FilePath, &e.Filename, &e.MimeType, &e.SizeBytes, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// CountEvidence returns how many evidence rows are attached to a rejection.
func (r *PurchaseRequestRepository) CountEvidence(ctx context.Context, rejectionID uuid.UUID) (int, error) {
	var n int
	if err := r.db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM purchase_rejection_evidence WHERE purchase_rejection_id = $1`, rejectionID).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// GetOrCreatePendingRejection returns the existing rejection row for the
// purchase (used across many evidence uploads) or creates a fresh
// `submitted` row with a placeholder reason. The reason + explanation get
// finalized on Submit.
func (r *PurchaseRequestRepository) GetOrCreatePendingRejection(ctx context.Context, purchaseID uuid.UUID) (*models.PurchaseRejection, error) {
	// Try fetch first.
	if existing, err := r.GetRejection(ctx, purchaseID); err == nil && existing != nil {
		return existing, nil
	}
	row := r.db.Pool.QueryRow(ctx, `
		INSERT INTO purchase_rejections
			(id, purchase_request_id, reason_category, explanation, status, created_at, updated_at)
		VALUES
			(gen_random_uuid(), $1, 'other', 'pending buyer submission - placeholder', 'submitted', NOW(), NOW())
		ON CONFLICT (purchase_request_id) DO NOTHING
		RETURNING id, purchase_request_id, reason_category, explanation, status, refund_status, admin_note, resolved_by, resolved_at, created_at, updated_at
	`, purchaseID)
	var rej models.PurchaseRejection
	var refundStatus sql.NullString
	if err := row.Scan(&rej.ID, &rej.PurchaseRequestID, &rej.ReasonCategory, &rej.Explanation, &rej.Status, &refundStatus, &rej.AdminNote, &rej.ResolvedBy, &rej.ResolvedAt, &rej.CreatedAt, &rej.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Row already existed — fetch again.
			return r.GetRejection(ctx, purchaseID)
		}
		return nil, fmt.Errorf("create pending rejection: %w", err)
	}
	if refundStatus.Valid {
		rs := models.VehicleReturnRefundStatus(refundStatus.String)
		rej.RefundStatus = &rs
	}
	return &rej, nil
}

// GetRejection returns the rejection row (if any) for a purchase.
func (r *PurchaseRequestRepository) GetRejection(ctx context.Context, purchaseID uuid.UUID) (*models.PurchaseRejection, error) {
	row := r.db.Pool.QueryRow(ctx, `
		SELECT id, purchase_request_id, reason_category, explanation, status, refund_status, admin_note, resolved_by, resolved_at, created_at, updated_at
		FROM purchase_rejections WHERE purchase_request_id = $1
	`, purchaseID)
	var rej models.PurchaseRejection
	var refundStatus sql.NullString
	if err := row.Scan(&rej.ID, &rej.PurchaseRequestID, &rej.ReasonCategory, &rej.Explanation, &rej.Status, &refundStatus, &rej.AdminNote, &rej.ResolvedBy, &rej.ResolvedAt, &rej.CreatedAt, &rej.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if refundStatus.Valid {
		rs := models.VehicleReturnRefundStatus(refundStatus.String)
		rej.RefundStatus = &rs
	}
	return &rej, nil
}

// GetRejectionByID looks up by rejection id, not purchase id. Used by the
// evidence upload endpoint which authenticates via the parent purchase.
func (r *PurchaseRequestRepository) GetRejectionByID(ctx context.Context, id uuid.UUID) (*models.PurchaseRejection, error) {
	row := r.db.Pool.QueryRow(ctx, `
		SELECT id, purchase_request_id, reason_category, explanation, status, refund_status, admin_note, resolved_by, resolved_at, created_at, updated_at
		FROM purchase_rejections WHERE id = $1
	`, id)
	var rej models.PurchaseRejection
	var refundStatus sql.NullString
	if err := row.Scan(&rej.ID, &rej.PurchaseRequestID, &rej.ReasonCategory, &rej.Explanation, &rej.Status, &refundStatus, &rej.AdminNote, &rej.ResolvedBy, &rej.ResolvedAt, &rej.CreatedAt, &rej.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrPurchaseRejectionNotFound
		}
		return nil, err
	}
	if refundStatus.Valid {
		rs := models.VehicleReturnRefundStatus(refundStatus.String)
		rej.RefundStatus = &rs
	}
	return &rej, nil
}

// SubmitRejection finalizes a rejection: updates reason + explanation, flips
// the parent purchase row to `inspection_rejected`. Guarded so the parent
// must be in `awaiting_inspection`.
func (r *PurchaseRequestRepository) SubmitRejection(ctx context.Context, purchaseID, buyerID uuid.UUID, body models.SubmitRejectionBody) (*models.PurchaseRejection, *models.PurchaseRequest, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	// Guard: purchase must be in awaiting_inspection and caller must be the buyer.
	row := tx.QueryRow(ctx, `
		UPDATE purchase_requests
		SET status = 'inspection_rejected', updated_at = NOW()
		WHERE id = $1 AND buyer_id = $2 AND status = 'awaiting_inspection'
		RETURNING `+purchaseRequestColumns, purchaseID, buyerID)
	p, err := scanPurchaseRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, models.ErrNotAwaitingInspection
	}
	if err != nil {
		return nil, nil, fmt.Errorf("submit rejection (purchase): %w", err)
	}

	// Upsert the rejection row (may exist as placeholder via evidence uploads).
	if _, err := tx.Exec(ctx, `
		INSERT INTO purchase_rejections
			(id, purchase_request_id, reason_category, explanation, status, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, 'submitted', NOW(), NOW())
		ON CONFLICT (purchase_request_id) DO UPDATE
		SET reason_category = EXCLUDED.reason_category,
		    explanation = EXCLUDED.explanation,
		    status = 'submitted',
		    updated_at = NOW()
	`, purchaseID, string(body.ReasonCategory), body.Explanation); err != nil {
		return nil, nil, fmt.Errorf("upsert rejection: %w", err)
	}

	// Re-read.
	rejRow := tx.QueryRow(ctx, `
		SELECT id, purchase_request_id, reason_category, explanation, status, refund_status, admin_note, resolved_by, resolved_at, created_at, updated_at
		FROM purchase_rejections WHERE purchase_request_id = $1
	`, purchaseID)
	var rej models.PurchaseRejection
	var refundStatus sql.NullString
	if err := rejRow.Scan(&rej.ID, &rej.PurchaseRequestID, &rej.ReasonCategory, &rej.Explanation, &rej.Status, &refundStatus, &rej.AdminNote, &rej.ResolvedBy, &rej.ResolvedAt, &rej.CreatedAt, &rej.UpdatedAt); err != nil {
		return nil, nil, err
	}
	if refundStatus.Valid {
		rs := models.VehicleReturnRefundStatus(refundStatus.String)
		rej.RefundStatus = &rs
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return &rej, p, nil
}

// WithdrawRejection is the buyer-only path back to awaiting_inspection.
// Only valid while the admin hasn't started reviewing yet.
func (r *PurchaseRequestRepository) WithdrawRejection(ctx context.Context, purchaseID, buyerID uuid.UUID) (*models.PurchaseRequest, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		UPDATE purchase_requests
		SET status = 'awaiting_inspection',
		    inspection_deadline_at = NOW() + INTERVAL '12 hours',
		    updated_at = NOW()
		WHERE id = $1 AND buyer_id = $2 AND status = 'inspection_rejected'
		RETURNING `+purchaseRequestColumns, purchaseID, buyerID)
	p, err := scanPurchaseRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrInvalidPurchaseAction
	}
	if err != nil {
		return nil, fmt.Errorf("withdraw rejection: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE purchase_rejections SET status = 'withdrawn', updated_at = NOW()
		WHERE purchase_request_id = $1 AND status IN ('submitted','under_review')
	`, purchaseID); err != nil {
		return nil, fmt.Errorf("mark rejection withdrawn: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

// ResolveRejection is the admin path off a submitted/under-review rejection.
//
//	resolution="accept" → rejection.status='accepted'; parent purchase
//	    stays at 'inspection_rejected' (handler follows up with Stripe
//	    Cancel + MarkAuthCancelled → 'rejected_refunded').
//	resolution="uphold" → rejection.status='upheld'; parent purchase moves
//	    to 'rejected_upheld' (handler follows up with Stripe Capture +
//	    MarkCaptured → 'completed').
func (r *PurchaseRequestRepository) ResolveRejection(ctx context.Context, rejectionID, adminID uuid.UUID, resolution string, note *string) (*models.PurchaseRejection, *models.PurchaseRequest, error) {
	if resolution != "accept" && resolution != "uphold" {
		return nil, nil, models.NewValidationError("resolution must be 'accept' or 'uphold'")
	}
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	newRejStatus := models.PurchaseRejectionAccepted
	if resolution == "uphold" {
		newRejStatus = models.PurchaseRejectionUpheld
	}

	rejRow := tx.QueryRow(ctx, `
		UPDATE purchase_rejections
		SET status = $2, admin_note = $3, resolved_by = $4, resolved_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND status IN ('submitted','under_review')
		RETURNING id, purchase_request_id, reason_category, explanation, status, refund_status, admin_note, resolved_by, resolved_at, created_at, updated_at
	`, rejectionID, string(newRejStatus), note, adminID)
	var rej models.PurchaseRejection
	var refundStatus sql.NullString
	if err := rejRow.Scan(&rej.ID, &rej.PurchaseRequestID, &rej.ReasonCategory, &rej.Explanation, &rej.Status, &refundStatus, &rej.AdminNote, &rej.ResolvedBy, &rej.ResolvedAt, &rej.CreatedAt, &rej.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, models.ErrInvalidPurchaseAction
		}
		return nil, nil, fmt.Errorf("resolve rejection: %w", err)
	}
	if refundStatus.Valid {
		rs := models.VehicleReturnRefundStatus(refundStatus.String)
		rej.RefundStatus = &rs
	}

	// Move parent purchase for the uphold branch. Accept path stays at
	// inspection_rejected until the handler completes the Stripe cancel.
	var pReturn *models.PurchaseRequest
	if resolution == "uphold" {
		prow := tx.QueryRow(ctx, `
			UPDATE purchase_requests
			SET status = 'rejected_upheld', updated_at = NOW()
			WHERE id = $1 AND status = 'inspection_rejected'
			RETURNING `+purchaseRequestColumns, rej.PurchaseRequestID)
		p, err := scanPurchaseRequest(prow)
		if err != nil {
			return nil, nil, err
		}
		pReturn = p
	} else {
		// Refresh the parent for reference.
		prow := tx.QueryRow(ctx, `SELECT `+purchaseRequestColumns+` FROM purchase_requests WHERE id = $1`, rej.PurchaseRequestID)
		p, err := scanPurchaseRequest(prow)
		if err != nil {
			return nil, nil, err
		}
		pReturn = p
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return &rej, pReturn, nil
}

// ─── Auth-expiry scanner support ────────────────────────────────────────────

// ListAuthExpired returns non-terminal rows whose Stripe auth window has
// elapsed and need to be flipped to `expired_auth`.
func (r *PurchaseRequestRepository) ListAuthExpired(ctx context.Context, limit int) ([]models.PurchaseRequest, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.Pool.Query(ctx, `
		SELECT `+purchaseRequestColumns+`
		FROM purchase_requests
		WHERE status IN ('payment_authorized','handover_scheduled','awaiting_inspection')
		  AND auth_expires_at IS NOT NULL
		  AND auth_expires_at <= NOW()
		ORDER BY auth_expires_at ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.PurchaseRequest{}
	for rows.Next() {
		p, err := scanPurchaseRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, nil
}

// ListOfferExpired returns `requested` rows past their ExpiresAt.
func (r *PurchaseRequestRepository) ListOfferExpired(ctx context.Context, limit int) ([]models.PurchaseRequest, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.Pool.Query(ctx, `
		SELECT `+purchaseRequestColumns+`
		FROM purchase_requests
		WHERE status = 'requested' AND expires_at <= NOW()
		ORDER BY expires_at ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.PurchaseRequest{}
	for rows.Next() {
		p, err := scanPurchaseRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, nil
}

// ─── Admin ─────────────────────────────────────────────────────────────────

// AdminList returns paginated purchases + total count for the admin panel.
func (r *PurchaseRequestRepository) AdminList(ctx context.Context, status string, page, limit int) ([]models.PurchaseRequest, int, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	args := []interface{}{}
	whereClauses := []string{}
	if status != "" {
		args = append(args, status)
		whereClauses = append(whereClauses, fmt.Sprintf("status = $%d", len(args)))
	}
	where := ""
	if len(whereClauses) > 0 {
		where = "WHERE " + whereClauses[0]
		for i := 1; i < len(whereClauses); i++ {
			where += " AND " + whereClauses[i]
		}
	}

	var total int
	if err := r.db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM purchase_requests "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("admin count purchases: %w", err)
	}
	args = append(args, limit, (page-1)*limit)
	q := fmt.Sprintf(`
		SELECT `+purchaseRequestColumns+`
		FROM purchase_requests
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, where, len(args)-1, len(args))

	rows, err := r.db.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("admin list purchases: %w", err)
	}
	defer rows.Close()
	out := []models.PurchaseRequest{}
	for rows.Next() {
		p, err := scanPurchaseRequest(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *p)
	}
	return out, total, nil
}

// ─── Today aggregation ──────────────────────────────────────────────────────

// ListTodayActionsForBuyer returns purchase actions that should surface on
// the buyer's Today tab. Mirrors LeaseRequestRepository.ListTodayActionsForDriver.
func (r *PurchaseRequestRepository) ListTodayActionsForBuyer(ctx context.Context, buyerID uuid.UUID) ([]models.TodayAction, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT pr.id, pr.car_id, pr.chat_id, pr.seller_id, pr.status, pr.expires_at, pr.created_at,
		       COALESCE(c.title, '') AS car_title,
		       COALESCE(u.first_name || ' ' || u.last_name, '') AS seller_name
		FROM purchase_requests pr
		LEFT JOIN cars c ON c.id = pr.car_id
		LEFT JOIN users u ON u.id = pr.seller_id
		WHERE pr.buyer_id = $1
		  AND pr.status IN (
		    'accepted','bos_pending_seller','bos_pending_buyer','bos_signed',
		    'payment_authorized','handover_scheduled','awaiting_inspection','inspection_rejected'
		  )
		ORDER BY pr.created_at DESC
	`, buyerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.TodayAction{}
	for rows.Next() {
		var a models.TodayAction
		var status string
		var expiresAt, createdAt time.Time
		if err := rows.Scan(&a.ID, &a.CarID, &a.ChatID, &a.CounterpartyID, &status, &expiresAt, &createdAt, &a.CarTitle, &a.CounterpartyName); err != nil {
			return nil, err
		}
		a.Status = status
		a.Type = models.TodayActionPurchaseAction
		a.Title, a.Body, a.PrimaryAction = purchaseCopyForBuyer(status, a.CarTitle)
		a.ExpiresAt = models.RFC3339Time(expiresAt)
		a.CreatedAt = models.RFC3339Time(createdAt)
		out = append(out, a)
	}
	return out, nil
}

// ListTodayActionsForSeller returns purchase actions for the seller's Today.
func (r *PurchaseRequestRepository) ListTodayActionsForSeller(ctx context.Context, sellerID uuid.UUID) ([]models.TodayAction, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT pr.id, pr.car_id, pr.chat_id, pr.buyer_id, pr.status, pr.expires_at, pr.created_at,
		       COALESCE(c.title, '') AS car_title,
		       COALESCE(u.first_name || ' ' || u.last_name, '') AS buyer_name
		FROM purchase_requests pr
		LEFT JOIN cars c ON c.id = pr.car_id
		LEFT JOIN users u ON u.id = pr.buyer_id
		WHERE pr.seller_id = $1
		  AND pr.status IN (
		    'requested','accepted','bos_pending_seller','bos_pending_buyer','bos_signed',
		    'payment_authorized','handover_scheduled','awaiting_inspection','inspection_rejected'
		  )
		ORDER BY pr.created_at DESC
	`, sellerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.TodayAction{}
	for rows.Next() {
		var a models.TodayAction
		var status string
		var expiresAt, createdAt time.Time
		if err := rows.Scan(&a.ID, &a.CarID, &a.ChatID, &a.CounterpartyID, &status, &expiresAt, &createdAt, &a.CarTitle, &a.CounterpartyName); err != nil {
			return nil, err
		}
		a.Status = status
		a.Type = models.TodayActionPurchaseAction
		a.Title, a.Body, a.PrimaryAction = purchaseCopyForSeller(status, a.CarTitle)
		a.ExpiresAt = models.RFC3339Time(expiresAt)
		a.CreatedAt = models.RFC3339Time(createdAt)
		out = append(out, a)
	}
	return out, nil
}

// purchaseCopyForBuyer / purchaseCopyForSeller return the {title, body,
// CTA} triple used on Today cards for each role×status combo.
func purchaseCopyForBuyer(status, carTitle string) (string, string, string) {
	if carTitle == "" {
		carTitle = "the vehicle"
	}
	switch models.PurchaseRequestStatus(status) {
	case models.PurchaseStatusAccepted, models.PurchaseStatusBOSPendingBuyer:
		return "Sign the Bill of Sale", carTitle, "Sign now"
	case models.PurchaseStatusBOSPendingSeller:
		return "Waiting on seller signature", carTitle, "View BoS"
	case models.PurchaseStatusBOSSigned:
		return "Authorize payment", carTitle, "Pay now"
	case models.PurchaseStatusPaymentAuthorized:
		return "Waiting on seller to schedule handover", carTitle, "View request"
	case models.PurchaseStatusHandoverScheduled:
		return "Meet the seller", carTitle, "Open chat"
	case models.PurchaseStatusAwaitingInspection:
		return "Inspect the vehicle", carTitle, "Inspect vehicle"
	case models.PurchaseStatusInspectionRejected:
		return "Support is reviewing your rejection", carTitle, "View evidence"
	}
	return "Purchase update", carTitle, "View"
}

func purchaseCopyForSeller(status, carTitle string) (string, string, string) {
	if carTitle == "" {
		carTitle = "your listing"
	}
	switch models.PurchaseRequestStatus(status) {
	case models.PurchaseStatusRequested:
		return "New purchase offer", carTitle, "Review offer"
	case models.PurchaseStatusAccepted, models.PurchaseStatusBOSPendingSeller:
		return "Sign the Bill of Sale", carTitle, "Sign now"
	case models.PurchaseStatusBOSPendingBuyer:
		return "Waiting on buyer signature", carTitle, "View BoS"
	case models.PurchaseStatusBOSSigned:
		return "Waiting on buyer payment", carTitle, "View request"
	case models.PurchaseStatusPaymentAuthorized:
		return "Schedule handover", carTitle, "Schedule meetup"
	case models.PurchaseStatusHandoverScheduled:
		return "Meet the buyer", carTitle, "Open chat"
	case models.PurchaseStatusAwaitingInspection:
		return "Buyer is inspecting", carTitle, "View request"
	case models.PurchaseStatusInspectionRejected:
		return "Rejection under review by DrivaBai support", carTitle, "View evidence"
	}
	return "Purchase update", carTitle, "View"
}

// AdminListRejections is the queue for adjudication.
func (r *PurchaseRequestRepository) AdminListRejections(ctx context.Context, status string, page, limit int) ([]models.PurchaseRejection, int, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	args := []interface{}{}
	where := ""
	if status != "" {
		args = append(args, status)
		where = "WHERE status = $1"
	}

	var total int
	if err := r.db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM purchase_rejections "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("admin count rejections: %w", err)
	}
	args = append(args, limit, (page-1)*limit)
	q := fmt.Sprintf(`
		SELECT id, purchase_request_id, reason_category, explanation, status, refund_status, admin_note, resolved_by, resolved_at, created_at, updated_at
		FROM purchase_rejections
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, where, len(args)-1, len(args))
	rows, err := r.db.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []models.PurchaseRejection{}
	for rows.Next() {
		var rej models.PurchaseRejection
		var refundStatus sql.NullString
		if err := rows.Scan(&rej.ID, &rej.PurchaseRequestID, &rej.ReasonCategory, &rej.Explanation, &rej.Status, &refundStatus, &rej.AdminNote, &rej.ResolvedBy, &rej.ResolvedAt, &rej.CreatedAt, &rej.UpdatedAt); err != nil {
			return nil, 0, err
		}
		if refundStatus.Valid {
			rs := models.VehicleReturnRefundStatus(refundStatus.String)
			rej.RefundStatus = &rs
		}
		out = append(out, rej)
	}
	return out, total, nil
}
