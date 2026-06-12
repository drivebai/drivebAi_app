import SwiftUI

/// Live HH/MM countdown to the pickup deadline. Visual urgency tiers:
///
/// * **normal**  — > 60 min left. Calm primary tint.
/// * **warning** — 15…60 min left. Amber accent.
/// * **critical** — < 15 min left. Red accent + bold weight.
///
/// Hours/minutes only (no seconds) by design: prevents the card from
/// constantly twitching while still communicating the time pressure.
/// `TimelineView(.periodic)` is what drives the redraws, so callers don't
/// need to manage their own timer.
struct PickupCountdownView: View {
    /// Absolute deadline. The card stops counting at this point and renders
    /// the "Deadline passed" state (driver-facing copy is owned by the card).
    let deadline: Date

    /// Headline above the timer. e.g. "Pick up the car before" or
    /// "Waiting for driver pickup". Caller chooses the wording.
    let headline: String

    /// Optional supporting copy below the timer (e.g. extension hint or
    /// refund notice). Hidden when nil/empty.
    let subline: String?

    /// Side-by-side accessory (used for the owner's "Add more time" button).
    /// Caller supplies the view; nil means nothing extra.
    @ViewBuilder var accessory: () -> AnyView

    init(
        deadline: Date,
        headline: String,
        subline: String? = nil,
        @ViewBuilder accessory: @escaping () -> AnyView = { AnyView(EmptyView()) }
    ) {
        self.deadline = deadline
        self.headline = headline
        self.subline = subline
        self.accessory = accessory
    }

    var body: some View {
        // 15s tick keeps HH/MM accurate and crosses the tier boundaries
        // (60 → 59 min, 15 → 14 min) without burning CPU on per-second
        // re-renders.
        TimelineView(.periodic(from: .now, by: 15)) { context in
            content(now: context.date)
        }
    }

    @ViewBuilder
    private func content(now: Date) -> some View {
        let remaining = max(0, deadline.timeIntervalSince(now))
        let tier = Tier(secondsRemaining: remaining)

        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .center, spacing: 10) {
                Image(systemName: tier.iconName)
                    .font(.title3.weight(.semibold))
                    .foregroundColor(tier.accentColor)
                    .frame(width: 28, height: 28)
                    .background(tier.accentColor.opacity(0.15))
                    .clipShape(RoundedRectangle(cornerRadius: 7))

                VStack(alignment: .leading, spacing: 2) {
                    Text(headline)
                        .font(.caption)
                        .foregroundColor(.secondary)

                    Text(Self.format(remainingSeconds: remaining))
                        .font(tier.timerFont)
                        .foregroundColor(tier.accentColor)
                        .monospacedDigit()
                }

                Spacer(minLength: 8)

                accessory()
            }

            if let subline, !subline.isEmpty {
                Text(subline)
                    .font(.caption)
                    .foregroundColor(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(12)
        .background(tier.accentColor.opacity(0.08))
        .overlay(
            RoundedRectangle(cornerRadius: 12)
                .stroke(tier.accentColor.opacity(tier == .critical ? 0.5 : 0.18), lineWidth: 1)
        )
        .clipShape(RoundedRectangle(cornerRadius: 12))
    }

    // MARK: - Tier

    enum Tier: Equatable {
        case normal, warning, critical

        init(secondsRemaining: TimeInterval) {
            let minutes = Int(secondsRemaining / 60)
            if minutes < 15 {
                self = .critical
            } else if minutes <= 60 {
                self = .warning
            } else {
                self = .normal
            }
        }

        var accentColor: Color {
            switch self {
            case .normal: return .driveBaiPrimary
            case .warning: return .orange
            case .critical: return .red
            }
        }

        var iconName: String {
            switch self {
            case .normal: return "clock"
            case .warning: return "clock.fill"
            case .critical: return "exclamationmark.triangle.fill"
            }
        }

        var timerFont: Font {
            switch self {
            case .normal: return .title3.weight(.semibold)
            case .warning: return .title3.weight(.bold)
            case .critical: return .title2.weight(.heavy)
            }
        }
    }

    // MARK: - Formatting

    /// Formats remaining time as `HHh MMm` (always two digits per field).
    /// Examples: `01h 45m`, `00h 23m`, `00h 00m` (at the deadline).
    static func format(remainingSeconds: TimeInterval) -> String {
        let total = Int(remainingSeconds)
        let hours = total / 3600
        let minutes = (total % 3600) / 60
        return String(format: "%02dh %02dm", hours, minutes)
    }
}
