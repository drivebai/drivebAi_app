import Foundation

// MARK: - Bill of Sale

/// Vehicle Bill of Sale domain model — standard fields (seller/buyer,
/// vehicle, price, signatures). Signature and finalized-PDF
/// URLs are backend-signed private paths — no additional token handling on
/// the iOS side.
struct BillOfSale: Identifiable, Equatable, Hashable {
    let id: UUID
    let purchaseRequestId: UUID

    // Vehicle block
    var vehicleYear: Int
    var vehicleMake: String
    var vehicleModel: String
    var vin: String

    // Sale block
    var saleAmountCents: Int64
    var currency: String

    // Terms
    var termsConditions: String

    // Seller block
    var sellerName: String
    var sellerAddress: String
    let sellerSignatureUrl: String?
    let sellerSignedAt: Date?

    // Buyer block
    var buyerName: String
    var buyerAddress: String
    let buyerSignatureUrl: String?
    let buyerSignedAt: Date?

    // Rendered artifact (server-generated after both signatures)
    let finalizedPdfUrl: String?
    let finalizedAt: Date?

    let createdAt: Date
    let updatedAt: Date

    var sellerHasSigned: Bool { sellerSignedAt != nil }
    var buyerHasSigned: Bool { buyerSignedAt != nil }
    var isFullySigned: Bool { sellerHasSigned && buyerHasSigned }

    var formattedSaleAmount: String {
        let dollars = Double(saleAmountCents) / 100.0
        let formatter = NumberFormatter()
        formatter.numberStyle = .currency
        formatter.currencyCode = currency
        formatter.maximumFractionDigits = 0
        return formatter.string(from: NSNumber(value: dollars)) ?? "$\(Int(dollars))"
    }
}
