import Foundation

// MARK: - Purchase Request API Models

/// Wire-shape mirror of the backend `PurchaseRequestResponse`. All decoding
/// goes through the client's shared `JSONDecoder` (which uses
/// ISO8601DateParser under the hood), so nested Date fields Just Work.
struct PurchaseRequestAPIResponse: Codable, Identifiable {
    let id: UUID
    let carId: UUID
    let sellerId: UUID
    let buyerId: UUID
    let chatId: UUID

    let sellerName: String?
    let buyerName: String?
    let carTitle: String?

    // Vehicle identity (present on the response; decoded defensively as
    // optional so older payloads without these keys still decode).
    let vehicleYear: Int?
    let vehicleMake: String?
    let vehicleModel: String?
    let vehicleVin: String?

    let offerAmountCents: Int64
    let currency: String
    let buyerMessage: String?

    let status: String
    let expiresAt: Date?
    let authExpiresAt: Date?
    let handoverLocation: String?
    let handoverLatitude: Double?
    let handoverLongitude: Double?
    let handoverScheduledAt: Date?
    let keysHandedOverAt: Date?
    let inspectionDeadlineAt: Date?
    let inspectionAcceptedAt: Date?
    let completedAt: Date?

    let paymentIntentId: String?
    let paymentStatus: String?
    let refundStatus: String?
    let refundId: String?
    let refundedAt: Date?

    let cancellationReason: String?

    let createdAt: Date
    let updatedAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case carId = "car_id"
        case sellerId = "seller_id"
        case buyerId = "buyer_id"
        case chatId = "chat_id"
        case sellerName = "seller_name"
        case buyerName = "buyer_name"
        case carTitle = "car_title"
        case vehicleYear = "vehicle_year"
        case vehicleMake = "vehicle_make"
        case vehicleModel = "vehicle_model"
        case vehicleVin = "vehicle_vin"
        case offerAmountCents = "offer_amount_cents"
        case currency
        case buyerMessage = "buyer_message"
        case status
        case expiresAt = "expires_at"
        case authExpiresAt = "auth_expires_at"
        case handoverLocation = "handover_location"
        case handoverLatitude = "handover_latitude"
        case handoverLongitude = "handover_longitude"
        case handoverScheduledAt = "handover_scheduled_at"
        case keysHandedOverAt = "keys_handed_over_at"
        case inspectionDeadlineAt = "inspection_deadline_at"
        case inspectionAcceptedAt = "inspection_accepted_at"
        case completedAt = "completed_at"
        case paymentIntentId = "payment_intent_id"
        case paymentStatus = "payment_status"
        case refundStatus = "refund_status"
        case refundId = "refund_id"
        case refundedAt = "refunded_at"
        case cancellationReason = "cancellation_reason"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }

    func toDomain() -> PurchaseRequest {
        PurchaseRequest(
            id: id,
            carId: carId,
            sellerId: sellerId,
            buyerId: buyerId,
            chatId: chatId,
            sellerName: sellerName ?? "Seller",
            buyerName: buyerName ?? "Buyer",
            carTitle: carTitle ?? "Car",
            vehicleYear: vehicleYear ?? 0,
            vehicleMake: vehicleMake ?? "",
            vehicleModel: vehicleModel ?? "",
            vehicleVin: vehicleVin ?? "",
            offerAmountCents: offerAmountCents,
            currency: currency,
            buyerMessage: buyerMessage,
            status: PurchaseRequestStatus(rawValue: status) ?? .requested,
            expiresAt: expiresAt,
            authExpiresAt: authExpiresAt,
            handoverLocation: handoverLocation,
            handoverLatitude: handoverLatitude,
            handoverLongitude: handoverLongitude,
            handoverScheduledAt: handoverScheduledAt,
            keysHandedOverAt: keysHandedOverAt,
            inspectionDeadlineAt: inspectionDeadlineAt,
            inspectionAcceptedAt: inspectionAcceptedAt,
            completedAt: completedAt,
            paymentIntentId: paymentIntentId,
            paymentStatus: paymentStatus,
            refundStatus: refundStatus.flatMap { PurchaseRefundStatus(rawValue: $0) },
            refundId: refundId,
            refundedAt: refundedAt,
            cancellationReason: cancellationReason,
            createdAt: createdAt,
            updatedAt: updatedAt
        )
    }
}

