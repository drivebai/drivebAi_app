import Foundation

// MARK: - Car API Response Models (match Go backend JSON)

struct CarsListResponse: Codable {
    let cars: [CarAPIResponse]
}

/// Response for public listings endpoint (for drivers)
struct ListingsResponse: Codable {
    let listings: [CarAPIResponse]
    let count: Int
}

struct CarAPIResponse: Codable {
    let id: UUID
    let ownerId: UUID
    let title: String
    let description: String
    let specs: CarSpecsResponse
    let location: CarLocationResponse
    let isForRent: Bool
    let weeklyRentPrice: Double?
    let isForSale: Bool
    let salePrice: Double?
    let currency: String
    let requirements: CarRequirementsResponse
    let status: String
    let isPaused: Bool
    let rentedWeeks: Int
    let totalEarned: Double
    let photos: [CarPhotoAPIResponse]
    let documents: [CarDocumentAPIResponse]
    let owner: CarOwnerAPIResponse?
    let createdAt: Date
    let updatedAt: Date
    /// Present when a paid+picked-up lease is currently attached to the car.
    /// Optional so older backends (or non-owner endpoints) that don't emit
    /// the field decode cleanly to nil.
    let activeRental: ActiveRentalAPIResponse?

    enum CodingKeys: String, CodingKey {
        case id
        case ownerId = "owner_id"
        case title, description, specs, location
        case isForRent = "is_for_rent"
        case weeklyRentPrice = "weekly_rent_price"
        case isForSale = "is_for_sale"
        case salePrice = "sale_price"
        case currency, requirements, status
        case isPaused = "is_paused"
        case rentedWeeks = "rented_weeks"
        case totalEarned = "total_earned"
        case photos, documents, owner
        case createdAt = "created_at"
        case updatedAt = "updated_at"
        case activeRental = "active_rental"
    }
}

/// Owner-facing snapshot of the current rental — mirrors the backend's
/// `ActiveRentalSummary` (`car.go`). All monetary fields are cents to avoid
/// float drift; the view layer formats them via `Money`.
struct ActiveRentalAPIResponse: Codable {
    let leaseRequestId: UUID
    let driverId: UUID
    let driverName: String
    let weeks: Int
    let weeklyPriceCents: Int64
    let pickupConfirmedAt: Date
    let plannedEndAt: Date
    let currentEarnedCents: Int64
    /// Owner↔driver chat for this rental. Optional/omitempty on the wire
    /// (`chat_id`) — nil against older backends or when no chat row exists.
    let chatId: UUID?

    enum CodingKeys: String, CodingKey {
        case leaseRequestId = "lease_request_id"
        case driverId = "driver_id"
        case driverName = "driver_name"
        case weeks
        case weeklyPriceCents = "weekly_price_cents"
        case pickupConfirmedAt = "pickup_confirmed_at"
        case plannedEndAt = "planned_end_at"
        case currentEarnedCents = "current_earned_cents"
        case chatId = "chat_id"
    }

    func toDomain() -> ActiveRentalSummary {
        ActiveRentalSummary(
            leaseRequestId: leaseRequestId,
            driverId: driverId,
            driverName: driverName,
            weeks: weeks,
            weeklyPriceCents: weeklyPriceCents,
            pickupConfirmedAt: pickupConfirmedAt,
            plannedEndAt: plannedEndAt,
            currentEarnedCents: currentEarnedCents,
            chatId: chatId
        )
    }
}

struct CarSpecsResponse: Codable {
    let vin: String?
    let make: String
    let model: String
    let year: Int
    let bodyType: String
    let fuelType: String
    let mileage: Int

    enum CodingKeys: String, CodingKey {
        case vin
        case make, model, year
        case bodyType = "body_type"
        case fuelType = "fuel_type"
        case mileage
    }
}

struct CarLocationResponse: Codable {
    let address: String
    let neighborhood: String
    let latitude: Double?
    let longitude: Double?
    let area: String?
    let street: String?
    let block: String?
    let zip: String?
}

struct CarRequirementsResponse: Codable {
    let minYearsLicensed: Int
    /// Deposit is being removed from the product (QA pt 7). The backend
    /// still emits the key (always 0) so shipped builds keep decoding, but
    /// new code treats it as optional and renders nothing when nil/0.
    let depositAmount: Double?
    let insuranceCoverage: String

