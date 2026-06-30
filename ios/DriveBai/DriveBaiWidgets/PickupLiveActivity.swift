import ActivityKit
import SwiftUI
import WidgetKit

// MARK: - Pickup Live Activity
//
// Reads `PickupActivityAttributes` (defined in the host app at
// Sources/LiveActivity/PickupActivityAttributes.swift) which must be added
// to BOTH targets via Target Membership.
//
// Visual language mirrors the in-app `PickupCountdownView` and the Today
// KeyHandoverCard:
//   - > 60 min remaining   → brand teal (AccentColor) "normal"
//   - 15-60 min            → amber "warning"
//   - < 15 min             → red "critical"
//
// Two system-driven primitives keep the surface live without burning
// battery:
//   - `Text(timerInterval:countsDown:)` makes the digits tick per second
//     in SpringBoard with zero work from the host app.
//   - `ProgressView(timerInterval:countsDown:)` (iOS 17+, built
//     specifically for Live Activities) auto-fills the progress bar
//     between our updates so the bar isn't frozen on a snapshot.
// Combined with a custom `ProgressViewStyle` for thick high-contrast
// capsules — the default `.linear` style renders as an almost-invisible
// hairline inside Live Activities, which is what made v1 look flat.

@available(iOS 16.1, *)
struct PickupLiveActivity: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: PickupActivityAttributes.self) { context in
            // MARK: Lock Screen / banner
            PickupLockScreenView(
                attributes: context.attributes,
                state: context.state
            )
            .widgetURL(PickupActivityDeepLink.url(forLease: context.attributes.leaseRequestId))
        } dynamicIsland: { context in
            // MARK: Dynamic Island
            DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    PickupExpandedLeading(state: context.state)
                }
                DynamicIslandExpandedRegion(.trailing) {
                    PickupExpandedTrailing(state: context.state)
                }
                DynamicIslandExpandedRegion(.bottom) {
                    PickupExpandedBottom(attributes: context.attributes, state: context.state)
                }
            } compactLeading: {
                Image(systemName: pickupIconName(for: context.state))
                    .foregroundStyle(pickupTint(for: context.state))
            } compactTrailing: {
                if let terminal = terminalLabel(for: context.state.phase) {
                    Text(terminal)
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(pickupTint(for: context.state))
                } else {
                    Text(
                        timerInterval: Date()...context.state.deadline,
                        countsDown: true,
                        showsHours: true
                    )
                    .monospacedDigit()
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(pickupTint(for: context.state))
                    .frame(maxWidth: 58, alignment: .trailing)
                    .multilineTextAlignment(.trailing)
                }
            } minimal: {
                Image(systemName: pickupIconName(for: context.state))
                    .foregroundStyle(pickupTint(for: context.state))
            }
            .widgetURL(PickupActivityDeepLink.url(forLease: context.attributes.leaseRequestId))
        }
    }
}

// MARK: - Lock Screen banner

@available(iOS 16.1, *)
private struct PickupLockScreenView: View {
    let attributes: PickupActivityAttributes
    let state: PickupActivityAttributes.ContentState

