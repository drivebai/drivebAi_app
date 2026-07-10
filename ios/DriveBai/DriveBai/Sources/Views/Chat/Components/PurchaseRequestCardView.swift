import SwiftUI

/// Chat-thread card for a purchase request.  Visual language mirrors
/// `LeaseRequestCardView` (icon + title + status pill + role-aware CTAs)
/// so the two flows sit comfortably in the same Requests tab.
struct PurchaseRequestCardView: View {
    let purchaseRequest: PurchaseRequest
    let currentUserId: UUID
    /// Optional BoS row (from ChatViewModel.billOfSalesByPurchase). When
    /// present the card refines its status copy based on how much of the
    /// document has been filled and signed — otherwise we fall back to
    /// the raw purchase status.
    var billOfSale: BillOfSale? = nil

    /// Called with a "which action to run" so ChatView keeps the sheet
    /// presentation state (BoS wizard, ChoosePaymentOptionView, …) instead
    /// of the card owning those modals.
    let onAction: (PurchaseCardAction) -> Void

    /// Busy indicator while a request-driven mutation is in flight.
    var isSubmitting: Bool = false

    private var isSeller: Bool { currentUserId == purchaseRequest.sellerId }
    private var isBuyer: Bool { currentUserId == purchaseRequest.buyerId }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            header
            offerLine
            if let msg = purchaseRequest.buyerMessage, !msg.isEmpty {
                messageLine(msg)
            }
            statusBanner
            participants
            actionButtons
            // Signed Bill of Sale PDF (or a "Preparing…" placeholder). Only
            // renders once both parties have signed; the finalized-PDF coach
            // mark anchors here.
            BillOfSalePDFRow(billOfSale: billOfSale, isTourTarget: true)
        }
        .padding()
        .background(Color(.systemBackground))
        .clipShape(RoundedRectangle(cornerRadius: 14))
        .shadow(color: .black.opacity(0.06), radius: 6, x: 0, y: 2)
        .onAppear { emitSignatureTourEvents() }
        .onChange(of: billOfSale) { _, _ in emitSignatureTourEvents() }
    }

    /// Mirror of the BoS signing transition for the product tour: when both
    /// signatures are present, fire `bothSignaturesPresent` (signature-lock
    /// teach) and, once the finalized PDF exists, flag the context so the
    /// pdf-ready teach can chain.
    private func emitSignatureTourEvents() {
        guard let bos = billOfSale, bos.isFullySigned else { return }
        if bos.finalizedPdfUrl?.isEmpty == false {
            ProductTourCoordinator.shared.updateContext { $0.pdfReady = true }
        }
        ProductTourCoordinator.shared.handle(.bothSignaturesPresent)
    }

    // MARK: - Header

    private var header: some View {
        HStack {
            Image(systemName: "car.side.fill")
                .font(.title3)
                .foregroundColor(.driveBaiPrimary)
                .frame(width: 32, height: 32)
                .background(Color.driveBaiPrimary.opacity(0.12))
                .clipShape(RoundedRectangle(cornerRadius: 8))

            VStack(alignment: .leading, spacing: 2) {
                Text(purchaseRequest.carTitle)
                    .font(.headline)
                    .lineLimit(1)
                Text("Purchase")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }

            Spacer()

            statusPill
        }
    }

    private var offerLine: some View {
        HStack(spacing: 16) {
            Label {
                Text(purchaseRequest.formattedOfferAmount)
                    .font(.subheadline.weight(.semibold))
            } icon: {
                Image(systemName: "banknote")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            Spacer()
        }
    }

    private func messageLine(_ msg: String) -> some View {
        Text(msg)
            .font(.subheadline)
            .foregroundColor(.secondary)
            .lineLimit(3)
    }

    // MARK: - Status pill

    private var statusPill: some View {
        Text(purchaseRequest.status.displayText)
            .font(.caption.weight(.medium))
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
        case .bosSigned, .paymentAuthorized: return Color.driveBaiPrimary
        case .handoverScheduled, .awaitingInspection: return .orange
        case .inspectionAccepted, .completed, .rejectedUpheld: return .green
        case .inspectionRejected: return .red
        case .rejectedRefunded: return .gray
        case .declined, .cancelled, .expired, .expiredAuth: return .gray
        }
    }

    // MARK: - Status banner (waiting messages)

    @ViewBuilder
    private var statusBanner: some View {
        if let text = statusBannerText {
            HStack(spacing: 6) {
                Image(systemName: statusBannerIcon)
                    .font(.caption)
                    .foregroundColor(.secondary)
                Text(text)
                    .font(.caption.weight(.medium))
                    .foregroundColor(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 6)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Color(.systemGray6))
            .clipShape(RoundedRectangle(cornerRadius: 8))
        }
    }

    private var statusBannerIcon: String {
        purchaseRequest.status == .inspectionRejected ? "exclamationmark.triangle.fill" : "hourglass"
    }

    /// BoS-aware banner copy. Falls back to the purchase-status defaults
    /// when we don't have a BoS row yet.
    private var statusBannerText: String? {
        switch purchaseRequest.status {
        case .accepted:
            return acceptedBannerText
        case .bosPendingSeller:
            return isSeller
                ? "Sign to complete the Bill of Sale."
                : "Waiting on the seller's signature."
        case .bosPendingBuyer:
            return isBuyer
                ? "Sign to continue to payment."
                : "Waiting on the buyer's signature."
        case .bosSigned:
            return isBuyer
                ? "Bill of Sale ready. Authorize payment to continue."
                : "Waiting for the buyer to authorize payment."
        case .paymentAuthorized:
            return isBuyer
                ? "Payment held. Waiting for the seller to schedule handover."
                : "Payment authorized — schedule the handover."
        case .handoverScheduled:
            if let ts = purchaseRequest.handoverScheduledAt,
               let loc = purchaseRequest.handoverLocation {
                return "Meet \(loc) on \(ts.formatted(date: .abbreviated, time: .shortened))."
            }
            return "Handover scheduled — check the details in chat."
        case .awaitingInspection:
            return isSeller
                ? "Buyer is inspecting the vehicle."
                : "Inspect the vehicle to accept or reject the sale."
        case .inspectionRejected:
            return "DrivaBai support is reviewing the rejection."
        default:
            return nil
        }
    }

    /// Copy for the `.accepted` status. Splits on how much of the BoS
    /// has been filled by which party.
    private var acceptedBannerText: String {
        guard let bos = billOfSale else {
            return isSeller
                ? "Bill of Sale not started. Open to fill in vehicle and seller details."
                : "Waiting for the seller to complete the Bill of Sale."
        }
        // Seller-owned fields all populated?
        let sellerReady = !bos.vehicleMake.isEmpty
            && !bos.vehicleModel.isEmpty
            && !bos.vin.isEmpty
            && !bos.sellerName.isEmpty
            && !bos.sellerAddress.isEmpty
        let buyerReady = !bos.buyerName.isEmpty && !bos.buyerAddress.isEmpty

        if !sellerReady {
            return isSeller
                ? "Complete your section of the Bill of Sale."
                : "Waiting for the seller to complete their section."
        }
        if !buyerReady {
            return isBuyer
                ? "Fill in your buyer details to continue."
                : "Waiting for the buyer to fill in their details."
        }
        // Both sides filled but nobody has signed yet.
        return "Ready to sign."
    }

    // MARK: - Participants footer

    private var participants: some View {
        HStack {
            Text(isSeller ? "From \(purchaseRequest.buyerName)" : "To \(purchaseRequest.sellerName)")
                .font(.caption)
                .foregroundColor(.secondary)
            Spacer()
            Text(purchaseRequest.createdAt, style: .relative)
                .font(.caption)
                .foregroundColor(.secondary)
        }
    }

    // MARK: - Action buttons

    @ViewBuilder
    private var actionButtons: some View {
        switch purchaseRequest.status {
        case .requested:
            if isSeller {
                HStack(spacing: 12) {
                    Button { onAction(.sellerDecline) } label: {
                        Text("Decline")
                            .font(.subheadline.weight(.medium))
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 10)
                            .background(Color(.systemGray5))
                            .foregroundColor(.primary)
                            .clipShape(RoundedRectangle(cornerRadius: 10))
                    }
                    Button { onAction(.sellerAccept) } label: {
                        Text("Accept offer")
                            .font(.subheadline.weight(.medium))
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 10)
                            .background(Color.driveBaiPrimary)
                            .foregroundColor(.white)
                            .clipShape(RoundedRectangle(cornerRadius: 10))
                    }
                }
            } else if isBuyer {
                Button { onAction(.buyerCancel) } label: {
                    Text("Cancel offer")
                        .font(.subheadline.weight(.medium))
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 10)
                        .background(Color(.systemGray5))
                        .foregroundColor(.red)
                        .clipShape(RoundedRectangle(cornerRadius: 10))
                }
            }

        case .accepted, .bosPendingSeller, .bosPendingBuyer, .bosSigned:
            openBoSButton
            if purchaseRequest.status == .bosSigned, isBuyer {
                Button { onAction(.buyerAuthorizePayment) } label: {
                    Label("Authorize payment", systemImage: "creditcard.fill")
                        .font(.subheadline.weight(.semibold))
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 12)
                        .background(Color.driveBaiPrimary)
                        .foregroundColor(.white)
                        .clipShape(RoundedRectangle(cornerRadius: 10))
                }
            }

        case .paymentAuthorized:
            if isSeller {
                Button { onAction(.sellerScheduleHandover) } label: {
                    Label("Schedule handover", systemImage: "calendar.badge.clock")
                        .font(.subheadline.weight(.semibold))
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 12)
                        .background(Color.driveBaiPrimary)
                        .foregroundColor(.white)
                        .clipShape(RoundedRectangle(cornerRadius: 10))
                }
            }

        case .handoverScheduled:
            if isSeller {
                Button { onAction(.sellerMarkHandedOver) } label: {
                    Label("Keys handed over", systemImage: "key.fill")
                        .font(.subheadline.weight(.semibold))
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 12)
                        .background(Color.driveBaiPrimary)
                        .foregroundColor(.white)
                        .clipShape(RoundedRectangle(cornerRadius: 10))
                }
            }

        case .awaitingInspection:
            if isBuyer {
                Button { onAction(.buyerInspect) } label: {
                    Label("Inspect vehicle", systemImage: "checkmark.shield.fill")
                        .font(.subheadline.weight(.semibold))
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 12)
                        .background(Color.driveBaiPrimary)
                        .foregroundColor(.white)
                        .clipShape(RoundedRectangle(cornerRadius: 10))
                }
            }

        case .completed, .rejectedUpheld:
            HStack(spacing: 6) {
                Image(systemName: "checkmark.seal.fill")
                Text("Sale complete")
            }
            .font(.subheadline.weight(.medium))
            .frame(maxWidth: .infinity)
            .padding(.vertical, 10)
            .foregroundColor(.green)

        case .rejectedRefunded:
            HStack(spacing: 6) {
                Image(systemName: "arrow.uturn.backward.circle.fill")
                Text("Payment hold released — rejection accepted")
            }
            .font(.subheadline.weight(.medium))
            .frame(maxWidth: .infinity)
            .padding(.vertical, 10)
            .foregroundColor(.secondary)

        case .declined, .cancelled, .expired, .expiredAuth:
            HStack(spacing: 6) {
                Image(systemName: "xmark.circle.fill")
                Text(purchaseRequest.status.displayText)
            }
            .font(.subheadline.weight(.medium))
            .frame(maxWidth: .infinity)
            .padding(.vertical, 10)
            .foregroundColor(.secondary)

        case .inspectionAccepted:
            HStack(spacing: 6) {
                Image(systemName: "hourglass")
                Text("Completing payment…")
            }
            .font(.subheadline.weight(.medium))
            .frame(maxWidth: .infinity)
            .padding(.vertical, 10)
            .foregroundColor(.secondary)

        case .inspectionRejected:
            EmptyView()
        }
    }

    private var openBoSButton: some View {
        Button { onAction(.openBillOfSale) } label: {
            HStack(spacing: 6) {
                Image(systemName: "doc.text")
                Text(openBoSLabel)
            }
            .font(.subheadline.weight(.semibold))
            .frame(maxWidth: .infinity)
            .padding(.vertical, 10)
            .background(Color.driveBaiPrimary.opacity(0.12))
            .foregroundColor(.driveBaiPrimary)
            .clipShape(RoundedRectangle(cornerRadius: 10))
        }
    }

    /// Label reads off the same "what's the next thing this user should
    /// do" logic as `acceptedBannerText`. If both parties have already
    /// signed we bias toward "Open Bill of Sale" (review-only).
    private var openBoSLabel: String {
        // Bill of Sale fully signed — view-only.
        if let bos = billOfSale, bos.isFullySigned { return "Open Bill of Sale" }

        // Role has already signed — waiting on the other side.
        if let bos = billOfSale {
            if isSeller && bos.sellerHasSigned { return "Open Bill of Sale" }
            if isBuyer && bos.buyerHasSigned { return "Open Bill of Sale" }
        }

        switch (purchaseRequest.status, isBuyer, isSeller) {
        case (.accepted, _, true),
             (.bosPendingSeller, _, true):
            if let bos = billOfSale {
                let sellerReady = !bos.vehicleMake.isEmpty
                    && !bos.vehicleModel.isEmpty && !bos.vin.isEmpty
                    && !bos.sellerName.isEmpty && !bos.sellerAddress.isEmpty
                return sellerReady ? "Sign Bill of Sale" : "Complete Bill of Sale"
            }
            return "Complete Bill of Sale"
        case (.accepted, true, _),
             (.bosPendingBuyer, true, _):
            if let bos = billOfSale {
                let buyerReady = !bos.buyerName.isEmpty && !bos.buyerAddress.isEmpty
                return buyerReady ? "Sign Bill of Sale" : "Complete Bill of Sale"
            }
            return "Open Bill of Sale"
        default:
            return "Open Bill of Sale"
        }
    }
}

