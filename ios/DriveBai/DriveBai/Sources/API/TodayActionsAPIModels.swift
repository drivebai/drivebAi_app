import Foundation

// MARK: - Today Actions API Models

struct TodayActionAPIModel: Codable {
    let id: UUID
    let type: String
    let title: String
    let body: String
    let carId: UUID
    let carTitle: String
    let chatId: UUID
    let counterpartyId: UUID
    let counterpartyName: String
    let status: String
    let createdAt: Date
    let expiresAt: Date
    let primaryAction: String
    let secondaryAction: String

    enum CodingKeys: String, CodingKey {
        case id, type, title, body, status
        case carId = "car_id"
        case carTitle = "car_title"
        case chatId = "chat_id"
        case counterpartyId = "counterparty_id"
        case counterpartyName = "counterparty_name"
        case createdAt = "created_at"
        case expiresAt = "expires_at"
        case primaryAction = "primary_action"
        case secondaryAction = "secondary_action"
    }

    func toOnboardingTask() -> OnboardingTask {
        OnboardingTask(
            id: id,
            title: title,
            description: body,
            dueDate: expiresAt,
            requestedBy: counterpartyName,
            priority: .high,
            options: [primaryAction.capitalized, secondaryAction.capitalized],
            countdown: CountdownConfig(deadline: expiresAt),
            chatId: chatId,
            carTitle: carTitle,
            requestType: type,
            counterpartyId: counterpartyId,
            counterpartyName: counterpartyName
        )
    }
}

// MARK: - Active rental (lifecycle batch, defect 3)

/// The driver's rental-in-progress — the standing "you have this car until
/// X" context that existed for owners (ActiveRentalSummary) but never for
/// the driver. Arrives as an ADDITIVE top-level field on today/actions, so
/// older builds simply ignore it.
struct ActiveRentalAPIModel: Codable {
    let leaseRequestId: UUID
    let carId: UUID
    let carTitle: String
    let carPhotoUrl: String?
    let ownerId: UUID
    let ownerName: String
    let chatId: UUID?
    let pickupConfirmedAt: Date
    let rentalEndsAt: Date
    /// Whole days until the end; negative once overdue (−1 = up to 24h past).
    let daysRemaining: Int
    /// "active" | "ending_soon" | "overdue" — server-computed so the card and
    /// the term scanner can never disagree about which bucket we're in.
    let termState: String
    let weeks: Int
    let weeklyPriceCents: Int64
    let paidAmountCents: Int64
    let returnLocationArea: String?
    /// "pickup" (the key-handover meeting point — return where you picked
    /// up) vs "listing" (only the owner's listed location is known).
    let returnLocationSource: String?

    enum CodingKeys: String, CodingKey {
        case leaseRequestId = "lease_request_id"
        case carId = "car_id"
        case carTitle = "car_title"
        case carPhotoUrl = "car_photo_url"
        case ownerId = "owner_id"
        case ownerName = "owner_name"
        case chatId = "chat_id"
        case pickupConfirmedAt = "pickup_confirmed_at"
        case rentalEndsAt = "rental_ends_at"
        case daysRemaining = "days_remaining"
        case termState = "term_state"
        case weeks
        case weeklyPriceCents = "weekly_price_cents"
        case paidAmountCents = "paid_amount_cents"
        case returnLocationArea = "return_location_area"
        case returnLocationSource = "return_location_source"
    }
}

/// Domain shape the Today card renders. Kept beside the API model (not a new
/// file) deliberately — new Swift files need manual pbxproj registration.
struct ActiveRental: Identifiable, Equatable {
    enum TermState: String {
        case active, endingSoon = "ending_soon", overdue
    }

    let id: UUID // lease request id
    let carId: UUID
    let carTitle: String
    let carPhotoUrl: String?
    let ownerId: UUID
    let ownerName: String
    let chatId: UUID?
    let pickupConfirmedAt: Date
    let rentalEndsAt: Date
    let daysRemaining: Int
    let termState: TermState
    let weeks: Int
    let weeklyPriceCents: Int64
    let paidAmountCents: Int64
    let returnLocationArea: String?
    let returnLocationSource: String?

    /// "Returns Fri, 22 Aug · 4 days left" — the deadline stated absolutely
    /// AND relatively, so neither timezone confusion nor mental math can
    /// mislead. Parsing is UTC (APIClient's ISO8601 strategy); display is
    /// device-local like every other deadline in the app.
    var returnsLine: String {
        let formatter = DateFormatter()
        formatter.dateFormat = "EEE, d MMM"
        let day = formatter.string(from: rentalEndsAt)
        switch termState {
        case .overdue:
            let days = max(1, -daysRemaining)
            return "Was due \(day) · overdue by \(days) day\(days == 1 ? "" : "s")"
        case .endingSoon, .active:
            if daysRemaining <= 1 {
                return "Returns \(day) · due today"
            }
            return "Returns \(day) · \(daysRemaining) days left"
        }
    }
}

extension ActiveRentalAPIModel {
    func toDomain() -> ActiveRental {
        ActiveRental(
            id: leaseRequestId,
            carId: carId,
            carTitle: carTitle,
            carPhotoUrl: carPhotoUrl,
            ownerId: ownerId,
            ownerName: ownerName,
            chatId: chatId,
            pickupConfirmedAt: pickupConfirmedAt,
            rentalEndsAt: rentalEndsAt,
            daysRemaining: daysRemaining,
            // Unknown future state decodes as .active — the neutral bucket;
            // the absolute date on the card stays correct either way.
            termState: ActiveRental.TermState(rawValue: termState) ?? .active,
            weeks: weeks,
            weeklyPriceCents: weeklyPriceCents,
            paidAmountCents: paidAmountCents,
            returnLocationArea: returnLocationArea,
            returnLocationSource: returnLocationSource
        )
    }
}

struct TodayActionsAPIResponse: Codable {
    let actions: [TodayActionAPIModel]
    let hasUnreadActions: Bool
    /// Optional so responses from older servers (and empty states) decode.
    let activeRentals: [ActiveRentalAPIModel]?

    enum CodingKeys: String, CodingKey {
        case actions
        case hasUnreadActions = "has_unread_actions"
        case activeRentals = "active_rentals"
    }
}
