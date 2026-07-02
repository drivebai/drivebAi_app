package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
)

type CarRepository struct {
	db *database.DB
}

func NewCarRepository(db *database.DB) *CarRepository {
	return &CarRepository{db: db}
}

// Create creates a new car listing
func (r *CarRepository) Create(ctx context.Context, car *models.Car) error {
	query := `
		INSERT INTO cars (
			id, owner_id, title, description,
			vin, make, model, year, body_type, fuel_type, mileage,
			address, neighborhood, latitude, longitude, area, street, block, zip,
			is_for_rent, weekly_rent_price, is_for_sale, sale_price, currency,
			min_years_licensed, deposit_amount, insurance_coverage,
			status, is_paused, rented_weeks, total_earned,
			created_at, updated_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8, $9, $10, $11,
			$12, $13, $14, $15, $16, $17, $18, $19,
			$20, $21, $22, $23, $24,
			$25, $26, $27,
			$28, $29, $30, $31,
			$32, $33
		)
	`

	_, err := r.db.Pool.Exec(ctx, query,
		car.ID, car.OwnerID, car.Title, car.Description,
		car.VIN, car.Make, car.Model, car.Year, car.BodyType, car.FuelType, car.Mileage,
		car.Address, car.Neighborhood, car.Latitude, car.Longitude, car.Area, car.Street, car.Block, car.Zip,
		car.IsForRent, car.WeeklyRentPrice, car.IsForSale, car.SalePrice, car.Currency,
		car.MinYearsLicensed, car.DepositAmount, car.InsuranceCoverage,
		car.Status, car.IsPaused, car.RentedWeeks, car.TotalEarned,
		car.CreatedAt, car.UpdatedAt,
	)

	return err
}

