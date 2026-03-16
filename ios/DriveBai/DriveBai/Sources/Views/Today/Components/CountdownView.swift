import SwiftUI

/// Displays a countdown timer showing Days, Hours, Minutes only.
/// Designed to be visually calm and informational (not stressful).
/// Updates once per minute since seconds are not displayed.
struct CountdownView: View {
    let countdown: CountdownConfig
    let currentTime: Date

    private var remaining: (days: Int, hours: Int, minutes: Int) {
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
        CountdownRow(days: remaining.days, hours: remaining.hours, minutes: remaining.minutes)
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

    var body: some View {
        HStack(spacing: 4) {
            Text("Due in")
                .font(.system(size: 12))
                .foregroundColor(.secondary)

            CountdownChip(value: days, unit: "d")
            CountdownChip(value: hours, unit: "h")
            CountdownChip(value: minutes, unit: "m")
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
        .background(TodayLayout.tealAccentLight.opacity(0.5))
        .cornerRadius(8)
        .overlay(
            RoundedRectangle(cornerRadius: 8)
                .stroke(TodayLayout.tealAccent.opacity(0.2), lineWidth: 1)
        )
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
