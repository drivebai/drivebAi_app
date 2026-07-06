import Foundation

// MARK: - Money Helper

struct Money: Equatable {
    let amount: Double
    let currency: String

    init(amount: Double, currency: String = "USD") {
        self.amount = amount
        self.currency = currency
    }

    var formatted: String {
        let formatter = NumberFormatter()
        formatter.numberStyle = .currency
        formatter.currencyCode = currency
        formatter.maximumFractionDigits = 0
        return formatter.string(from: NSNumber(value: amount)) ?? "$\(Int(amount))"
    }

    var formattedCompact: String {
        if amount >= 1000 {
            return "$\(Int(amount / 1000))k"
        }
        return "$\(Int(amount))"
    }

    var formattedWithSuffix: String {
        formatted
    }
}

// MARK: - Enums

enum CarListingStatus: String, CaseIterable, Identifiable {
    case available = "available"
    case rented = "rented"
    case pending = "pending"
    case paused = "paused"

    var id: String { rawValue }

    var displayText: String {
        switch self {
        case .available: return "Available now!"
        case .rented: return "Currently rented"
        case .pending: return "Pending approval"
        case .paused: return "Listing paused"
        }
    }

    var badgeColor: String {
        switch self {
        case .available: return "green"
        case .rented: return "blue"
        case .pending: return "orange"
        case .paused: return "gray"
        }
    }
}

enum CarBodyType: String, CaseIterable, Identifiable {
    case sedan = "Sedan"
    case suv = "SUV"
    case coupe = "Coupe"
    case hatchback = "Hatchback"
    case truck = "Truck"
    case van = "Van"
    case convertible = "Convertible"
    case wagon = "Wagon"

    var id: String { rawValue }

    var displayText: String {
        switch self {
        case .sedan: return "Sedan (Regular)"
        case .suv: return "SUV"
        case .coupe: return "Coupe"
        case .hatchback: return "Hatchback"
        case .truck: return "Truck"
        case .van: return "Van"
        case .convertible: return "Convertible"
        case .wagon: return "Wagon"
        }
    }
}

enum FuelType: String, CaseIterable, Identifiable {
    case gas = "Gas"
    case diesel = "Diesel"
    case electric = "Electric"
    case hybrid = "Hybrid"
    case plugInHybrid = "Plug-in Hybrid"

    var id: String { rawValue }
}

enum InsuranceCoverage: String, CaseIterable, Identifiable {
    case liabilityOnly = "liability_only"
    case fullCoverage = "full_coverage"

    var id: String { rawValue }

    var displayText: String {
        switch self {
        case .liabilityOnly: return "Liability Only (state minimum)"
        case .fullCoverage: return "Comprehensive + Collision"
        }
    }
}

enum CarDocumentType: String, CaseIterable, Identifiable {
    case inspection = "inspection"
    case registration = "registration"
    case permit = "permit"
    case insurance = "insurance"
    /// Proof-of-ownership title. Required server-side before a car can be
    /// listed for sale (SALE_REQUIREMENTS_NOT_MET) — QA round pt 8.
    case title = "title"

    var id: String { rawValue }

    var displayText: String {
        switch self {
        case .inspection: return "Vehicle Inspection"
        case .registration: return "Registration"
        case .permit: return "Parking Permit"
        case .insurance: return "Insurance Certificate"
        case .title: return "Title"
        }
    }

    var iconName: String {
        switch self {
        case .inspection: return "doc.text.magnifyingglass"
        case .registration: return "doc.badge.ellipsis"
        case .permit: return "parkingsign.circle"
        case .insurance: return "shield.checkered"
        case .title: return "doc.text.fill"
        }
    }
}

enum PhotoSlotType: String, CaseIterable, Identifiable {
    // Raw values are the canonical storage strings on `car_photos.slot_type`.
    // The original five are unchanged (preserves cover_front auto-publish and
    // every cover-photo subquery); the three ¾/interior slots are new in the
    // guided-capture QA round and accepted by the backend's extended
    // validSlots map.
    case coverFront = "cover_front"
    case frontLeft34 = "front_left_34"
    case left = "left"
    case back = "back"
    case rearRight34 = "rear_right_34"
    case right = "right"
    case dashboard = "dashboard"
    case interior = "interior"

    var id: String { rawValue }

    /// Guided-capture label. Ordering and labels live entirely client-side —
    /// the backend only knows the raw slot strings.
    var displayLabel: String {
        switch self {
        case .coverFront: return "Front"
        case .frontLeft34: return "Front-left ¾"
        case .left: return "Driver side"
        case .back: return "Rear"
        case .rearRight34: return "Rear-right ¾"
        case .right: return "Passenger side"
        case .dashboard: return "Dashboard"
        case .interior: return "Interior"
        }
    }

