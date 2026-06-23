import SwiftUI

// MARK: - Task Action Model

/// Represents a task action with its display properties
struct TaskAction: Identifiable {
    let id = UUID()
    let title: String
    let style: ActionStyle
    let handler: () -> Void

    enum ActionStyle {
        case primary
        case secondary
        case overflow
    }
}

// MARK: - Due Badge Model

/// Urgency level determines badge styling intensity
enum DueUrgency {
    case normal   // > 24h away
    case soon     // 2h–24h away
    case urgent   // < 2h away
    case overdue  // past deadline
}

/// Model for the due badge display
struct DueBadgeModel {
    let text: String
    let urgency: DueUrgency

    var isOverdue: Bool { urgency == .overdue }

    /// Creates a due badge from countdown config.
    ///
    /// Seconds appear once we're inside the final hour — that's where the
    /// pickup-deadline UI lives (default 2-hour deadline ticks down through
    /// "1h 42m" then into the seconds-visible zone) and where the urgency
    /// is high enough to justify the per-second redraw.
    static func from(countdown: CountdownConfig?, currentTime: Date) -> DueBadgeModel? {
        guard let countdown = countdown else { return nil }

        let remaining = countdown.remainingTime(from: currentTime)
        let isOverdue = countdown.isOverdue

        if isOverdue {
            let overdueInterval = currentTime.timeIntervalSince(countdown.deadline)
            let overdueMinutes = Int(overdueInterval / 60)
            let overdueHours = overdueMinutes / 60

            if overdueHours >= 1 {
                return DueBadgeModel(text: "Overdue by \(overdueHours)h", urgency: .overdue)
            } else {
                return DueBadgeModel(text: "Overdue by \(overdueMinutes)m", urgency: .overdue)
            }
        }

        let days = remaining.days
        let hours = remaining.hours
        let minutes = remaining.minutes
        let seconds = remaining.seconds
        let totalHours = days * 24 + hours

        // Smart formatting with urgency tiers
        if days > 1 {
            return DueBadgeModel(text: "Due in \(days)d", urgency: .normal)
        } else if days == 1 {
            return DueBadgeModel(text: "Due tomorrow", urgency: .normal)
        } else if totalHours >= 2 {
            return DueBadgeModel(text: "Due in \(hours)h \(minutes)m", urgency: .soon)
        } else if hours >= 1 {
            // Last hour and a bit: show seconds so the badge counts down
            // visibly. Urgent tier already drives the warning color.
            return DueBadgeModel(text: "Due in \(hours)h \(minutes)m \(seconds)s", urgency: .urgent)
        } else if minutes >= 1 {
            return DueBadgeModel(text: "Due in \(minutes)m \(seconds)s", urgency: .urgent)
        } else {
            return DueBadgeModel(text: "Due in \(seconds)s", urgency: .urgent)
        }
    }
}

// MARK: - Task Card (Main Component)

/// Modern task card with calmer hierarchy
/// - Header: Title + optional due badge
/// - Meta: Requester + date on one line
/// - Description: 2-line truncated
/// - Actions: Primary + Secondary + Overflow menu
struct TaskCard: View {
    let task: OnboardingTask
    let currentTime: Date
    var onOptionSelect: ((Int) -> Void)?

    init(task: OnboardingTask, currentTime: Date, onOpenTap: (() -> Void)? = nil, onOptionSelect: ((Int) -> Void)? = nil) {
        self.task = task
        self.currentTime = currentTime
        self.onOptionSelect = onOptionSelect
    }

    private var dueBadge: DueBadgeModel? {
        DueBadgeModel.from(countdown: task.countdown, currentTime: currentTime)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            TaskCardHeader(title: task.title, badge: dueBadge)
            TaskCardMetaRow(requestedBy: task.requestedBy, date: task.formattedDateTime)
            TaskCardDescription(text: task.description)
            TaskCardActionsRow(options: task.options, onSelect: onOptionSelect)
        }
        .padding(16)
        .background(Color(.systemBackground))
        .cornerRadius(12)
        .shadow(color: Color.black.opacity(0.04), radius: 8, x: 0, y: 2)
        .overlay(
            RoundedRectangle(cornerRadius: 12)
                .stroke(Color(.systemGray5), lineWidth: 1)
        )
    }
}

// MARK: - Task Card Header

private struct TaskCardHeader: View {
    let title: String
    let badge: DueBadgeModel?

