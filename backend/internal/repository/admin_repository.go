package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
	"github.com/google/uuid"
)

// AdminRepository centralizes all read/write queries used by the admin panel.
// Kept separate from the user-facing repos so admin views can join freely
// without leaking internals into mobile-facing query paths.
type AdminRepository struct {
	db *database.DB
}

func NewAdminRepository(db *database.DB) *AdminRepository {
	return &AdminRepository{db: db}
}

// ========== USERS ==========

type AdminUserRow struct {
	ID               uuid.UUID  `json:"id"`
	Email            string     `json:"email"`
	Role             string     `json:"role"`
	FirstName        string     `json:"first_name"`
	LastName         string     `json:"last_name"`
	Phone            *string    `json:"phone,omitempty"`
	IsEmailVerified  bool       `json:"is_email_verified"`
	OnboardingStatus string     `json:"onboarding_status"`
	IsBlocked        bool       `json:"is_blocked"`
	BlockedAt        *time.Time `json:"blocked_at,omitempty"`
	ProfilePhotoURL  *string    `json:"profile_photo_url,omitempty"`
	HasLicense       bool       `json:"has_license"`
	HasRegistration  bool       `json:"has_registration"`
	CreatedAt        time.Time  `json:"created_at"`
}

type AdminUsersPage struct {
	Items []AdminUserRow `json:"items"`
	Total int            `json:"total"`
	Page  int            `json:"page"`
	Limit int            `json:"limit"`
}

func (r *AdminRepository) ListUsers(ctx context.Context, query, role, status string, page, limit int) (*AdminUsersPage, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}

	args := []interface{}{}
	where := []string{"u.role <> 'admin'"}
	if q := strings.TrimSpace(query); q != "" {
		args = append(args, "%"+strings.ToLower(q)+"%")
		where = append(where, fmt.Sprintf("(LOWER(u.email) LIKE $%d OR LOWER(u.first_name || ' ' || u.last_name) LIKE $%d)", len(args), len(args)))
	}
	if role != "" {
		args = append(args, role)
		where = append(where, fmt.Sprintf("u.role = $%d", len(args)))
	}
	switch status {
	case "active":
		where = append(where, "u.is_blocked = false")
	case "blocked":
		where = append(where, "u.is_blocked = true")
	}

	whereSQL := "WHERE " + strings.Join(where, " AND ")

	var total int
	if err := r.db.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM users u "+whereSQL, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count users: %w", err)
	}

	args = append(args, limit, (page-1)*limit)
	listSQL := fmt.Sprintf(`
		SELECT u.id, u.email, u.role, u.first_name, u.last_name, u.phone,
		       u.is_email_verified, u.onboarding_status,
		       u.is_blocked, u.blocked_at, u.profile_photo_url,
		       EXISTS(SELECT 1 FROM documents d WHERE d.user_id = u.id AND d.type = 'drivers_license') AS has_license,
		       EXISTS(SELECT 1 FROM documents d WHERE d.user_id = u.id AND d.type = 'registration')     AS has_registration,
		       u.created_at
		FROM users u
		%s
		ORDER BY u.created_at DESC
		LIMIT $%d OFFSET $%d
	`, whereSQL, len(args)-1, len(args))

	rows, err := r.db.Pool.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	out := []AdminUserRow{}
	for rows.Next() {
		var u AdminUserRow
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &u.FirstName, &u.LastName, &u.Phone,
			&u.IsEmailVerified, &u.OnboardingStatus,
			&u.IsBlocked, &u.BlockedAt, &u.ProfilePhotoURL,
			&u.HasLicense, &u.HasRegistration,
			&u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return &AdminUsersPage{Items: out, Total: total, Page: page, Limit: limit}, nil
}