    /// Guided capture sequence (spec Section 5): front → front-left ¾ →
    /// driver side → rear → rear-right ¾ → passenger side → dashboard →
    /// interior.
    var sortOrder: Int {
        switch self {
        case .coverFront: return 0
        case .frontLeft34: return 1
        case .left: return 2
        case .back: return 3
        case .rearRight34: return 4
        case .right: return 5
        case .dashboard: return 6
        case .interior: return 7
        }
    }

    /// SF Symbol used as the translucent silhouette overlay in the guided
    /// capture flow. Simple shapes only — no hand-authored assets.
    var overlayAssetName: String {
        switch self {
        case .coverFront: return "car.front.waves.up"
        case .frontLeft34: return "car.fill"
        case .left: return "car.side"
        case .back: return "car.rear"
        case .rearRight34: return "car.fill"
        case .right: return "car.side.fill"
        case .dashboard: return "steeringwheel"
        case .interior: return "carseat.left.fill"
        }
    }

    /// One-line hint under the guided-capture caption.
    var guidedHint: String {
        switch self {
        case .coverFront: return "Stand in front of the car, centered"
        case .frontLeft34: return "Angle shot from the front-left corner"
        case .left: return "Full side profile, driver side"
        case .back: return "Stand directly behind the car"
        case .rearRight34: return "Angle shot from the rear-right corner"
        case .right: return "Full side profile, passenger side"
        case .dashboard: return "Capture the dashboard and odometer"
        case .interior: return "Capture the cabin and seats"
        }
    }
}

// MARK: - Supporting Models

struct CarSpecs: Equatable {
    var bodyType: CarBodyType
    var fuelType: FuelType
    var mileage: Int
    var year: Int
    var make: String
    var model: String
    /// 17-character VIN, normalized upper-case. Optional — listings created
    /// before the VIN field existed (or owners who skip the VIN-autofill
    /// step) have no value. Persisted server-side in `cars.vin`.
    var vin: String? = nil

    var mileageFormatted: String {
        let formatter = NumberFormatter()
        formatter.numberStyle = .decimal
        return "\(formatter.string(from: NSNumber(value: mileage)) ?? "\(mileage)") mi"
    }
}

struct CarRequirements: Equatable {
    var minYearsLicensedDriving: Int
    var depositAmount: Money
    var insuranceCoverage: InsuranceCoverage

    var licensingText: String {
        "\(minYearsLicensedDriving) years licensed driving"
    }

    var depositText: String {
        "\(depositAmount.formatted) deposit"
    }
}

struct CarPhotoSlot: Identifiable, Equatable {
    let id: UUID
    let slotType: PhotoSlotType
    var imageURL: String?
    var localImageData: Data?

    var hasImage: Bool {
        imageURL != nil || localImageData != nil
    }

    /// Full URL for loading the image from the server
    var fullImageURL: URL? {
        guard let imageURL = imageURL else { return nil }
        return ImageURLHelper.fullURL(for: imageURL)
    }

    init(id: UUID = UUID(), slotType: PhotoSlotType, imageURL: String? = nil, localImageData: Data? = nil) {
        self.id = id
        self.slotType = slotType
        self.imageURL = imageURL
        self.localImageData = localImageData
    }
}

// MARK: - Image URL Helper

/// Helper to construct full URLs from relative paths returned by the backend
enum ImageURLHelper {
    /// Base server URL (not the API base, but the server root)
    static var serverBaseURL: URL { AppConfig.serverBaseURL }


    /// Construct full URL from relative path (e.g., "/uploads/cars/...")
    static func fullURL(for relativePath: String?) -> URL? {
        guard let path = relativePath, !path.isEmpty else { return nil }
        // If it's already a full URL, parse it directly
        if path.hasPrefix("http://") || path.hasPrefix("https://") {
            return URL(string: path)
        }
        // Otherwise, append to server base
        return URL(string: path, relativeTo: serverBaseURL)
    }
}

struct CarDocument: Identifiable, Equatable {
    let id: UUID
    let documentType: CarDocumentType
    let filename: String
    let fileSize: Int
    let uploadedAt: Date

    /// Signed download URL as returned by the backend (`file_url`). Relative
    /// or absolute — resolve through `ImageURLHelper.fullURL(for:)` before
    /// fetching. Nil for locally-staged docs that haven't been uploaded yet.
    var fileURL: String?

    /// Convenience: resolved absolute URL for previewing/downloading.
    var resolvedFileURL: URL? {
        ImageURLHelper.fullURL(for: fileURL)
    }

