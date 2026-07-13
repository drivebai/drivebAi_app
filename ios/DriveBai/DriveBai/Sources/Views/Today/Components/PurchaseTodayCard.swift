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
    /// Buyer-only: present the payment screen INLINE from the Today host
    /// instead of deep-linking into Chat. Supplied by DriverTodayView for the
    /// buyer; nil on the seller side, where it never applies. When set and the
    /// purchase is at `.bosSigned`, the primary "Authorize payment" CTA (and a
    /// card tap) open payment directly rather than routing through Chats.
    var onAuthorizePayment: (() -> Void)? = nil

    private var isBuyer: Bool { currentUserId == purchaseRequest.buyerId }
    private var isSeller: Bool { currentUserId == purchaseRequest.sellerId }

    /// Lazily-fetched Bill of Sale so this card can surface the signed PDF
    /// (or a "Preparing…" placeholder) once both parties have signed.
    @State private var bos: BillOfSale?

    /// True once the purchase has reached (or passed) the fully-signed BoS
    /// stage — the only point at which a finalized PDF can exist.
    private var bosStageReached: Bool {
        switch purchaseRequest.status {
        case .bosSigned, .paymentAuthorized, .handoverScheduled, .awaitingInspection,
             .inspectionAccepted, .completed, .rejectedUpheld, .rejectedRefunded, .expiredAuth:
            return true
        default:
            return false
        }
    }

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

            // Signed Bill of Sale PDF (or "Preparing…") — renders only once
            // both parties have signed.
            BillOfSalePDFRow(billOfSale: bos, isTourTarget: true)

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
        .onTapGesture { primaryTap() }
        .task(id: purchaseRequest.status) { await loadBoSIfNeeded() }
        // The PDF is rendered after the second signature, without changing the
        // purchase status — so a status-keyed task alone would leave this card
        // on "Preparing Bill of Sale…" until something else moved. Listen for
        // the same WS event the chat card uses.
        .onReceive(WebSocketManager.shared.purchaseBillOfSaleUpdatedPublisher) { purchaseID in
            guard purchaseID == nil || purchaseID == purchaseRequest.id else { return }
            Task { await loadBoSIfNeeded() }
        }
    }

    /// Fetch the BoS row while the finalized PDF might still be missing.
    /// No timers — driven by the purchase status and by the Bill-of-Sale
    /// WebSocket event that fires when the finalized PDF lands.
    @MainActor
    private func loadBoSIfNeeded() async {
        guard bosStageReached else { return }
        if bos != nil, bos?.finalizedPdfUrl?.isEmpty == false { return }
        if let response = try? await APIClient.shared.getBillOfSale(
            purchaseRequestId: purchaseRequest.id
        ) {
            bos = response.toDomain()
        }
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
        Button(action: primaryTap) {
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

    /// True when the buyer can pay directly from the Today card: BoS is signed,
    /// this viewer is the buyer, and the host supplied an inline-payment hook.
    private var shouldAuthorizeInline: Bool {
        isBuyer
            && purchaseRequest.status == .bosSigned
            && onAuthorizePayment != nil
    }

    /// The card body and its primary CTA share this handler so a single tap
    /// opens payment (buyer @ bos_signed) or otherwise routes to Chat.
    private func primaryTap() {
        if shouldAuthorizeInline {
            onAuthorizePayment?()
        } else {
            onOpen()
        }
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
