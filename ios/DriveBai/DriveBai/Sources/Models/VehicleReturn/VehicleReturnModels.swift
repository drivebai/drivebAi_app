import Foundation

// MARK: - Vehicle Return Domain Model

/// Lifecycle for the post-rental return handshake. Mirrors the backend's
/// `vehicle_returns.status` column. The two terminal states are `completed`
/// (refund finalized — or zero refund — and car released) and `cancelled`
/// (driver self-cancelled within the 5-min window OR admin rejected dispute).
enum VehicleReturnStatus: String, Codable, Equatable {
    case driverInitiated = "driver_initiated"
    case ownerConfirmed = "owner_confirmed"
    case disputed
    case completed
    case cancelled

    var isTerminal: Bool {
        switch self {
        case .completed, .cancelled: return true
        case .driverInitiated, .ownerConfirmed, .disputed: return false
        }
    }
}

/// Viewer-side role for a vehicle return. Mirrors `KeyHandoverRole`.
enum VehicleReturnRole: String, Codable, Equatable {
    case owner
    case driver
}

/// Mirrors `refund_status` on the backend row. Optional values map to nil.
enum VehicleReturnRefundStatus: String, Codable, Equatable {
    case pending
    case succeeded
    case failed
    case notApplicable = "not_applicable"
}

// MARK: - Vehicle Return

struct VehicleReturn: Identifiable, Equatable, Hashable {
    let id: UUID
    let leaseRequestId: UUID
    let chatId: UUID?
    let carId: UUID
    let carTitle: String
    let ownerId: UUID
    let driverId: UUID
    let ownerName: String
    let driverName: String
    let counterpartyName: String
    let viewerRole: VehicleReturnRole
    let status: VehicleReturnStatus

    // Lifecycle timestamps (all server-stamped)
    let driverInitiatedAt: Date
    let ownerConfirmedAt: Date?
    let disputedAt: Date?
    let completedAt: Date?
    let cancelledAt: Date?

    // Rental clock + refund computation snapshot
    let pickupConfirmedAt: Date
    let returnedAt: Date
    let rentalWeeks: Int
    let paidAmountCents: Int64
    let usedDays: Int
    let refundAmountCents: Int64
    let refundStatus: VehicleReturnRefundStatus?
    let refundId: String?
    let refundedAt: Date?

    // Dispute metadata
    let disputeReason: String?

    // 5-minute driver-cancel window (server-supplied so we don't drift)
    let cancelWindowExpiresAt: Date?

    let createdAt: Date
    let updatedAt: Date

    /// Fallback cancel window used when the backend payload omits the
    /// server-computed `cancel_window_expires_at` (older builds talking to
    /// a stale API). Mirrors the backend's `VehicleReturnCancelWindow`.
    static let driverCancelWindow: TimeInterval = 5 * 60
}

// MARK: - UI Helpers

extension VehicleReturn {
    var isOwner: Bool { viewerRole == .owner }
    var isDriver: Bool { viewerRole == .driver }

    /// True while the row should still appear in the Today list. Terminal
    /// states linger for one render cycle so the user sees the success/cancel
    /// banner, but `isActive` lets parents drop them after dismissal.
    var isActive: Bool { !status.isTerminal }

    /// Returns the effective cancel-window expiry. Prefers the server's
    /// value, falls back to `driverInitiatedAt + 5min` when absent.
    var effectiveCancelWindowExpiresAt: Date {
        cancelWindowExpiresAt ?? driverInitiatedAt.addingTimeInterval(VehicleReturn.driverCancelWindow)
    }

    /// True iff the driver still has time to undo the return.
    func isWithinDriverCancelWindow(now: Date = Date()) -> Bool {
        guard status == .driverInitiated, viewerRole == .driver else { return false }
        return effectiveCancelWindowExpiresAt > now
    }

    /// Time left for the driver to undo. nil outside the window.
    func driverCancelTimeRemaining(now: Date = Date()) -> TimeInterval? {
        guard isWithinDriverCancelWindow(now: now) else { return nil }
        return max(0, effectiveCancelWindowExpiresAt.timeIntervalSince(now))
    }