// MARK: - Bill of Sale API Models

struct BillOfSaleAPIResponse: Codable, Identifiable {
    let id: UUID
    let purchaseRequestId: UUID
    let vehicleYear: Int
    let vehicleMake: String
    let vehicleModel: String
    let vin: String
    let saleAmountCents: Int64
    let currency: String
    let termsConditions: String
    let sellerName: String
    let sellerAddress: String
    let sellerAddressLat: Double?
    let sellerAddressLng: Double?
    let sellerSignatureUrl: String?
    let sellerSignedAt: Date?
    let buyerName: String
    let buyerAddress: String
    let buyerAddressLat: Double?
    let buyerAddressLng: Double?
    let buyerSignatureUrl: String?
    let buyerSignedAt: Date?
    let titleCondition: String?
    let titleConditionOther: String?
    let sellerIdDocumentUrl: String?
    let buyerIdDocumentUrl: String?
    let titleDocumentUrl: String?
    let titleUploaded: Bool?
    let finalizedPdfUrl: String?
    let finalizedAt: Date?
    let createdAt: Date
    let updatedAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case purchaseRequestId = "purchase_request_id"
        case vehicleYear = "vehicle_year"
        case vehicleMake = "vehicle_make"
        case vehicleModel = "vehicle_model"
        case vin
        case saleAmountCents = "sale_amount_cents"
        case currency
        case termsConditions = "terms_conditions"
        case sellerName = "seller_name"
        case sellerAddress = "seller_address"
        case sellerAddressLat = "seller_address_lat"
        case sellerAddressLng = "seller_address_lng"
        case sellerSignatureUrl = "seller_signature_url"
        case sellerSignedAt = "seller_signed_at"
        case buyerName = "buyer_name"
        case buyerAddress = "buyer_address"
        case buyerAddressLat = "buyer_address_lat"
        case buyerAddressLng = "buyer_address_lng"
        case buyerSignatureUrl = "buyer_signature_url"
        case buyerSignedAt = "buyer_signed_at"
        case titleCondition = "title_condition"
        case titleConditionOther = "title_condition_other"
        case sellerIdDocumentUrl = "seller_id_document_url"
        case buyerIdDocumentUrl = "buyer_id_document_url"
        case titleDocumentUrl = "title_document_url"
        case titleUploaded = "title_uploaded"
        case finalizedPdfUrl = "finalized_pdf_url"
        case finalizedAt = "finalized_at"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }

    func toDomain() -> BillOfSale {
        BillOfSale(
            id: id,
            purchaseRequestId: purchaseRequestId,
            vehicleYear: vehicleYear,
            vehicleMake: vehicleMake,
            vehicleModel: vehicleModel,
            vin: vin,
            saleAmountCents: saleAmountCents,
            currency: currency,
            termsConditions: termsConditions,
            sellerName: sellerName,
            sellerAddress: sellerAddress,
            sellerAddressLat: sellerAddressLat,
            sellerAddressLng: sellerAddressLng,
            sellerSignatureUrl: sellerSignatureUrl,
            sellerSignedAt: sellerSignedAt,
            buyerName: buyerName,
            buyerAddress: buyerAddress,
            buyerAddressLat: buyerAddressLat,
            buyerAddressLng: buyerAddressLng,
            buyerSignatureUrl: buyerSignatureUrl,
            buyerSignedAt: buyerSignedAt,
            titleCondition: titleCondition.flatMap { TitleCondition(rawValue: $0) },
            titleConditionOther: titleConditionOther,
            sellerIdDocumentUrl: sellerIdDocumentUrl,
            buyerIdDocumentUrl: buyerIdDocumentUrl,
            titleDocumentUrl: titleDocumentUrl,
            titleUploaded: titleUploaded ?? false,
            finalizedPdfUrl: finalizedPdfUrl,
            finalizedAt: finalizedAt,
            createdAt: createdAt,
            updatedAt: updatedAt
        )
    }
}

