package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
	"github.com/google/uuid"
)

// AccidentRepository handles user-facing accident CRUD.
// Admin queries are in AdminRepository.
type AccidentRepository struct {
	db *database.DB
}

func NewAccidentRepository(db *database.DB) *AccidentRepository {
	return &AccidentRepository{db: db}
}

// Create inserts a new draft accident for the given user.
func (r *AccidentRepository) Create(ctx context.Context, reporterID uuid.UUID, chatID, carID *uuid.UUID) (*models.Accident, error) {
	var a models.Accident
	err := r.db.Pool.QueryRow(ctx, `
		INSERT INTO accidents (id, reporter_id, related_chat_id, related_car_id, status, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, 'draft', NOW(), NOW())
		RETURNING id, reporter_id, related_chat_id, related_car_id, status, created_at, updated_at
	`, reporterID, chatID, carID).Scan(
		&a.ID, &a.ReporterID, &a.RelatedChatID, &a.RelatedCarID,
		&a.Status, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create accident: %w", err)
	}
	a.Attachments = []models.AccidentAttachment{}
	return &a, nil
}

// GetDraftForUser returns the most recent draft for the user, optionally filtered by chatID and carID.
// Returns pgx.ErrNoRows (wrapped) when no matching draft exists.
func (r *AccidentRepository) GetDraftForUser(ctx context.Context, userID uuid.UUID, chatID, carID *uuid.UUID) (*models.Accident, error) {
	query := `SELECT id, reporter_id, related_chat_id, related_car_id, status,
	                 driver1_info, driver2_info, vehicle_damage,
	                 COALESCE(accident_description, ''), insurance_info, other_info,
	                 COALESCE(signature_url, ''), signature_signed_at, submitted_at,
	                 created_at, updated_at
	          FROM accidents
	          WHERE reporter_id = $1 AND status = 'draft'`
	args := []any{userID}
	argIdx := 2
	if chatID != nil {
		query += fmt.Sprintf(" AND related_chat_id = $%d", argIdx)
		args = append(args, *chatID)
		argIdx++
	}
	if carID != nil {
		query += fmt.Sprintf(" AND related_car_id = $%d", argIdx)
		args = append(args, *carID)
		argIdx++
	}
	_ = argIdx
	query += " ORDER BY created_at DESC LIMIT 1"
	return scanAccident(r.db.Pool.QueryRow(ctx, query, args...))
}

// GetByIDForUser fetches a single accident and verifies it belongs to userID.
func (r *AccidentRepository) GetByIDForUser(ctx context.Context, accidentID, userID uuid.UUID) (*models.Accident, error) {
	row := r.db.Pool.QueryRow(ctx, `
		SELECT id, reporter_id, related_chat_id, related_car_id, status,
		       driver1_info, driver2_info, vehicle_damage,
		       COALESCE(accident_description, ''), insurance_info, other_info,
		       COALESCE(signature_url, ''), signature_signed_at, submitted_at,
		       created_at, updated_at
		FROM accidents
		WHERE id = $1 AND reporter_id = $2
	`, accidentID, userID)
	return scanAccident(row)
}

// ListForUser returns all accidents belonging to a user (newest first).
func (r *AccidentRepository) ListForUser(ctx context.Context, userID uuid.UUID) ([]models.Accident, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT id, reporter_id, related_chat_id, related_car_id, status,
		       driver1_info, driver2_info, vehicle_damage,
		       COALESCE(accident_description, ''), insurance_info, other_info,
		       COALESCE(signature_url, ''), signature_signed_at, submitted_at,
		       created_at, updated_at
		FROM accidents
		WHERE reporter_id = $1
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list accidents: %w", err)
	}
	defer rows.Close()

	out := []models.Accident{}
	for rows.Next() {
		a, err := scanAccident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, nil
}

// Update applies partial step-data patches to an accident.
// Only non-nil pointers are updated (zero-value string patches are allowed).
type AccidentPatch struct {
	Driver1Info         *models.DriverInfo
	Driver2Info         *models.DriverInfo
	VehicleDamage       *models.VehicleDamage
	AccidentDescription *string
	InsuranceInfo       *models.InsuranceInfo
	OtherInfo           *models.OtherInfo
}

func (r *AccidentRepository) Update(ctx context.Context, accidentID, userID uuid.UUID, p AccidentPatch) (*models.Accident, error) {
	// Build SET clauses dynamically
	sets := []string{"updated_at = NOW()"}
	args := []any{}
	argIdx := 1

	add := func(col string, v any) {
		b, _ := json.Marshal(v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, argIdx))
		args = append(args, string(b))
		argIdx++
	}
	addText := func(col string, v string) {
		sets = append(sets, fmt.Sprintf("%s = $%d", col, argIdx))
		args = append(args, v)
		argIdx++
	}

	if p.Driver1Info != nil {
		add("driver1_info", p.Driver1Info)
	}
	if p.Driver2Info != nil {
		add("driver2_info", p.Driver2Info)
	}
	if p.VehicleDamage != nil {
		add("vehicle_damage", p.VehicleDamage)
	}
	if p.AccidentDescription != nil {
		addText("accident_description", *p.AccidentDescription)
	}
	if p.InsuranceInfo != nil {
		add("insurance_info", p.InsuranceInfo)
	}
	if p.OtherInfo != nil {
		add("other_info", p.OtherInfo)
	}

	query := "UPDATE accidents SET "
	for i, s := range sets {
		if i > 0 {
			query += ", "
		}
		query += s
	}
	args = append(args, accidentID, userID)
	query += fmt.Sprintf(" WHERE id = $%d AND reporter_id = $%d AND status = 'draft'", argIdx, argIdx+1)

	_, err := r.db.Pool.Exec(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("update accident: %w", err)
	}

	return r.GetByIDForUser(ctx, accidentID, userID)
}

