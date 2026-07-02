import SwiftUI

/// Chat-thread card for a purchase request.  Visual language mirrors
/// `LeaseRequestCardView` (icon + title + status pill + role-aware CTAs)
/// so the two flows sit comfortably in the same Requests tab.
struct PurchaseRequestCardView: View {
    let purchaseRequest: PurchaseRequest
    let currentUserId: UUID

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
        }
        .padding()
        .background(Color(.systemBackground))
        .clipShape(RoundedRectangle(cornerRadius: 14))
        .shadow(color: .black.opacity(0.06), radius: 6, x: 0, y: 2)
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

    private var statusBannerText: String? {
        switch (purchaseRequest.status, isBuyer, isSeller) {
        case (.bosPendingSeller, true, _):
            return "Waiting on the seller's signature."
        case (.bosPendingBuyer, _, true):
            return "Waiting on the buyer's signature."
        case (.bosSigned, false, true):
            return "Waiting for the buyer to authorize payment."
        case (.paymentAuthorized, true, _):
            return "Payment held. Waiting for the seller to schedule handover."
        case (.handoverScheduled, _, _):
            if let ts = purchaseRequest.handoverScheduledAt,
               let loc = purchaseRequest.handoverLocation {
                return "Meet \(loc) on \(ts.formatted(date: .abbreviated, time: .shortened))."
            }
            return "Handover scheduled — check the details in chat."
        case (.awaitingInspection, false, true):
            return "Buyer is inspecting the vehicle."
        case (.inspectionRejected, _, _):
            return "DrivaBai support is reviewing the rejection."
        default:
            return nil
        }
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
                Text("Payment released — rejection accepted")
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
                Text("Capturing payment…")
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

    private var openBoSLabel: String {
        switch (purchaseRequest.status, isBuyer, isSeller) {
        case (.accepted, true, _),
             (.bosPendingBuyer, true, _):
            return "Sign Bill of Sale"
        case (.accepted, _, true),
             (.bosPendingSeller, _, true):
            return "Sign Bill of Sale"
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
