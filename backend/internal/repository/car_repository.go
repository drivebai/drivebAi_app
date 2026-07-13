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

// VIN-uniqueness three-place invariant.
//
// The partial unique index `cars_vin_unique_lower_idx` (migration 000035)
// enforces VIN uniqueness over exactly the set of rows:
//
//	vin IS NOT NULL AND vin <> '' AND archived_at IS NULL AND status <> 'sold'
//
// ExistsByVIN and ExistsByVINExcludingID below MUST use the same predicate so
// the pre-flight 409 and the DB constraint agree in all three places. Two
// escape hatches deliberately free a VIN for a re-reviewed relist by a new
// owner: archiving a listing (archived_at) and completing a sale (status
// 'sold', which also auto-archives). Changing any one of the three requires
// changing the other two in lockstep.

// ExistsByVIN reports whether any live car already holds the given VIN
// (case-insensitive). Empty VINs always return false. Callers should
// normalize the VIN (trim + uppercase) before invoking so the comparison
// matches what's written on insert.
func (r *CarRepository) ExistsByVIN(ctx context.Context, vin string) (bool, error) {
	if strings.TrimSpace(vin) == "" {
		return false, nil
	}
	const query = `SELECT EXISTS (SELECT 1 FROM cars WHERE LOWER(vin) = LOWER($1) AND vin IS NOT NULL AND vin <> '' AND archived_at IS NULL AND status <> 'sold')`
	var exists bool
	if err := r.db.Pool.QueryRow(ctx, query, vin).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// ExistsByVINExcludingID is the UpdateCar-side companion to ExistsByVIN: it
// checks for any OTHER live car (id != excludeID) holding this VIN. Lets an
// owner PATCH unrelated fields on their own listing without false-positive
// 409s when the VIN field is round-tripped unchanged.
func (r *CarRepository) ExistsByVINExcludingID(ctx context.Context, vin string, excludeID uuid.UUID) (bool, error) {
	if strings.TrimSpace(vin) == "" {
		return false, nil
	}
	const query = `SELECT EXISTS (SELECT 1 FROM cars WHERE LOWER(vin) = LOWER($1) AND vin IS NOT NULL AND vin <> '' AND archived_at IS NULL AND status <> 'sold' AND id <> $2)`
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

// GetByID retrieves a car by its ID. Archived cars ARE returned (with
// ArchivedAt set) — internal flows like lease/purchase history still need
// to resolve them; user-facing handlers decide how to treat archived rows.
func (r *CarRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.Car, error) {
	query := `
		SELECT
			id, owner_id, title, description,
			vin, make, model, year, body_type, fuel_type, mileage,
			address, neighborhood, latitude, longitude, area, street, block, zip,
			is_for_rent, weekly_rent_price, is_for_sale, sale_price, currency,
			min_years_licensed, deposit_amount, insurance_coverage,
			status, is_paused, is_approved, rented_weeks, total_earned,
			archived_at, created_at, updated_at
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
		&car.Status, &car.IsPaused, &car.IsApproved, &car.RentedWeeks, &car.TotalEarned,
		&car.ArchivedAt, &car.CreatedAt, &car.UpdatedAt,
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
	LeaseRequestID            uuid.UUID
	DriverID                  uuid.UUID
	DriverName                string
	Weeks                     int
	EffectiveWeeklyPriceCents int64
	PickupConfirmedAt         time.Time
	// ChatID is the driver↔owner chat for this car, when one exists.
	// Deterministic single row via uq_chats_car_driver_owner.
	ChatID *uuid.UUID
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
	query := ownerCarWithActiveRentalSelect + `
		WHERE c.owner_id = $1 AND c.archived_at IS NULL
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
		car, rental, err := scanCarWithActiveRental(rows)
		if err != nil {
			return nil, nil, err
		}
		cars = append(cars, car)
		rentals = append(rentals, rental)
	}

	return cars, rentals, nil
}

// ownerCarWithActiveRentalSelect is the shared SELECT for the owner-facing
// car(+active rental) queries: the My Cars list (GetByOwnerIDWithActiveRental)
// and the single-car detail (GetByIDWithActiveRental). Keeping ONE join
// definition is what guarantees list/detail status parity (QA pt-3 / D9).
//
// The chats LEFT JOIN resolves the driver↔owner conversation for the active
// rental; uq_chats_car_driver_owner makes it deterministic (0 or 1 row).
const ownerCarWithActiveRentalSelect = `
	SELECT
		c.id, c.owner_id, c.title, c.description,
		c.vin, c.make, c.model, c.year, c.body_type, c.fuel_type, c.mileage,
		c.address, c.neighborhood, c.latitude, c.longitude, c.area, c.street, c.block, c.zip,
		c.is_for_rent, c.weekly_rent_price, c.is_for_sale, c.sale_price, c.currency,
		c.min_years_licensed, c.deposit_amount, c.insurance_coverage,
		c.status, c.is_paused, c.is_approved, c.rented_weeks, c.total_earned,
		c.archived_at, c.created_at, c.updated_at,
		lr.id, lr.driver_id, lr.weeks,
		COALESCE(lr.offered_weekly_price, lr.weekly_price),
		lr.pickup_confirmed_at,
		u.first_name, u.last_name,
		ch.id
	FROM cars c
	LEFT JOIN lease_requests lr
	       ON lr.id = c.reserved_by_lease_request_id
	      AND lr.status = 'paid'
	      AND lr.pickup_confirmed_at IS NOT NULL
	      AND lr.vehicle_returned_at IS NULL
	LEFT JOIN users u
	       ON u.id = lr.driver_id
	LEFT JOIN chats ch
	       ON ch.car_id = c.id
	      AND ch.driver_id = lr.driver_id
	      AND ch.owner_id = c.owner_id
`

// scanCarWithActiveRental scans one row of ownerCarWithActiveRentalSelect.
// The rental pointer is nil when the LEFT JOIN found no active lease.
func scanCarWithActiveRental(row pgx.Row) (*models.Car, *OwnerCarActiveRental, error) {
	var car models.Car
	var (
		leaseID            *uuid.UUID
		driverID           *uuid.UUID
		weeks              *int
		weeklyPriceDollars *float64
		pickupConfirmedAt  *time.Time
		firstName          *string
		lastName           *string
		chatID             *uuid.UUID
	)
	if err := row.Scan(
		&car.ID, &car.OwnerID, &car.Title, &car.Description,
		&car.VIN, &car.Make, &car.Model, &car.Year, &car.BodyType, &car.FuelType, &car.Mileage,
		&car.Address, &car.Neighborhood, &car.Latitude, &car.Longitude, &car.Area, &car.Street, &car.Block, &car.Zip,
		&car.IsForRent, &car.WeeklyRentPrice, &car.IsForSale, &car.SalePrice, &car.Currency,
		&car.MinYearsLicensed, &car.DepositAmount, &car.InsuranceCoverage,
		&car.Status, &car.IsPaused, &car.IsApproved, &car.RentedWeeks, &car.TotalEarned,
		&car.ArchivedAt, &car.CreatedAt, &car.UpdatedAt,
		&leaseID, &driverID, &weeks,
		&weeklyPriceDollars,
		&pickupConfirmedAt,
		&firstName, &lastName,
		&chatID,
	); err != nil {
		return nil, nil, err
	}

	// A NULL lease id means the LEFT JOIN found no active rental for
	// this car — all other rental fields will also be NULL.
	if leaseID == nil || weeks == nil || weeklyPriceDollars == nil || pickupConfirmedAt == nil || driverID == nil {
		return &car, nil, nil
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

	return &car, &OwnerCarActiveRental{
		LeaseRequestID:            *leaseID,
		DriverID:                  *driverID,
		DriverName:                name,
		Weeks:                     *weeks,
		EffectiveWeeklyPriceCents: int64(*weeklyPriceDollars * 100),
		PickupConfirmedAt:         *pickupConfirmedAt,
		ChatID:                    chatID,
	}, nil
}

// GetByIDWithActiveRental is the single-car companion to
// GetByOwnerIDWithActiveRental — the exact same join/derivation scoped to
// one car, so GET /cars/{id} shows the same rented state as the My Cars
// list (QA pt-3: detail/list status parity). Returns (nil, nil, nil) when
// the car does not exist. Archived cars ARE returned (ArchivedAt set) so
// the handler can decide (GetCar 404s them).
func (r *CarRepository) GetByIDWithActiveRental(ctx context.Context, carID uuid.UUID) (*models.Car, *OwnerCarActiveRental, error) {
	query := ownerCarWithActiveRentalSelect + `
		WHERE c.id = $1
	`
	car, rental, err := scanCarWithActiveRental(r.db.Pool.QueryRow(ctx, query, carID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	return car, rental, nil
}

// GetByOwnerID retrieves all non-archived cars for a specific owner
func (r *CarRepository) GetByOwnerID(ctx context.Context, ownerID uuid.UUID) ([]*models.Car, error) {
	query := `
		SELECT
			id, owner_id, title, description,
			vin, make, model, year, body_type, fuel_type, mileage,
			address, neighborhood, latitude, longitude, area, street, block, zip,
			is_for_rent, weekly_rent_price, is_for_sale, sale_price, currency,
			min_years_licensed, deposit_amount, insurance_coverage,
			status, is_paused, is_approved, rented_weeks, total_earned,
			created_at, updated_at
		FROM cars
		WHERE owner_id = $1 AND archived_at IS NULL
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
			&car.Status, &car.IsPaused, &car.IsApproved, &car.RentedWeeks, &car.TotalEarned,
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

// Delete deletes a car listing.
//
// DEPRECATED for user-facing flows (QA pt-9 / D3): hard DELETE either
// CASCADE-destroys leases/chats/payments or 500s on purchase/accident
// RESTRICT FKs. DeleteCar now soft-archives via ArchiveCar; this method is
// retained only for tooling/tests.
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

// ArchiveCar soft-archives a listing (D3): the row and its files survive
// so historical chats/leases/payments keep resolving, but the car drops
// out of Discover, the owner's list, and VIN uniqueness. Idempotent —
// archiving an already-archived car is a no-op success.
func (r *CarRepository) ArchiveCar(ctx context.Context, id uuid.UUID) error {
	result, err := r.db.Pool.Exec(ctx,
		`UPDATE cars SET archived_at = COALESCE(archived_at, NOW()) WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("car not found")
	}
	return nil
}

// Obligation kinds returned by GetActiveObligations. Also serialized into
// the 409 CAR_HAS_ACTIVE_OBLIGATIONS details so clients can explain WHY the
// delete is blocked.
const (
	ObligationActiveLease  = "active_lease"
	ObligationOpenReturn   = "open_vehicle_return"
	ObligationOpenHandover = "open_key_handover"
	ObligationLivePurchase = "live_purchase_request"
)

// GetActiveObligations reports which live commitments currently block
// archiving this car (D3):
//   - a lease that is reserved or picked-up and not yet returned
//     (accepted / payment_pending / paid without vehicle_returned_at);
//   - an open vehicle return (initiated / owner-confirmed / disputed);
//   - an open key handover (pending / owner_confirmed);
//   - a purchase request in any non-terminal state (same set as the
//     idx_purchase_requests_active_unique partial index).
//
// Returns an empty slice when the car is free of obligations.
func (r *CarRepository) GetActiveObligations(ctx context.Context, carID uuid.UUID) ([]string, error) {
	const query = `
		SELECT
			EXISTS (
				SELECT 1 FROM lease_requests lr
				WHERE lr.listing_id = $1
				  AND lr.status IN ('accepted', 'payment_pending', 'paid')
				  AND lr.vehicle_returned_at IS NULL
			),
			EXISTS (
				SELECT 1 FROM vehicle_returns vr
				WHERE vr.car_id = $1
				  AND vr.status IN ('driver_initiated', 'owner_confirmed', 'disputed')
			),
			EXISTS (
				SELECT 1 FROM key_handovers kh
				WHERE kh.car_id = $1
				  AND kh.status IN ('pending', 'owner_confirmed')
			),
			EXISTS (
				SELECT 1 FROM purchase_requests pr
				WHERE pr.car_id = $1
				  AND pr.status IN (
					'requested','accepted','bos_pending_seller','bos_pending_buyer','bos_signed',
					'payment_authorized','handover_scheduled','awaiting_inspection',
					'inspection_accepted','inspection_rejected'
				  )
			)
	`

	var hasLease, hasReturn, hasHandover, hasPurchase bool
	if err := r.db.Pool.QueryRow(ctx, query, carID).Scan(&hasLease, &hasReturn, &hasHandover, &hasPurchase); err != nil {
		return nil, err
	}

	obligations := []string{}
	if hasLease {
		obligations = append(obligations, ObligationActiveLease)
	}
	if hasReturn {
		obligations = append(obligations, ObligationOpenReturn)
	}
	if hasHandover {
		obligations = append(obligations, ObligationOpenHandover)
	}
	if hasPurchase {
		obligations = append(obligations, ObligationLivePurchase)
	}
	return obligations, nil
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
			c.status, c.is_paused, c.is_approved, c.rented_weeks, c.total_earned,
			c.created_at, c.updated_at
		FROM cars c
		JOIN users u ON u.id = c.owner_id
		WHERE c.is_paused = false
		  AND c.is_approved = true
		  AND u.is_blocked = false
		  AND c.reserved_by_lease_request_id IS NULL
		  AND c.reserved_by_purchase_request_id IS NULL
		  AND c.status <> 'sold'
		  AND c.archived_at IS NULL
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
			&car.Status, &car.IsPaused, &car.IsApproved, &car.RentedWeeks, &car.TotalEarned,
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
