import Foundation

// Rolling weekly billing — the driver-facing surface.
// Mirrors internal/handlers/rolling_driver.go: every key below is the
// backend's, and the cycle status vocabulary is the engine's, so the card
// branches on exactly the states the server can be in.
//
// Dates decode through APIClient's shared date strategy (ISO8601DateParser),
// which already accepts both the RFC3339 the lease payload emits and the
// fractional-second RFC3339Nano these endpoints emit from raw time.Time.

/// The one open cycle on a lease (structural invariant: at most one).
struct BillingOpenCycleAPIModel: Codable, Equatable {
    let id: UUID
    /// scheduled | charging | retrying | needs_action | failed_final
    let status: String
    let amountCents: Int64
    let periodStart: Date?
    let periodEnd: Date?
    /// Present for the DRIVER only, and only in the states a driver action
    /// can rescue (needs_action / retrying / failed_final) on a live lease.
    /// The server withholds it on a stopped, halted, or returning lease.
    let clientSecret: String?

    enum CodingKeys: String, CodingKey {
        case id, status
        case amountCents = "amount_cents"
        case periodStart = "period_start"
        case periodEnd = "period_end"
        case clientSecret = "client_secret"
    }

    /// Stripe needs the driver to complete authentication (3DS) before this
    /// week's charge can settle.
    var needsAuthentication: Bool { status == "needs_action" }

    /// The charge failed and is either mid-retry-ladder or out of retries.
    var isUncollected: Bool { status == "retrying" || status == "failed_final" }
}

/// A post-return balance: days used on a final week that was never collected.
struct BillingArrearsAPIModel: Codable, Equatable {
    let cycleId: UUID
    let amountCents: Int64
    let periodStart: Date?
    let periodEnd: Date?

    enum CodingKeys: String, CodingKey {
        case cycleId = "cycle_id"
        case amountCents = "amount_cents"
        case periodStart = "period_start"
        case periodEnd = "period_end"
    }
}

/// A price change offered mid-rental. It applies from the NEXT cycle and
/// never to the week already paid for — the driver's authorization records a
/// specific amount, so changing it without a fresh agreement would be an
/// unauthorized charge, which auto-loses as a dispute.
struct BillingAmendmentAPIModel: Codable, Identifiable, Equatable {
    let id: UUID
    let leaseRequestId: UUID
    let proposedBy: UUID
    /// "price" | "interval"
    let kind: String
    let newAmountCents: Int64
    let newInterval: String?
    let status: String
    let expiresAt: Date?

    enum CodingKeys: String, CodingKey {
        case id
        case leaseRequestId = "lease_request_id"
        case proposedBy = "proposed_by"
        case kind
        case newAmountCents = "new_amount_cents"
        case newInterval = "new_interval"
        case status
        case expiresAt = "expires_at"
    }
}

/// The pending amendment plus the EXACT text acceptance would record, so the
/// driver sees what they are agreeing to before they agree to it.
struct PendingAmendmentAPIModel: Codable, Equatable {
    let offer: BillingAmendmentAPIModel
    let disclosurePreview: String?

    enum CodingKeys: String, CodingKey {
        case offer
        case disclosurePreview = "disclosure_preview"
    }
}

struct ProposeAmendmentAPIRequest: Codable {
    let kind: String
    let newAmountCents: Int64

    enum CodingKeys: String, CodingKey {
        case kind
        case newAmountCents = "new_amount_cents"
    }
}

/// GET /lease-requests/{id}/billing
struct BillingStatusAPIResponse: Codable {
    let billingMode: String
    let rentalEndsAt: Date?
    let renewalStoppedAt: Date?
    let renewalHaltedReason: String?
    let delinquentSince: Date?
    let vehicleReturnedAt: Date?
    /// Mandate summary — absent until a consent row exists.
    let amountCents: Int64?
    let cardBrand: String?
    let cardLast4: String?
    let consentActive: Bool?
    let termsVersion: String?
    /// Only present while renewals are actually live.
    let nextChargeAt: Date?
    let openCycle: BillingOpenCycleAPIModel?
    let arrears: BillingArrearsAPIModel?
    let pendingAmendment: PendingAmendmentAPIModel?