    /// Raw bytes from the file picker, kept ONLY for docs not yet pushed
    /// to the backend. `CarDetailEditView.saveCar()` walks docs with
    /// non-nil `localData` and uploads them via OwnerCarsStore. Server-
    /// fetched rows have this nil.
    var localData: Data?
    /// MIME type tagged alongside `localData` so the multipart upload
    /// gets the correct Content-Type. Same lifecycle as `localData`.
    var localMimeType: String?

    var fileSizeFormatted: String {
        let formatter = ByteCountFormatter()
        formatter.countStyle = .file
        return formatter.string(fromByteCount: Int64(fileSize))
    }

    var uploadedAtFormatted: String {
        let formatter = DateFormatter()
        formatter.dateStyle = .medium
        formatter.timeStyle = .none
        return formatter.string(from: uploadedAt)
    }

    /// True when this row exists only locally and needs to be pushed up
    /// the next time the parent view saves. Used by saveCar() to decide
    /// what to upload.
    var needsUpload: Bool { localData != nil }

    init(
        id: UUID = UUID(),
        documentType: CarDocumentType,
        filename: String,
        fileSize: Int,
        uploadedAt: Date = Date(),
        fileURL: String? = nil,
        localData: Data? = nil,
        localMimeType: String? = nil
    ) {
        self.id = id
        self.documentType = documentType
        self.filename = filename
        self.fileSize = fileSize
        self.uploadedAt = uploadedAt
        self.fileURL = fileURL
        self.localData = localData
        self.localMimeType = localMimeType
    }
}

struct CarOwnerInfo: Equatable {
    let id: UUID
    let name: String
    let avatarURL: String?
    let rating: Double
    let reviewCount: Int

    var ratingFormatted: String {
        String(format: "%.1f", rating)
    }

    init(id: UUID = UUID(), name: String, avatarURL: String? = nil, rating: Double, reviewCount: Int) {
        self.id = id
        self.name = name
        self.avatarURL = avatarURL
        self.rating = rating
        self.reviewCount = reviewCount
    }
}

struct CarLocation: Equatable {
    let address: String
    let neighborhood: String
    let distanceMiles: Double
    let latitude: Double
    let longitude: Double
    let area: String
    let street: String
    let block: String
    let zip: String

    var displayText: String {
        if distanceMiles > 0 {
            return "\(displayArea), \(String(format: "%.1f", distanceMiles)) miles away"
        }
        return displayArea
    }

    /// Best available area name (area > neighborhood > "Unknown")
    var displayArea: String {
        if !area.isEmpty { return area }
        if !neighborhood.isEmpty { return neighborhood }
        return "Unknown"
    }

    /// Combined address line: "street, area zip" or fallback to address/neighborhood
    var displayAddressLine: String {
        var parts: [String] = []
        if !street.isEmpty { parts.append(street) }
        if !area.isEmpty { parts.append(area) }
        if !zip.isEmpty { parts.append(zip) }
        if !parts.isEmpty { return parts.joined(separator: ", ") }
        if !address.isEmpty { return address }
        if !neighborhood.isEmpty { return neighborhood }
        return ""
    }

    /// Whether this location has valid coordinates
    var hasCoordinate: Bool {
        latitude != 0 && longitude != 0
    }

    init(
        address: String = "",
        neighborhood: String = "",
        distanceMiles: Double = 0,
        latitude: Double = 0,
        longitude: Double = 0,
        area: String = "",
        street: String = "",
        block: String = "",
        zip: String = ""
    ) {
        self.address = address
        self.neighborhood = neighborhood
        self.distanceMiles = distanceMiles
        self.latitude = latitude
        self.longitude = longitude
        self.area = area
        self.street = street
        self.block = block
        self.zip = zip
    }
}

// MARK: - Active Rental Summary
//
// Derived owner-facing snapshot of the lease currently attached to a car,
// so the "For rent" tab can render a "Rented" chip + summary line without a
// second lease-detail fetch. Backend deploys this alongside `CarResponse`
// under `active_rental`; iOS gracefully decodes it as nil when the field is
// missing (older backends or the car isn't rented right now).

struct ActiveRentalSummary: Equatable, Hashable {
    let leaseRequestId: UUID
    let driverId: UUID
    let driverName: String
    let weeks: Int
    let weeklyPriceCents: Int64
    let pickupConfirmedAt: Date
    let plannedEndAt: Date
    let currentEarnedCents: Int64
    /// Owner↔driver chat for this rental (`chat_id`). Nil when the backend
    /// hasn't shipped the field yet or no chat row exists for the pair.
    /// Drives the chat shortcut on the owner's car card (QA pt 6).
    var chatId: UUID? = nil
}

// MARK: - Main Car Model

struct Car: Identifiable, Equatable, Hashable {
    // Hashable based on id only for navigation
    func hash(into hasher: inout Hasher) {
        hasher.combine(id)
    }

