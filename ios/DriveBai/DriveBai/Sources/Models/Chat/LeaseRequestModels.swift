import Foundation

// MARK: - Lease Request Status

enum LeaseRequestStatus: String, Codable {
    case requested
    case accepted
    case declined
    case cancelled
    case paymentPending = "payment_pending"
    case paid
    case expired
    /// Driver failed to confirm pickup within PICKUP_DEADLINE_MINUTES; payment
    /// was refunded and the car returned to discovery. Terminal.
    case expiredRefunded = "expired_refunded"

    var displayText: String {
        switch self {
        case .requested: return "Requested"
        case .accepted: return "Accepted"
        case .declined: return "Declined"
        case .cancelled: return "Cancelled"
        case .paymentPending: return "Payment Pending"
        case .paid: return "Paid"
        case .expired: return "Expired"
        case .expiredRefunded: return "Refunded"
        }
    }

    var isTerminal: Bool {
        switch self {
        case .declined, .cancelled, .paid, .expired, .expiredRefunded: return true
        case .requested, .accepted, .paymentPending: return false
        }
    }

    /// Whether the owner can accept/decline
    var ownerCanRespond: Bool { self == .requested }

    /// Whether the driver can cancel
    var driverCanCancel: Bool { self == .requested }
}

// MARK: - Payment Summary Status

enum PaymentSummaryStatus: String, Codable {
    case requiresPaymentMethod = "requires_payment_method"
    case requiresConfirmation = "requires_confirmation"
    case processing
    case succeeded
    case canceled
    case failed

    var isTerminal: Bool {
        self == .succeeded || self == .canceled || self == .failed
    }
}

// MARK: - Payment Summary

struct PaymentSummary: Equatable {
    let id: UUID
    let paymentIntentId: String?
    let amount: Int64
    let platformFeeAmount: Int64
    let currency: String
    let status: PaymentSummaryStatus
}

// MARK: - Lease Request

struct LeaseRequest: Identifiable, Equatable {
    let id: UUID
    let chatId: UUID
    let listingId: UUID
    let ownerId: UUID
    let driverId: UUID
    let driverName: String
    let ownerName: String
    let status: LeaseRequestStatus
    let weeklyPrice: Double
    let offeredWeeklyPrice: Double?
    let totalAmount: Double
    let currency: String
    let weeks: Int
    let message: String?
    let carTitle: String
    let payment: PaymentSummary?
    /// Pickup deadline lifecycle (backend migration 000024). Only present
    /// once `status == .paid`; cleared by `confirmPickup`/refund flows.
    let pickupDeadlineAt: Date?
    let pickupConfirmedAt: Date?
    let refundId: String?
    let refundedAt: Date?
    let refundStatus: String?
    /// Pickup extension (backend migration 000025). Total minutes the owner
    /// has added across all extensions, plus remaining headroom against the
    /// `maxPickupExtensionMinutes` cap. The remaining value is computed
    /// server-side so the client doesn't need to know the cap to disable
    /// preset buttons that would exceed it.
    let pickupExtensionTotalMinutes: Int
    let pickupExtensionCount: Int
    let pickupExtensionRemainingMinutes: Int
    let pickupLastExtendedAt: Date?
    let createdAt: Date
    let updatedAt: Date

    /// Hard cap kept in sync with the Go layer's `PickupMaxExtensionMinutes`.
    /// Used as the fallback default when decoding older API payloads.
    static let maxPickupExtensionMinutes: Int = 120

    /// Preset increments matching the backend's `AllowedPickupExtensionMinutes`.
    static let allowedPickupExtensionMinutes: [Int] = [15, 30, 60]

    /// The price actually in effect: owner's offer when set, otherwise the base listing price.
    var effectiveWeeklyPrice: Double { offeredWeeklyPrice ?? weeklyPrice }

    /// True when the owner has explicitly set a custom price different from the listing price.
    var isPriceAdjusted: Bool { offeredWeeklyPrice != nil && offeredWeeklyPrice != weeklyPrice }

    var formattedWeeklyPrice: String? {
        let formatter = NumberFormatter()
        formatter.numberStyle = .currency
        formatter.currencyCode = currency
        return formatter.string(from: NSNumber(value: weeklyPrice))
    }

    var formattedEffectiveWeeklyPrice: String? {
        let formatter = NumberFormatter()
        formatter.numberStyle = .currency
        formatter.currencyCode = currency
        return formatter.string(from: NSNumber(value: effectiveWeeklyPrice))
    }

    var formattedTotalAmount: String? {
        let formatter = NumberFormatter()
        formatter.numberStyle = .currency
        formatter.currencyCode = currency
        return formatter.string(from: NSNumber(value: totalAmount))
    }

    /// Whether the driver can initiate or retry payment
    var driverCanPay: Bool {
        if status == .accepted { return true }
        // payment_pending: only allow retry if payment failed or was canceled
        if status == .paymentPending, let p = payment {
            return p.status == .failed || p.status == .canceled
        }
        return false
    }

    /// Payment is in-flight (Stripe processing, webhook pending)
    var isPaymentProcessing: Bool {
        guard status == .paymentPending, let p = payment else { return false }
        return p.status == .processing || p.status == .requiresPaymentMethod || p.status == .requiresConfirmation
    }

    /// Payment succeeded on Stripe but webhook hasn't flipped lease to paid yet
    var isPaymentSucceededAwaitingWebhook: Bool {
        status == .paymentPending && payment?.status == .succeeded
    }

    /// True when the lease is paid and the driver still needs to confirm
    /// pickup before the deadline elapses.
    var isAwaitingPickupConfirmation: Bool {
        status == .paid && pickupConfirmedAt == nil && pickupDeadlineAt != nil
    }

    /// Time remaining (positive) until the pickup deadline. nil if not paid
    /// or no deadline is set.
    func pickupTimeRemaining(now: Date = Date()) -> TimeInterval? {
        guard let deadline = pickupDeadlineAt, isAwaitingPickupConfirmation else { return nil }
        return max(0, deadline.timeIntervalSince(now))
    }

    /// True when the owner is still allowed to add time. Mirrors the server-side
    /// guard so we can disable the "Add more time" button preemptively.
    func canOwnerExtendPickup(now: Date = Date()) -> Bool {
        guard isAwaitingPickupConfirmation,
              let remaining = pickupTimeRemaining(now: now),
              remaining > 0 else { return false }
        return pickupExtensionRemainingMinutes >= LeaseRequest.allowedPickupExtensionMinutes.min() ?? 0
    }

    /// Presets the UI should surface — only those that still fit in the cap.
    var availableExtensionPresets: [Int] {
        LeaseRequest.allowedPickupExtensionMinutes.filter { $0 <= pickupExtensionRemainingMinutes }
    }
}