func (r *AdminRepository) GetUserDetail(ctx context.Context, id uuid.UUID) (*AdminUserRow, error) {
	var u AdminUserRow
	err := r.db.Pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.role, u.first_name, u.last_name, u.phone,
		       u.is_email_verified, u.onboarding_status,
		       u.is_blocked, u.blocked_at, u.profile_photo_url,
		       EXISTS(SELECT 1 FROM documents d WHERE d.user_id = u.id AND d.type = 'drivers_license'),
		       EXISTS(SELECT 1 FROM documents d WHERE d.user_id = u.id AND d.type = 'registration'),
		       u.created_at
		FROM users u
		WHERE u.id = $1
	`, id).Scan(&u.ID, &u.Email, &u.Role, &u.FirstName, &u.LastName, &u.Phone,
		&u.IsEmailVerified, &u.OnboardingStatus,
		&u.IsBlocked, &u.BlockedAt, &u.ProfilePhotoURL,
		&u.HasLicense, &u.HasRegistration,
		&u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *AdminRepository) SetUserBlocked(ctx context.Context, id uuid.UUID, blocked bool) error {
	var (
		blockedAt interface{}
	)
	if blocked {
		blockedAt = time.Now().UTC()
	} else {
		blockedAt = nil
	}
	res, err := r.db.Pool.Exec(ctx,
		`UPDATE users SET is_blocked = $2, blocked_at = $3, updated_at = NOW() WHERE id = $1`,
		id, blocked, blockedAt)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return models.ErrUserNotFound
	}
	return nil
}

// ========== CARS ==========

type AdminCarRow struct {
	ID              uuid.UUID  `json:"id"`
	Title           string     `json:"title"`
	Make            string     `json:"make"`
	Model           string     `json:"model"`
	Year            int        `json:"year"`
	OwnerID         *uuid.UUID `json:"owner_id,omitempty"`
	OwnerEmail      *string    `json:"owner_email,omitempty"`
	OwnerName       *string    `json:"owner_name,omitempty"`
	Status          string     `json:"status"`
	IsPaused        bool       `json:"is_paused"`
	IsApproved      bool       `json:"is_approved"`
	IsForRent       bool       `json:"is_for_rent"`
	IsForSale       bool       `json:"is_for_sale"`
	WeeklyRentPrice *float64   `json:"weekly_rent_price,omitempty"`
	SalePrice       *float64   `json:"sale_price,omitempty"`
	Currency        string     `json:"currency"`
	Address         *string    `json:"address,omitempty"`
	CoverPhotoURL   *string    `json:"cover_photo_url,omitempty"`
	// MissingRequiredDocuments is server-computed (QA pt-10): the required
	// doc types (registration/inspection/insurance) this car does NOT yet
	// have on file. Title is no longer required at approval (decision C —
	// enforced at the Bill-of-Sale stage instead). The admin UI badges rows
	// with a non-empty list, and ApproveCar 422s on the same computation.
	MissingRequiredDocuments []string  `json:"missing_required_documents"`
	CreatedAt                time.Time `json:"created_at"`
}

type AdminCarsPage struct {
	Items []AdminCarRow `json:"items"`
	Total int           `json:"total"`
	Page  int           `json:"page"`
	Limit int           `json:"limit"`
}

func (r *AdminRepository) ListCars(ctx context.Context, query string, page, limit int) (*AdminCarsPage, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}

	args := []interface{}{}
	// Exclude soft-archived rows from the default active approval queue: a
	// sold car auto-archives, and an owner-deleted listing is archived too —
	// neither belongs in the moderation list. FOLLOW-UP: there is no explicit
	// history/archived filter param today; when one is added, gate this
	// predicate on it so admins can still inspect archived rows on demand.
	where := []string{"c.archived_at IS NULL"}
	if q := strings.TrimSpace(query); q != "" {
		args = append(args, "%"+strings.ToLower(q)+"%")
		where = append(where, fmt.Sprintf(
			"(LOWER(c.title) LIKE $%d OR LOWER(c.make) LIKE $%d OR LOWER(c.model) LIKE $%d OR LOWER(u.email) LIKE $%d)",
			len(args), len(args), len(args), len(args)))
	}
	whereSQL := "WHERE " + strings.Join(where, " AND ")

	var total int
	if err := r.db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM cars c LEFT JOIN users u ON u.id = c.owner_id `+whereSQL, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count cars: %w", err)
	}

	args = append(args, limit, (page-1)*limit)
	listSQL := fmt.Sprintf(`
		SELECT c.id, c.title, c.make, c.model, c.year,
		       c.owner_id, u.email, COALESCE(u.first_name || ' ' || u.last_name, ''),
		       c.status::text, c.is_paused, c.is_approved,
		       c.is_for_rent, c.is_for_sale, c.weekly_rent_price, c.sale_price, c.currency,
		       c.address,
		       (SELECT p.file_url FROM car_photos p WHERE p.car_id = c.id AND p.slot_type = 'cover_front' LIMIT 1),
		       ARRAY(SELECT DISTINCT d.document_type::text FROM car_documents d WHERE d.car_id = c.id),
		       c.created_at
		FROM cars c
		LEFT JOIN users u ON u.id = c.owner_id
		%s
		ORDER BY c.created_at DESC
		LIMIT $%d OFFSET $%d
	`, whereSQL, len(args)-1, len(args))

	rows, err := r.db.Pool.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("list cars: %w", err)
	}
	defer rows.Close()

	out := []AdminCarRow{}
	for rows.Next() {
		var c AdminCarRow
		var docTypes []string
		if err := rows.Scan(&c.ID, &c.Title, &c.Make, &c.Model, &c.Year,
			&c.OwnerID, &c.OwnerEmail, &c.OwnerName,
			&c.Status, &c.IsPaused, &c.IsApproved,
			&c.IsForRent, &c.IsForSale, &c.WeeklyRentPrice, &c.SalePrice, &c.Currency,
			&c.Address,
			&c.CoverPhotoURL,
			&docTypes,
			&c.CreatedAt); err != nil {
			return nil, err
		}
		c.MissingRequiredDocuments = models.MissingRequiredCarDocuments(c.IsForSale, docTypes)
		out = append(out, c)
	}
	return &AdminCarsPage{Items: out, Total: total, Page: page, Limit: limit}, nil
}

type AdminCarPhoto struct {
	ID       uuid.UUID `json:"id"`
	SlotType string    `json:"slot_type"`
	FileURL  string    `json:"file_url"`
}

// AdminCarDocument is one car document (title/registration/inspection/
// insurance) surfaced in the admin car detail. FileURL is the RAW private
// path as stored in the DB — the AdminHandler signs it per response before
// emitting, so a private path is never returned unsigned.
type AdminCarDocument struct {
	ID           uuid.UUID `json:"id"`
	DocumentType string    `json:"document_type"`
	FileName     string    `json:"file_name"`
	FileURL      string    `json:"file_url"`
}

type AdminCarDetail struct {
	AdminCarRow
	Description *string            `json:"description,omitempty"`
	Photos      []AdminCarPhoto    `json:"photos"`
	Documents   []AdminCarDocument `json:"documents"`
}