// SetSignature stores the signature file URL after upload.
func (r *AccidentRepository) SetSignature(ctx context.Context, accidentID, userID uuid.UUID, fileURL string) error {
	now := time.Now().UTC()
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE accidents
		SET signature_url = $1, signature_signed_at = $2, updated_at = NOW()
		WHERE id = $3 AND reporter_id = $4
	`, fileURL, now, accidentID, userID)
	return err
}

// Submit transitions the accident from draft → submitted.
func (r *AccidentRepository) Submit(ctx context.Context, accidentID, userID uuid.UUID) (*models.Accident, error) {
	now := time.Now().UTC()
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE accidents
		SET status = 'submitted', submitted_at = $1, updated_at = NOW()
		WHERE id = $2 AND reporter_id = $3 AND status = 'draft'
	`, now, accidentID, userID)
	if err != nil {
		return nil, fmt.Errorf("submit accident: %w", err)
	}
	return r.GetByIDForUser(ctx, accidentID, userID)
}

// AddAttachment inserts a new file record for the accident.
func (r *AccidentRepository) AddAttachment(ctx context.Context, accidentID uuid.UUID, slot models.AttachmentSlot, fileURL, filePath string, fileSize int64, mimeType string) (*models.AccidentAttachment, error) {
	var att models.AccidentAttachment
	err := r.db.Pool.QueryRow(ctx, `
		INSERT INTO accident_attachments (id, accident_id, slot, file_url, file_path, file_size, mime_type, created_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, NOW())
		RETURNING id, accident_id, slot, file_url, file_size, mime_type, created_at
	`, accidentID, string(slot), fileURL, filePath, fileSize, mimeType).Scan(
		&att.ID, &att.AccidentID, &att.Slot, &att.FileURL,
		&att.FileSize, &att.MimeType, &att.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("add attachment: %w", err)
	}
	return &att, nil
}

// DeleteAttachment removes an attachment record + returns file_path for disk cleanup.
func (r *AccidentRepository) DeleteAttachment(ctx context.Context, attachID, accidentID uuid.UUID) (string, error) {
	var filePath string
	err := r.db.Pool.QueryRow(ctx, `
		DELETE FROM accident_attachments
		WHERE id = $1 AND accident_id = $2
		RETURNING file_path
	`, attachID, accidentID).Scan(&filePath)
	if err != nil {
		return "", fmt.Errorf("delete attachment: %w", err)
	}
	return filePath, nil
}

// ListAttachments returns all attachments for an accident.
func (r *AccidentRepository) ListAttachments(ctx context.Context, accidentID uuid.UUID) ([]models.AccidentAttachment, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT id, accident_id, slot, file_url, file_size, mime_type, created_at
		FROM accident_attachments
		WHERE accident_id = $1
		ORDER BY created_at ASC
	`, accidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.AccidentAttachment{}
	for rows.Next() {
		var a models.AccidentAttachment
		if err := rows.Scan(&a.ID, &a.AccidentID, &a.Slot, &a.FileURL, &a.FileSize, &a.MimeType, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// scanRow is an interface satisfied by both pgx.Row and pgx.Rows.
type scanRow interface {
	Scan(dest ...any) error
}

func scanAccident(row scanRow) (*models.Accident, error) {
	var a models.Accident
	a.Attachments = []models.AccidentAttachment{}
	var d1, d2, vd, ins, oth []byte
	err := row.Scan(
		&a.ID, &a.ReporterID, &a.RelatedChatID, &a.RelatedCarID, &a.Status,
		&d1, &d2, &vd,
		&a.AccidentDescription, &ins, &oth,
		&a.SignatureURL, &a.SignatureSignedAt, &a.SubmittedAt,
		&a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if d1 != nil {
		a.Driver1Info = new(models.DriverInfo)
		json.Unmarshal(d1, a.Driver1Info)
	}
	if d2 != nil {
		a.Driver2Info = new(models.DriverInfo)
		json.Unmarshal(d2, a.Driver2Info)
	}
	if vd != nil {
		a.VehicleDamage = new(models.VehicleDamage)
		json.Unmarshal(vd, a.VehicleDamage)
	}
	if ins != nil {
		a.InsuranceInfo = new(models.InsuranceInfo)
		json.Unmarshal(ins, a.InsuranceInfo)
	}
	if oth != nil {
		a.OtherInfo = new(models.OtherInfo)
		json.Unmarshal(oth, a.OtherInfo)
	}
	return &a, nil
}
