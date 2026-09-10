import Foundation

// Driver debt — the running balance, not one rental's arrears.
//
// A debt outlives the rental that created it and sums across rentals, so the
// app reads one number for the person rather than a per-lease state. Mirrors
// GET /me/balance.

struct DriverDebtAPIModel: Codable, Identifiable, Equatable {
    let id: UUID
    let leaseRequestId: UUID
    let originalAmountCents: Int64
    let outstandingCents: Int64
    let currency: String
    let status: String
    let reason: String
    let openedAt: Date?

    enum CodingKeys: String, CodingKey {
        case id
        case leaseRequestId = "lease_request_id"
        case originalAmountCents = "original_amount_cents"
        case outstandingCents = "outstanding_cents"
        case currency, status, reason
        case openedAt = "opened_at"
    }
}

/// GET /me/balance — zero rather than 404 when nothing is owed, so the clear
/// state needs no special case.
struct DriverBalanceAPIResponse: Codable {
    let outstandingCents: Int64
    let currency: String
    let openDebtCount: Int
    let debts: [DriverDebtAPIModel]?

    enum CodingKeys: String, CodingKey {
        case outstandingCents = "outstanding_cents"
        case currency
        case openDebtCount = "open_debt_count"
        case debts
    }

    var hasBalance: Bool { outstandingCents > 0 }

    var formatted: String {
        let f = NumberFormatter()
        f.numberStyle = .currency
        f.currencyCode = currency.isEmpty ? "USD" : currency
        let v = Double(outstandingCents) / 100
        return f.string(from: NSNumber(value: v)) ?? String(format: "$%.2f", v)
    }
}

/// The lease the driver should pay against. Debts are per-rental underneath
/// the balance, and Pay-now is a per-lease endpoint, so the app pays the
/// oldest one first — the order a person expects to clear a balance in.
extension DriverBalanceAPIResponse {
    var oldestOpenDebt: DriverDebtAPIModel? {
        (debts ?? [])
            .filter { $0.status == "open" && $0.outstandingCents > 0 }
            .sorted { ($0.openedAt ?? .distantPast) < ($1.openedAt ?? .distantPast) }
            .first
    }
}

/// Error codes the debt surfaces branch on by name.
enum DebtErrorCode {
    /// A new booking was refused because the driver owes money. The details
    /// carry outstanding_cents so the UI can offer to clear it.
    static let outstandingBalance = "OUTSTANDING_BALANCE"
}
