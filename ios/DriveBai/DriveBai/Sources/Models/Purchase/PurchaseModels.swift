import Foundation

// MARK: - Purchase Request Status

/// Mirrors backend `purchase_request_status` enum. New states may be added by
/// the server; unknown values decode into `.requested` via
/// `PurchaseRequestStatus(rawValue:)?` fallbacks at the API layer.
enum PurchaseRequestStatus: String, Codable, Equatable {
    case requested
    case accepted
    case declined
    case cancelled
    case bosPendingSeller = "bos_pending_seller"
    case bosPendingBuyer = "bos_pending_buyer"
    case bosSigned = "bos_signed"
    case paymentAuthorized = "payment_authorized"
    case handoverScheduled = "handover_scheduled"
    case awaitingInspection = "awaiting_inspection"
    case inspectionAccepted = "inspection_accepted"
    case completed
    case inspectionRejected = "inspection_rejected"
    case rejectedRefunded = "rejected_refunded"
    case rejectedUpheld = "rejected_upheld"
    case expired
    case expiredAuth = "expired_auth"

    var displayText: String {
        switch self {
        case .requested: return "Offer sent"
        case .accepted: return "Offer accepted"
        case .declined: return "Offer declined"
        case .cancelled: return "Offer cancelled"
        case .bosPendingSeller: return "Awaiting seller signature"
        case .bosPendingBuyer: return "Awaiting buyer signature"
        case .bosSigned: return "Bill of Sale signed"
        case .paymentAuthorized: return "Payment authorized"
        case .handoverScheduled: return "Handover scheduled"
        case .awaitingInspection: return "Awaiting inspection"
        case .inspectionAccepted: return "Vehicle accepted"
        case .completed: return "Sold"
        case .inspectionRejected: return "Rejection under review"
        case .rejectedRefunded: return "Refunded"
        case .rejectedUpheld: return "Sale upheld"
        case .expired: return "Offer expired"
        case .expiredAuth: return "Authorization expired"
        }
    }

    var isTerminal: Bool {
        switch self {
        case .completed, .declined, .cancelled, .expired, .expiredAuth,
             .rejectedRefunded, .rejectedUpheld:
            return true
        default:
            return false
        }
    }

    /// True when the Bill of Sale wizard should be available for editing /
    /// signing. Covers `accepted` through `bosSigned`.
    var isBoSStage: Bool {
        switch self {
        case .accepted, .bosPendingSeller, .bosPendingBuyer, .bosSigned:
            return true
        default:
            return false
        }
    }
}

// MARK: - Rejection Enums

enum PurchaseRejectionReason: String, Codable, CaseIterable, Identifiable {
    case undisclosedDamage = "undisclosed_damage"
    case mechanicalIssues = "mechanical_issues"
    case titleOrPaperwork = "title_or_paperwork"
    case vinMismatch = "vin_mismatch"
    case notAsDescribed = "not_as_described"
    case noShow = "no_show"
    case other

    var id: String { rawValue }

    var displayText: String {
        switch self {
        case .undisclosedDamage: return "Undisclosed damage"
        case .mechanicalIssues: return "Mechanical issues"
        case .titleOrPaperwork: return "Title or paperwork"
        case .vinMismatch: return "VIN mismatch"
        case .notAsDescribed: return "Not as described"
        case .noShow: return "Seller no-show"
        case .other: return "Other"
        }
    }
}

enum PurchaseRejectionStatus: String, Codable {
    case submitted
    case underReview = "under_review"
    case accepted
    case upheld
    case withdrawn
}

enum PurchaseRefundStatus: String, Codable {
    case pending
    case succeeded
    case failed
    case notApplicable = "not_applicable"
}

// MARK: - Payment Held Copy

/// Single-source-of-truth copy for the payment-hold disclaimer. Referenced by
/// BoS review, ChoosePaymentOptionView, and InspectionView so the wording
/// stays identical across surfaces.
enum PurchaseCopy {
    static let paymentHoldHeadline = "Payment held by platform until you inspect and accept the vehicle"

    static let paymentHoldDetail: String = """
When you tap Authorize payment, DrivaBai places a hold on your card for the sale amount through Stripe. \
No funds are transferred to the seller yet — your bank simply reserves the amount.

When you accept the vehicle after inspection, the hold is captured and funds are charged. If you reject the \
vehicle with valid evidence and DrivaBai support agrees, the hold is released and you are not charged.

DrivaBai is not a licensed escrow agent, does not hold funds on the seller's behalf, and does not guarantee \
the sale. Vehicle title transfer, DMV paperwork, and any warranties are the responsibility of the buyer and \
seller.
"""
}

// MARK: - Purchase Request

struct PurchaseRequest: Identifiable, Equatable, Hashable {
    let id: UUID
    let carId: UUID
    let sellerId: UUID
    let buyerId: UUID
    let chatId: UUID

    let sellerName: String
    let buyerName: String
    let carTitle: String

    let offerAmountCents: Int64
    let currency: String
    let buyerMessage: String?

    let status: PurchaseRequestStatus
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
    let refundStatus: PurchaseRefundStatus?
    let refundId: String?
    let refundedAt: Date?

    let cancellationReason: String?

    let createdAt: Date
    let updatedAt: Date

    // MARK: - Money formatting

    var formattedOfferAmount: String {
        let dollars = Double(offerAmountCents) / 100.0
        let formatter = NumberFormatter()
        formatter.numberStyle = .currency
        formatter.currencyCode = currency
        formatter.maximumFractionDigits = 0
        return formatter.string(from: NSNumber(value: dollars)) ?? "$\(Int(dollars))"
    }

    var offerAmountDollars: Double {
        Double(offerAmountCents) / 100.0
    }

    // MARK: - Role helpers

    func isBuyer(_ userId: UUID) -> Bool { userId == buyerId }
    func isSeller(_ userId: UUID) -> Bool { userId == sellerId }

    /// A short "who to look at" string for the chat card.
    var counterpartyLabel: String {
        "\(buyerName) → \(sellerName)"
    }

    /// True when the buyer's card should surface an "Authorize payment" CTA.
    var buyerCanAuthorize: Bool {
        status == .bosSigned
    }

    /// True when the buyer's card should surface an "Inspect vehicle" CTA.
    var buyerCanInspect: Bool {
        status == .awaitingInspection
    }

    /// Time remaining on the inspection window, or nil when not applicable.
    func inspectionTimeRemaining(now: Date = Date()) -> TimeInterval? {
        guard let deadline = inspectionDeadlineAt, status == .awaitingInspection else {
            return nil
        }
        return max(0, deadline.timeIntervalSince(now))
    }
}