    enum CodingKeys: String, CodingKey {
        case billingMode = "billing_mode"
        case rentalEndsAt = "rental_ends_at"
        case renewalStoppedAt = "renewal_stopped_at"
        case renewalHaltedReason = "renewal_halted_reason"
        case delinquentSince = "delinquent_since"
        case vehicleReturnedAt = "vehicle_returned_at"
        case amountCents = "amount_cents"
        case cardBrand = "card_brand"
        case cardLast4 = "card_last4"
        case consentActive = "consent_active"
        case termsVersion = "terms_version"
        case nextChargeAt = "next_charge_at"
        case openCycle = "open_cycle"
        case arrears
        case pendingAmendment = "pending_amendment"
    }

    var isRolling: Bool { billingMode == "rolling" }
    var isReturned: Bool { vehicleReturnedAt != nil }
    var renewalsRunning: Bool {
        renewalStoppedAt == nil && renewalHaltedReason == nil && vehicleReturnedAt == nil
    }
}

/// POST /lease-requests/{id}/billing/card-update — a SetupIntent for
/// PaymentSheet in setup mode.
struct CardUpdateStartAPIResponse: Codable {
    let setupIntentClientSecret: String
    let setupIntentId: String
    let publishableKey: String
    let customerId: String?
    let ephemeralKeySecret: String?

    enum CodingKeys: String, CodingKey {
        case setupIntentClientSecret = "setup_intent_client_secret"
        case setupIntentId = "setup_intent_id"
        case publishableKey = "publishable_key"
        case customerId = "customer_id"
        case ephemeralKeySecret = "ephemeral_key_secret"
    }
}

struct CardUpdateCompleteAPIRequest: Codable {
    let setupIntentId: String

    enum CodingKeys: String, CodingKey {
        case setupIntentId = "setup_intent_id"
    }
}

/// POST /lease-requests/{id}/billing/card-update/complete
struct CardUpdateCompleteAPIResponse: Codable {
    let cardBrand: String?
    let cardLast4: String?
    let updated: Bool

    enum CodingKeys: String, CodingKey {
        case cardBrand = "card_brand"
        case cardLast4 = "card_last4"
        case updated
    }
}

/// GET /config — what the server currently allows the app to offer.
/// Every field is optional: a server that predates a switch simply omits
/// it, and the app falls back to the conservative answer (feature off).
struct AppConfigAPIResponse: Codable {
    let rollingRentalsEnabled: Bool?

    enum CodingKeys: String, CodingKey {
        case rollingRentalsEnabled = "rolling_rentals_enabled"
    }

    var rollingRentals: Bool { rollingRentalsEnabled ?? false }
}

/// `{ "ok": true }` — stop-renewal's response shape.
struct OKAPIResponse: Codable {
    let ok: Bool
}

/// Error codes these endpoints return that the UI must handle by name
/// rather than by message, because each one means a different next step.
enum RollingBillingErrorCode {
    /// The flag is off server-side — weekly rentals cannot be created.
    static let rollingDisabled = "ROLLING_DISABLED"
    /// Nothing is owed: the charge settled, or was waived, before we asked.
    static let nothingDue = "NOTHING_DUE"
    /// The money is already in — the UI should refresh, not charge again.
    static let alreadyPaid = "ALREADY_PAID"
    /// A return handshake is live; billing actions are paused until it ends.
    static let returnInProgress = "RETURN_IN_PROGRESS"
    /// Auto-renew was already ended.
    static let renewalsStopped = "RENEWALS_STOPPED"
    static let renewalStopRefused = "RENEWAL_STOP_REFUSED"
    /// The rental is over — there is no future charge to update a card for.
    static let rentalEnded = "RENTAL_ENDED"
    /// This lease is not a rolling lease (fixed-term leases refuse by predicate).
    static let notRolling = "NOT_ROLLING"
    /// The card form was dismissed before Stripe saved the card.
    static let setupIncomplete = "SETUP_INCOMPLETE"
}