    enum CodingKeys: String, CodingKey {
        case minYearsLicensed = "min_years_licensed"
        case depositAmount = "deposit_amount"
        case insuranceCoverage = "insurance_coverage"
    }
}

struct CarPhotoAPIResponse: Codable {
    let id: UUID
    let slotType: String
    let fileUrl: String
    let fileSize: Int
    let createdAt: Date
    let updatedAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case slotType = "slot_type"
        case fileUrl = "file_url"
        case fileSize = "file_size"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }
}

struct CarDocumentAPIResponse: Codable {
    let id: UUID
    let documentType: String
    let fileName: String
    let fileUrl: String
    let fileSize: Int
    let createdAt: Date
    let updatedAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case documentType = "document_type"
        case fileName = "file_name"
        case fileUrl = "file_url"
        case fileSize = "file_size"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }
}

struct CarOwnerAPIResponse: Codable {
    let id: UUID
    let name: String
    let profilePhotoUrl: String?
    let rating: Double
    let reviewCount: Int

    enum CodingKeys: String, CodingKey {
        case id, name
        case profilePhotoUrl = "profile_photo_url"
        case rating
        case reviewCount = "review_count"
    }
}

// MARK: - Car API Request Models

struct CreateCarRequest: Codable {
    var title: String?
    var description: String?
    var vin: String?
    var make: String
    var model: String
    var year: Int
    var bodyType: String
    var fuelType: String
    var mileage: Int
    var address: String?
    var neighborhood: String?
    var latitude: Double?
    var longitude: Double?
    var area: String?
    var street: String?
    var block: String?
    var zip: String?
    var isForRent: Bool
    var weeklyRentPrice: Double?
    var isForSale: Bool
    var salePrice: Double?
    var minYearsLicensed: Int?
    // deposit_amount intentionally not encoded anymore (QA pt 7) — the
    // backend ignores it and forces 0.
    var insuranceCoverage: String?

    enum CodingKeys: String, CodingKey {
        case title, description, vin
        case make, model, year
        case bodyType = "body_type"
        case fuelType = "fuel_type"
        case mileage, address, neighborhood, latitude, longitude
        case area, street, block, zip
        case isForRent = "is_for_rent"
        case weeklyRentPrice = "weekly_rent_price"
        case isForSale = "is_for_sale"
        case salePrice = "sale_price"
        case minYearsLicensed = "min_years_licensed"
        case insuranceCoverage = "insurance_coverage"
    }
}

struct UpdateCarRequest: Codable {
    var title: String?
    var description: String?
    var make: String?
    var model: String?
    var year: Int?
    var bodyType: String?
    var fuelType: String?
    var mileage: Int?
    var address: String?
    var neighborhood: String?
    var latitude: Double?
    var longitude: Double?
    var isForRent: Bool?
    var weeklyRentPrice: Double?
    var isForSale: Bool?
    var salePrice: Double?
    var minYearsLicensed: Int?
    // deposit_amount intentionally not encoded anymore (QA pt 7) — the
    // backend ignores it and forces 0.
    // status / is_paused intentionally not encoded anymore (QA pt 9) — the
    // backend ignores both; pause state flows exclusively through
    // POST /cars/{id}/pause so a full-car autosave PATCH can never clobber
    // the listing status.
    var insuranceCoverage: String?

    enum CodingKeys: String, CodingKey {
        case title, description, make, model, year
        case bodyType = "body_type"
        case fuelType = "fuel_type"
        case mileage, address, neighborhood, latitude, longitude
        case isForRent = "is_for_rent"
        case weeklyRentPrice = "weekly_rent_price"
        case isForSale = "is_for_sale"
        case salePrice = "sale_price"
        case minYearsLicensed = "min_years_licensed"
        case insuranceCoverage = "insurance_coverage"
    }
}

// MARK: - VIN Decode