func (r *AdminRepository) GetCarDetail(ctx context.Context, id uuid.UUID) (*AdminCarDetail, error) {
	var c AdminCarDetail
	var docTypes []string
	err := r.db.Pool.QueryRow(ctx, `
		SELECT c.id, c.title, c.make, c.model, c.year,
		       c.owner_id, u.email, COALESCE(u.first_name || ' ' || u.last_name, ''),
		       c.status::text, c.is_paused, c.is_approved,
		       c.is_for_rent, c.is_for_sale, c.weekly_rent_price, c.sale_price, c.currency,
		       c.address,
		       (SELECT p.file_url FROM car_photos p WHERE p.car_id = c.id AND p.slot_type = 'cover_front' LIMIT 1),
		       ARRAY(SELECT DISTINCT d.document_type::text FROM car_documents d WHERE d.car_id = c.id),
		       c.created_at,
		       c.description
		FROM cars c
		LEFT JOIN users u ON u.id = c.owner_id
		WHERE c.id = $1
	`, id).Scan(&c.ID, &c.Title, &c.Make, &c.Model, &c.Year,
		&c.OwnerID, &c.OwnerEmail, &c.OwnerName,
		&c.Status, &c.IsPaused, &c.IsApproved,
		&c.IsForRent, &c.IsForSale, &c.WeeklyRentPrice, &c.SalePrice, &c.Currency,
		&c.Address,
		&c.CoverPhotoURL,
		&docTypes,
		&c.CreatedAt,
		&c.Description)
	if err != nil {
		return nil, err
	}
	c.MissingRequiredDocuments = models.MissingRequiredCarDocuments(c.IsForSale, docTypes)

	rows, err := r.db.Pool.Query(ctx,
		`SELECT id, slot_type::text, file_url FROM car_photos WHERE car_id = $1 ORDER BY slot_type`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	c.Photos = []AdminCarPhoto{}
	for rows.Next() {
		var p AdminCarPhoto
		if err := rows.Scan(&p.ID, &p.SlotType, &p.FileURL); err != nil {
			return nil, err
		}
		c.Photos = append(c.Photos, p)
	}
	rows.Close()

	// Documents (title/registration/inspection/insurance). Raw private URLs
	// here; the handler signs them before responding.
	docRows, err := r.db.Pool.Query(ctx,
		`SELECT id, document_type::text, file_name, file_url FROM car_documents WHERE car_id = $1 ORDER BY document_type`, id)
	if err != nil {
		return nil, err
	}
	defer docRows.Close()
	c.Documents = []AdminCarDocument{}
	for docRows.Next() {
		var d AdminCarDocument
		if err := docRows.Scan(&d.ID, &d.DocumentType, &d.FileName, &d.FileURL); err != nil {
			return nil, err
		}
		c.Documents = append(c.Documents, d)
	}
	return &c, nil
}

// CarApprovalInfo is the minimal snapshot ApproveCar needs to enforce the
// required-documents gate (QA pt-10 / D5).
type CarApprovalInfo struct {
	IsForSale  bool
	IsApproved bool
	// DocumentTypes are the distinct document_type values on file.
	DocumentTypes []string
}

// GetCarApprovalInfo loads the approval-gate inputs for one car. Returns
// pgx.ErrNoRows when the car doesn't exist.
func (r *AdminRepository) GetCarApprovalInfo(ctx context.Context, id uuid.UUID) (*CarApprovalInfo, error) {
	var info CarApprovalInfo
	err := r.db.Pool.QueryRow(ctx, `
		SELECT c.is_for_sale, c.is_approved,
		       ARRAY(SELECT DISTINCT d.document_type::text FROM car_documents d WHERE d.car_id = c.id)
		FROM cars c
		WHERE c.id = $1
	`, id).Scan(&info.IsForSale, &info.IsApproved, &info.DocumentTypes)
	if err != nil {
		return nil, err
	}
	return &info, nil
}

// SetCarApproved sets is_approved on a car. Admin approval is the SINGLE
// publish gate: when approval flips to TRUE and the car is still in the
// 'pending' moderation state, the SAME update publishes the listing by
// moving status 'pending' → 'available'. rented/sold/paused are deliberately
// left untouched so approval can never resurrect a sold car or unpause a
// paused one.
//
// Returns the owner_id (so the caller can target the WS/push that tells the
// owner their listing is live) and the resulting status.
func (r *AdminRepository) SetCarApproved(ctx context.Context, id uuid.UUID, approved bool) (uuid.UUID, models.CarListingStatus, error) {
	var (
		ownerID    uuid.UUID
		statusText string
	)
	err := r.db.Pool.QueryRow(ctx, `
		UPDATE cars
		SET is_approved = $2,
		    status = CASE
		                 WHEN $2 = true AND status = 'pending'
		                 THEN 'available'
		                 ELSE status
		             END,
		    updated_at = NOW()
		WHERE id = $1
		RETURNING owner_id, status::text
	`, id, approved).Scan(&ownerID, &statusText)
	if err != nil {
		return uuid.Nil, "", err
	}
	return ownerID, models.CarListingStatus(statusText), nil
}

// ========== CHATS (request chats: driver↔owner) ==========

type AdminChatRow struct {
	ID              uuid.UUID  `json:"id"`
	CarID           uuid.UUID  `json:"car_id"`
	CarTitle        string     `json:"car_title"`
	CarYear         int        `json:"car_year"`
	CoverPhotoURL   *string    `json:"cover_photo_url,omitempty"`
	DriverID        uuid.UUID  `json:"driver_id"`
	DriverName      string     `json:"driver_name"`
	DriverEmail     string     `json:"driver_email"`
	OwnerID         uuid.UUID  `json:"owner_id"`
	OwnerName       string     `json:"owner_name"`
	OwnerEmail      string     `json:"owner_email"`
	LastMessageBody *string    `json:"last_message_body,omitempty"`
	LastMessageAt   *time.Time `json:"last_message_at,omitempty"`
}

type AdminChatsPage struct {
	Items []AdminChatRow `json:"items"`
	Total int            `json:"total"`
	Page  int            `json:"page"`
	Limit int            `json:"limit"`
}

func (r *AdminRepository) ListChats(ctx context.Context, query string, page, limit int) (*AdminChatsPage, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}

	args := []interface{}{}
	where := []string{"1=1"}
	if q := strings.TrimSpace(query); q != "" {
		args = append(args, "%"+strings.ToLower(q)+"%")
		where = append(where, fmt.Sprintf(
			`(LOWER(c.title) LIKE $%d
			  OR LOWER(d.email) LIKE $%d OR LOWER(d.first_name || ' ' || d.last_name) LIKE $%d
			  OR LOWER(o.email) LIKE $%d OR LOWER(o.first_name || ' ' || o.last_name) LIKE $%d
			  OR ch.id::text LIKE $%d)`,
			len(args), len(args), len(args), len(args), len(args), len(args)))
	}
	whereSQL := "WHERE " + strings.Join(where, " AND ")

	var total int
	if err := r.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM chats ch
		JOIN cars c  ON c.id  = ch.car_id
		JOIN users d ON d.id  = ch.driver_id
		JOIN users o ON o.id  = ch.owner_id
		`+whereSQL, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count chats: %w", err)
	}

	args = append(args, limit, (page-1)*limit)
	listSQL := fmt.Sprintf(`
		SELECT ch.id, c.id, c.title, c.year,
		       (SELECT p.file_url FROM car_photos p WHERE p.car_id = c.id AND p.slot_type = 'cover_front' LIMIT 1),
		       d.id, COALESCE(d.first_name || ' ' || d.last_name, ''), d.email,
		       o.id, COALESCE(o.first_name || ' ' || o.last_name, ''), o.email,
		       (SELECT m.body FROM messages m WHERE m.chat_id = ch.id ORDER BY m.created_at DESC LIMIT 1),
		       ch.last_message_at
		FROM chats ch
		JOIN cars c  ON c.id  = ch.car_id
		JOIN users d ON d.id  = ch.driver_id
		JOIN users o ON o.id  = ch.owner_id
		%s
		ORDER BY ch.last_message_at DESC NULLS LAST, ch.created_at DESC
		LIMIT $%d OFFSET $%d
	`, whereSQL, len(args)-1, len(args))

	rows, err := r.db.Pool.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("list chats: %w", err)
	}
	defer rows.Close()
	out := []AdminChatRow{}
	for rows.Next() {
		var ch AdminChatRow
		if err := rows.Scan(&ch.ID, &ch.CarID, &ch.CarTitle, &ch.CarYear, &ch.CoverPhotoURL,
			&ch.DriverID, &ch.DriverName, &ch.DriverEmail,
			&ch.OwnerID, &ch.OwnerName, &ch.OwnerEmail,
			&ch.LastMessageBody, &ch.LastMessageAt); err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return &AdminChatsPage{Items: out, Total: total, Page: page, Limit: limit}, nil
}

type AdminMessage struct {
	ID         uuid.UUID `json:"id"`
	ChatID     uuid.UUID `json:"chat_id"`
	SenderID   uuid.UUID `json:"sender_id"`
	SenderName string    `json:"sender_name"`
	SenderKind string    `json:"sender_kind"` // "user" | "admin"
	Type       string    `json:"type"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"created_at"`
}

func (r *AdminRepository) ListChatMessages(ctx context.Context, chatID uuid.UUID, limit int) ([]AdminMessage, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := r.db.Pool.Query(ctx, `
		SELECT m.id, m.chat_id, m.sender_id,
		       COALESCE(u.first_name || ' ' || u.last_name, ''),
		       m.sender_kind, m.type::text, m.body, m.created_at
		FROM messages m
		LEFT JOIN users u ON u.id = m.sender_id
		WHERE m.chat_id = $1
		ORDER BY m.created_at ASC
		LIMIT $2
	`, chatID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminMessage{}
	for rows.Next() {
		var m AdminMessage
		if err := rows.Scan(&m.ID, &m.ChatID, &m.SenderID, &m.SenderName, &m.SenderKind, &m.Type, &m.Body, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// AdminSendChatMessage inserts a message with sender_kind='admin' into a user-to-user chat.
// Returns the inserted message and the driver/owner IDs for WS broadcast.
func (r *AdminRepository) AdminSendChatMessage(ctx context.Context, chatID, adminID uuid.UUID, body string) (*AdminMessage, uuid.UUID, uuid.UUID, error) {
	var driverID, ownerID uuid.UUID
	err := r.db.Pool.QueryRow(ctx,
		`SELECT driver_id, owner_id FROM chats WHERE id = $1`, chatID,
	).Scan(&driverID, &ownerID)
	if err != nil {
		return nil, uuid.Nil, uuid.Nil, models.ErrChatNotFound
	}

	var adminName string
	_ = r.db.Pool.QueryRow(ctx,
		`SELECT COALESCE(first_name || ' ' || last_name, 'Admin') FROM users WHERE id = $1`, adminID,
	).Scan(&adminName)
	if adminName == "" {
		adminName = "Admin"
	}

	now := time.Now().UTC()
	msgID := uuid.New()

	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, uuid.Nil, uuid.Nil, err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO messages (id, chat_id, sender_id, type, body, sender_kind, created_at)
		VALUES ($1, $2, $3, 'text', $4, 'admin', $5)
	`, msgID, chatID, adminID, body, now)
	if err != nil {
		return nil, uuid.Nil, uuid.Nil, fmt.Errorf("insert admin message: %w", err)
	}

	_, err = tx.Exec(ctx, `UPDATE chats SET last_message_at = $2 WHERE id = $1`, chatID, now)
	if err != nil {
		return nil, uuid.Nil, uuid.Nil, fmt.Errorf("update last_message_at: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, uuid.Nil, uuid.Nil, err
	}

	msg := &AdminMessage{
		ID:         msgID,
		ChatID:     chatID,
		SenderID:   adminID,
		SenderName: adminName,
		SenderKind: "admin",
		Type:       "text",
		Body:       body,
		CreatedAt:  now,
	}
	return msg, driverID, ownerID, nil
}

