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

    var displayText: String {
        switch self {
        case .requested: return "Requested"
        case .accepted: return "Accepted"
        case .declined: return "Declined"
        case .cancelled: return "Cancelled"
        case .paymentPending: return "Payment Pending"
        case .paid: return "Paid"
        case .expired: return "Expired"
        }
    }

    var isTerminal: Bool {
        switch self {
        case .declined, .cancelled, .paid, .expired: return true
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
    let createdAt: Date
    let updatedAt: Date

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
}
