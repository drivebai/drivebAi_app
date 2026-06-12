import SwiftUI

/// Today-tab card for the post-payment key handover. Shows pickup location,
/// per-role status messaging, a confirmation countdown (while the driver's
/// window is open), and the role-appropriate confirm CTA.
struct KeyHandoverCard: View {
    let handover: KeyHandover
    let currentTime: Date
    var isSubmitting: Bool = false
    var onAct: () -> Void
    var onOpen: () -> Void
    /// Owner-only: invoked with a preset (15/30/60). nil when not provided
    /// (e.g. driver-side or older call sites — extend UI is hidden).
    var onExtendPickup: ((Int) -> Void)? = nil
    /// Per-user "Got it" tap on the terminal refunded state. nil when not
    /// wired (older call sites — the button is hidden).
    var onDismiss: (() -> Void)? = nil

    @State private var showingExtendDialog = false

    /// Once the owner has confirmed key handover the in-person handshake
    /// owns the screen — the pickup countdown is no longer the urgent
    /// deadline. Gating both countdowns on this avoids the "two timers
    /// stacked" UX bug.
    private var isHandoverHandshakeActive: Bool {
        handover.status == .ownerConfirmed || handover.status == .completed
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            header
            carRow
            Text(handover.statusMessage)
                .font(.subheadline)
                .foregroundColor(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            // Pickup-deadline countdown (lease-side). Renders for both roles
            // while the lease is paid + awaiting pickup AND the in-person
            // handshake hasn't started. The owner also gets an "Add time"
            // accessory when the cap has headroom.
            if handover.isAwaitingPickupConfirmation,
               !isHandoverHandshakeActive,
               let pickupDeadline = handover.pickupDeadlineAt {
                PickupCountdownView(
                    deadline: pickupDeadline,
                    headline: handover.viewerRole == .driver
                        ? "Pick up before"
                        : "Driver pickup deadline",
                    subline: pickupSubline,
                    accessory: { AnyView(extendAccessory) }
                )
            } else if handover.isPickupRefunded {
                refundedRow
            }

            // Existing handover-confirmation window (15-min driver receipt
            // confirmation), shown only after owner taps "I handed over".
            // Suppressed when the lease was already refunded — the dismiss
            // CTA owns this state.
            if handover.showsCountdown,
               !handover.isPickupRefunded,
               let deadline = handover.confirmationDeadline {
                countdownRow(deadline: deadline)
            }

            // Terminal-state acknowledgement OR the role's confirm CTA.
            // Refunded leases never expose the confirm-keys CTA — once the
            // payment is refunded, "I handed over the keys" would be a no-op
            // (or worse, racy).
            if handover.isPickupRefunded {
                if onDismiss != nil {
                    dismissButton
                }
            } else if let cta = handover.primaryActionTitle {
                actionButton(title: cta)
            }
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
        .confirmationDialog(
            "Add more time?",
            isPresented: $showingExtendDialog,
            titleVisibility: .visible
        ) {
            ForEach(handover.availableExtensionPresets, id: \.self) { minutes in
                Button("+\(minutes) minutes") {
                    onExtendPickup?(minutes)
                }
            }
            Button("Cancel", role: .cancel) { }
        } message: {
            Text("Gives the driver more time to pick up the car before the automatic refund. Up to \(handover.pickupExtensionRemainingMinutes) minutes left to add.")
        }
    }

    // MARK: Pickup-deadline accessories

    /// Subline shown under the pickup countdown. Role-specific copy.
    private var pickupSubline: String {
        if handover.viewerRole == .driver {
            if handover.pickupExtensionCount > 0 {
                return "Owner added \(handover.pickupExtensionTotalMinutes) min to your deadline. Confirm pickup or you'll be auto-refunded."
            }
            return "Confirm pickup or you'll be auto-refunded when the timer hits zero."
        }
        // Owner
        if handover.canOwnerExtendPickup() {
            if handover.pickupExtensionCount > 0 {
                return "You've added \(handover.pickupExtensionTotalMinutes) min so far. Up to \(handover.pickupExtensionRemainingMinutes) min still available."
            }
            return "If the driver is on the way, give them a few more minutes before the auto-refund kicks in."
        }
        // Owner can't extend — explain why with copy that actually matches reality.
        if let deadline = handover.pickupDeadlineAt, deadline <= currentTime {
            return "Pickup deadline has passed."
        }
        if handover.pickupExtensionRemainingMinutes <= 0 {
            return "Maximum extra pickup time has already been added."
        }
        if handover.pickupExtensionRemainingMinutes < (LeaseRequest.allowedPickupExtensionMinutes.min() ?? 15) {
            return "No preset extension fits the remaining limit."
        }
        return ""
    }

    /// "Got it" button shown on the terminal refunded card. Tapping it
    /// fires the dismiss API + optimistically removes the card.
    private var dismissButton: some View {
        Button(action: { onDismiss?() }) {
            Text("Got it")
                .font(.subheadline.weight(.semibold))
                .frame(maxWidth: .infinity)
                .frame(height: TodayLayout.optionButtonHeight)
                .background(Color(.systemGray5))
                .foregroundColor(.primary)
                .cornerRadius(8)
        }
        .buttonStyle(.plain)
    }

    /// Owner-only "+ Add time" pill shown next to the countdown. Hidden for
    /// the driver, or when the cap is exhausted, or when no callback was wired.
    @ViewBuilder
    private var extendAccessory: some View {
        if handover.canOwnerExtendPickup() && onExtendPickup != nil {
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
                .background(TodayLayout.tealAccent)
                .foregroundColor(.white)
                .clipShape(Capsule())
            }
            .buttonStyle(.plain)
        } else {
            EmptyView()
        }
    }