// ========== RENTS (lease_requests joined with payments) ==========

type AdminRentRow struct {
	ID              uuid.UUID  `json:"id"`
	ChatID          uuid.UUID  `json:"chat_id"`
	Status          string     `json:"status"`
	WeeklyPrice     float64    `json:"weekly_price"`
	Weeks           int        `json:"weeks"`
	Currency        string     `json:"currency"`
	DriverID        uuid.UUID  `json:"driver_id"`
	DriverName      string     `json:"driver_name"`
	DriverEmail     string     `json:"driver_email"`
	OwnerID         uuid.UUID  `json:"owner_id"`
	OwnerName       string     `json:"owner_name"`
	OwnerEmail      string     `json:"owner_email"`
	CarID           uuid.UUID  `json:"car_id"`
	CarTitle        string     `json:"car_title"`
	CarYear         int        `json:"car_year"`
	PaymentIntentID *string    `json:"payment_intent_id,omitempty"`
	PaymentStatus   *string    `json:"payment_status,omitempty"`
	StartDate       time.Time  `json:"start_date"`
	EndDate         *time.Time `json:"end_date,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`

	// Vehicle-return columns (LEFT-joined from vehicle_returns).
	// Only the driver can initiate, so return_initiated_by_* always
	// resolves to the driver — kept as separate fields for the admin UI
	// because the admin layout treats them as the "who started it" lens.
	ReturnID                  *uuid.UUID `json:"return_id,omitempty"`
	ReturnStatus              *string    `json:"return_status,omitempty"`
	ReturnInitiatedByID       *uuid.UUID `json:"return_initiated_by_id,omitempty"`
	ReturnInitiatedByName     *string    `json:"return_initiated_by_name,omitempty"`
	ReturnInitiatedByEmail    *string    `json:"return_initiated_by_email,omitempty"`
	ReturnDriverConfirmedAt   *time.Time `json:"return_driver_confirmed_at,omitempty"`
	ReturnOwnerConfirmedAt    *time.Time `json:"return_owner_confirmed_at,omitempty"`
	ReturnCompletedAt         *time.Time `json:"return_completed_at,omitempty"`
	ReturnDisputedAt          *time.Time `json:"return_disputed_at,omitempty"`
	ReturnCancelledAt         *time.Time `json:"return_cancelled_at,omitempty"`
	ReturnUsedDays            *int       `json:"return_used_days,omitempty"`
	ReturnUnusedDays          *int       `json:"return_unused_days,omitempty"`
	ReturnRefundAmountCents   *int64     `json:"return_refund_amount_cents,omitempty"`
	ReturnRefundStatus        *string    `json:"return_refund_status,omitempty"`
	ReturnRefundID            *string    `json:"return_refund_id,omitempty"`
	ReturnRefundedAt          *time.Time `json:"return_refunded_at,omitempty"`
	ReturnRefundFailureReason *string    `json:"return_refund_failure_reason,omitempty"`
	ReturnDisputeReason       *string    `json:"return_dispute_reason,omitempty"`
}