// MARK: - Action Enum

/// Actions the chat card can trigger.  ChatView handles presentation of
/// the corresponding sheet (BoS wizard, ChoosePaymentOptionView, etc.).
enum PurchaseCardAction {
    case sellerAccept
    case sellerDecline
    case buyerCancel
    case openBillOfSale
    case buyerAuthorizePayment
    case sellerScheduleHandover
    case sellerMarkHandedOver
    case buyerInspect
}

// MARK: - Bill of Sale PDF row (shared)

/// Reusable "View / Preparing" affordance for the finalized Bill of Sale
/// PDF. Shared by `PurchaseRequestCardView`, `BillOfSaleFlowView` (review
/// step) and `PurchaseTodayCard`.
///
/// Behaviour:
///  - Renders **nothing** until both parties have signed, so the action
///    never appears before the PDF can exist.
///  - Once both have signed and `finalizedPdfUrl` is populated, shows a
///    "View Bill of Sale" button that opens the signed PDF in
///    `DocumentPreviewSheet` (QuickLook preview + Share / Save to Files).
///  - While both are signed but the server is still rendering the PDF
///    (`finalizedPdfUrl == nil`), shows a disabled "Preparing Bill of
///    Sale…" row with a spinner. It clears automatically when the
///    `purchase_bill_of_sale_updated` WS event delivers the populated URL
///    and the caller re-renders with a fresh `BillOfSale`.
struct BillOfSalePDFRow: View {
    let billOfSale: BillOfSale?
    /// When true, the "View" button is tagged as the `pdf_ready` coach-mark
    /// target. Keep this to a single on-screen host at a time.
    var isTourTarget: Bool = false

