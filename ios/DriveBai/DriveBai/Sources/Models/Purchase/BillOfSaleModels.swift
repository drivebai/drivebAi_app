import Foundation

// MARK: - Title Condition

/// Seller-declared title/branding condition on the Bill of Sale. Raw values
/// mirror the backend `title_condition` enum EXACTLY. `.other` requires a
/// free-text description (`titleConditionOther`).
enum TitleCondition: String, CaseIterable, Identifiable, Equatable, Hashable {
    case clean
    case lienRecorded = "lien_recorded"
    case salvage
    case rebuilt
    case lemonBuyback = "lemon_buyback"
    case flood
    case manufacturerBuyback = "manufacturer_buyback"
    case other

    var id: String { rawValue }

    var displayText: String {
        switch self {
        case .clean: return "Clean"
        case .lienRecorded: return "Lien Recorded"
        case .salvage: return "Salvage"
        case .rebuilt: return "Rebuilt"
        case .lemonBuyback: return "Lemon Buyback"
        case .flood: return "Flood"
        case .manufacturerBuyback: return "Manufacturer Buyback"
        case .other: return "Other"
        }
    }

    var requiresDetail: Bool { self == .other }
}

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
    /// Structured coordinate backing `sellerAddress` (map-picker sourced).
    var sellerAddressLat: Double?
    var sellerAddressLng: Double?
    let sellerSignatureUrl: String?
    let sellerSignedAt: Date?

    // Buyer block
    var buyerName: String
    var buyerAddress: String
    var buyerAddressLat: Double?
    var buyerAddressLng: Double?
    let buyerSignatureUrl: String?
    let buyerSignedAt: Date?

    // Title condition (seller-declared branding)
    var titleCondition: TitleCondition?
    var titleConditionOther: String?

    // ID documents (backend-signed private URLs; nil when not on file).
    let sellerIdDocumentUrl: String?
    let buyerIdDocumentUrl: String?

    // Vehicle title document (backend-signed; derived from the car's `title`
    // document). `titleUploaded` is the authoritative "on file" flag.
    let titleDocumentUrl: String?
    let titleUploaded: Bool

    // Rendered artifact (server-generated after both signatures)
    let finalizedPdfUrl: String?
    let finalizedAt: Date?

    let createdAt: Date
    let updatedAt: Date

    /// Seller-declared title condition rendered for display, expanding the
    /// `.other` free-text when present.
    var titleConditionDisplay: String? {
        guard let titleCondition else { return nil }
        if titleCondition == .other {
            let detail = (titleConditionOther ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
            return detail.isEmpty ? titleCondition.displayText : "\(titleCondition.displayText) — \(detail)"
        }
        return titleCondition.displayText
    }

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