    var body: some View {
        // Hero-first layout: icon tile + small two-line header at top,
        // then the BIG countdown across its own row, then a thick
        // progress bar, then a footer row. Stacking (instead of cramming
        // everything in one horizontal row like v1) prevents the
        // "Pickup window…" truncation seen in QA and gives the timer
        // the visual weight it needs.
        VStack(alignment: .leading, spacing: 10) {
            // ── Header row ───────────────────────────────────────────
            HStack(alignment: .center, spacing: 12) {
                iconTile
                VStack(alignment: .leading, spacing: 2) {
                    Text(headline)
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(.primary)
                        .lineLimit(1)
                    Text(attributes.carTitle)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
                Spacer(minLength: 0)
            }

            // ── Hero countdown ──────────────────────────────────────
            // Big bold monospaced numerals so the deadline reads at a
            // glance from across the room. System ticks them per-second.
            heroTimer

            // ── Progress bar (system-driven, custom thick capsule) ──
            progressBar

            // ── Footer row ──────────────────────────────────────────
            footerRow
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 14)
        // No app-drawn background — iOS provides the Lock Screen card
        // chrome that adapts to the wallpaper. Painting our own would
        // double up.
    }

    private var iconTile: some View {
        let tint = pickupTint(for: state)
        return Image(systemName: pickupIconName(for: state))
            .font(.title3.weight(.semibold))
            .foregroundStyle(tint)
            .frame(width: 44, height: 44)
            .background(
                RoundedRectangle(cornerRadius: 11, style: .continuous)
                    .fill(tint.opacity(0.18))
            )
    }

    @ViewBuilder
    private var heroTimer: some View {
        let tint = pickupTint(for: state)
        if let label = terminalLabel(for: state.phase) {
            Text(label)
                .font(.system(size: 34, weight: .heavy, design: .rounded))
                .foregroundStyle(tint)
        } else {
            Text(
                timerInterval: Date()...state.deadline,
                countsDown: true,
                showsHours: true
            )
            .font(.system(size: 38, weight: .heavy, design: .rounded).monospacedDigit())
            .foregroundStyle(tint)
            .lineLimit(1)
            .minimumScaleFactor(0.6)
        }
    }

    @ViewBuilder
    private var progressBar: some View {
        if state.phase == .active {
            // ProgressView(timerInterval:) is the only way to get a
            // truly live, auto-advancing bar inside a Live Activity —
            // ProgressView(value:) is a snapshot frozen between updates.
            ProgressView(
                timerInterval: state.startedAt...state.deadline,
                countsDown: false,
                label: { EmptyView() },
                currentValueLabel: { EmptyView() }
            )
            .progressViewStyle(
                ThickCapsuleProgressStyle(
                    tint: pickupTint(for: state),
                    height: 8
                )
            )
        } else {
            // Terminal states show a fully-filled (or empty) bar in the
            // terminal color so the card doesn't suddenly look empty.
            ThickCapsuleStatic(
                fraction: state.phase == .pickupConfirmed ? 1.0 : 0.0,
                tint: pickupTint(for: state),
                height: 8
            )
        }
    }

    @ViewBuilder
    private var footerRow: some View {
        HStack(spacing: 8) {
            footerLeft
            Spacer(minLength: 8)
            footerRight
        }
    }

    @ViewBuilder
    private var footerLeft: some View {
        if state.phase == .active {
            HStack(spacing: 4) {
                Image(systemName: "flag.checkered")
                    .font(.caption2)
                Text("Pick up before")
                    .font(.caption)
                Text(state.deadline, style: .time)
                    .font(.caption.weight(.semibold).monospacedDigit())
            }
            .foregroundStyle(.secondary)
            .lineLimit(1)
            .minimumScaleFactor(0.75)
        } else {
            Text(terminalFooter)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .minimumScaleFactor(0.75)
        }
    }

    @ViewBuilder
    private var footerRight: some View {
        if state.phase == .active {
            Text(footerCTA)
                .font(.caption.weight(.semibold))
                .foregroundStyle(pickupTint(for: state))
                .lineLimit(1)
                .minimumScaleFactor(0.75)
                .layoutPriority(1) // narrow-device tie-breaker: keep CTA, shrink left
        } else {
            EmptyView()
        }
    }

    private var headline: String {
        switch state.phase {
        case .active:
            return attributes.viewerRole == .owner ? "Driver picking up" : "Pickup active"
        case .pickupConfirmed:
            return "Pickup confirmed"
        case .expired:
            return "Pickup expired"
        case .cancelled:
            return "Pickup cancelled"
        }
    }

    private var footerCTA: String {
        attributes.viewerRole == .owner ? "Waiting on driver" : "Confirm in app"
    }

    private var terminalFooter: String {
        switch state.phase {
        case .active:
            return ""
        case .pickupConfirmed:
            return "Rental started — have a great trip."
        case .expired:
            return "Window expired — refund in progress."
        case .cancelled:
            return "Rental was cancelled."
        }
    }
}

// MARK: - Dynamic Island regions
//
// Apple's expanded layout splits into leading (above the notch, left),
// trailing (above the notch, right), and bottom (full-width row beneath).
// The previous version stuffed icon + headline + car title into leading,
// which crowded out the trailing region. New layout:
//   - leading: ONE row — icon + short status (no car title; bottom owns that)
//   - trailing: the big timer (hero)
//   - bottom: progress bar + car title + deadline

@available(iOS 16.1, *)
private struct PickupExpandedLeading: View {
    let state: PickupActivityAttributes.ContentState

    var body: some View {
        HStack(spacing: 6) {
            Image(systemName: pickupIconName(for: state))
                .font(.callout.weight(.semibold))
                .foregroundStyle(pickupTint(for: state))
            Text(headline)
                .font(.callout.weight(.semibold))
                .lineLimit(1)
        }
    }

