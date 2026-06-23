import Foundation

// MARK: - Lease Request API Response Models

struct LeaseRequestAPIResponse: Codable, Identifiable {
    let id: UUID
    let chatId: UUID
    let listingId: UUID
    let ownerId: UUID
    let driverId: UUID
    let driverName: String
    let ownerName: String
    let status: String
    let weeklyPrice: Double
    let offeredWeeklyPrice: Double?
    let totalAmount: Double
    let currency: String
    let weeks: Int
    let message: String?
    let carTitle: String
    let payment: PaymentSummaryAPIResponse?
    // Pickup deadline lifecycle (backend migration 000024)
    let pickupDeadlineAt: Date?
    let pickupConfirmedAt: Date?
    let refundId: String?
    let refundedAt: Date?
    let refundStatus: String?
    // Pickup extension (backend migration 000025). Always present; default 0
    // server-side so absent values from older payloads decode as 0.
    let pickupExtensionTotalMinutes: Int?
    let pickupExtensionCount: Int?
    let pickupExtensionRemainingMinutes: Int?
    let pickupLastExtendedAt: Date?
    // Price-review (backend migration 000028). Always present on new
    // backends; optional here so older builds talking to a newer backend
    // simply default the flag to false / nil.
    let priceChangePending: Bool?
    let previousOfferedWeeklyPrice: Double?
    let priceChangeActedAt: Date?
    let createdAt: Date
    let updatedAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case chatId = "chat_id"
        case listingId = "listing_id"
        case ownerId = "owner_id"
        case driverId = "driver_id"
        case driverName = "driver_name"
        case ownerName = "owner_name"
        case status
        case weeklyPrice = "weekly_price"
        case offeredWeeklyPrice = "offered_weekly_price"
        case totalAmount = "total_amount"
        case currency, weeks, message
        case carTitle = "car_title"
        case payment
        case pickupDeadlineAt = "pickup_deadline_at"
        case pickupConfirmedAt = "pickup_confirmed_at"
        case refundId = "refund_id"
        case refundedAt = "refunded_at"
        case refundStatus = "refund_status"
        case pickupExtensionTotalMinutes = "pickup_extension_total_minutes"
        case pickupExtensionCount = "pickup_extension_count"
        case pickupExtensionRemainingMinutes = "pickup_extension_remaining_minutes"
        case pickupLastExtendedAt = "pickup_last_extended_at"
        case priceChangePending = "price_change_pending"
        case previousOfferedWeeklyPrice = "previous_offered_weekly_price"
        case priceChangeActedAt = "price_change_acted_at"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }

    func toLeaseRequest() -> LeaseRequest {
        LeaseRequest(
            id: id, chatId: chatId, listingId: listingId,
            ownerId: ownerId, driverId: driverId,
            driverName: driverName, ownerName: ownerName,
            status: LeaseRequestStatus(rawValue: status) ?? .requested,
            weeklyPrice: weeklyPrice, offeredWeeklyPrice: offeredWeeklyPrice,
            totalAmount: totalAmount,
            currency: currency, weeks: weeks, message: message,
            carTitle: carTitle,
            payment: payment?.toPaymentSummary(),
            pickupDeadlineAt: pickupDeadlineAt,
            pickupConfirmedAt: pickupConfirmedAt,
            refundId: refundId,
            refundedAt: refundedAt,
            refundStatus: refundStatus,
            pickupExtensionTotalMinutes: pickupExtensionTotalMinutes ?? 0,
            pickupExtensionCount: pickupExtensionCount ?? 0,
            pickupExtensionRemainingMinutes: pickupExtensionRemainingMinutes ?? LeaseRequest.maxPickupExtensionMinutes,
            pickupLastExtendedAt: pickupLastExtendedAt,
            priceChangePending: priceChangePending ?? false,
            previousOfferedWeeklyPrice: previousOfferedWeeklyPrice,
            priceChangeActedAt: priceChangeActedAt,
            createdAt: createdAt, updatedAt: updatedAt
        )
    }
}

struct PaymentSummaryAPIResponse: Codable {
    let id: UUID
    let paymentIntentId: String?
    let amount: Int64
    let platformFeeAmount: Int64
    let currency: String
    let status: String

    enum CodingKeys: String, CodingKey {
        case id
        case paymentIntentId = "payment_intent_id"
        case amount
        case platformFeeAmount = "platform_fee_amount"
        case currency, status
    }

    func toPaymentSummary() -> PaymentSummary {
        PaymentSummary(
            id: id, paymentIntentId: paymentIntentId,
            amount: amount, platformFeeAmount: platformFeeAmount,
            currency: currency,
            status: PaymentSummaryStatus(rawValue: status) ?? .requiresPaymentMethod
        )
    }
}

struct LeaseRequestsListAPIResponse: Codable {
    let leaseRequests: [LeaseRequestAPIResponse]