/// Backend-normalized response for `/api/v1/cars/vin-decode/{vin}`.
///
/// The Go layer talks to NHTSA's vPIC `DecodeVinValues` upstream and:
///   - maps `body_type` / `fuel_type` to the same enum strings the rest of
///     our car APIs use (lower-cased: "suv", "plug_in_hybrid", …),
///   - title-cases the manufacturer name,
///   - parses the year string to an integer,
///   - surfaces any partial-decode condition via `warning`.
///
/// Any of the optional fields may be missing — NHTSA frequently omits Model
/// or BodyClass even for valid VINs. The UI autofills what it gets and
/// leaves the rest for the user to enter manually.
struct VINDecodeAPIResponse: Codable {
    let vin: String
    let make: String?
    let model: String?
    let year: Int?
    let bodyType: String?
    let fuelType: String?
    let manufacturer: String?
    let vehicleType: String?
    let warning: String?
    /// Advisory VIN-availability signal (QA pt 12): `false` when a
    /// non-archived car already uses this VIN, `true` when it's free.
    /// Absent/nil = unknown (the backend omits the field on a DB error or
    /// hasn't shipped it yet) — callers MUST treat nil as "allowed" and let
    /// the create-time 409 remain the authoritative backstop. Never treat
    /// nil as unavailable.
    let available: Bool?

    enum CodingKeys: String, CodingKey {
        case vin, make, model, year
        case bodyType = "body_type"
        case fuelType = "fuel_type"
        case manufacturer
        case vehicleType = "vehicle_type"
        case warning
        case available
    }
}

// MARK: - Update Car Location Request

struct UpdateCarLocationRequest: Codable {
    let latitude: Double
    let longitude: Double
    var area: String?
    var street: String?
    var block: String?
    var zip: String?
}

struct CarPhotosListResponse: Codable {
    let photos: [CarPhotoAPIResponse]
}

struct CarDocumentsListResponse: Codable {
    let documents: [CarDocumentAPIResponse]
}

// MARK: - Conversion Extensions

extension CarAPIResponse {
    /// Convert API response to local Car model
    func toCar() -> Car {
        // Case-insensitive lookup: the backend stores enum strings lower-cased
        // ("suv", "plug-in hybrid"), but our raw values keep their canonical
        // display case ("SUV", "Plug-in Hybrid"). A naive `.capitalized` round-trip
        // mangles them ("Suv", "Plug-In Hybrid") and silently coerces every save
        // back to the default — making autosaved Body/Fuel picks appear lost.
        let bodyType = CarBodyType.allCases.first { $0.rawValue.lowercased() == specs.bodyType.lowercased() } ?? .sedan
        let fuelType = FuelType.allCases.first { $0.rawValue.lowercased() == specs.fuelType.lowercased() } ?? .gas
        let insuranceCoverage = InsuranceCoverage(rawValue: requirements.insuranceCoverage) ?? .fullCoverage
        let carStatus = CarListingStatus(rawValue: status) ?? .pending

        let carSpecs = CarSpecs(
            bodyType: bodyType,
            fuelType: fuelType,
            mileage: specs.mileage,
            year: specs.year,
            make: specs.make,
            model: specs.model,
            vin: specs.vin
        )

        let carRequirements = CarRequirements(
            minYearsLicensedDriving: requirements.minYearsLicensed,
            // Deposit is deprecated (QA pt 7): decode optionally, default 0.
            // UI renders nothing for 0-deposit cars.
            depositAmount: Money(amount: requirements.depositAmount ?? 0, currency: currency),
            insuranceCoverage: insuranceCoverage
        )

        let carLocation = CarLocation(
            address: location.address,
            neighborhood: location.neighborhood,
            distanceMiles: 0, // TODO: Calculate from lat/lng if needed
            latitude: location.latitude ?? 0,
            longitude: location.longitude ?? 0,
            area: location.area ?? "",
            street: location.street ?? "",
            block: location.block ?? "",
            zip: location.zip ?? ""
        )

        let carOwner: CarOwnerInfo
        if let owner = owner {
            carOwner = CarOwnerInfo(
                id: owner.id,
                name: owner.name,
                avatarURL: owner.profilePhotoUrl,
                rating: owner.rating,
                reviewCount: owner.reviewCount
            )
        } else {
            carOwner = CarOwnerInfo(
                id: ownerId,
                name: "Owner",
                avatarURL: nil,
                rating: 5.0,
                reviewCount: 0
            )
        }

        let photoSlots = PhotoSlotType.allCases
            .sorted { $0.sortOrder < $1.sortOrder }
            .map { slotType -> CarPhotoSlot in
                if let photo = photos.first(where: { $0.slotType == slotType.rawValue }) {
                    return CarPhotoSlot(
                        id: photo.id,
                        slotType: slotType,
                        imageURL: photo.fileUrl,
                        localImageData: nil
                    )
                }
                return CarPhotoSlot(slotType: slotType)
            }

        let carDocuments = documents.map { doc -> CarDocument in
            let docType = CarDocumentType(rawValue: doc.documentType) ?? .inspection
            return CarDocument(
                id: doc.id,
                documentType: docType,
                filename: doc.fileName,
                fileSize: doc.fileSize,
                uploadedAt: doc.createdAt,
                fileURL: doc.fileUrl
            )
        }

        return Car(
            id: id,
            title: title,
            description: description,
            specs: carSpecs,
            requirements: carRequirements,
            location: carLocation,
            owner: carOwner,
            isForRent: isForRent,
            weeklyRentPrice: weeklyRentPrice.map { Money(amount: $0, currency: currency) },
            isForSale: isForSale,
            salePrice: salePrice.map { Money(amount: $0, currency: currency) },
            status: carStatus,
            isPaused: isPaused,
            photoSlots: photoSlots,
            documents: carDocuments,
            rentedWeeks: rentedWeeks,
            totalEarned: Money(amount: totalEarned, currency: currency),
            createdAt: createdAt,
            updatedAt: updatedAt,
            activeRental: activeRental?.toDomain(),
            // CarListingStatus has no .sold case (see Car.isSold docs), so a
            // sold car decodes with status fallback .pending + this flag set.
            isSold: status.lowercased() == "sold"
        )
    }
}

