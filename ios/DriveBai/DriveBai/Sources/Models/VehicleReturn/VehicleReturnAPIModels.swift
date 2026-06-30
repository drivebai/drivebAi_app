import Foundation

// MARK: - Vehicle Return API Models

/// Wire-format mirror of `vehicle_returns` rows, as returned by the
/// driver/owner/admin endpoints. The struct is intentionally optional-heavy:
/// older backends may omit fields, and disabling/completed rows leave most
/// of the lifecycle timestamps null.
struct VehicleReturnAPIModel: Codable, Identifiable {
    let id: UUID
    let leaseRequestId: UUID
    let chatId: UUID?
    let carId: UUID
    let carTitle: String?
    let ownerId: UUID
    let driverId: UUID
    let ownerName: String?
    let driverName: String?
    let counterpartyName: String?
    let viewerRole: String?

    let status: String

    let driverInitiatedAt: Date
    let ownerConfirmedAt: Date?
    let disputedAt: Date?
    let completedAt: Date?
    let cancelledAt: Date?

    let pickupConfirmedAt: Date
    let returnedAt: Date
    let rentalWeeks: Int
    let paidAmountCents: Int64
    let usedDays: Int
    let refundAmountCents: Int64
    let refundStatus: String?
    let refundId: String?
    let refundedAt: Date?

    let disputeReason: String?
    let cancelWindowExpiresAt: Date?

    let createdAt: Date
    let updatedAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case leaseRequestId = "lease_request_id"
        case chatId = "chat_id"
        case carId = "car_id"
        case carTitle = "car_title"
        case ownerId = "owner_id"
        case driverId = "driver_id"
        case ownerName = "owner_name"
        case driverName = "driver_name"
        case counterpartyName = "counterparty_name"
        case viewerRole = "viewer_role"
        case status
        case driverInitiatedAt = "driver_initiated_at"
        case ownerConfirmedAt = "owner_confirmed_at"
        case disputedAt = "disputed_at"
        case completedAt = "completed_at"
        case cancelledAt = "cancelled_at"
        case pickupConfirmedAt = "pickup_confirmed_at"
        case returnedAt = "returned_at"
        case rentalWeeks = "rental_weeks"
        case paidAmountCents = "paid_amount_cents"
        case usedDays = "used_days"
        case refundAmountCents = "refund_amount_cents"
        case refundStatus = "refund_status"
        case refundId = "refund_id"
        case refundedAt = "refunded_at"
        case disputeReason = "dispute_reason"
        case cancelWindowExpiresAt = "cancel_window_expires_at"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }

    func toDomain() -> VehicleReturn {
        let role = VehicleReturnRole(rawValue: viewerRole ?? "") ?? .driver
        let resolvedCounterparty = counterpartyName
            ?? (role == .owner ? driverName : ownerName)
            ?? ""
        return VehicleReturn(
            id: id,
            leaseRequestId: leaseRequestId,
            chatId: chatId,
            carId: carId,
            carTitle: carTitle ?? "",
            ownerId: ownerId,
            driverId: driverId,
            ownerName: ownerName ?? "",
            driverName: driverName ?? "",
            counterpartyName: resolvedCounterparty,
            viewerRole: role,
            status: VehicleReturnStatus(rawValue: status) ?? .driverInitiated,
            driverInitiatedAt: driverInitiatedAt,
            ownerConfirmedAt: ownerConfirmedAt,
            disputedAt: disputedAt,
            completedAt: completedAt,
            cancelledAt: cancelledAt,
            pickupConfirmedAt: pickupConfirmedAt,
            returnedAt: returnedAt,
            rentalWeeks: rentalWeeks,
            paidAmountCents: paidAmountCents,
            usedDays: usedDays,
            refundAmountCents: refundAmountCents,
            refundStatus: refundStatus.flatMap { VehicleReturnRefundStatus(rawValue: $0) },
            refundId: refundId,
            refundedAt: refundedAt,
            disputeReason: disputeReason,
            cancelWindowExpiresAt: cancelWindowExpiresAt,
            createdAt: createdAt,
            updatedAt: updatedAt
        )
    }
}

struct VehicleReturnsListAPIResponse: Codable {
    let vehicleReturns: [VehicleReturnAPIModel]

    enum CodingKeys: String, CodingKey {
        case vehicleReturns = "vehicle_returns"
    }
}

/// Body for `POST /vehicle-returns/{id}/dispute`. Required `reason` is the
/// 5-500 char justification the backend persists.
struct DisputeVehicleReturnAPIRequest: Codable {
    let reason: String
}