    /// Formatted dollars-with-cents for the refund amount. Returns "$0.00"
    /// when there's no refund due — callers decide whether to render it.
    var formattedRefundAmount: String {
        let dollars = Double(refundAmountCents) / 100.0
        let formatter = NumberFormatter()
        formatter.numberStyle = .currency
        formatter.currencyCode = "USD"
        return formatter.string(from: NSNumber(value: dollars)) ?? String(format: "$%.2f", dollars)
    }

    /// Formatted total paid (matches refund formatting).
    var formattedPaidAmount: String {
        let dollars = Double(paidAmountCents) / 100.0
        let formatter = NumberFormatter()
        formatter.numberStyle = .currency
        formatter.currencyCode = "USD"
        return formatter.string(from: NSNumber(value: dollars)) ?? String(format: "$%.2f", dollars)
    }

    /// True when the computed refund is positive and Stripe will actually
    /// move money. Drives the "$X.XX refunded" copy vs. the "no refund due"
    /// copy on the Today card / chat banner.
    var hasRefund: Bool { refundAmountCents > 0 }

    /// Per-role headline text for the Today card.
    var headline: String {
        switch (status, viewerRole) {
        case (.driverInitiated, .driver):
            return isWithinDriverCancelWindow() ? "Return submitted" : "Awaiting owner confirmation"
        case (.driverInitiated, .owner):
            return "Driver returned the car"
        case (.ownerConfirmed, _):
            return "Processing refund"
        case (.disputed, _):
            return "Return disputed"
        case (.completed, _):
            return hasRefund
                ? "Return complete — \(formattedRefundAmount) refunded"
                : "Return complete"
        case (.cancelled, _):
            return "Return cancelled"
        }
    }

    /// Per-role body text shown beneath the headline on the Today card.
    var bodyMessage: String {
        switch (status, viewerRole) {
        case (.driverInitiated, .driver):
            if isWithinDriverCancelWindow() {
                return "Waiting for \(counterpartyName) to confirm. You can undo for the next few minutes."
            }
            return "\(counterpartyName) will confirm receipt of the car."
        case (.driverInitiated, .owner):
            if hasRefund {
                return "\(counterpartyName) marked it returned. A refund of \(formattedRefundAmount) for \(unusedDaysCount()) unused day\(unusedDaysCount() == 1 ? "" : "s") will be issued on confirm."
            }
            return "Full rental period used. No refund will be issued on confirm."
        case (.ownerConfirmed, _):
            return hasRefund
                ? "Owner confirmed the return. Issuing your \(formattedRefundAmount) refund now."
                : "Owner confirmed the return. No refund is due."
        case (.disputed, _):
            if let reason = disputeReason, !reason.isEmpty {
                return "\"\(reason)\" — our team will follow up within 24 hours."
            }
            return "Our team has been notified and will follow up within 24 hours."
        case (.completed, _):
            return hasRefund
                ? "Refund posted to your original payment method."
                : "Full rental period was used."
        case (.cancelled, _):
            return "This return was cancelled."
        }
    }

    /// Primary CTA label, or nil when no action applies for this role/state.
    var primaryActionTitle: String? {
        switch (status, viewerRole) {
        case (.driverInitiated, .driver):
            return isWithinDriverCancelWindow() ? "Undo return" : nil
        case (.driverInitiated, .owner):
            return "Confirm return"
        default:
            return nil
        }
    }

    /// Secondary CTA label, currently only used for the owner-side Dispute.
    var secondaryActionTitle: String? {
        switch (status, viewerRole) {
        case (.driverInitiated, .owner):
            return "Dispute"
        default:
            return nil
        }
    }

    /// Total paid days (rental_weeks * 7), defended against missing data.
    var totalPaidDays: Int { max(rentalWeeks, 1) * 7 }

    /// Unused-days count for the disclaimer banner. Floor at 0.
    func unusedDaysCount() -> Int {
        max(0, totalPaidDays - usedDays)
    }
}
