import SwiftUI

struct LeaseRequestCardView: View {
    let leaseRequest: LeaseRequest
    let currentUserId: UUID
    let onAccept: () -> Void
    let onDecline: () -> Void
    let onPay: () -> Void
    let onCancel: () -> Void
    let onAdjustPrice: () -> Void
    let onConfirmPickup: () -> Void
    /// Owner-only: tap "Cancel acceptance" while the lease is still in the
    /// `accepted` state (no payment in flight). Backend RescindAccept
    /// returns the lease to cancelled and unreserves the car.
    let onRescindAccept: () -> Void
    /// Owner-only: invoked with one of LeaseRequest.allowedPickupExtensionMinutes.
    let onExtendPickup: (Int) -> Void
    /// Driver-only: the owner adjusted the price; accept it to re-enable
    /// Pay Now. Defaults to a no-op so legacy call sites still compile.
    var onAcceptPriceChange: () -> Void = {}
    /// Driver-only: decline the new price. Cancels the lease server-side.
    var onDeclinePriceChange: () -> Void = {}

    private var isOwner: Bool { currentUserId == leaseRequest.ownerId }
    private var isDriver: Bool { currentUserId == leaseRequest.driverId }

    /// True while the rental hasn't been paid for or terminated — i.e. the
    /// stages where "unused days will be refunded" is information the user
    /// actually needs (decide to accept, decide to pay, retry payment).
    /// Hidden once the lease is paid, declined, cancelled, expired, or
    /// refunded so the line doesn't become noise on terminal cards.
    private var showsUnusedDaysDisclaimer: Bool {
        switch leaseRequest.status {
        case .requested, .accepted, .paymentPending:
            return true
        case .paid, .declined, .cancelled, .expired, .expiredRefunded:
            return false
        }
    }

    /// Shown when the owner taps "Add more time" — surfaces only the
    /// presets that still fit within the 120-minute cap.
    @State private var showingExtendDialog = false
    @State private var showingRescindConfirm = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            // Header: car icon + title + status badge
            HStack {
                Image(systemName: "car.fill")
                    .font(.title3)
                    .foregroundColor(.driveBaiPrimary)
                    .frame(width: 32, height: 32)
                    .background(Color.driveBaiPrimary.opacity(0.12))
                    .clipShape(RoundedRectangle(cornerRadius: 8))

                VStack(alignment: .leading, spacing: 2) {
                    Text(leaseRequest.carTitle)
                        .font(.headline)
                        .lineLimit(1)

                    Text("Lease Request")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }

                Spacer()

                statusBadge
            }

            // Message
            if let message = leaseRequest.message, !message.isEmpty {
                Text(message)
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                    .lineLimit(3)
            }

