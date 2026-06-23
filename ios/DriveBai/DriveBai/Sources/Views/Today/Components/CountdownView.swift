import SwiftUI

/// Displays a countdown timer.
///
/// Adapts the precision to the time scale so multi-day countdowns don't
/// twitch on a per-second redraw and short countdowns convey urgency:
///   - ≥ 1 day remaining: shows D / H / M chips (parent timer can tick once
///     per minute — there's nothing to update faster than that).
///   - < 1 day remaining: drops the D chip and adds an S chip; meant to be
///     driven by a 1-second-cadence parent so the post-payment pickup
///     deadline counts down visibly.
///
/// The parent owns the timer cadence via `currentTime`; this view is pure.
struct CountdownView: View {
    let countdown: CountdownConfig
    let currentTime: Date

    private var remaining: (days: Int, hours: Int, minutes: Int, seconds: Int) {
        countdown.remainingTime(from: currentTime)
    }

    private var isOverdue: Bool {
        countdown.isOverdue
    }

    var body: some View {
        if isOverdue {
            overdueView
        } else {
            countdownView
        }
    }

    // MARK: - Overdue View

    private var overdueView: some View {
        OverdueBadge()
    }

    // MARK: - Countdown View

    private var countdownView: some View {
        CountdownRow(
            days: remaining.days,
            hours: remaining.hours,
            minutes: remaining.minutes,
            seconds: remaining.seconds
        )
    }
}

// MARK: - Overdue Badge

private struct OverdueBadge: View {
    var body: some View {
        HStack(spacing: 6) {
            Image(systemName: "clock.badge.exclamationmark")
                .font(.system(size: 14, weight: .medium))
                .foregroundColor(.orange)

            Text("Overdue")
                .font(.system(size: 13, weight: .medium))
                .foregroundColor(.orange)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(Color.orange.opacity(0.1))
        .cornerRadius(8)
        .overlay(
            RoundedRectangle(cornerRadius: 8)
                .stroke(Color.orange.opacity(0.3), lineWidth: 1)
        )
    }
}

// MARK: - Countdown Row

private struct CountdownRow: View {
    let days: Int
    let hours: Int
    let minutes: Int
    let seconds: Int

    var body: some View {
        HStack(spacing: 4) {
            Text("Due in")
                .font(.system(size: 12))
                .foregroundColor(.secondary)

            // Multi-day countdowns: keep the calm D/H/M layout — adding a
            // seconds chip at this scale just turns the card into a flicker.
            // Sub-day countdowns: drop the always-zero "d" chip and add an
            // "s" chip so the post-payment pickup deadline visibly counts
            // down (the urgent product cue this view was changed for).
            if days > 0 {
                CountdownChip(value: days, unit: "d")
                CountdownChip(value: hours, unit: "h")
                CountdownChip(value: minutes, unit: "m")
            } else {
                CountdownChip(value: hours, unit: "h")
                CountdownChip(value: minutes, unit: "m")
                CountdownChip(value: seconds, unit: "s")
            }
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
        .background(TodayLayout.tealAccentLight.opacity(0.5))
        .cornerRadius(8)
        .overlay(
            RoundedRectangle(cornerRadius: 8)
                .stroke(TodayLayout.tealAccent.opacity(0.2), lineWidth: 1)
        )
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Self.accessibilityLabel(days: days, hours: hours, minutes: minutes, seconds: seconds))
    }

    /// Spoken form for VoiceOver: "Due in 1 hour, 42 minutes, 18 seconds".
    /// Skipped fields say nothing (1 minute 0 seconds → "1 minute"), and
    /// long countdowns keep the days/hours/minutes form they used before.
    private static func accessibilityLabel(days: Int, hours: Int, minutes: Int, seconds: Int) -> String {
        var parts: [String] = []
        if days > 0 { parts.append("\(days) \(days == 1 ? "day" : "days")") }
        if hours > 0 { parts.append("\(hours) \(hours == 1 ? "hour" : "hours")") }
        if minutes > 0 { parts.append("\(minutes) \(minutes == 1 ? "minute" : "minutes")") }
        if days == 0 && seconds > 0 { parts.append("\(seconds) \(seconds == 1 ? "second" : "seconds")") }
        if parts.isEmpty { return "Due now" }
        return "Due in " + parts.joined(separator: ", ")
    }
}

// MARK: - Countdown Chip

private struct CountdownChip: View {
    let value: Int
    let unit: String

    var body: some View {
        HStack(spacing: 1) {
            Text("\(value)")
                .font(.system(size: 13, weight: .semibold, design: .monospaced))
                .monospacedDigit()
                .foregroundColor(.primary)

            Text(unit)
                .font(.system(size: 11, weight: .medium))
                .foregroundColor(.secondary)
        }
        .padding(.horizontal, 6)
        .padding(.vertical, 3)
        .background(Color(.systemBackground).opacity(0.8))
        .cornerRadius(4)
    }
}

// MARK: - Preview

#Preview("Countdown States") {
    // Pre-compute dates to avoid complex expressions in the view builder
    let now = Date()
    let normalDeadline = now.addingTimeInterval(187800) // 2d 5h 30m
    let shortDeadline = now.addingTimeInterval(8100)    // 2h 15m
    let veryShortDeadline = now.addingTimeInterval(300) // 5m
    let overdueDeadline = now.addingTimeInterval(-3600) // -1h

    return VStack(spacing: 20) {
        VStack(alignment: .leading, spacing: 4) {
            Text("Normal (2d 5h 30m)")
                .font(.caption)
                .foregroundColor(.secondary)
            CountdownView(
                countdown: CountdownConfig(deadline: normalDeadline),
                currentTime: now
            )
        }

        VStack(alignment: .leading, spacing: 4) {
            Text("Short (2h 15m)")
                .font(.caption)
                .foregroundColor(.secondary)
            CountdownView(
                countdown: CountdownConfig(deadline: shortDeadline),
                currentTime: now
            )
        }

        VStack(alignment: .leading, spacing: 4) {
            Text("Very Short (5m)")
                .font(.caption)
                .foregroundColor(.secondary)
            CountdownView(
                countdown: CountdownConfig(deadline: veryShortDeadline),
                currentTime: now
            )
        }

        VStack(alignment: .leading, spacing: 4) {
            Text("Overdue (-1h)")
                .font(.caption)
                .foregroundColor(.secondary)
            CountdownView(
                countdown: CountdownConfig(deadline: overdueDeadline),
                currentTime: now
            )
        }
    }
    .padding()
    .background(Color(.systemBackground))
}