type AdminRentsPage struct {
	Items []AdminRentRow `json:"items"`
	Total int            `json:"total"`
	Page  int            `json:"page"`
	Limit int            `json:"limit"`
}

func (r *AdminRepository) ListRents(ctx context.Context, query, statusFilter string, page, limit int) (*AdminRentsPage, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}

	args := []interface{}{}
	where := []string{"1=1"}
	if q := strings.TrimSpace(query); q != "" {
		args = append(args, "%"+strings.ToLower(q)+"%")
		where = append(where, fmt.Sprintf(
			`(LOWER(d.email) LIKE $%d OR LOWER(o.email) LIKE $%d
			  OR LOWER(d.first_name || ' ' || d.last_name) LIKE $%d
			  OR LOWER(o.first_name || ' ' || o.last_name) LIKE $%d
			  OR p.payment_intent_id ILIKE $%d)`,
			len(args), len(args), len(args), len(args), len(args)))
	}
	switch statusFilter {
	case "active":
		where = append(where, "lr.status IN ('accepted','payment_pending','paid')")
	case "finished":
		where = append(where, "lr.status IN ('declined','cancelled','expired')")
	case "":
		// no filter
	default:
		args = append(args, statusFilter)
		where = append(where, fmt.Sprintf("lr.status = $%d", len(args)))
	}
	whereSQL := "WHERE " + strings.Join(where, " AND ")

	var total int
	if err := r.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM lease_requests lr
		JOIN cars  c ON c.id = lr.listing_id
		JOIN users d ON d.id = lr.driver_id
		JOIN users o ON o.id = lr.owner_id
		LEFT JOIN payments p ON p.lease_request_id = lr.id
		`+whereSQL, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count rents: %w", err)
	}

	args = append(args, limit, (page-1)*limit)
	listSQL := fmt.Sprintf(`
		SELECT %s
		FROM lease_requests lr
		JOIN cars  c ON c.id = lr.listing_id
		JOIN users d ON d.id = lr.driver_id
		JOIN users o ON o.id = lr.owner_id
		LEFT JOIN payments p ON p.lease_request_id = lr.id
		LEFT JOIN vehicle_returns vr ON vr.lease_request_id = lr.id
		%s
		ORDER BY lr.created_at DESC
		LIMIT $%d OFFSET $%d
	`, adminRentSelectCols, whereSQL, len(args)-1, len(args))

	rows, err := r.db.Pool.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("list rents: %w", err)
	}
	defer rows.Close()
	out := []AdminRentRow{}
	for rows.Next() {
		var rent AdminRentRow
		if err := scanAdminRent(rows, &rent); err != nil {
			return nil, err
		}
		out = append(out, rent)
	}
	return &AdminRentsPage{Items: out, Total: total, Page: page, Limit: limit}, nil
}

// adminRentSelectCols is the canonical column list for the rents joined
// view — kept here so ListRents and GetRentDetail can't drift apart.
// vr.* columns come back NULL when no vehicle_return exists, which the
// pointer fields on AdminRentRow handle cleanly.
const adminRentSelectCols = `
	lr.id, lr.chat_id, lr.status::text, lr.weekly_price, lr.weeks, lr.currency,
	d.id, COALESCE(d.first_name || ' ' || d.last_name, ''), d.email,
	o.id, COALESCE(o.first_name || ' ' || o.last_name, ''), o.email,
	c.id, c.title, c.year,
	p.payment_intent_id, p.status::text,
	lr.created_at,
	CASE WHEN lr.status IN ('declined','cancelled','expired','paid') THEN lr.updated_at ELSE NULL END,
	lr.created_at,
	vr.id,
	vr.status,
	CASE WHEN vr.id IS NULL THEN NULL ELSE d.id END                                   AS return_initiated_by_id,
	CASE WHEN vr.id IS NULL THEN NULL ELSE COALESCE(d.first_name || ' ' || d.last_name, '') END AS return_initiated_by_name,
	CASE WHEN vr.id IS NULL THEN NULL ELSE d.email END                                AS return_initiated_by_email,
	vr.driver_initiated_at,
	vr.owner_confirmed_at,
	vr.completed_at,
	vr.disputed_at,
	vr.cancelled_at,
	vr.used_days,
	CASE WHEN vr.id IS NULL THEN NULL ELSE GREATEST(vr.rental_weeks * 7 - vr.used_days, 0) END AS return_unused_days,
	vr.refund_amount_cents,
	vr.refund_status,
	vr.refund_id,
	vr.refunded_at,
	vr.refund_failure_reason,
	vr.dispute_reason