    private var headline: String {
        switch state.phase {
        case .active:          return "Pickup"
        case .pickupConfirmed: return "Confirmed"
        case .expired:         return "Expired"
        case .cancelled:       return "Cancelled"
        }
    }
}

@available(iOS 16.1, *)
private struct PickupExpandedTrailing: View {
    let state: PickupActivityAttributes.ContentState

    var body: some View {
        let tint = pickupTint(for: state)
        if let terminal = terminalLabel(for: state.phase) {
            Text(terminal)
                .font(.title3.weight(.bold))
                .foregroundStyle(tint)
        } else {
            Text(
                timerInterval: Date()...state.deadline,
                countsDown: true,
                showsHours: true
            )
            .font(.system(size: 26, weight: .heavy, design: .rounded).monospacedDigit())
            .foregroundStyle(tint)
            .lineLimit(1)
            .minimumScaleFactor(0.6)
            .frame(maxWidth: 130, alignment: .trailing)
        }
    }
}

@available(iOS 16.1, *)
private struct PickupExpandedBottom: View {
    let attributes: PickupActivityAttributes
    let state: PickupActivityAttributes.ContentState

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if state.phase == .active {
                ProgressView(
                    timerInterval: state.startedAt...state.deadline,
                    countsDown: false,
                    label: { EmptyView() },
                    currentValueLabel: { EmptyView() }
                )
                .progressViewStyle(
                    ThickCapsuleProgressStyle(
                        tint: pickupTint(for: state),
                        height: 6
                    )
                )
            } else {
                ThickCapsuleStatic(
                    fraction: state.phase == .pickupConfirmed ? 1.0 : 0.0,
                    tint: pickupTint(for: state),
                    height: 6
                )
            }

            HStack(spacing: 8) {
                Text(attributes.carTitle)
                    .font(.caption.weight(.medium))
                    .foregroundStyle(.primary)
                    .lineLimit(1)
                Spacer(minLength: 8)
                if state.phase == .active {
                    HStack(spacing: 4) {
                        Image(systemName: "flag.checkered")
                            .font(.caption2)
                        Text(state.deadline, style: .time)
                            .font(.caption.monospacedDigit())
                    }
                    .foregroundStyle(.secondary)
                }
            }
        }
        .padding(.top, 2)
    }
}

// MARK: - Tier-aware styling (shared by Lock Screen + Island)

/// DriveBai brand teal, hard-coded as a literal so the widget renders
/// correctly even if the asset catalog's `AccentColor` ever ships empty
/// (which IS what caused QA #N: the Xcode wizard generated an empty
/// `AccentColor.colorset` and `Color("AccentColor", bundle: nil)`
/// silently resolved to `.clear`, making every tinted view — icon tile,
/// hero countdown, progress bar, "Confirm in app" footer — invisible).
/// RGB matches `DriveBai/Assets.xcassets/AccentColor.colorset` exactly.
@available(iOS 16.1, *)
private let brandTeal = Color(
    .sRGB,
    red: 0.306,
    green: 0.804,
    blue: 0.769,
    opacity: 1.0
)

@available(iOS 16.1, *)
private func pickupTint(for state: PickupActivityAttributes.ContentState) -> Color {
    switch state.phase {
    case .pickupConfirmed: return .green
    case .expired:         return .gray
    case .cancelled:       return .gray
    case .active:
        let remaining = max(0, state.deadline.timeIntervalSinceNow)
        switch PickupActivityTier(remainingSeconds: remaining) {
        case .normal:   return brandTeal
        case .warning:  return .orange
        case .critical: return .red
        }
    }
}

@available(iOS 16.1, *)
private func pickupIconName(for state: PickupActivityAttributes.ContentState) -> String {
    switch state.phase {
    case .pickupConfirmed: return "checkmark.circle.fill"
    case .expired:         return "clock.badge.xmark.fill"
    case .cancelled:       return "xmark.circle.fill"
    case .active:
        // Always use the car icon for the hero — matches the in-app
        // KeyHandoverCard's identity. The tier color is what conveys
        // urgency, not the icon swap.
        return "car.fill"
    }
}

@available(iOS 16.1, *)
private func terminalLabel(for phase: PickupActivityAttributes.ContentState.Phase) -> String? {
    switch phase {
    case .active:          return nil
    case .pickupConfirmed: return "Confirmed"
    case .expired:         return "Expired"
    case .cancelled:       return "Cancelled"
    }
}

// MARK: - Thick capsule progress (system-driven)

