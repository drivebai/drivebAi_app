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
    /// Owner-changed-price review (backend migration 000028). When the
    /// owner adjusts the offered price mid-flow, `priceChangePending`
    /// flips to true and the driver MUST accept or decline before Pay Now
    /// becomes available — `driverCanPay` enforces the gate. The previous
    /// offered price is captured so the card can show old → new.
    let priceChangePending: Bool
    let previousOfferedWeeklyPrice: Double?
    let priceChangeActedAt: Date?
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

    /// Whether the driver can initiate or retry payment.
    ///
    /// Critically, this also returns false while the driver still has to
    /// accept or decline a price change the owner just made — paying
    /// through a stale offer would silently lock in an amount the driver
    /// never agreed to. Backend enforces the same gate on
    /// `/payments/intent` (409 PRICE_REVIEW_PENDING) as defence in depth.
    var driverCanPay: Bool {
        if priceChangePending { return false }
        if status == .accepted { return true }
        // payment_pending: a Stripe PaymentIntent exists but no payment has
        // succeeded yet. The driver should be able to (re)submit when the
        // intent is in any non-terminal, non-in-flight state. Critically
        // this includes `.requiresPaymentMethod` — that's the state Stripe
        // leaves the intent in after the user dismisses PaymentSheet
        // without confirming. Before this fix the card stayed on a yellow
        // "Processing" spinner forever in that scenario.
        if status == .paymentPending, let p = payment {
            return p.status == .failed
                || p.status == .canceled
                || p.status == .requiresPaymentMethod
        }
        return false
    }

    /// Formatted "old price" line for the strikethrough comparison on the
    /// card. Prefers the prior offered price (the thing the driver was
    /// last shown) when a review is pending; falls back to the base
    /// listing price when the current offered price simply differs from
    /// the listing price. Returns nil when there's nothing to compare.
    var priceComparisonOld: String? {
        let formatter = NumberFormatter()
        formatter.numberStyle = .currency
        formatter.currencyCode = currency
        if priceChangePending, let prev = previousOfferedWeeklyPrice {
            return formatter.string(from: NSNumber(value: prev))
        }
        if isPriceAdjusted {
            return formatter.string(from: NSNumber(value: weeklyPrice))
        }
        return nil
    }

    /// True iff the driver-side card should render the Accept/Decline
    /// price-review buttons. Non-terminal lease + price-review pending.
    var driverShouldReviewPrice: Bool {
        guard priceChangePending else { return false }
        switch status {
        case .requested, .accepted, .paymentPending:
            return true
        case .declined, .cancelled, .paid, .expired, .expiredRefunded:
            return false
        }
    }

    /// Payment is genuinely in-flight (Stripe processing, webhook pending).
    ///
    /// `.requiresPaymentMethod` is intentionally NOT in this set: that's the
    /// state Stripe assigns at intent creation and the state it returns to
    /// after the user dismisses PaymentSheet without confirming — there is
    /// no work happening on Stripe's side and the user must act. Counting it
    /// as "processing" was the cause of the stuck yellow-spinner bug.
    /// `.requiresConfirmation` IS in-flight (Stripe needs a confirm to
    /// finalize the payment).
    var isPaymentProcessing: Bool {
        guard status == .paymentPending, let p = payment else { return false }
        return p.status == .processing || p.status == .requiresConfirmation
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