    @State private var showPreview = false

    private var bothSigned: Bool { billOfSale?.isFullySigned == true }
    private var pdfURL: String? {
        guard let url = billOfSale?.finalizedPdfUrl, !url.isEmpty else { return nil }
        return url
    }

    var body: some View {
        if bothSigned {
            if let url = pdfURL {
                viewButton(url: url)
            } else {
                preparingRow
            }
        }
    }

    @ViewBuilder
    private func viewButton(url: String) -> some View {
        let button = Button {
            showPreview = true
        } label: {
            HStack(spacing: 6) {
                Image(systemName: "doc.richtext")
                Text("View Bill of Sale")
            }
            .font(.subheadline.weight(.semibold))
            .frame(maxWidth: .infinity)
            .padding(.vertical, 10)
            .background(Color.driveBaiPrimary.opacity(0.12))
            .foregroundColor(.driveBaiPrimary)
            .clipShape(RoundedRectangle(cornerRadius: 10))
        }
        .buttonStyle(.plain)
        .sheet(isPresented: $showPreview) {
            DocumentPreviewSheet(
                source: .remoteURL(url, filename: "Bill of Sale.pdf")
            )
        }

        if isTourTarget {
            button.onboardingTarget(.finalizedPdfLink)
        } else {
            button
        }
    }

    private var preparingRow: some View {
        HStack(spacing: 8) {
            ProgressView().scaleEffect(0.8)
            Text("Preparing Bill of Sale…")
                .font(.subheadline.weight(.medium))
                .foregroundColor(.secondary)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 10)
        .background(Color(.systemGray6))
        .clipShape(RoundedRectangle(cornerRadius: 10))
    }
}