/// Custom ProgressViewStyle that draws a thick high-contrast capsule
/// instead of the default hairline. Used together with
/// `ProgressView(timerInterval:countsDown:)` so SpringBoard advances the
/// fill automatically between our `Activity.update(...)` calls — that's
/// what makes the bar feel alive instead of frozen.
@available(iOS 16.1, *)
private struct ThickCapsuleProgressStyle: ProgressViewStyle {
    let tint: Color
    let height: CGFloat

    func makeBody(configuration: Configuration) -> some View {
        // Defensive clamp: ProgressView(timerInterval:) can hand us back
        // a nil or NaN fractionCompleted if the underlying interval is
        // inverted (deadline < startedAt) due to bad state. Clamping to
        // [0, 1] guarantees we always draw SOMETHING — never an
        // invisible zero-width fill that would re-create the original
        // "where did the progress bar go?" bug.
        let raw = configuration.fractionCompleted ?? 0
        let fraction = max(0, min(1, raw.isFinite ? raw : 0))
        return GeometryReader { geo in
            ZStack(alignment: .leading) {
                Capsule()
                    .fill(tint.opacity(0.18))
                Capsule()
                    .fill(tint)
                    .frame(width: geo.size.width * CGFloat(fraction))
            }
        }
        .frame(height: height)
    }
}

/// Static counterpart for terminal phases. ProgressView(timerInterval:)
/// keeps advancing past its end date, so for confirmed/expired/cancelled
/// we explicitly draw a snapshot capsule that matches the same look.
@available(iOS 16.1, *)
private struct ThickCapsuleStatic: View {
    let fraction: Double
    let tint: Color
    let height: CGFloat

    var body: some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                Capsule()
                    .fill(tint.opacity(0.18))
                Capsule()
                    .fill(tint)
                    .frame(width: geo.size.width * CGFloat(max(0, min(1, fraction))))
            }
        }
        .frame(height: height)
    }
}

// MARK: - Previews
//
// Xcode 15+ supports `#Preview` for Live Activities via the .previewContext
// modifier on ActivityAttributes. Lets the dev iterate the design in the
// preview canvas without running the full payment + lock-screen flow on
// device. One preview per urgency tier + terminal phase to catch
// invisible-color regressions like the one that motivated this fix.

@available(iOS 17.0, *)
private extension PickupActivityAttributes {
    static var preview: PickupActivityAttributes {
        PickupActivityAttributes(
            leaseRequestId: UUID(),
            chatId: UUID(),
            carTitle: "2003 Honda Accord",
            viewerRole: .driver
        )
    }
}

@available(iOS 17.0, *)
private extension PickupActivityAttributes.ContentState {
    static func preview(remainingMinutes: Int, phase: Phase = .active) -> Self {
        let deadline = Date().addingTimeInterval(TimeInterval(remainingMinutes * 60))
        let started = deadline.addingTimeInterval(-120 * 60)
        return PickupActivityAttributes.ContentState(
            deadline: deadline,
            startedAt: started,
            phase: phase
        )
    }
}

#if DEBUG
@available(iOS 17.0, *)
#Preview("Lock — normal (1h 57m)", as: .content, using: PickupActivityAttributes.preview) {
    PickupLiveActivity()
} contentStates: {
    PickupActivityAttributes.ContentState.preview(remainingMinutes: 117)
}

@available(iOS 17.0, *)
#Preview("Lock — warning (42m)", as: .content, using: PickupActivityAttributes.preview) {
    PickupLiveActivity()
} contentStates: {
    PickupActivityAttributes.ContentState.preview(remainingMinutes: 42)
}

@available(iOS 17.0, *)
#Preview("Lock — critical (8m)", as: .content, using: PickupActivityAttributes.preview) {
    PickupLiveActivity()
} contentStates: {
    PickupActivityAttributes.ContentState.preview(remainingMinutes: 8)
}

@available(iOS 17.0, *)
#Preview("Lock — confirmed", as: .content, using: PickupActivityAttributes.preview) {
    PickupLiveActivity()
} contentStates: {
    PickupActivityAttributes.ContentState.preview(remainingMinutes: 117, phase: .pickupConfirmed)
}

@available(iOS 17.0, *)
#Preview("Island — normal", as: .dynamicIsland(.expanded), using: PickupActivityAttributes.preview) {
    PickupLiveActivity()
} contentStates: {
    PickupActivityAttributes.ContentState.preview(remainingMinutes: 117)
}

@available(iOS 17.0, *)
#Preview("Island — critical", as: .dynamicIsland(.compact), using: PickupActivityAttributes.preview) {
    PickupLiveActivity()
} contentStates: {
    PickupActivityAttributes.ContentState.preview(remainingMinutes: 8)
}
#endif
