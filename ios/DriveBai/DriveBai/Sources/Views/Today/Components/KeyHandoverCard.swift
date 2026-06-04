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

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            header
            carRow
            Text(handover.statusMessage)
                .font(.subheadline)
                .foregroundColor(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            if handover.showsCountdown, let deadline = handover.confirmationDeadline {
                countdownRow(deadline: deadline)
            }

            if let cta = handover.primaryActionTitle {
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