    var body: some View {
        HStack(alignment: .center, spacing: 8) {
            Text(title)
                .font(.system(size: 16, weight: .semibold))
                .foregroundColor(.primary)
                .lineLimit(1)

            Spacer()

            if let badge = badge {
                DueBadge(model: badge)
            }
        }
    }
}

// MARK: - Task Card Meta Row

private struct TaskCardMetaRow: View {
    let requestedBy: String
    let date: String

    var body: some View {
        Text("\(requestedBy) • \(date)")
            .font(.system(size: 12))
            .foregroundColor(.secondary)
            .lineLimit(1)
    }
}

// MARK: - Task Card Description

private struct TaskCardDescription: View {
    let text: String

    var body: some View {
        Text(text)
            .font(.system(size: 14))
            .foregroundColor(.secondary)
            .lineLimit(2)
            .fixedSize(horizontal: false, vertical: true)
    }
}

// MARK: - Task Card Actions Row

private struct TaskCardActionsRow: View {
    let options: [String]
    var onSelect: ((Int) -> Void)?

    private var primaryTitle: String {
        options.first ?? "Action"
    }

    private var secondaryTitle: String? {
        options.count > 1 ? options[1] : nil
    }

    private var overflowOptions: [(index: Int, title: String)] {
        guard options.count > 2 else { return [] }
        return options.dropFirst(2).enumerated().map { (index: $0.offset + 2, title: $0.element) }
    }

    var body: some View {
        HStack(spacing: 10) {
            // Primary action button
            PrimaryActionButton(title: primaryTitle) {
                onSelect?(0)
            }

            // Secondary action button (if exists)
            if let secondary = secondaryTitle {
                SecondaryActionButton(title: secondary) {
                    onSelect?(1)
                }
            }

            // Overflow menu (if more than 2 options)
            if !overflowOptions.isEmpty {
                OverflowMenuButton(options: overflowOptions, onSelect: onSelect)
            }
        }
        .padding(.top, 4)
    }
}

// MARK: - Due Badge

private struct DueBadge: View {
    let model: DueBadgeModel

    private var backgroundColor: Color {
        switch model.urgency {
        case .normal:
            return Color(.systemGray6)
        case .soon:
            return Color.orange.opacity(0.10)
        case .urgent:
            return Color.driveBaiSecondary.opacity(0.12)
        case .overdue:
            return Color.driveBaiSecondary.opacity(0.14)
        }
    }

    private var textColor: Color {
        switch model.urgency {
        case .normal:
            return .secondary
        case .soon:
            return .orange
        case .urgent, .overdue:
            return .driveBaiSecondary
        }
    }

    private var showIcon: Bool {
        model.urgency == .urgent || model.urgency == .overdue
    }

    private var fontSize: CGFloat {
        switch model.urgency {
        case .normal, .soon:
            return 11
        case .urgent, .overdue:
            return 12
        }
    }

    var body: some View {
        HStack(spacing: 3) {
            if showIcon {
                Image(systemName: model.urgency == .overdue ? "exclamationmark.circle.fill" : "clock.fill")
                    .font(.system(size: fontSize - 1, weight: .semibold))
            }
            Text(model.text)
                .font(.system(size: fontSize, weight: .semibold))
        }
        .foregroundColor(textColor)
        .padding(.horizontal, showIcon ? 10 : 8)
        .padding(.vertical, showIcon ? 5 : 4)
        .background(backgroundColor)
        .clipShape(Capsule())
    }
}

// MARK: - Primary Action Button

private struct PrimaryActionButton: View {
    let title: String
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Text(title)
                .font(.system(size: 14, weight: .semibold))
                .foregroundColor(.white)
                .lineLimit(1)
                .frame(maxWidth: .infinity)
                .frame(height: 40)
                .background(TodayLayout.tealAccent)
                .cornerRadius(8)
        }
        .buttonStyle(PlainButtonStyle())
    }
}

// MARK: - Secondary Action Button

private struct SecondaryActionButton: View {
    let title: String
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Text(title)
                .font(.system(size: 14, weight: .medium))
                .foregroundColor(.primary)
                .lineLimit(1)
                .frame(maxWidth: .infinity)
                .frame(height: 40)
                .background(Color(.systemGray6))
                .cornerRadius(8)
        }
        .buttonStyle(PlainButtonStyle())
    }
}