// MARK: - Rejection API Models

struct PurchaseRejectionEvidenceAPIResponse: Codable, Identifiable {
    let id: UUID
    let purchaseRejectionId: UUID?
    let fileUrl: String
    let filename: String?
    let mimeType: String
    let sizeBytes: Int64
    let createdAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case purchaseRejectionId = "purchase_rejection_id"
        case fileUrl = "file_url"
        case filename
        case mimeType = "mime_type"
        case sizeBytes = "size_bytes"
        case createdAt = "created_at"
    }

    func toDomain() -> PurchaseRejectionEvidence {
        PurchaseRejectionEvidence(
            id: id,
            purchaseRejectionId: purchaseRejectionId ?? UUID(),
            fileUrl: fileUrl,
            filename: filename ?? "file",
            mimeType: mimeType,
            sizeBytes: sizeBytes,
            createdAt: createdAt
        )
    }
}

struct PurchaseRejectionAPIResponse: Codable, Identifiable {
    let id: UUID
    let purchaseRequestId: UUID
    let reasonCategory: String
    let explanation: String
    let status: String
    let refundStatus: String?
    let adminNote: String?
    let resolvedBy: UUID?
    let resolvedAt: Date?
    let evidence: [PurchaseRejectionEvidenceAPIResponse]?
    let createdAt: Date
    let updatedAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case purchaseRequestId = "purchase_request_id"
        case reasonCategory = "reason_category"
        case explanation
        case status
        case refundStatus = "refund_status"
        case adminNote = "admin_note"
        case resolvedBy = "resolved_by"
        case resolvedAt = "resolved_at"
        case evidence
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }

    func toDomain() -> PurchaseRejection {
        PurchaseRejection(
            id: id,
            purchaseRequestId: purchaseRequestId,
            reasonCategory: PurchaseRejectionReason(rawValue: reasonCategory) ?? .other,
            explanation: explanation,
            status: PurchaseRejectionStatus(rawValue: status) ?? .submitted,
            refundStatus: refundStatus.flatMap { PurchaseRefundStatus(rawValue: $0) },
            adminNote: adminNote,
            resolvedBy: resolvedBy,
            resolvedAt: resolvedAt,
            evidence: (evidence ?? []).map { $0.toDomain() },
            createdAt: createdAt,
            updatedAt: updatedAt
        )
    }
}

// MARK: - Envelope Types

struct PurchaseRequestsListAPIResponse: Codable {
    let purchaseRequests: [PurchaseRequestAPIResponse]

    enum CodingKeys: String, CodingKey {
        case purchaseRequests = "purchase_requests"
    }
}

// NOTE: POST /cars/{carId}/purchase-requests returns the raw
// PurchaseRequestAPIResponse (matching GET /purchase-requests/{id} and
// all other purchase reads) — no wrapper envelope. The previous
// wrapper struct triggered "The data couldn't be read because it is
// missing" every time a buyer tapped Send offer.

// MARK: - Request payloads

struct CreatePurchaseRequestAPIRequest: Codable {
    let offerAmountCents: Int64
    let buyerMessage: String?

    enum CodingKeys: String, CodingKey {
        case offerAmountCents = "offer_amount_cents"
        case buyerMessage = "buyer_message"
    }
}