extension Car {
    /// Convert local Car model to CreateCarRequest
    func toCreateRequest() -> CreateCarRequest {
        CreateCarRequest(
            title: title,
            description: description,
            vin: specs.vin,
            make: specs.make,
            model: specs.model,
            year: specs.year,
            bodyType: specs.bodyType.rawValue.lowercased(),
            fuelType: specs.fuelType.rawValue.lowercased(),
            mileage: specs.mileage,
            address: location.address.isEmpty ? nil : location.address,
            neighborhood: location.neighborhood.isEmpty ? nil : location.neighborhood,
            latitude: location.latitude != 0 ? location.latitude : nil,
            longitude: location.longitude != 0 ? location.longitude : nil,
            area: location.area.isEmpty ? nil : location.area,
            street: location.street.isEmpty ? nil : location.street,
            block: location.block.isEmpty ? nil : location.block,
            zip: location.zip.isEmpty ? nil : location.zip,
            isForRent: isForRent,
            weeklyRentPrice: weeklyRentPrice?.amount,
            isForSale: isForSale,
            salePrice: salePrice?.amount,
            minYearsLicensed: requirements.minYearsLicensedDriving,
            insuranceCoverage: requirements.insuranceCoverage.rawValue
        )
    }

    /// Convert local Car model to UpdateCarRequest
    func toUpdateRequest() -> UpdateCarRequest {
        UpdateCarRequest(
            title: title,
            description: description,
            make: specs.make,
            model: specs.model,
            year: specs.year,
            bodyType: specs.bodyType.rawValue.lowercased(),
            fuelType: specs.fuelType.rawValue.lowercased(),
            mileage: specs.mileage,
            address: location.address.isEmpty ? nil : location.address,
            neighborhood: location.neighborhood.isEmpty ? nil : location.neighborhood,
            latitude: location.latitude != 0 ? location.latitude : nil,
            longitude: location.longitude != 0 ? location.longitude : nil,
            isForRent: isForRent,
            weeklyRentPrice: weeklyRentPrice?.amount,
            isForSale: isForSale,
            salePrice: salePrice?.amount,
            minYearsLicensed: requirements.minYearsLicensedDriving,
            insuranceCoverage: requirements.insuranceCoverage.rawValue
        )
    }
}
