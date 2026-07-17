import Foundation

/// Body for POST /reviews — rates a COMPLETED purchase or rental in one
/// call: the car (buyer/driver only) and/or the counterparty. The server
/// validates parties, completion, and the 1–5 range, and rejects double
/// submissions with 409 REVIEW_ALREADY_EXISTS.
struct SubmitReviewRequest: Codable {
    /// "purchase" (purchase_requests.id) or "rental" (vehicle_returns.id).
    let transactionType: String
    let transactionId: UUID
    let carRating: Int?
    let partnerRating: Int?
    let comment: String?

    enum CodingKeys: String, CodingKey {
        case transactionType = "transaction_type"
        case transactionId = "transaction_id"
        case carRating = "car_rating"
        case partnerRating = "partner_rating"
        case comment
    }
}

struct SubmitReviewResponse: Codable {
    let ok: Bool
    let submitted: Int
}
