import SwiftUI

/// Today-tab card summarising an in-flight purchase for either the buyer
/// (Driver tab) or seller (Owner tab).  Mirrors `VehicleReturnCard` so
/// the visual language across handshake-y cards stays consistent.
struct PurchaseTodayCard: View {
    let purchaseRequest: PurchaseRequest
    let currentUserId: UUID
    /// Tap goes to Chat → Requests for the matching purchase row — the
    /// card itself does not present any modals.
    let onOpen: () -> Void

    private var isBuyer: Bool { currentUserId == purchaseRequest.buyerId }
    private var isSeller: Bool { currentUserId == purchaseRequest.sellerId }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            header
            carRow
            Text(bodyText)
                .font(.subheadline)
                .foregroundColor(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            if purchaseRequest.status == .paymentAuthorized
                || purchaseRequest.status == .handoverScheduled
                || purchaseRequest.status == .awaitingInspection {
                paymentHeldBanner
            }

            openButton
        }
        .padding(16)
        .background(TodayLayout.cardBackgroundColor)
        .cornerRadius(TodayLayout.cardCornerRadius)
        .overlay(
            RoundedRectangle(cornerRadius: TodayLayout.cardCornerRadius)
                .stroke(TodayLayout.cardBorderColor, lineWidth: TodayLayout.cardBorderWidth)
        )
        .shadow(color: Color.black.opacity(0.04), radius: 8, x: 0, y: 2)
        .contentShape(Rectangle())
        .onTapGesture { onOpen() }
    }

    // MARK: - Sections

    private var header: some View {
        HStack(spacing: 8) {
            Image(systemName: "car.side.fill")
                .font(.system(size: 14, weight: .semibold))
                .foregroundColor(TodayLayout.tealAccent)
            Text("Purchase")
                .font(.headline)
            Spacer()
            statusPill
        }
    }

    private var carRow: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(purchaseRequest.carTitle)
                .font(.subheadline.weight(.semibold))
                .foregroundColor(.primary)
            Text(headline)
                .font(.subheadline.weight(.medium))
                .foregroundColor(.primary)
        }
    }

    private var statusPill: some View {
        Text(purchaseRequest.status.displayText)
            .font(.caption2.weight(.semibold))
            .foregroundColor(statusColor)
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .background(statusColor.opacity(0.12))
            .clipShape(Capsule())
    }

    private var statusColor: Color {
        switch purchaseRequest.status {
        case .requested: return .blue
        case .accepted, .bosPendingSeller, .bosPendingBuyer: return .orange
        case .bosSigned, .paymentAuthorized: return TodayLayout.tealAccent
        case .handoverScheduled, .awaitingInspection: return .orange
        case .inspectionAccepted, .completed, .rejectedUpheld: return .green
        case .inspectionRejected: return .red
        default: return Color.driveBaiSecondary
        }
    }

    private var paymentHeldBanner: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: "lock.shield")
                .font(.system(size: 16, weight: .semibold))
                .foregroundColor(TodayLayout.tealAccent)
            VStack(alignment: .leading, spacing: 2) {
                Text(PurchaseCopy.paymentHoldHeadline)
                    .font(.subheadline.weight(.semibold))
                    .foregroundColor(.primary)
                Text("Charged only when the buyer accepts after inspection.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            Spacer(minLength: 0)
        }
        .padding(10)
        .background(TodayLayout.tealAccent.opacity(0.08))
        .clipShape(RoundedRectangle(cornerRadius: 10))
    }

    private var openButton: some View {
        Button(action: onOpen) {
            Text(ctaLabel)
                .font(.subheadline.weight(.semibold))
                .frame(maxWidth: .infinity)
                .frame(height: TodayLayout.optionButtonHeight)
                .background(TodayLayout.tealAccent)
                .foregroundColor(.white)
                .cornerRadius(8)
        }
        .buttonStyle(.plain)
    }

    // MARK: - Copy

    /// One-liner headline immediately below the car title.
    private var headline: String {
        switch (purchaseRequest.status, isBuyer, isSeller) {
        case (.requested, false, true):
            return "New offer of \(purchaseRequest.formattedOfferAmount) from \(purchaseRequest.buyerName)"
        case (.requested, true, _):
            return "Offer sent — awaiting \(purchaseRequest.sellerName)"
        case (.accepted, _, _):
            return "Bill of Sale ready to sign"
        case (.bosPendingSeller, _, true), (.bosPendingBuyer, true, _):
            return "Your signature is needed"
        case (.bosPendingSeller, true, _), (.bosPendingBuyer, _, true):
            return "Waiting on counterparty signature"
        case (.bosSigned, true, _):
            return "Authorize payment to proceed"
        case (.bosSigned, false, true):
            return "Waiting for buyer to authorize payment"
        case (.paymentAuthorized, false, true):
            return "Schedule the handover"
        case (.paymentAuthorized, true, _):
            return "Waiting for seller to schedule handover"
        case (.handoverScheduled, _, _):
            return "Handover scheduled"
        case (.awaitingInspection, true, _):
            return "Inspect the vehicle to complete the sale"
        case (.awaitingInspection, false, true):
            return "Buyer is inspecting the vehicle"
        case (.inspectionRejected, _, _):
            return "Rejection under DrivaBai review"
        case (.completed, _, _), (.rejectedUpheld, _, _):
            return "Sale complete"
        case (.rejectedRefunded, _, _):
            return "Sale cancelled — payment hold released"
        case (.declined, _, _), (.cancelled, _, _), (.expired, _, _), (.expiredAuth, _, _):
            return purchaseRequest.status.displayText
        default:
            return purchaseRequest.status.displayText
        }
    }

    private var bodyText: String {
        switch purchaseRequest.status {
        case .requested where isSeller:
            return "\(purchaseRequest.buyerName) offered \(purchaseRequest.formattedOfferAmount) for your car."
        case .requested where isBuyer:
            return "Waiting for the seller to accept your offer."
        case .accepted, .bosPendingSeller, .bosPendingBuyer:
            return "Sign the Bill of Sale to move to payment."
        case .bosSigned where isBuyer:
            return "Bill of Sale complete. Authorize the payment hold to move to handover."
        case .bosSigned where isSeller:
            return "Bill of Sale complete. Buyer is authorizing payment."
        case .paymentAuthorized:
            return isSeller
                ? "Payment is held. Pick a time and place to meet the buyer."
                : "Payment is held. Awaiting the seller's meetup details."
        case .handoverScheduled:
            if let loc = purchaseRequest.handoverLocation,
               let ts = purchaseRequest.handoverScheduledAt {
                return "Meet at \(loc) on \(ts.formatted(date: .abbreviated, time: .shortened))."
            }
            return "Handover scheduled — check the chat for details."
        case .awaitingInspection where isBuyer:
            if let deadline = purchaseRequest.inspectionDeadlineAt {
                return "Inspect the vehicle before \(deadline.formatted(date: .abbreviated, time: .shortened)) and accept or reject."
            }
            return "Inspect the vehicle and accept or reject."
        case .awaitingInspection where isSeller:
            return "Buyer has the car and is inspecting."
        case .inspectionAccepted:
            return "Payment is being completed — sale finalising."
        case .inspectionRejected:
            return "DrivaBai support is reviewing the rejection evidence."
        case .completed, .rejectedUpheld:
            return "Payment completed. Congratulations!"
        case .rejectedRefunded:
            return "The payment hold has been released — the buyer was not charged."
        default:
            return purchaseRequest.status.displayText
        }
    }

    private var ctaLabel: String {
        switch (purchaseRequest.status, isBuyer, isSeller) {
        case (.requested, false, true):
            return "Review offer"
        case (.accepted, _, _),
             (.bosPendingSeller, _, _),
             (.bosPendingBuyer, _, _):
            return "Open Bill of Sale"
        case (.bosSigned, true, _):
            return "Authorize payment"
        case (.paymentAuthorized, false, true):
            return "Schedule handover"
        case (.awaitingInspection, true, _):
            return "Inspect vehicle"
        case (.inspectionRejected, _, _):
            return "View evidence"
        default:
            return "Open in chat"
        }
    }
}
