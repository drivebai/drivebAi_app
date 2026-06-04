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
