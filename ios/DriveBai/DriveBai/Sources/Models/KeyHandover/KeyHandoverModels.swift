import Foundation

// MARK: - Key Handover Domain Model

enum KeyHandoverStatus: String {
    case pending
    case ownerConfirmed = "owner_confirmed"
    case completed
    case expired
}

enum KeyHandoverRole: String {
    case owner
    case driver
}

struct KeyHandover: Identifiable, Equatable, Hashable {
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
    let viewerRole: KeyHandoverRole
    let pickupArea: String
    let pickupLatitude: Double?
    let pickupLongitude: Double?
    let status: KeyHandoverStatus
    let ownerConfirmedAt: Date?
    let driverConfirmedAt: Date?
    let confirmationDeadline: Date?
    let startedAt: Date?
    let createdAt: Date
    let updatedAt: Date
}

// MARK: - UI Helpers
// Centralized per-role / per-status presentation so the Today card and the
// detail view stay consistent.

extension KeyHandover {
    var isOwner: Bool { viewerRole == .owner }

    /// Short status line shown on the card and detail header.
    var statusMessage: String {
        switch status {
        case .pending:
            return isOwner
                ? "Meet the driver at the pickup location and hand over the keys."
                : "Meet the owner at the pickup location to collect the keys."
        case .ownerConfirmed:
            return isOwner
                ? "You marked the keys as handed over. Waiting for the driver to confirm receipt."
                : "The owner handed over the keys. Confirm receipt to start your rental."
        case .completed:
            return "Handover complete — the rental has started."
        case .expired:
            return "The confirmation window expired before receipt was confirmed."
        }
    }

    /// The primary CTA title for the current viewer, or nil when no action applies.
    var primaryActionTitle: String? {
        switch (status, viewerRole) {
        case (.pending, .owner):
            return "I handed over the keys"
        case (.ownerConfirmed, .driver):
            return "I received the keys"
        default:
            return nil
        }
    }

    var canAct: Bool { primaryActionTitle != nil }

    /// True while the driver's confirmation window is counting down.
    var showsCountdown: Bool {
        status == .ownerConfirmed && confirmationDeadline != nil
    }

    var pickupLocationText: String {
        pickupArea.isEmpty ? "See chat for the pickup location" : pickupArea
    }
}
