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

struct SharedDocumentsListAPIResponse: Codable {
    let documents: [SharedDocumentAPIResponse]
}