/// Seller-side PATCH body — mirrors the backend's UpdateBOSBody exactly.
/// sale_amount_cents intentionally NOT included; the BoS sale amount is
/// locked to the purchase's offer_amount_cents to prevent contract-vs-
/// charge divergence. Buyer identity fields go through the separate
/// UpdateBillOfSaleBuyerFieldsAPIRequest against /bos/buyer-fields.
struct UpdateBillOfSaleAPIRequest: Codable {
    let vehicleYear: Int?
    let vehicleMake: String?
    let vehicleModel: String?
    let vin: String?
    let termsConditions: String?
    let sellerName: String?
    let sellerAddress: String?
    let sellerAddressLat: Double?
    let sellerAddressLng: Double?
    let titleCondition: String?
    let titleConditionOther: String?

    enum CodingKeys: String, CodingKey {
        case vehicleYear = "vehicle_year"
        case vehicleMake = "vehicle_make"
        case vehicleModel = "vehicle_model"
        case vin
        case termsConditions = "terms_conditions"
        case sellerName = "seller_name"
        case sellerAddress = "seller_address"
        case sellerAddressLat = "seller_address_lat"
        case sellerAddressLng = "seller_address_lng"
        case titleCondition = "title_condition"
        case titleConditionOther = "title_condition_other"
    }
}

/// Buyer-side PATCH body — mirrors backend's UpdateBOSBuyerFieldsBody.
/// Hits /bos/buyer-fields; using the seller endpoint returns 403.
struct UpdateBillOfSaleBuyerFieldsAPIRequest: Codable {
    let buyerName: String?
    let buyerAddress: String?
    let buyerAddressLat: Double?
    let buyerAddressLng: Double?

    enum CodingKeys: String, CodingKey {
        case buyerName = "buyer_name"
        case buyerAddress = "buyer_address"
        case buyerAddressLat = "buyer_address_lat"
        case buyerAddressLng = "buyer_address_lng"
    }
}

/// Buyer inspection-accept body — mirrors backend's InspectVehicleAcceptBody.
/// Every field must be `true`; the server also requires a title document on
/// file (409 TITLE_REQUIRED) and a BoS title_condition set (400
/// INSPECTION_CHECKLIST_INCOMPLETE) before it will capture payment.
struct InspectVehicleAcceptAPIRequest: Codable {
    let vinMatches: Bool
    let odometerReviewed: Bool
    let exteriorOk: Bool
    let interiorOk: Bool
    let mechanicalTestDriveOk: Bool
    let titleReviewed: Bool
    let keysHandedOver: Bool
    let buyerUnderstandsAcceptanceCompletesPayment: Bool

    enum CodingKeys: String, CodingKey {
        case vinMatches = "vin_matches"
        case odometerReviewed = "odometer_reviewed"
        case exteriorOk = "exterior_ok"
        case interiorOk = "interior_ok"
        case mechanicalTestDriveOk = "mechanical_test_drive_ok"
        case titleReviewed = "title_reviewed"
        case keysHandedOver = "keys_handed_over"
        case buyerUnderstandsAcceptanceCompletesPayment = "buyer_understands_acceptance_completes_payment"
    }
}

struct RejectVehicleAPIRequest: Codable {
    let reasonCategory: String
    let explanation: String
    let evidenceIds: [UUID]

    enum CodingKeys: String, CodingKey {
        case reasonCategory = "reason_category"
        case explanation
        case evidenceIds = "evidence_ids"
    }
}

struct DeclinePurchaseAPIRequest: Codable {
    let reason: String?
}

struct ScheduleHandoverAPIRequest: Codable {
    let handoverScheduledAt: Date
    let handoverLocation: String
    let handoverLatitude: Double?
    let handoverLongitude: Double?

    enum CodingKeys: String, CodingKey {
        case handoverScheduledAt = "handover_scheduled_at"
        case handoverLocation = "handover_location"
        case handoverLatitude = "handover_latitude"
        case handoverLongitude = "handover_longitude"
    }
}