    enum CodingKeys: String, CodingKey {
        case leaseRequests = "lease_requests"
    }
}

struct CreateLeaseRequestAPIResponse: Codable {
    let chatId: UUID
    let leaseRequest: LeaseRequestAPIResponse

    enum CodingKeys: String, CodingKey {
        case chatId = "chat_id"
        case leaseRequest = "lease_request"
    }
}

struct PaymentIntentAPIResponse: Codable {
    let paymentIntentClientSecret: String
    let paymentIntentId: String
    let publishableKey: String
    let customerId: String?
    let ephemeralKeySecret: String?
    let amount: Int64
    let currency: String

    enum CodingKeys: String, CodingKey {
        case paymentIntentClientSecret = "payment_intent_client_secret"
        case paymentIntentId = "payment_intent_id"
        case publishableKey = "publishable_key"
        case customerId = "customer_id"
        case ephemeralKeySecret = "ephemeral_key_secret"
        case amount, currency
    }
}

// MARK: - Lease Request API Request Models

struct CreateLeaseRequestAPIRequest: Codable {
    let weeks: Int?
    let message: String?
}

struct UpdateLeaseRequestPriceAPIRequest: Codable {
    let offeredWeeklyPrice: Double

    enum CodingKeys: String, CodingKey {
        case offeredWeeklyPrice = "offered_weekly_price"
    }
}

struct ExtendPickupDeadlineAPIRequest: Codable {
    let minutes: Int
}

// MARK: - Shared Driver Documents

/// A driver onboarding document shared with the car owner through a lease
/// request. Mirrors `SharedDocumentResponse` in the Go backend. The backend
/// only exposes the public `fileUrl` (under /uploads/...) — no on-disk path.
struct SharedDocumentAPIResponse: Codable, Identifiable, Equatable {
    let id: UUID
    let documentId: UUID
    let uploaderId: UUID
    let type: String
    let fileName: String
    let fileUrl: String
    let fileSize: Int64
    let mimeType: String
    let status: String
    let sharedAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case documentId = "document_id"
        case uploaderId = "uploader_id"
        case type
        case fileName = "file_name"
        case fileUrl = "file_url"
        case fileSize = "file_size"
        case mimeType = "mime_type"
        case status
        case sharedAt = "shared_at"
    }

    var documentType: DocumentType? { DocumentType(rawValue: type) }
}

/// A vehicle/car document the owner uploaded for the listing (registration,
/// insurance, …). Surfaced to the DRIVER side of a chat. Mirrors
/// `VehicleDocumentResponse` in the Go backend; `file_url` is the signed
/// /uploads URL.
struct VehicleDocumentAPIResponse: Codable, Identifiable, Equatable {
    let id: UUID
    let documentType: String
    let fileName: String
    let fileUrl: String
    let fileSize: Int
    let mimeType: String
    let createdAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case documentType = "document_type"
        case fileName = "file_name"
        case fileUrl = "file_url"
        case fileSize = "file_size"
        case mimeType = "mime_type"
        case createdAt = "created_at"
    }

    /// Owner-facing label for the car-document categories the backend ships.
    /// Falls back to the raw string if the backend adds new types we don't
    /// know about yet.
    var displayName: String {
        switch documentType {
        case "registration": return "Vehicle Registration"
        case "insurance":    return "Insurance"
        case "inspection":   return "Inspection"
        case "permit":       return "Permit"
        default:             return documentType.capitalized
        }
    }
}

/// Role-aware response. `viewer_role` tells the iOS layer which section
/// to render; the empty-but-typed arrays let SwiftUI ForEach stay simple
/// regardless of role. The legacy `documents` field mirrors
/// `driver_documents` for backwards-compatibility with older app builds.
struct SharedDocumentsListAPIResponse: Codable {
    let viewerRole: String?
    let driverDocuments: [SharedDocumentAPIResponse]?
    let vehicleDocuments: [VehicleDocumentAPIResponse]?
    /// Legacy field — same payload as `driver_documents`. Older releases
    /// of this app only knew about `documents`; the backend keeps emitting
    /// it so they don't break. New code should use `driverDocuments`.
    let documents: [SharedDocumentAPIResponse]?

    enum CodingKeys: String, CodingKey {
        case viewerRole = "viewer_role"
        case driverDocuments = "driver_documents"
        case vehicleDocuments = "vehicle_documents"
        case documents
    }

    /// Normalized driver doc list across the two fields, in case we hit
    /// an older or newer backend during a rolling deploy.
    var driverDocumentsOrLegacy: [SharedDocumentAPIResponse] {
        driverDocuments ?? documents ?? []
    }

    /// Empty array if the backend hasn't started returning vehicle docs yet.
    var vehicleDocumentsOrEmpty: [VehicleDocumentAPIResponse] {
        vehicleDocuments ?? []
    }
}