`

// rowScanner is satisfied by pgx.Row and pgx.Rows; lets scanAdminRent
// serve both ListRents (rows) and GetRentDetail (row).
type rowScanner interface {
	Scan(dest ...any) error
}

func scanAdminRent(s rowScanner, rent *AdminRentRow) error {
	return s.Scan(&rent.ID, &rent.ChatID, &rent.Status, &rent.WeeklyPrice, &rent.Weeks, &rent.Currency,
		&rent.DriverID, &rent.DriverName, &rent.DriverEmail,
		&rent.OwnerID, &rent.OwnerName, &rent.OwnerEmail,
		&rent.CarID, &rent.CarTitle, &rent.CarYear,
		&rent.PaymentIntentID, &rent.PaymentStatus,
		&rent.StartDate, &rent.EndDate, &rent.CreatedAt,
		&rent.ReturnID, &rent.ReturnStatus,
		&rent.ReturnInitiatedByID, &rent.ReturnInitiatedByName, &rent.ReturnInitiatedByEmail,
		&rent.ReturnDriverConfirmedAt, &rent.ReturnOwnerConfirmedAt,
		&rent.ReturnCompletedAt, &rent.ReturnDisputedAt, &rent.ReturnCancelledAt,
		&rent.ReturnUsedDays, &rent.ReturnUnusedDays,
		&rent.ReturnRefundAmountCents, &rent.ReturnRefundStatus,
		&rent.ReturnRefundID, &rent.ReturnRefundedAt, &rent.ReturnRefundFailureReason,
		&rent.ReturnDisputeReason,
	)
}

// ========== SUPPORT CHATS ==========

type AdminSupportChat struct {
	ID              uuid.UUID  `json:"id"`
	UserID          uuid.UUID  `json:"user_id"`
	UserName        string     `json:"user_name"`
	UserEmail       string     `json:"user_email"`
	UserRole        string     `json:"user_role"`
	UserPhotoURL    *string    `json:"user_photo_url,omitempty"`
	LastMessageBody *string    `json:"last_message_body,omitempty"`
	LastMessageAt   *time.Time `json:"last_message_at,omitempty"`
	UnreadCount     int        `json:"unread_count"` // user messages admin hasn't read
}

func (r *AdminRepository) ListSupportChats(ctx context.Context) ([]AdminSupportChat, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT sc.id, sc.user_id,
		       COALESCE(u.first_name || ' ' || u.last_name, ''),
		       u.email, u.role::text, u.profile_photo_url,
		       (SELECT body FROM support_messages WHERE support_chat_id = sc.id ORDER BY created_at DESC LIMIT 1),
		       sc.last_message_at,
		       (
		         SELECT COUNT(*) FROM support_messages sm
		         WHERE sm.support_chat_id = sc.id
		           AND sm.sender_kind = 'user'
		           AND (sc.admin_last_read_at IS NULL OR sm.created_at > sc.admin_last_read_at)
		       ) AS unread_count
		FROM support_chats sc
		JOIN users u ON u.id = sc.user_id
		ORDER BY sc.last_message_at DESC NULLS LAST, sc.created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminSupportChat{}
	for rows.Next() {
		var c AdminSupportChat
		if err := rows.Scan(&c.ID, &c.UserID, &c.UserName, &c.UserEmail, &c.UserRole, &c.UserPhotoURL,
			&c.LastMessageBody, &c.LastMessageAt, &c.UnreadCount); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// GetAdminUserIDs returns IDs of all users with role='admin'. Used to target WS broadcasts.
func (r *AdminRepository) GetAdminUserIDs(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := r.db.Pool.Query(ctx, "SELECT id FROM users WHERE role = 'admin'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// MarkSupportChatAdminRead stamps admin_last_read_at = NOW() for the given chat.
func (r *AdminRepository) MarkSupportChatAdminRead(ctx context.Context, chatID uuid.UUID) error {
	_, err := r.db.Pool.Exec(ctx,
		"UPDATE support_chats SET admin_last_read_at = NOW() WHERE id = $1", chatID)
	return err
}

type AdminSupportMessage struct {
	ID            uuid.UUID `json:"id"`
	SupportChatID uuid.UUID `json:"support_chat_id"`
	SenderID      uuid.UUID `json:"sender_id"`
	SenderKind    string    `json:"sender_kind"` // 'user' | 'admin'
	Body          string    `json:"body"`
	CreatedAt     time.Time `json:"created_at"`
}

func (r *AdminRepository) ListSupportMessages(ctx context.Context, chatID uuid.UUID) ([]AdminSupportMessage, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT id, support_chat_id, sender_id, sender_kind, body, created_at
		FROM support_messages
		WHERE support_chat_id = $1
		ORDER BY created_at ASC
	`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminSupportMessage{}
	for rows.Next() {
		var m AdminSupportMessage
		if err := rows.Scan(&m.ID, &m.SupportChatID, &m.SenderID, &m.SenderKind, &m.Body, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// PostSupportMessage inserts an admin message and returns it plus the chat's user_id
// (needed for WS broadcast targeting).
func (r *AdminRepository) PostSupportMessage(ctx context.Context, chatID, senderID uuid.UUID, kind, body string) (*AdminSupportMessage, uuid.UUID, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, uuid.Nil, err
	}
	defer tx.Rollback(ctx)

	// Fetch chat user_id inside the transaction.
	var chatUserID uuid.UUID
	if err := tx.QueryRow(ctx, "SELECT user_id FROM support_chats WHERE id = $1", chatID).Scan(&chatUserID); err != nil {
		return nil, uuid.Nil, fmt.Errorf("support chat not found: %w", err)
	}

	var m AdminSupportMessage
	now := time.Now().UTC()
	err = tx.QueryRow(ctx, `
		INSERT INTO support_messages (id, support_chat_id, sender_id, sender_kind, body, created_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5)
		RETURNING id, support_chat_id, sender_id, sender_kind, body, created_at
	`, chatID, senderID, kind, body, now).Scan(
		&m.ID, &m.SupportChatID, &m.SenderID, &m.SenderKind, &m.Body, &m.CreatedAt,
	)
	if err != nil {
		return nil, uuid.Nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE support_chats SET last_message_at = $2, updated_at = $2 WHERE id = $1`, chatID, now); err != nil {
		return nil, uuid.Nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, uuid.Nil, err
	}
	return &m, chatUserID, nil
}

func (r *AdminRepository) GetRentDetail(ctx context.Context, id uuid.UUID) (*AdminRentRow, error) {
	var rent AdminRentRow
	row := r.db.Pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT %s
		FROM lease_requests lr
		JOIN cars  c ON c.id = lr.listing_id
		JOIN users d ON d.id = lr.driver_id
		JOIN users o ON o.id = lr.owner_id
		LEFT JOIN payments p ON p.lease_request_id = lr.id
		LEFT JOIN vehicle_returns vr ON vr.lease_request_id = lr.id
		WHERE lr.id = $1
	`, adminRentSelectCols), id)
	if err := scanAdminRent(row, &rent); err != nil {
		return nil, err
	}
	return &rent, nil
}