            // Price-review banner. Driver-side, fires whenever the owner's
            // last price change is still awaiting accept/decline. Replaces
            // the older "Price updated by owner" hint that only fired in
            // the requested state — the new flow gates Pay Now until the
            // driver makes a call regardless of status.
            if isDriver && leaseRequest.driverShouldReviewPrice {
                HStack(spacing: 6) {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .font(.caption)
                        .foregroundColor(.orange)
                    Text("Owner updated the price — review before paying")
                        .font(.caption.weight(.medium))
                        .foregroundColor(.orange)
                        .lineLimit(2)
                        .multilineTextAlignment(.leading)
                }
                .padding(.horizontal, 10)
                .padding(.vertical, 6)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Color.orange.opacity(0.1))
                .clipShape(RoundedRectangle(cornerRadius: 8))
            }
            // Owner-side mirror so the owner knows they're waiting on the
            // driver's decision (rather than wondering why Pay Now hasn't
            // fired). Quiet styling — owner is not the actor here.
            if isOwner && leaseRequest.driverShouldReviewPrice {
                HStack(spacing: 6) {
                    Image(systemName: "hourglass")
                        .font(.caption)
                        .foregroundColor(.secondary)
                    Text("Waiting for driver to review the new price")
                        .font(.caption.weight(.medium))
                        .foregroundColor(.secondary)
                        .lineLimit(2)
                        .multilineTextAlignment(.leading)
                }
                .padding(.horizontal, 10)
                .padding(.vertical, 6)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Color(.systemGray6))
                .clipShape(RoundedRectangle(cornerRadius: 8))
            }

            // Pricing details. When a price review is pending, the "old"
            // line should be the price the driver previously agreed to
            // (the prior offered price, if any) rather than the base
            // listing price — that's the actual delta they're being asked
            // to accept.
            HStack(spacing: 16) {
                Label {
                    if let new = leaseRequest.formattedEffectiveWeeklyPrice,
                       let oldText = leaseRequest.priceComparisonOld {
                        HStack(spacing: 4) {
                            Text("\(oldText)/wk")
                                .strikethrough()
                                .foregroundColor(.secondary)
                            Text("\(new)/wk")
                                .font(.subheadline.weight(.semibold))
                                .foregroundColor(.primary)
                        }
                    } else if let weeklyFormatted = leaseRequest.formattedEffectiveWeeklyPrice {
                        Text("\(weeklyFormatted)/wk")
                            .font(.subheadline.weight(.semibold))
                    }
                } icon: {
                    Image(systemName: "calendar")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }

                Text("\(leaseRequest.weeks) \(leaseRequest.weeks == 1 ? "week" : "weeks")")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
            }

            // Total amount
            if let totalFormatted = leaseRequest.formattedTotalAmount {
                HStack(spacing: 4) {
                    Image(systemName: "banknote")
                        .font(.caption)
                        .foregroundColor(.secondary)
                    Text("Total: \(totalFormatted)")
                        .font(.subheadline.weight(.semibold))
                }
            }

            // Refund disclaimer — shown only while the rental is still
            // pre-active (requested / accepted / payment-pending). Once the
            // lease is paid, declined, cancelled, or refunded, the line
            // would be either redundant or misleading.
            if showsUnusedDaysDisclaimer {
                Label("Any unused days will be refunded.", systemImage: "info.circle")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }

            // Participants
            HStack {
                Text(isOwner ? "From \(leaseRequest.driverName)" : "To \(leaseRequest.ownerName)")
                    .font(.caption)
                    .foregroundColor(.secondary)

                Spacer()

                Text(leaseRequest.createdAt, style: .relative)
                    .font(.caption)
                    .foregroundColor(.secondary)
            }

            // Action buttons based on role + status
            actionButtons
        }
        .padding()
        .background(Color(.systemBackground))
        .clipShape(RoundedRectangle(cornerRadius: 14))
        .shadow(color: .black.opacity(0.06), radius: 6, x: 0, y: 2)
        .confirmationDialog(
            "Add more time?",
            isPresented: $showingExtendDialog,
            titleVisibility: .visible
        ) {
            ForEach(leaseRequest.availableExtensionPresets, id: \.self) { minutes in
                Button("+\(minutes) minutes") {
                    onExtendPickup(minutes)
                }
            }
            Button("Cancel", role: .cancel) { }
        } message: {
            Text("Gives the driver more time to pick up the car before the automatic refund. Up to \(leaseRequest.pickupExtensionRemainingMinutes) minutes left to add.")
        }
        .alert("Cancel acceptance?", isPresented: $showingRescindConfirm) {
            Button("Keep", role: .cancel) { }
            Button("Cancel acceptance", role: .destructive) { onRescindAccept() }
        } message: {
            Text("The driver will be told the rental is cancelled. No charge has been made — your car returns to Discovery right away.")
        }
    }

    // MARK: - Status Badge

    @ViewBuilder
    private var statusBadge: some View {
        let (text, color) = statusInfo
        Text(text)
            .font(.caption.weight(.medium))
            .foregroundColor(color)
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .background(color.opacity(0.12))
            .clipShape(Capsule())
    }

    private var statusInfo: (String, Color) {
        // Show "Paid" badge if payment succeeded even if lease status hasn't caught up
        if leaseRequest.status == .paid || leaseRequest.isPaymentSucceededAwaitingWebhook {
            return ("Paid", .green)
        }
        // Pending price review wins over the underlying status — this is
        // the "you need to do something" signal the rest of the card layout
        // is hanging on.
        if leaseRequest.priceChangePending && !leaseRequest.status.isTerminal {
            return ("Price updated", .orange)
        }
        switch leaseRequest.status {
        case .requested: return ("Requested", .blue)
        case .accepted: return ("Accepted", .green)
        case .declined: return ("Declined", .red)
        case .cancelled: return ("Cancelled", .gray)
        case .paymentPending:
            if leaseRequest.isPaymentProcessing {
                return ("Processing", .orange)
            }
            return ("Payment Pending", .orange)
        case .paid: return ("Paid", .green)
        case .expired: return ("Expired", .gray)
        case .expiredRefunded: return ("Refunded", .gray)
        }
    }

    // MARK: - Owner extension UI

    /// Subline shown under the owner's countdown. Reads either as "tap to add
    /// more time" while extensions are still available, or as a hard stop
    /// when the cap is hit.
    private var ownerSubline: String {
        if leaseRequest.canOwnerExtendPickup() {
            if leaseRequest.pickupExtensionCount > 0 {
                return "You've added \(leaseRequest.pickupExtensionTotalMinutes) min so far. Up to \(leaseRequest.pickupExtensionRemainingMinutes) min still available."
            }
            return "If the driver is on the way, give them a few more minutes before the auto-refund kicks in."
        }
        if leaseRequest.pickupExtensionTotalMinutes > 0 {
            return "Pickup deadline already extended by \(leaseRequest.pickupExtensionTotalMinutes) min — no more time can be added."
        }
        return ""
    }

    /// Owner's "Add more time" CTA. Disabled (hidden) when the lease has
    /// hit the cap or the deadline has already lapsed.
    @ViewBuilder
    private var extendButton: some View {
        if leaseRequest.canOwnerExtendPickup() {
            Button {
                showingExtendDialog = true
            } label: {
                HStack(spacing: 4) {
                    Image(systemName: "plus.circle.fill")
                    Text("Add time")
                }
                .font(.caption.weight(.semibold))
                .padding(.horizontal, 10)
                .padding(.vertical, 6)
                .background(Color.driveBaiPrimary)
                .foregroundColor(.white)
                .clipShape(Capsule())
            }
            .buttonStyle(.plain)
        } else {
            EmptyView()
        }
    }

    // MARK: - Action Buttons

    @ViewBuilder
    private var actionButtons: some View {
        // Priority branch: the owner just changed the price and the driver
        // hasn't reviewed yet. This MUST come before Pay Now so we never
        // show the payment button against a stale offer.
        if isDriver && leaseRequest.driverShouldReviewPrice {
            HStack(spacing: 12) {
                Button(action: onDeclinePriceChange) {
                    Text("Decline")
                        .font(.subheadline.weight(.medium))
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 10)
                        .background(Color(.systemGray5))
                        .foregroundColor(.red)
                        .clipShape(RoundedRectangle(cornerRadius: 10))
                }
                Button(action: onAcceptPriceChange) {
                    Text("Accept new price")
                        .font(.subheadline.weight(.semibold))
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 10)
                        .background(Color.driveBaiPrimary)
                        .foregroundColor(.white)
                        .clipShape(RoundedRectangle(cornerRadius: 10))
                }
            }
        } else if isOwner && leaseRequest.status.ownerCanRespond {
            // Owner: adjust price + accept/decline
            Button(action: onAdjustPrice) {
                HStack(spacing: 6) {
                    Image(systemName: "pencil")
                    Text(leaseRequest.isPriceAdjusted ? "Adjust Price Again" : "Adjust Price")
                }
                .font(.subheadline.weight(.medium))
                .frame(maxWidth: .infinity)
                .padding(.vertical, 10)
                .background(Color(.systemGray6))
                .foregroundColor(.primary)
                .clipShape(RoundedRectangle(cornerRadius: 10))
            }

            HStack(spacing: 12) {
                Button(action: onDecline) {
                    Text("Decline")
                        .font(.subheadline.weight(.medium))
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 10)
                        .background(Color(.systemGray5))
                        .foregroundColor(.primary)
                        .clipShape(RoundedRectangle(cornerRadius: 10))
                }

                Button(action: onAccept) {
                    Text("Accept")
                        .font(.subheadline.weight(.medium))
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 10)
                        .background(Color.driveBaiPrimary)
                        .foregroundColor(.white)
                        .clipShape(RoundedRectangle(cornerRadius: 10))
                }
            }
        } else if isOwner && leaseRequest.status == .accepted {
            // Owner accepted but driver hasn't paid yet — let the owner back
            // out without admin intervention. Once payment is in flight the
            // backend refuses with 409 and we don't expose the button.
            Button {
                showingRescindConfirm = true
            } label: {
                Text("Cancel acceptance")
                    .font(.subheadline.weight(.medium))
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 10)
                    .background(Color(.systemGray5))
                    .foregroundColor(.red)
                    .clipShape(RoundedRectangle(cornerRadius: 10))
            }
        } else if isDriver && leaseRequest.isAwaitingPickupConfirmation,
                  let deadline = leaseRequest.pickupDeadlineAt {
            // Driver: countdown + confirm-pickup CTA.
            VStack(spacing: 10) {
                PickupCountdownView(
                    deadline: deadline,
                    headline: "Pick up the car before",
                    subline: leaseRequest.pickupExtensionCount > 0
                        ? "Owner added \(leaseRequest.pickupExtensionTotalMinutes) min to your deadline. Confirm pickup or you'll be auto-refunded."
                        : "Confirm pickup or you'll be auto-refunded when the timer hits zero."
                )

                Button(action: onConfirmPickup) {
                    HStack(spacing: 6) {
                        Image(systemName: "key.fill")
                        Text("I've picked up the car")
                    }
                    .font(.subheadline.weight(.semibold))
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 12)
                    .background(Color.driveBaiPrimary)
                    .foregroundColor(.white)
                    .clipShape(RoundedRectangle(cornerRadius: 10))
                }
            }
        } else if isOwner && leaseRequest.isAwaitingPickupConfirmation,
                  let deadline = leaseRequest.pickupDeadlineAt {
            // Owner: same countdown + "Add more time" accessory when allowed.
            PickupCountdownView(
                deadline: deadline,
                headline: "Waiting for driver pickup",
                subline: ownerSubline,
                accessory: { AnyView(extendButton) }
            )
        } else if isDriver && (leaseRequest.status == .paid || leaseRequest.isPaymentSucceededAwaitingWebhook) {
            // Paid + pickup already confirmed (deadline cleared).
            HStack(spacing: 6) {
                Image(systemName: "checkmark.circle.fill")
                Text(leaseRequest.pickupConfirmedAt != nil ? "Pickup Confirmed" : "Payment Complete")
            }
            .font(.subheadline.weight(.medium))
            .frame(maxWidth: .infinity)
            .padding(.vertical, 10)
            .foregroundColor(.green)
        } else if leaseRequest.status == .expiredRefunded {
            // Terminal: deadline missed, payment refunded, car back on market.
            HStack(spacing: 6) {
                Image(systemName: "arrow.uturn.backward.circle.fill")
                Text(isDriver
                    ? "Pickup deadline missed — payment refunded"
                    : "Driver missed pickup — rental cancelled")
            }
            .font(.subheadline.weight(.medium))
            .frame(maxWidth: .infinity)
            .padding(.vertical, 10)
            .foregroundColor(.secondary)
        } else if isDriver && leaseRequest.isPaymentProcessing {
            // Payment in-flight — show processing indicator
            HStack(spacing: 6) {
                ProgressView()
                    .scaleEffect(0.8)
                Text("Processing Payment...")
            }
            .font(.subheadline.weight(.medium))
            .frame(maxWidth: .infinity)
            .padding(.vertical, 10)
            .foregroundColor(.orange)
        } else if isDriver && leaseRequest.driverCanPay {
            // Driver can pay (accepted, or payment_pending with failed/canceled payment)
            let isRetry = leaseRequest.status == .paymentPending
            Button(action: onPay) {
                HStack(spacing: 6) {
                    Image(systemName: "creditcard.fill")
                    Text(isRetry ? "Retry Payment" : "Pay Now")
                }
                .font(.subheadline.weight(.medium))
                .frame(maxWidth: .infinity)
                .padding(.vertical, 10)
                .background(Color.driveBaiPrimary)
                .foregroundColor(.white)
                .clipShape(RoundedRectangle(cornerRadius: 10))
            }
        } else if isDriver && leaseRequest.status.driverCanCancel {
            // Driver can cancel
            Button(action: onCancel) {
                Text("Cancel Request")
                    .font(.subheadline.weight(.medium))
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 10)
                    .background(Color(.systemGray5))
                    .foregroundColor(.red)
                    .clipShape(RoundedRectangle(cornerRadius: 10))
            }
        }
    }
}
