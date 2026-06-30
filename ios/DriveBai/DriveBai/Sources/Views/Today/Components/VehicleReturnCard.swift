import SwiftUI

/// Today-tab card for the post-rental return handshake. Mirrors the visual
/// language of `KeyHandoverCard` (teal icon + status pill + per-role body +
/// primary/secondary CTAs), but adds a dedicated refund banner under the
/// body so the user sees the dollar figure before tapping anything.
struct VehicleReturnCard: View {
    let vehicleReturn: VehicleReturn
    let currentTime: Date
    var isSubmitting: Bool = false

    /// Driver: tap "I returned the car" / "Undo return".
    /// Owner: tap "Confirm return".
    var onAct: () -> Void
    /// Owner-only: opens the dispute sheet (filled with the user's reason).
    var onDispute: (() -> Void)? = nil
    /// Tap-through into the chat thread (used for both terminal-info open
    /// and just exploring context). nil = no-op.
    var onOpen: () -> Void = {}
    /// "Got it" tap on a terminal completed/cancelled card.
    var onDismiss: (() -> Void)? = nil

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            header
            carRow

            Text(vehicleReturn.bodyMessage)
                .font(.subheadline)
                .foregroundColor(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            // Refund banner — shown for any state where the refund amount
            // is interesting (driver_initiated through completed). Suppressed
            // for cancelled (refund never happened) and for zero-refund
            // disputed rows (no money to discuss).
            if shouldShowRefundBanner {
                refundBanner
            }

            // Driver cancel-window countdown line. Only visible to the
            // driver, only while the 5-min window is open.
            if let remaining = vehicleReturn.driverCancelTimeRemaining(now: currentTime) {
                cancelWindowRow(remaining: remaining)
            }

            // CTAs: undo / confirm / dispute / dismiss.
            actionRow
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

    // MARK: - Header / car

    private var header: some View {
        HStack(spacing: 8) {
            Image(systemName: "arrow.uturn.backward.circle.fill")
                .font(.system(size: 14, weight: .semibold))
                .foregroundColor(TodayLayout.tealAccent)
            Text("Vehicle return")
                .font(.headline)
            Spacer()
            statusPill
        }
    }

    private var carRow: some View {
        VStack(alignment: .leading, spacing: 4) {
            if !vehicleReturn.carTitle.isEmpty {
                Text(vehicleReturn.carTitle)
                    .font(.subheadline.weight(.semibold))
                    .foregroundColor(.primary)
            }
            Text(vehicleReturn.headline)
                .font(.subheadline.weight(.medium))
                .foregroundColor(.primary)
        }
    }

    // MARK: - Status pill

    private var statusPill: some View {
        Text(statusLabel)
            .font(.caption2.weight(.semibold))
            .foregroundColor(statusColor)
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .background(statusColor.opacity(0.12))
            .clipShape(Capsule())
    }

    private var statusLabel: String {
        switch vehicleReturn.status {
        case .driverInitiated:  return "Pending"
        case .ownerConfirmed:   return "Confirmed"
        case .disputed:         return "Disputed"
        case .completed:        return "Completed"
        case .cancelled:        return "Cancelled"
        }
    }

    private var statusColor: Color {
        switch vehicleReturn.status {
        case .driverInitiated:  return .orange
        case .ownerConfirmed:   return TodayLayout.tealAccent
        case .disputed:         return .red
        case .completed:        return .green
        case .cancelled:        return Color.driveBaiSecondary
        }
    }

    // MARK: - Refund banner

    /// Render the refund banner whenever there's a meaningful dollar amount
    /// to talk about: pending review, in-flight, or finalized. Suppress for
    /// cancelled/disputed-zero-refund rows so we don't show a misleading
    /// "$0.00" pill.
    private var shouldShowRefundBanner: Bool {
        switch vehicleReturn.status {
        case .cancelled:
            return false
        case .disputed:
            return vehicleReturn.hasRefund
        case .driverInitiated, .ownerConfirmed, .completed:
            return true
        }
    }

    private var refundBanner: some View {
        let refundAmount = vehicleReturn.formattedRefundAmount
        let primaryLine: String
        let detailLine: String
        let icon: String
        let tone: Color

        switch vehicleReturn.status {
        case .completed where vehicleReturn.hasRefund,
             .ownerConfirmed where vehicleReturn.refundStatus == .succeeded:
            primaryLine = "\(refundAmount) refunded"
            detailLine = "Sent to your original payment method"
            icon = "checkmark.seal.fill"
            tone = .green
        case .ownerConfirmed:
            primaryLine = "Refund \(refundAmount) — processing"
            detailLine = "We'll let you know once Stripe confirms"
            icon = "clock.fill"
            tone = TodayLayout.tealAccent
        case .completed:
            // hasRefund == false branch
            primaryLine = "No refund due"
            detailLine = "Full rental period was used"
            icon = "checkmark.circle.fill"
            tone = .green
        case .driverInitiated where vehicleReturn.hasRefund:
            primaryLine = "Estimated refund \(refundAmount)"
            detailLine = "\(vehicleReturn.usedDays) of \(vehicleReturn.totalPaidDays) days used"
            icon = "dollarsign.circle.fill"
            tone = TodayLayout.tealAccent
        case .driverInitiated:
            primaryLine = "No refund due"
            detailLine = "Full rental period used (\(vehicleReturn.totalPaidDays) days)"
            icon = "info.circle.fill"
            tone = Color.driveBaiSecondary
        default:
            primaryLine = "Refund \(refundAmount)"
            detailLine = ""
            icon = "dollarsign.circle.fill"
            tone = TodayLayout.tealAccent
        }

        return HStack(alignment: .top, spacing: 10) {
            Image(systemName: icon)
                .font(.system(size: 16, weight: .semibold))
                .foregroundColor(tone)
            VStack(alignment: .leading, spacing: 2) {
                Text(primaryLine)
                    .font(.subheadline.weight(.semibold))
                    .foregroundColor(.primary)
                if !detailLine.isEmpty {
                    Text(detailLine)
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
            }
            Spacer(minLength: 0)
        }
        .padding(10)
        .background(tone.opacity(0.08))
        .clipShape(RoundedRectangle(cornerRadius: 10))
    }

    // MARK: - Cancel window row

    private func cancelWindowRow(remaining: TimeInterval) -> some View {
        Label(
            "You can undo for \(Self.minutesAndSecondsString(remaining))",
            systemImage: "clock.fill"
        )
        .font(.caption.weight(.medium))
        .foregroundColor(TodayLayout.tealAccent)
    }

    // MARK: - Action row

    @ViewBuilder
    private var actionRow: some View {
        switch vehicleReturn.status {
        case .driverInitiated where vehicleReturn.viewerRole == .driver:
            if vehicleReturn.isWithinDriverCancelWindow(now: currentTime) {
                actionButton(
                    title: vehicleReturn.primaryActionTitle ?? "Undo return",
                    style: .destructive
                )
            }
        case .driverInitiated where vehicleReturn.viewerRole == .owner:
            HStack(spacing: 10) {
                if vehicleReturn.secondaryActionTitle != nil {
                    Button(action: { onDispute?() }) {
                        Text(vehicleReturn.secondaryActionTitle ?? "Dispute")
                            .font(.subheadline.weight(.medium))
                            .frame(maxWidth: .infinity)
                            .frame(height: TodayLayout.optionButtonHeight)
                            .background(Color(.systemGray5))
                            .foregroundColor(.red)
                            .cornerRadius(8)
                    }
                    .buttonStyle(.plain)
                    .disabled(isSubmitting)
                }
                actionButton(
                    title: vehicleReturn.primaryActionTitle ?? "Confirm return",
                    style: .primary
                )
            }
        case .completed, .cancelled:
            if onDismiss != nil {
                dismissButton
            }
        default:
            EmptyView()
        }
    }

    private enum ButtonStyleKind { case primary, destructive }

    private func actionButton(title: String, style: ButtonStyleKind) -> some View {
        let bg: Color = {
            switch style {
            case .primary:
                return isSubmitting ? TodayLayout.tealAccent.opacity(0.6) : TodayLayout.tealAccent
            case .destructive:
                return isSubmitting ? Color.red.opacity(0.6) : Color.red
            }
        }()
        return Button(action: onAct) {
            HStack(spacing: 8) {
                if isSubmitting {
                    ProgressView().tint(.white).scaleEffect(0.85)
                }
                Text(isSubmitting ? "Working…" : title)
                    .font(.subheadline.weight(.semibold))
            }
            .frame(maxWidth: .infinity)
            .frame(height: TodayLayout.optionButtonHeight)
            .background(bg)
            .foregroundColor(.white)
            .cornerRadius(8)
        }
        .buttonStyle(.plain)
        .disabled(isSubmitting)
    }

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

    // MARK: - Helpers

    /// Renders "4:32" while >1 min remaining, or "0:42" / "12s" near zero.
    /// Kept tight so the cancel-window line doesn't wrap.
    private static func minutesAndSecondsString(_ seconds: TimeInterval) -> String {
        let total = Int(ceil(seconds))
        if total >= 60 {
            let m = total / 60
            let s = total % 60
            return String(format: "%d:%02d", m, s)
        }
        return "\(total)s"
    }
}