// ========== ACCIDENTS ==========

// AdminAccidentRow is the shaped response for admin list + detail views.
type AdminAccidentRow struct {
	ID                  uuid.UUID                   `json:"id"`
	ReporterID          uuid.UUID                   `json:"reporter_id"`
	ReporterName        string                      `json:"reporter_name"`
	ReporterEmail       string                      `json:"reporter_email"`
	RelatedChatID       *uuid.UUID                  `json:"related_chat_id,omitempty"`
	RelatedCarID        *uuid.UUID                  `json:"related_car_id,omitempty"`
	CarTitle            *string                     `json:"car_title,omitempty"`
	Status              models.AccidentStatus       `json:"status"`
	Driver1Info         *models.DriverInfo          `json:"driver1_info,omitempty"`
	Driver2Info         *models.DriverInfo          `json:"driver2_info,omitempty"`
	VehicleDamage       *models.VehicleDamage       `json:"vehicle_damage,omitempty"`
	AccidentDescription string                      `json:"accident_description"`
	InsuranceInfo       *models.InsuranceInfo       `json:"insurance_info,omitempty"`
	OtherInfo           *models.OtherInfo           `json:"other_info,omitempty"`
	SignatureURL        string                      `json:"signature_url"`
	SignatureSignedAt   *time.Time                  `json:"signature_signed_at,omitempty"`
	SubmittedAt         *time.Time                  `json:"submitted_at,omitempty"`
	Attachments         []models.AccidentAttachment `json:"attachments"`
	CreatedAt           time.Time                   `json:"created_at"`
	UpdatedAt           time.Time                   `json:"updated_at"`
}

type AdminAccidentsPage struct {
	Items []AdminAccidentRow `json:"items"`
	Total int                `json:"total"`
	Page  int                `json:"page"`
	Limit int                `json:"limit"`
}