// MARK: - Overflow Menu Button

private struct OverflowMenuButton: View {
    let options: [(index: Int, title: String)]
    var onSelect: ((Int) -> Void)?

    var body: some View {
        Menu {
            ForEach(options, id: \.index) { option in
                Button(option.title) {
                    onSelect?(option.index)
                }
            }
        } label: {
            Image(systemName: "ellipsis")
                .font(.system(size: 16, weight: .medium))
                .foregroundColor(.secondary)
                .frame(width: 40, height: 40)
                .background(Color(.systemGray6))
                .cornerRadius(8)
        }
    }
}

// MARK: - All Done View

/// Empty state view when all tasks are completed
struct AllDoneView: View {
    var body: some View {
        VStack(spacing: 12) {
            Image(systemName: "checkmark.circle.fill")
                .font(.system(size: 44))
                .foregroundColor(TodayLayout.tealAccent)

            Text("All done, enjoy your day!")
                .font(.subheadline)
                .fontWeight(.medium)
                .foregroundColor(.secondary)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 40)
        .background(TodayLayout.tealAccentLight)
        .cornerRadius(TodayLayout.cardCornerRadius)
        .overlay(
            RoundedRectangle(cornerRadius: TodayLayout.cardCornerRadius)
                .stroke(TodayLayout.cardBorderColor, lineWidth: TodayLayout.cardBorderWidth)
        )
    }
}

// MARK: - Previews

#Preview("Task Cards - Various States") {
    let now = Date()

    // Create test tasks with different countdown states
    let urgentSoonTask = OnboardingTask(
        id: UUID(),
        title: "Pick up the car",
        description: "Meet Mike at 123 Main St to pick up the 2023 Honda Accord. Don't forget to inspect the vehicle.",
        dueDate: now.addingTimeInterval(3600 * 5),
        requestedBy: "Mike R.",
        priority: .high,
        options: ["Get Directions", "Message Owner", "Reschedule", "Cancel"],
        selectedOptionIndex: nil,
        countdown: CountdownConfig(deadline: now.addingTimeInterval(3600 * 5))
    )

    let urgentTomorrowTask = OnboardingTask(
        id: UUID(),
        title: "Confirm handover with owner",
        description: "Lisa wants to confirm the handover time for the Mercedes GLE.",
        dueDate: now.addingTimeInterval(86400),
        requestedBy: "Lisa K.",
        priority: .high,
        options: ["Confirm", "Propose New Time", "Cancel"],
        selectedOptionIndex: nil,
        countdown: CountdownConfig(deadline: now.addingTimeInterval(86400))
    )

    let nonUrgentTask = OnboardingTask(
        id: UUID(),
        title: "Complete driving history",
        description: "Provide your driving history to get access to premium vehicles and better rates.",
        dueDate: now.addingTimeInterval(86400 * 5),
        requestedBy: "Verification Team",
        priority: .medium,
        options: ["Complete", "Remind Me", "Skip"],
        selectedOptionIndex: nil,
        countdown: nil
    )

    let overdueTask = OnboardingTask(
        id: UUID(),
        title: "Return the car",
        description: "The rental period has ended. Please return the vehicle to the owner.",
        dueDate: now.addingTimeInterval(-7200),
        requestedBy: "Owner",
        priority: .high,
        options: ["Contact Owner", "Get Directions"],
        selectedOptionIndex: nil,
        countdown: CountdownConfig(deadline: now.addingTimeInterval(-7200))
    )

    return ScrollView {
        VStack(spacing: 16) {
            Text("Urgent - Due in 5h")
                .font(.caption)
                .foregroundColor(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)

            TaskCard(task: urgentSoonTask, currentTime: now)

            Text("Urgent - Due tomorrow")
                .font(.caption)
                .foregroundColor(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)

            TaskCard(task: urgentTomorrowTask, currentTime: now)

            Text("Non-urgent - No countdown")
                .font(.caption)
                .foregroundColor(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)

            TaskCard(task: nonUrgentTask, currentTime: now)

            Text("Overdue")
                .font(.caption)
                .foregroundColor(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)

            TaskCard(task: overdueTask, currentTime: now)

            Text("All Done State")
                .font(.caption)
                .foregroundColor(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)

            AllDoneView()
        }
        .padding()
    }
    .background(Color(.systemGroupedBackground))
}
