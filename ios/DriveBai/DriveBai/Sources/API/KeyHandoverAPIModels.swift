import Foundation

// MARK: - Key Handover API Models

struct KeyHandoverAPIModel: Codable {
    let id: UUID
    let leaseRequestId: UUID
    let carId: UUID
    let carTitle: String
    let chatId: UUID?
    let ownerId: UUID
    let driverId: UUID
    let ownerName: String
    let driverName: String
    let counterpartyName: String
    let viewerRole: String
    let pickupArea: String
    let pickupLatitude: Double?
    let pickupLongitude: Double?
    let status: String
    let ownerConfirmedAt: Date?
    let driverConfirmedAt: Date?
    let confirmationDeadline: Date?
    let startedAt: Date?
    // Lease-side mirror (added so the Today tab can render the pickup
    // countdown + owner extension UI without a second fetch).
    let leaseStatus: String?
    let pickupDeadlineAt: Date?
    let pickupConfirmedAt: Date?
    let pickupExtensionTotalMinutes: Int?
    let pickupExtensionCount: Int?
    let pickupExtensionRemainingMinutes: Int?
    let pickupLastExtendedAt: Date?
    let createdAt: Date
    let updatedAt: Date

    enum CodingKeys: String, CodingKey {
        case id, status
        case leaseRequestId = "lease_request_id"
        case carId = "car_id"
        case carTitle = "car_title"
        case chatId = "chat_id"
        case ownerId = "owner_id"
        case driverId = "driver_id"
        case ownerName = "owner_name"
        case driverName = "driver_name"
        case counterpartyName = "counterparty_name"
        case viewerRole = "viewer_role"
        case pickupArea = "pickup_area"
        case pickupLatitude = "pickup_latitude"
        case pickupLongitude = "pickup_longitude"
        case ownerConfirmedAt = "owner_confirmed_at"
        case driverConfirmedAt = "driver_confirmed_at"
        case confirmationDeadline = "confirmation_deadline"
        case startedAt = "started_at"
        case leaseStatus = "lease_status"
        case pickupDeadlineAt = "pickup_deadline_at"
        case pickupConfirmedAt = "pickup_confirmed_at"
        case pickupExtensionTotalMinutes = "pickup_extension_total_minutes"
        case pickupExtensionCount = "pickup_extension_count"
        case pickupExtensionRemainingMinutes = "pickup_extension_remaining_minutes"
        case pickupLastExtendedAt = "pickup_last_extended_at"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }

    func toDomain() -> KeyHandover {
        KeyHandover(
            id: id,
            leaseRequestId: leaseRequestId,
            carId: carId,
            carTitle: carTitle,
            chatId: chatId,
            ownerId: ownerId,
            driverId: driverId,
            ownerName: ownerName,
            driverName: driverName,
            counterpartyName: counterpartyName,
            viewerRole: KeyHandoverRole(rawValue: viewerRole) ?? .driver,
            pickupArea: pickupArea,
            pickupLatitude: pickupLatitude,
            pickupLongitude: pickupLongitude,
            status: KeyHandoverStatus(rawValue: status) ?? .pending,
            ownerConfirmedAt: ownerConfirmedAt,
            driverConfirmedAt: driverConfirmedAt,
            confirmationDeadline: confirmationDeadline,
            startedAt: startedAt,
            leaseStatus: leaseStatus.flatMap { LeaseRequestStatus(rawValue: $0) },
            pickupDeadlineAt: pickupDeadlineAt,
            pickupConfirmedAt: pickupConfirmedAt,
            pickupExtensionTotalMinutes: pickupExtensionTotalMinutes ?? 0,
            pickupExtensionCount: pickupExtensionCount ?? 0,
            pickupExtensionRemainingMinutes: pickupExtensionRemainingMinutes ?? LeaseRequest.maxPickupExtensionMinutes,
            pickupLastExtendedAt: pickupLastExtendedAt,
            createdAt: createdAt,
            updatedAt: updatedAt
        )
    }
}

struct KeyHandoversListAPIResponse: Codable {
    let keyHandovers: [KeyHandoverAPIModel]

    enum CodingKeys: String, CodingKey {
        case keyHandovers = "key_handovers"
    }
}

/// Response body of POST /key-handovers/{id}/dismiss. Always `"dismissed"`
/// on success; kept as a struct (not just a string) so we can extend it
/// later without breaking the call site.
struct DismissAPIResponse: Codable {
    let status: String
}