    let id: UUID
    var title: String
    var description: String
    var specs: CarSpecs
    var requirements: CarRequirements
    var location: CarLocation
    var owner: CarOwnerInfo

    // Pricing
    var isForRent: Bool
    var weeklyRentPrice: Money?
    var isForSale: Bool
    var salePrice: Money?

    // Status
    var status: CarListingStatus
    var isPaused: Bool

    // Media
    var photoSlots: [CarPhotoSlot]
    var documents: [CarDocument]

    // Stats
    var rentedWeeks: Int
    var totalEarned: Money

    // Timestamps
    let createdAt: Date
    var updatedAt: Date

    /// Owner-facing snapshot of the lease currently attached to this car.
    /// Populated by the backend when a paid+picked-up lease is active; nil
    /// otherwise (or when the backend hasn't shipped the field yet — we
    /// decode it optionally so iOS runs against both).
    var activeRental: ActiveRentalSummary? = nil

    /// True when the backend reports `status == "sold"`. Kept as a separate
    /// flag because `CarListingStatus` has no `.sold` case (adding one would
    /// break exhaustive switches owned by wave-2 screens); a sold car's
    /// typed `status` falls back to `.pending`. `CarBusinessState.forCar`
    /// checks this flag before the pending fallback, so sold cars render
    /// correctly. Never rendered directly — go through `CarBusinessState`.
    var isSold: Bool = false

    // Computed
    var coverPhotoURL: String? {
        photoSlots.first(where: { $0.slotType == .coverFront })?.imageURL
    }

    var hasAllRequiredPhotos: Bool {
        photoSlots.allSatisfy { $0.hasImage }
    }

    var displayTitle: String {
        "\(specs.year) \(specs.make) \(specs.model)"
    }

    init(
        id: UUID = UUID(),
        title: String,
        description: String,
        specs: CarSpecs,
        requirements: CarRequirements,
        location: CarLocation,
        owner: CarOwnerInfo,
        isForRent: Bool = true,
        weeklyRentPrice: Money? = nil,
        isForSale: Bool = false,
        salePrice: Money? = nil,
        status: CarListingStatus = .available,
        isPaused: Bool = false,
        photoSlots: [CarPhotoSlot] = [],
        documents: [CarDocument] = [],
        rentedWeeks: Int = 0,
        totalEarned: Money = Money(amount: 0),
        createdAt: Date = Date(),
        updatedAt: Date = Date(),
        activeRental: ActiveRentalSummary? = nil,
        isSold: Bool = false
    ) {
        self.id = id
        self.title = title
        self.description = description
        self.specs = specs
        self.requirements = requirements
        self.location = location
        self.owner = owner
        self.isForRent = isForRent
        self.weeklyRentPrice = weeklyRentPrice
        self.isForSale = isForSale
        self.salePrice = salePrice
        self.status = status
        self.isPaused = isPaused
        self.photoSlots = photoSlots
        self.documents = documents
        self.rentedWeeks = rentedWeeks
        self.totalEarned = totalEarned
        self.createdAt = createdAt
        self.updatedAt = updatedAt
        self.activeRental = activeRental
        self.isSold = isSold
    }
}

// MARK: - Car Extensions for Display

extension Car {
    var weeklyPriceFormatted: String {
        guard let price = weeklyRentPrice else { return "N/A" }
        return "\(price.formatted) / week"
    }

    var salePriceFormatted: String {
        guard let price = salePrice else { return "N/A" }
        return price.formatted
    }

    var rentedWeeksFormatted: String {
        "\(rentedWeeks)w"
    }

    var totalEarnedFormatted: String {
        totalEarned.formatted
    }

    static func createEmptyPhotoSlots() -> [CarPhotoSlot] {
        PhotoSlotType.allCases
            .sorted { $0.sortOrder < $1.sortOrder }
            .map { CarPhotoSlot(slotType: $0) }
    }
}

// MARK: - Status Mapping (CarListingStatus <-> ListingStatus)

extension CarListingStatus {
    /// Convert to ListingStatus for Today tab display
    var toListingStatus: ListingStatus {
        switch self {
        case .available: return .active
        case .rented: return .rented
        case .pending: return .pending
        case .paused: return .paused
        }
    }
}

// MARK: - Car to ListingSummary Conversion

extension Car {
    /// Convert Car to ListingSummary for Today tab display
    var toListingSummary: ListingSummary {
        ListingSummary(
            id: id,
            title: displayTitle,
            imageURL: coverPhotoURL,
            weeklyPrice: weeklyRentPrice?.amount ?? 0,
            rentedWeeks: rentedWeeks,
            totalEarned: totalEarned.amount,
            status: status.toListingStatus
        )
    }
}