func (r *AdminRepository) ListAccidents(ctx context.Context, page, limit int, status string) (*AdminAccidentsPage, error) {
	offset := (page - 1) * limit

	// Count query uses its own arg slice so it never contaminates the main query args.
	var totalCount int
	countQuery := `SELECT COUNT(*) FROM accidents`
	countArgs := []any{}
	if status != "" {
		countQuery += ` WHERE status = $1`
		countArgs = append(countArgs, status)
	} else {
		// Exclude drafts from the default admin view — they are unsubmitted work-in-progress.
		countQuery += ` WHERE status != 'draft'`
	}
	if err := r.db.Pool.QueryRow(ctx, countQuery, countArgs...).Scan(&totalCount); err != nil {
		return nil, fmt.Errorf("count accidents: %w", err)
	}

	query := `
		SELECT a.id, a.reporter_id,
		       COALESCE(u.first_name || ' ' || u.last_name, u.email) AS reporter_name,
		       u.email AS reporter_email,
		       a.related_chat_id, a.related_car_id,
		       c.make || ' ' || c.model || ' ' || c.year::text AS car_title,
		       a.status, a.driver1_info, a.submitted_at, a.created_at, a.updated_at
		FROM accidents a
		JOIN users u ON u.id = a.reporter_id
		LEFT JOIN cars c ON c.id = a.related_car_id`

	mainArgs := []any{}
	argIdx := 1
	if status != "" {
		query += fmt.Sprintf(" WHERE a.status = $%d", argIdx)
		mainArgs = append(mainArgs, status)
		argIdx++
	} else {
		query += " WHERE a.status != 'draft'"
	}
	mainArgs = append(mainArgs, limit, offset)
	query += fmt.Sprintf(" ORDER BY a.created_at DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)

	rows, err := r.db.Pool.Query(ctx, query, mainArgs...)
	if err != nil {
		return nil, fmt.Errorf("list accidents: %w", err)
	}
	defer rows.Close()

	items := []AdminAccidentRow{}
	for rows.Next() {
		var a AdminAccidentRow
		var d1 []byte
		var carTitle *string
		err := rows.Scan(
			&a.ID, &a.ReporterID, &a.ReporterName, &a.ReporterEmail,
			&a.RelatedChatID, &a.RelatedCarID, &carTitle,
			&a.Status, &d1, &a.SubmittedAt, &a.CreatedAt, &a.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		a.CarTitle = carTitle
		if d1 != nil {
			a.Driver1Info = new(models.DriverInfo)
			jsonUnmarshal(d1, a.Driver1Info)
		}
		items = append(items, a)
	}

	return &AdminAccidentsPage{Items: items, Total: totalCount, Page: page, Limit: limit}, nil
}

func (r *AdminRepository) GetAccident(ctx context.Context, id uuid.UUID) (*AdminAccidentRow, error) {
	var a AdminAccidentRow
	var d1, d2, vd, ins, oth []byte
	var carTitle *string

	err := r.db.Pool.QueryRow(ctx, `
		SELECT a.id, a.reporter_id,
		       COALESCE(u.first_name || ' ' || u.last_name, u.email),
		       u.email,
		       a.related_chat_id, a.related_car_id,
		       c.make || ' ' || c.model || ' ' || c.year::text,
		       a.status, a.driver1_info, a.driver2_info, a.vehicle_damage,
		       COALESCE(a.accident_description, ''),
		       a.insurance_info, a.other_info,
		       COALESCE(a.signature_url, ''),
		       a.signature_signed_at, a.submitted_at,
		       a.created_at, a.updated_at
		FROM accidents a
		JOIN users u ON u.id = a.reporter_id
		LEFT JOIN cars c ON c.id = a.related_car_id
		WHERE a.id = $1
	`, id).Scan(
		&a.ID, &a.ReporterID, &a.ReporterName, &a.ReporterEmail,
		&a.RelatedChatID, &a.RelatedCarID, &carTitle,
		&a.Status, &d1, &d2, &vd,
		&a.AccidentDescription, &ins, &oth,
		&a.SignatureURL, &a.SignatureSignedAt, &a.SubmittedAt,
		&a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	a.CarTitle = carTitle
	if d1 != nil {
		a.Driver1Info = new(models.DriverInfo)
		jsonUnmarshal(d1, a.Driver1Info)
	}
	if d2 != nil {
		a.Driver2Info = new(models.DriverInfo)
		jsonUnmarshal(d2, a.Driver2Info)
	}
	if vd != nil {
		a.VehicleDamage = new(models.VehicleDamage)
		jsonUnmarshal(vd, a.VehicleDamage)
	}
	if ins != nil {
		a.InsuranceInfo = new(models.InsuranceInfo)
		jsonUnmarshal(ins, a.InsuranceInfo)
	}
	if oth != nil {
		a.OtherInfo = new(models.OtherInfo)
		jsonUnmarshal(oth, a.OtherInfo)
	}

	// Load attachments
	attRows, err := r.db.Pool.Query(ctx, `
		SELECT id, accident_id, slot, file_url, file_size, mime_type, created_at
		FROM accident_attachments
		WHERE accident_id = $1
		ORDER BY created_at ASC
	`, id)
	if err == nil {
		defer attRows.Close()
		for attRows.Next() {
			var att models.AccidentAttachment
			attRows.Scan(&att.ID, &att.AccidentID, &att.Slot, &att.FileURL, &att.FileSize, &att.MimeType, &att.CreatedAt)
			a.Attachments = append(a.Attachments, att)
		}
	}
	if a.Attachments == nil {
		a.Attachments = []models.AccidentAttachment{}
	}

	return &a, nil
}

func (r *AdminRepository) UpdateAccidentStatus(ctx context.Context, id uuid.UUID, status models.AccidentStatus) error {
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE accidents SET status = $1, updated_at = NOW() WHERE id = $2
	`, string(status), id)
	return err
}

// jsonUnmarshal is a nil-safe json.Unmarshal helper for JSONB columns.
func jsonUnmarshal(data []byte, v any) {
	if len(data) > 0 {
		_ = json.Unmarshal(data, v)
	}
}
