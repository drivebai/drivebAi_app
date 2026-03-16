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
    }
}

struct CarSpecsResponse: Codable {
    let make: String
    let model: String
    let year: Int
    let bodyType: String
    let fuelType: String
    let mileage: Int

    enum CodingKeys: String, CodingKey {
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
    let depositAmount: Double
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
    var depositAmount: Double?
    var insuranceCoverage: String?

    enum CodingKeys: String, CodingKey {
        case title, description, make, model, year
        case bodyType = "body_type"
        case fuelType = "fuel_type"
        case mileage, address, neighborhood, latitude, longitude
        case area, street, block, zip
        case isForRent = "is_for_rent"
        case weeklyRentPrice = "weekly_rent_price"
        case isForSale = "is_for_sale"
        case salePrice = "sale_price"
        case minYearsLicensed = "min_years_licensed"
        case depositAmount = "deposit_amount"
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
    var depositAmount: Double?
    var insuranceCoverage: String?
    var status: String?
    var isPaused: Bool?

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
        case depositAmount = "deposit_amount"
        case insuranceCoverage = "insurance_coverage"
        case status
        case isPaused = "is_paused"
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
        let bodyType = CarBodyType(rawValue: specs.bodyType.capitalized) ?? .sedan
        let fuelType = FuelType(rawValue: specs.fuelType.capitalized) ?? .gas
        let insuranceCoverage = InsuranceCoverage(rawValue: requirements.insuranceCoverage) ?? .fullCoverage
        let carStatus = CarListingStatus(rawValue: status) ?? .pending

        let carSpecs = CarSpecs(
            bodyType: bodyType,
            fuelType: fuelType,
            mileage: specs.mileage,
            year: specs.year,
            make: specs.make,
            model: specs.model
        )

        let carRequirements = CarRequirements(
            minYearsLicensedDriving: requirements.minYearsLicensed,
            depositAmount: Money(amount: requirements.depositAmount, currency: currency),
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
                uploadedAt: doc.createdAt
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
            updatedAt: updatedAt
        )
    }
}

extension Car {
    /// Convert local Car model to CreateCarRequest
    func toCreateRequest() -> CreateCarRequest {
        CreateCarRequest(
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
            area: location.area.isEmpty ? nil : location.area,
            street: location.street.isEmpty ? nil : location.street,
            block: location.block.isEmpty ? nil : location.block,
            zip: location.zip.isEmpty ? nil : location.zip,
            isForRent: isForRent,
            weeklyRentPrice: weeklyRentPrice?.amount,
            isForSale: isForSale,
            salePrice: salePrice?.amount,
            minYearsLicensed: requirements.minYearsLicensedDriving,
            depositAmount: requirements.depositAmount.amount,
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
            depositAmount: requirements.depositAmount.amount,
            insuranceCoverage: requirements.insuranceCoverage.rawValue,
            status: status.rawValue,
            isPaused: isPaused
        )
    }
}