// ExistsByVIN reports whether any car already has the given VIN
// (case-insensitive). Empty VINs always return false — the partial unique
// index `cars_vin_unique_lower_idx` excludes NULL/'' as well, so the two
// agree. Callers should normalize the VIN (trim + uppercase) before invoking
// to stay aligned with what's written on insert.
func (r *CarRepository) ExistsByVIN(ctx context.Context, vin string) (bool, error) {
	if strings.TrimSpace(vin) == "" {
		return false, nil
	}
	const query = `SELECT EXISTS (SELECT 1 FROM cars WHERE LOWER(vin) = LOWER($1) AND vin IS NOT NULL AND vin <> '')`
	var exists bool
	if err := r.db.Pool.QueryRow(ctx, query, vin).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// ExistsByVINExcludingID is the UpdateCar-side companion to ExistsByVIN: it
// checks for any OTHER car (id != excludeID) holding this VIN. Lets an owner
// PATCH unrelated fields on their own listing without false-positive 409s when
// the VIN field is round-tripped unchanged.
func (r *CarRepository) ExistsByVINExcludingID(ctx context.Context, vin string, excludeID uuid.UUID) (bool, error) {
	if strings.TrimSpace(vin) == "" {
		return false, nil
	}
	const query = `SELECT EXISTS (SELECT 1 FROM cars WHERE LOWER(vin) = LOWER($1) AND vin IS NOT NULL AND vin <> '' AND id <> $2)`
	var exists bool
	if err := r.db.Pool.QueryRow(ctx, query, vin, excludeID).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// SetApproved sets is_approved on a car. Used by the auto-approve path when AUTO_APPROVE_CARS=true.
func (r *CarRepository) SetApproved(ctx context.Context, id uuid.UUID, approved bool) error {
	_, err := r.db.Pool.Exec(ctx,
		`UPDATE cars SET is_approved = $2, updated_at = NOW() WHERE id = $1`, id, approved)
	return err
}

// GetByID retrieves a car by its ID
func (r *CarRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.Car, error) {
	query := `
		SELECT
			id, owner_id, title, description,
			vin, make, model, year, body_type, fuel_type, mileage,
			address, neighborhood, latitude, longitude, area, street, block, zip,
			is_for_rent, weekly_rent_price, is_for_sale, sale_price, currency,
			min_years_licensed, deposit_amount, insurance_coverage,
			status, is_paused, rented_weeks, total_earned,
			created_at, updated_at
		FROM cars
		WHERE id = $1
	`

	var car models.Car
	err := r.db.Pool.QueryRow(ctx, query, id).Scan(
		&car.ID, &car.OwnerID, &car.Title, &car.Description,
		&car.VIN, &car.Make, &car.Model, &car.Year, &car.BodyType, &car.FuelType, &car.Mileage,
		&car.Address, &car.Neighborhood, &car.Latitude, &car.Longitude, &car.Area, &car.Street, &car.Block, &car.Zip,
		&car.IsForRent, &car.WeeklyRentPrice, &car.IsForSale, &car.SalePrice, &car.Currency,
		&car.MinYearsLicensed, &car.DepositAmount, &car.InsuranceCoverage,
		&car.Status, &car.IsPaused, &car.RentedWeeks, &car.TotalEarned,
		&car.CreatedAt, &car.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return &car, nil
}

// OwnerCarActiveRental is the shape returned by GetByOwnerIDWithActiveRental
// alongside each *models.Car when a lease is currently active on that car.
// "Active" means: the lease is paid, pickup was confirmed, and the vehicle
// hasn't been returned yet.
//
// EffectiveWeeklyPriceCents mirrors LeaseRequest.TotalAmountCents: it
// coalesces offered_weekly_price with weekly_price and converts dollars → cents.
type OwnerCarActiveRental struct {
	LeaseRequestID           uuid.UUID
	DriverID                 uuid.UUID
	DriverName               string
	Weeks                    int
	EffectiveWeeklyPriceCents int64
	PickupConfirmedAt        time.Time
}

// GetByOwnerIDWithActiveRental is a cover over GetByOwnerID that additionally
// left-joins each car row with the lease currently occupying it (if any). It
// returns parallel slices so the caller can zip them 1:1; the rental pointer
// is nil for any car that is not currently rented.
//
// This is the query behind the owner's My Cars grid — the rental sub-object
// is what drives the "Rented to Jamie R. · 4 weeks · $180/wk" line and the
// derived Rented/Reserved status chip on the client. Discovery + admin
// endpoints do NOT use this path; they take the plain GetByOwnerID above.
func (r *CarRepository) GetByOwnerIDWithActiveRental(ctx context.Context, ownerID uuid.UUID) ([]*models.Car, []*OwnerCarActiveRental, error) {
	query := `
		SELECT
			c.id, c.owner_id, c.title, c.description,
			c.vin, c.make, c.model, c.year, c.body_type, c.fuel_type, c.mileage,
			c.address, c.neighborhood, c.latitude, c.longitude, c.area, c.street, c.block, c.zip,
			c.is_for_rent, c.weekly_rent_price, c.is_for_sale, c.sale_price, c.currency,
			c.min_years_licensed, c.deposit_amount, c.insurance_coverage,
			c.status, c.is_paused, c.rented_weeks, c.total_earned,
			c.created_at, c.updated_at,
			lr.id, lr.driver_id, lr.weeks,
			COALESCE(lr.offered_weekly_price, lr.weekly_price),
			lr.pickup_confirmed_at,
			u.first_name, u.last_name
		FROM cars c
		LEFT JOIN lease_requests lr
		       ON lr.id = c.reserved_by_lease_request_id
		      AND lr.status = 'paid'
		      AND lr.pickup_confirmed_at IS NOT NULL
		      AND lr.vehicle_returned_at IS NULL
		LEFT JOIN users u
		       ON u.id = lr.driver_id
		WHERE c.owner_id = $1
		ORDER BY c.created_at DESC
	`

	rows, err := r.db.Pool.Query(ctx, query, ownerID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var cars []*models.Car
	var rentals []*OwnerCarActiveRental
	for rows.Next() {
		var car models.Car
		var (
			leaseID           *uuid.UUID
			driverID          *uuid.UUID
			weeks             *int
			weeklyPriceDollars *float64
			pickupConfirmedAt *time.Time
			firstName         *string
			lastName          *string
		)
		if err := rows.Scan(
			&car.ID, &car.OwnerID, &car.Title, &car.Description,
			&car.VIN, &car.Make, &car.Model, &car.Year, &car.BodyType, &car.FuelType, &car.Mileage,
			&car.Address, &car.Neighborhood, &car.Latitude, &car.Longitude, &car.Area, &car.Street, &car.Block, &car.Zip,
			&car.IsForRent, &car.WeeklyRentPrice, &car.IsForSale, &car.SalePrice, &car.Currency,
			&car.MinYearsLicensed, &car.DepositAmount, &car.InsuranceCoverage,
			&car.Status, &car.IsPaused, &car.RentedWeeks, &car.TotalEarned,
			&car.CreatedAt, &car.UpdatedAt,
			&leaseID, &driverID, &weeks,
			&weeklyPriceDollars,
			&pickupConfirmedAt,
			&firstName, &lastName,
		); err != nil {
			return nil, nil, err
		}
		cars = append(cars, &car)

		// A NULL lease id means the LEFT JOIN found no active rental for
		// this car — all other rental fields will also be NULL.
		if leaseID == nil || weeks == nil || weeklyPriceDollars == nil || pickupConfirmedAt == nil || driverID == nil {
			rentals = append(rentals, nil)
			continue
		}

		name := ""
		if firstName != nil {
			name = *firstName
		}
		if lastName != nil && *lastName != "" {
			if name != "" {
				name += " "
			}
			name += *lastName
		}

		rentals = append(rentals, &OwnerCarActiveRental{
			LeaseRequestID:           *leaseID,
			DriverID:                 *driverID,
			DriverName:               name,
			Weeks:                    *weeks,
			EffectiveWeeklyPriceCents: int64(*weeklyPriceDollars * 100),
			PickupConfirmedAt:        *pickupConfirmedAt,
		})
	}

	return cars, rentals, nil
}

// GetByOwnerID retrieves all cars for a specific owner
func (r *CarRepository) GetByOwnerID(ctx context.Context, ownerID uuid.UUID) ([]*models.Car, error) {
	query := `
		SELECT
			id, owner_id, title, description,
			vin, make, model, year, body_type, fuel_type, mileage,
			address, neighborhood, latitude, longitude, area, street, block, zip,
			is_for_rent, weekly_rent_price, is_for_sale, sale_price, currency,
			min_years_licensed, deposit_amount, insurance_coverage,
			status, is_paused, rented_weeks, total_earned,
			created_at, updated_at
		FROM cars
		WHERE owner_id = $1
		ORDER BY created_at DESC
	`

	rows, err := r.db.Pool.Query(ctx, query, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cars []*models.Car
	for rows.Next() {
		var car models.Car
		err := rows.Scan(
			&car.ID, &car.OwnerID, &car.Title, &car.Description,
			&car.VIN, &car.Make, &car.Model, &car.Year, &car.BodyType, &car.FuelType, &car.Mileage,
			&car.Address, &car.Neighborhood, &car.Latitude, &car.Longitude, &car.Area, &car.Street, &car.Block, &car.Zip,
			&car.IsForRent, &car.WeeklyRentPrice, &car.IsForSale, &car.SalePrice, &car.Currency,
			&car.MinYearsLicensed, &car.DepositAmount, &car.InsuranceCoverage,
			&car.Status, &car.IsPaused, &car.RentedWeeks, &car.TotalEarned,
			&car.CreatedAt, &car.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		cars = append(cars, &car)
	}

	return cars, nil
}

// Update updates a car listing
func (r *CarRepository) Update(ctx context.Context, car *models.Car) error {
	query := `
		UPDATE cars SET
			title = $2, description = $3,
			vin = $4,
			make = $5, model = $6, year = $7, body_type = $8, fuel_type = $9, mileage = $10,
			address = $11, neighborhood = $12, latitude = $13, longitude = $14,
			area = $15, street = $16, block = $17, zip = $18,
			is_for_rent = $19, weekly_rent_price = $20, is_for_sale = $21, sale_price = $22,
			min_years_licensed = $23, deposit_amount = $24, insurance_coverage = $25,
			status = $26, is_paused = $27
		WHERE id = $1
	`

	result, err := r.db.Pool.Exec(ctx, query,
		car.ID, car.Title, car.Description,
		car.VIN,
		car.Make, car.Model, car.Year, car.BodyType, car.FuelType, car.Mileage,
		car.Address, car.Neighborhood, car.Latitude, car.Longitude,
		car.Area, car.Street, car.Block, car.Zip,
		car.IsForRent, car.WeeklyRentPrice, car.IsForSale, car.SalePrice,
		car.MinYearsLicensed, car.DepositAmount, car.InsuranceCoverage,
		car.Status, car.IsPaused,
	)

	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("car not found")
	}

	return nil
}

// Delete deletes a car listing
func (r *CarRepository) Delete(ctx context.Context, id uuid.UUID) error {
	query := `DELETE FROM cars WHERE id = $1`
	result, err := r.db.Pool.Exec(ctx, query, id)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("car not found")
	}

	return nil
}

// UpdateStatus updates only the status and is_paused fields
func (r *CarRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status models.CarListingStatus, isPaused bool) error {
	query := `UPDATE cars SET status = $2, is_paused = $3 WHERE id = $1`
	result, err := r.db.Pool.Exec(ctx, query, id, status, isPaused)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("car not found")
	}

	return nil
}

// GetAvailableListings retrieves all available car listings for drivers to browse
func (r *CarRepository) GetAvailableListings(ctx context.Context, status string, search string) ([]*models.Car, error) {
	query := `
		SELECT
			c.id, c.owner_id, c.title, c.description,
			c.vin, c.make, c.model, c.year, c.body_type, c.fuel_type, c.mileage,
			c.address, c.neighborhood, c.latitude, c.longitude, c.area, c.street, c.block, c.zip,
			c.is_for_rent, c.weekly_rent_price, c.is_for_sale, c.sale_price, c.currency,
			c.min_years_licensed, c.deposit_amount, c.insurance_coverage,
			c.status, c.is_paused, c.rented_weeks, c.total_earned,
			c.created_at, c.updated_at
		FROM cars c
		JOIN users u ON u.id = c.owner_id
		WHERE c.is_paused = false
		  AND c.is_approved = true
		  AND u.is_blocked = false
		  AND c.reserved_by_lease_request_id IS NULL
		  AND c.reserved_by_purchase_request_id IS NULL
		  AND c.status <> 'sold'
	`

	args := []interface{}{}
	argIndex := 1

	// Filter by status if provided
	if status != "" && status != "all" {
		query += fmt.Sprintf(" AND c.status = $%d", argIndex)
		args = append(args, status)
		argIndex++
	}

	// Search by make, model, or title
	if search != "" {
		query += fmt.Sprintf(" AND (LOWER(c.make) LIKE $%d OR LOWER(c.model) LIKE $%d OR LOWER(c.title) LIKE $%d)", argIndex, argIndex, argIndex)
		args = append(args, "%"+strings.ToLower(search)+"%")
	}

	query += " ORDER BY c.created_at DESC"

	rows, err := r.db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cars []*models.Car
	for rows.Next() {
		var car models.Car
		err := rows.Scan(
			&car.ID, &car.OwnerID, &car.Title, &car.Description,
			&car.VIN, &car.Make, &car.Model, &car.Year, &car.BodyType, &car.FuelType, &car.Mileage,
			&car.Address, &car.Neighborhood, &car.Latitude, &car.Longitude, &car.Area, &car.Street, &car.Block, &car.Zip,
			&car.IsForRent, &car.WeeklyRentPrice, &car.IsForSale, &car.SalePrice, &car.Currency,
			&car.MinYearsLicensed, &car.DepositAmount, &car.InsuranceCoverage,
			&car.Status, &car.IsPaused, &car.RentedWeeks, &car.TotalEarned,
			&car.CreatedAt, &car.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		cars = append(cars, &car)
	}

	return cars, nil
}

// UpdateLocation updates only the location fields for a car
func (r *CarRepository) UpdateLocation(ctx context.Context, id uuid.UUID, lat, lng float64, area, street, block, zip string) error {
	query := `
		UPDATE cars SET
			latitude = $2, longitude = $3,
			area = $4, street = $5, block = $6, zip = $7
		WHERE id = $1
	`
	result, err := r.db.Pool.Exec(ctx, query, id, lat, lng, area, street, block, zip)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("car not found")
	}
	return nil
}

// CarPhotoRepository handles car photo database operations
type CarPhotoRepository struct {
	db *database.DB
}

func NewCarPhotoRepository(db *database.DB) *CarPhotoRepository {
	return &CarPhotoRepository{db: db}
}

// Create creates a new car photo record
func (r *CarPhotoRepository) Create(ctx context.Context, photo *models.CarPhoto) error {
	query := `
		INSERT INTO car_photos (id, car_id, slot_type, file_path, file_url, file_size, mime_type, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`

	_, err := r.db.Pool.Exec(ctx, query,
		photo.ID, photo.CarID, photo.SlotType, photo.FilePath, photo.FileURL,
		photo.FileSize, photo.MimeType, photo.CreatedAt, photo.UpdatedAt,
	)

	return err
}

// GetByCarID retrieves all photos for a car
func (r *CarPhotoRepository) GetByCarID(ctx context.Context, carID uuid.UUID) ([]models.CarPhoto, error) {
	query := `
		SELECT id, car_id, slot_type, file_path, file_url, file_size, mime_type, created_at, updated_at
		FROM car_photos
		WHERE car_id = $1
		ORDER BY slot_type
	`

	rows, err := r.db.Pool.Query(ctx, query, carID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var photos []models.CarPhoto
	for rows.Next() {
		var photo models.CarPhoto
		err := rows.Scan(
			&photo.ID, &photo.CarID, &photo.SlotType, &photo.FilePath, &photo.FileURL,
			&photo.FileSize, &photo.MimeType, &photo.CreatedAt, &photo.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		photos = append(photos, photo)
	}

	return photos, nil
}

// GetByCarIDAndSlot retrieves a specific photo by car ID and slot type
func (r *CarPhotoRepository) GetByCarIDAndSlot(ctx context.Context, carID uuid.UUID, slotType models.PhotoSlotType) (*models.CarPhoto, error) {
	query := `
		SELECT id, car_id, slot_type, file_path, file_url, file_size, mime_type, created_at, updated_at
		FROM car_photos
		WHERE car_id = $1 AND slot_type = $2
	`

	var photo models.CarPhoto
	err := r.db.Pool.QueryRow(ctx, query, carID, slotType).Scan(
		&photo.ID, &photo.CarID, &photo.SlotType, &photo.FilePath, &photo.FileURL,
		&photo.FileSize, &photo.MimeType, &photo.CreatedAt, &photo.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return &photo, nil
}

// GetByID retrieves a photo by its ID
func (r *CarPhotoRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.CarPhoto, error) {
	query := `
		SELECT id, car_id, slot_type, file_path, file_url, file_size, mime_type, created_at, updated_at
		FROM car_photos
		WHERE id = $1
	`

	var photo models.CarPhoto
	err := r.db.Pool.QueryRow(ctx, query, id).Scan(
		&photo.ID, &photo.CarID, &photo.SlotType, &photo.FilePath, &photo.FileURL,
		&photo.FileSize, &photo.MimeType, &photo.CreatedAt, &photo.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return &photo, nil
}

// Upsert creates or updates a photo for a specific slot
func (r *CarPhotoRepository) Upsert(ctx context.Context, photo *models.CarPhoto) error {
	query := `
		INSERT INTO car_photos (id, car_id, slot_type, file_path, file_url, file_size, mime_type, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (car_id, slot_type)
		DO UPDATE SET
			file_path = EXCLUDED.file_path,
			file_url = EXCLUDED.file_url,
			file_size = EXCLUDED.file_size,
			mime_type = EXCLUDED.mime_type,
			updated_at = EXCLUDED.updated_at
	`

	_, err := r.db.Pool.Exec(ctx, query,
		photo.ID, photo.CarID, photo.SlotType, photo.FilePath, photo.FileURL,
		photo.FileSize, photo.MimeType, photo.CreatedAt, photo.UpdatedAt,
	)

	return err
}

// Delete deletes a photo by ID
func (r *CarPhotoRepository) Delete(ctx context.Context, id uuid.UUID) error {
	query := `DELETE FROM car_photos WHERE id = $1`
	_, err := r.db.Pool.Exec(ctx, query, id)
	return err
}

// DeleteByCarID deletes all photos for a car
func (r *CarPhotoRepository) DeleteByCarID(ctx context.Context, carID uuid.UUID) error {
	query := `DELETE FROM car_photos WHERE car_id = $1`
	_, err := r.db.Pool.Exec(ctx, query, carID)
	return err
}

// CarDocumentRepository handles car document database operations
type CarDocumentRepository struct {
	db *database.DB
}

func NewCarDocumentRepository(db *database.DB) *CarDocumentRepository {
	return &CarDocumentRepository{db: db}
}

// Create creates a new car document record
func (r *CarDocumentRepository) Create(ctx context.Context, doc *models.CarDocument) error {
	query := `
		INSERT INTO car_documents (id, car_id, document_type, file_name, file_path, file_url, file_size, mime_type, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`

	_, err := r.db.Pool.Exec(ctx, query,
		doc.ID, doc.CarID, doc.DocumentType, doc.FileName, doc.FilePath, doc.FileURL,
		doc.FileSize, doc.MimeType, doc.CreatedAt, doc.UpdatedAt,
	)

	return err
}

// GetByCarID retrieves all documents for a car
func (r *CarDocumentRepository) GetByCarID(ctx context.Context, carID uuid.UUID) ([]models.CarDocument, error) {
	query := `
		SELECT id, car_id, document_type, file_name, file_path, file_url, file_size, mime_type, created_at, updated_at
		FROM car_documents
		WHERE car_id = $1
		ORDER BY document_type
	`

	rows, err := r.db.Pool.Query(ctx, query, carID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var docs []models.CarDocument
	for rows.Next() {
		var doc models.CarDocument
		err := rows.Scan(
			&doc.ID, &doc.CarID, &doc.DocumentType, &doc.FileName, &doc.FilePath, &doc.FileURL,
			&doc.FileSize, &doc.MimeType, &doc.CreatedAt, &doc.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}

	return docs, nil
}

// GetByID retrieves a document by its ID
func (r *CarDocumentRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.CarDocument, error) {
	query := `
		SELECT id, car_id, document_type, file_name, file_path, file_url, file_size, mime_type, created_at, updated_at
		FROM car_documents
		WHERE id = $1
	`

	var doc models.CarDocument
	err := r.db.Pool.QueryRow(ctx, query, id).Scan(
		&doc.ID, &doc.CarID, &doc.DocumentType, &doc.FileName, &doc.FilePath, &doc.FileURL,
		&doc.FileSize, &doc.MimeType, &doc.CreatedAt, &doc.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return &doc, nil
}

// Delete deletes a document by ID
func (r *CarDocumentRepository) Delete(ctx context.Context, id uuid.UUID) error {
	query := `DELETE FROM car_documents WHERE id = $1`
	_, err := r.db.Pool.Exec(ctx, query, id)
	return err
}

// DeleteByCarID deletes all documents for a car
func (r *CarDocumentRepository) DeleteByCarID(ctx context.Context, carID uuid.UUID) error {
	query := `DELETE FROM car_documents WHERE car_id = $1`
	_, err := r.db.Pool.Exec(ctx, query, carID)
	return err
}