    /// Terminal-state notice when the lease is `expired_refunded` but the
    /// handover row is still on the Today list during the next refresh.
    private var refundedRow: some View {
        Label(
            handover.viewerRole == .driver
                ? "Pickup deadline missed — payment refunded"
                : "Driver missed pickup — rental cancelled",
            systemImage: "arrow.uturn.backward.circle.fill"
        )
        .font(.caption.weight(.medium))
        .foregroundColor(.secondary)
    }

    // MARK: Subviews

    private var header: some View {
        HStack(spacing: 8) {
            Image(systemName: "key.fill")
                .font(.system(size: 14, weight: .semibold))
                .foregroundColor(TodayLayout.tealAccent)
            Text("Key handover")
                .font(.headline)
            Spacer()
            statusPill
        }
    }

    private var carRow: some View {
        VStack(alignment: .leading, spacing: 4) {
            if !handover.carTitle.isEmpty {
                Text(handover.carTitle)
                    .font(.subheadline.weight(.semibold))
                    .foregroundColor(.primary)
            }
            Label(handover.pickupLocationText, systemImage: "mappin.and.ellipse")
                .font(.caption)
                .foregroundColor(.secondary)
                .lineLimit(2)
        }
    }

    private var statusPill: some View {
        Text(statusLabel)
            .font(.caption2.weight(.semibold))
            .foregroundColor(statusColor)
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .background(statusColor.opacity(0.12))
            .clipShape(Capsule())
    }

    private func countdownRow(deadline: Date) -> some View {
        let remaining = max(0, deadline.timeIntervalSince(currentTime))
        let expired = remaining <= 0
        return Label(
            expired ? "Confirmation window closed" : "Confirm within \(Self.minutesString(remaining))",
            systemImage: expired ? "exclamationmark.triangle.fill" : "clock.fill"
        )
        .font(.caption.weight(.medium))
        .foregroundColor(expired ? Color.driveBaiSecondary : TodayLayout.tealAccent)
    }

    private func actionButton(title: String) -> some View {
        Button(action: onAct) {
            HStack(spacing: 8) {
                if isSubmitting {
                    ProgressView().tint(.white).scaleEffect(0.85)
                }
                Text(isSubmitting ? "Confirming…" : title)
                    .font(.subheadline.weight(.semibold))
            }
            .frame(maxWidth: .infinity)
            .frame(height: TodayLayout.optionButtonHeight)
            .background(isSubmitting ? TodayLayout.tealAccent.opacity(0.6) : TodayLayout.tealAccent)
            .foregroundColor(.white)
            .cornerRadius(8)
        }
        .disabled(isSubmitting)
    }

    // MARK: Status styling

    private var statusLabel: String {
        switch handover.status {
        case .pending:         return "Pending"
        case .ownerConfirmed:  return "Awaiting driver"
        case .completed:       return "Completed"
        case .expired:         return "Expired"
        }
    }

    private var statusColor: Color {
        switch handover.status {
        case .pending:         return TodayLayout.tealAccent
        case .ownerConfirmed:  return .orange
        case .completed:       return .green
        case .expired:         return Color.driveBaiSecondary
        }
    }

    private static func minutesString(_ seconds: TimeInterval) -> String {
        let mins = Int(ceil(seconds / 60))
        return mins <= 1 ? "1 min" : "\(mins) min"
    }
}
