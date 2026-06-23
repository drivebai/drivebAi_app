import Foundation

// MARK: - Listing Status

enum ListingStatus: String, CaseIterable {
    case active = "Active"
    case rented = "Rented"
    case pending = "Pending"
    case paused = "Paused"

    var color: String {
        switch self {
        case .active: return "green"
        case .rented: return "blue"
        case .pending: return "orange"
        case .paused: return "gray"
        }
    }
}

// MARK: - Listing Summary

struct ListingSummary: Identifiable {
    let id: UUID
    let title: String
    let imageURL: String?
    let weeklyPrice: Double
    let rentedWeeks: Int
    let totalEarned: Double
    let status: ListingStatus

    // For display
    var formattedWeeklyPrice: String {
        String(format: "$%.0f/week", weeklyPrice)
    }

    var formattedTotalEarned: String {
        String(format: "$%.2f", totalEarned)
    }
}

// MARK: - Task Priority

enum TaskPriority: Int, CaseIterable, Comparable {
    case high = 0
    case medium = 1
    case low = 2

    static func < (lhs: TaskPriority, rhs: TaskPriority) -> Bool {
        lhs.rawValue < rhs.rawValue
    }

    var indicatorColor: String {
        switch self {
        case .high: return "red"
        case .medium: return "orange"
        case .low: return "green"
        }
    }
}

// MARK: - Countdown Configuration

/// Configuration for countdown display on urgent tasks.
/// Only tasks that require interaction with the "other side" (driver ↔ owner) should have a countdown.
struct CountdownConfig {
    let deadline: Date

    /// Returns remaining time components (days, hours, minutes, seconds).
    /// Clamped at zero — never returns negatives so the UI doesn't have to.
    /// Callers that don't need second-precision can simply ignore the
    /// `seconds` field; the lone-second tick is what drives urgency on the
    /// post-payment pickup deadline card.
    func remainingTime(from now: Date = Date()) -> (days: Int, hours: Int, minutes: Int, seconds: Int) {
        let interval = deadline.timeIntervalSince(now)

        // Clamp at zero if overdue
        guard interval > 0 else {
            return (0, 0, 0, 0)
        }

        let totalSeconds = Int(interval)
        let days = totalSeconds / 86400
        let hours = (totalSeconds % 86400) / 3600
        let minutes = (totalSeconds % 3600) / 60
        let seconds = totalSeconds % 60

        return (days, hours, minutes, seconds)
    }

    var isOverdue: Bool {
        deadline < Date()
    }
}

// MARK: - Onboarding Task

struct OnboardingTask: Identifiable {
    let id: UUID
    let title: String
    let description: String
    let dueDate: Date
    let requestedBy: String
    let priority: TaskPriority
    let options: [String]
    var selectedOptionIndex: Int?

    /// Optional countdown configuration. Only set for urgent tasks requiring interaction.
    /// When nil, no countdown is displayed on the task card.
    let countdown: CountdownConfig?

    // Backend action fields (nil for local/mock tasks)
    let chatId: UUID?
    let carTitle: String?
    let requestType: String?
    let counterpartyId: UUID?
    let counterpartyName: String?

    /// Whether this task originated from the backend actions endpoint
    var isBackendAction: Bool { chatId != nil }

    /// Returns a copy of this task whose `options` array contains a single
    /// label. Used by surfaces that want to render the card with one CTA
    /// instead of the backend-provided two-option (Accept/Decline) shape —
    /// e.g. the owner Today card for lease requests, which collapses to a
    /// single "Go to requests" button so accept/decline lives in one place.
    func withSingleOption(_ label: String) -> OnboardingTask {
        OnboardingTask(
            id: id, title: title, description: description, dueDate: dueDate,
            requestedBy: requestedBy, priority: priority,
            options: [label],
            selectedOptionIndex: nil,
            countdown: countdown,
            chatId: chatId, carTitle: carTitle, requestType: requestType,
            counterpartyId: counterpartyId, counterpartyName: counterpartyName
        )
    }

    init(
        id: UUID, title: String, description: String, dueDate: Date,
        requestedBy: String, priority: TaskPriority, options: [String],
        selectedOptionIndex: Int? = nil, countdown: CountdownConfig?,
        chatId: UUID? = nil, carTitle: String? = nil, requestType: String? = nil,
        counterpartyId: UUID? = nil, counterpartyName: String? = nil
    ) {
        self.id = id
        self.title = title
        self.description = description
        self.dueDate = dueDate
        self.requestedBy = requestedBy
        self.priority = priority
        self.options = options
        self.selectedOptionIndex = selectedOptionIndex
        self.countdown = countdown
        self.chatId = chatId
        self.carTitle = carTitle
        self.requestType = requestType
        self.counterpartyId = counterpartyId
        self.counterpartyName = counterpartyName
    }

    /// Helper to check if countdown should be shown
    var shouldShowCountdown: Bool {
        countdown != nil
    }

    // Computed properties for display
    var formattedDate: String {
        let formatter = DateFormatter()
        formatter.dateFormat = "MMM dd, yyyy"
        return formatter.string(from: dueDate)
    }

    var formattedTime: String {
        let formatter = DateFormatter()
        formatter.dateFormat = "HH:mm"
        return formatter.string(from: dueDate)
    }

    /// Combined date and time format matching Figma: "Jun 06, 2025 10:34"
    var formattedDateTime: String {
        let formatter = DateFormatter()
        formatter.dateFormat = "MMM dd, yyyy HH:mm"
        return formatter.string(from: dueDate)
    }

    /// Returns remaining time components (days, hours, minutes) - deprecated, use countdown.remainingTime instead
    func remainingTime(from now: Date = Date()) -> (days: Int, hours: Int, minutes: Int, seconds: Int) {
        let interval = dueDate.timeIntervalSince(now)

        // Clamp at zero if overdue
        guard interval > 0 else {
            return (0, 0, 0, 0)
        }

        let totalSeconds = Int(interval)
        let days = totalSeconds / 86400
        let hours = (totalSeconds % 86400) / 3600
        let minutes = (totalSeconds % 3600) / 60
        let seconds = totalSeconds % 60

        return (days, hours, minutes, seconds)
    }

    var isOverdue: Bool {
        dueDate < Date()
    }
}

// MARK: - Notification Type

enum NotificationType: String, CaseIterable {
    case booking = "booking"
    case leaseRequest = "lease_request"
    case message = "message"
    case payment = "payment"
    case reminder = "reminder"
    case system = "system"

    var iconName: String {
        switch self {
        case .booking: return "calendar"
        case .leaseRequest: return "key.fill"
        case .message: return "message"
        case .payment: return "creditcard"
        case .reminder: return "bell"
        case .system: return "gear"
        }
    }
}

// MARK: - Notification Item

struct NotificationItem: Identifiable {
    let id: UUID
    let type: NotificationType
    let title: String
    let body: String
    let date: Date
    let isRead: Bool
    let relatedChatId: UUID?

    var formattedDate: String {
        let formatter = RelativeDateTimeFormatter()
        formatter.unitsStyle = .abbreviated
        return formatter.localizedString(for: date, relativeTo: Date())
    }
}

// MARK: - Mock Data Generators

extension ListingSummary {
    static func mockListings() -> [ListingSummary] {
        let listing1 = ListingSummary(
            id: UUID(),
            title: "2022 BMW X6 M Competition",
            imageURL: nil,
            weeklyPrice: 850,
            rentedWeeks: 12,
            totalEarned: 10200,
            status: .rented
        )

        let listing2 = ListingSummary(
            id: UUID(),
            title: "2023 Tesla Model Y Long Range",
            imageURL: nil,
            weeklyPrice: 650,
            rentedWeeks: 8,
            totalEarned: 5200,
            status: .active
        )

        let listing3 = ListingSummary(
            id: UUID(),
            title: "2021 Mercedes-Benz GLE 450",
            imageURL: nil,
            weeklyPrice: 750,
            rentedWeeks: 5,
            totalEarned: 3750,
            status: .pending
        )

        let listings: [ListingSummary] = [listing1, listing2, listing3]
        return listings
    }
}

extension OnboardingTask {
    /// Mock tasks for Car Owners
    /// Includes: 2 urgent tasks (with countdown) + 2 non-urgent tasks (no countdown)
    static func mockTasks() -> [OnboardingTask] {
        let now = Date()

        // URGENT: Approve lease request - requires driver ↔ owner interaction
        let task1 = OnboardingTask(
            id: UUID(),
            title: "Approve lease request",
            description: "John D. wants to rent your BMW X6 for 2 weeks. Review and approve the request.",
            dueDate: now.addingTimeInterval(3600 * 12), // 12 hours from now
            requestedBy: "John D.",
            priority: .high,
            options: ["Approve", "Decline", "Message"],
            selectedOptionIndex: nil,
            countdown: CountdownConfig(deadline: now.addingTimeInterval(3600 * 12))
        )

        // URGENT: Meet driver for handover - requires driver ↔ owner interaction
        let task2 = OnboardingTask(
            id: UUID(),
            title: "Meet driver for handover",
            description: "Sarah M. is scheduled to pick up your Tesla Model Y. Confirm the handover location.",
            dueDate: now.addingTimeInterval(86400 * 2), // 2 days from now
            requestedBy: "Sarah M.",
            priority: .high,
            options: ["Confirm", "Reschedule", "Cancel"],
            selectedOptionIndex: nil,
            countdown: CountdownConfig(deadline: now.addingTimeInterval(86400 * 2))
        )

        // NON-URGENT: Add vehicle details - no countdown (self-paced onboarding)
        let task3 = OnboardingTask(
            id: UUID(),
            title: "Add vehicle details",
            description: "Complete your vehicle listing with specifications, features, and pricing to attract more drivers.",
            dueDate: now.addingTimeInterval(86400 * 7), // 7 days from now
            requestedBy: "DriveBai Team",
            priority: .medium,
            options: ["Complete Now", "Remind Later", "Skip"],
            selectedOptionIndex: nil,
            countdown: nil // No countdown for non-urgent tasks
        )

        // NON-URGENT: Upload more car photos - no countdown (self-paced onboarding)
        let task4 = OnboardingTask(
            id: UUID(),
            title: "Upload more car photos",
            description: "Listings with 5+ photos get 40% more views. Add interior and detail shots.",
            dueDate: now.addingTimeInterval(86400 * 14), // 14 days from now
            requestedBy: "DriveBai Team",
            priority: .low,
            options: ["Upload Now", "Later", "Learn More"],
            selectedOptionIndex: nil,
            countdown: nil // No countdown for non-urgent tasks
        )

        var tasks: [OnboardingTask] = [task1, task2, task3, task4]
        tasks.sort { $0.priority < $1.priority }
        return tasks
    }

    /// Mock tasks for Drivers
    /// Includes: 2 urgent tasks (with countdown) + 2 non-urgent tasks (no countdown)
    static func mockDriverTasks() -> [OnboardingTask] {
        let now = Date()

        // URGENT: Pick up the car - requires driver ↔ owner interaction
        let task1 = OnboardingTask(
            id: UUID(),
            title: "Pick up the car",
            description: "Meet Mike at 123 Main St to pick up the 2023 Honda Accord. Don't forget to inspect the vehicle.",
            dueDate: now.addingTimeInterval(3600 * 5), // 5 hours from now
            requestedBy: "Mike R.",
            priority: .high,
            options: ["Get Directions", "Message Owner", "Reschedule"],
            selectedOptionIndex: nil,
            countdown: CountdownConfig(deadline: now.addingTimeInterval(3600 * 5))
        )

        // URGENT: Confirm handover with owner - requires driver ↔ owner interaction
        let task2 = OnboardingTask(
            id: UUID(),
            title: "Confirm handover with owner",
            description: "Lisa wants to confirm the handover time for the Mercedes GLE. Please respond to finalize.",
            dueDate: now.addingTimeInterval(86400 * 1), // 1 day from now
            requestedBy: "Lisa K.",
            priority: .high,
            options: ["Confirm", "Propose New Time", "Cancel"],
            selectedOptionIndex: nil,
            countdown: CountdownConfig(deadline: now.addingTimeInterval(86400 * 1))
        )

        // NON-URGENT: Complete driving history - no countdown (self-paced onboarding)
        let task3 = OnboardingTask(
            id: UUID(),
            title: "Complete driving history",
            description: "Provide your driving history to get access to premium vehicles and better rates.",
            dueDate: now.addingTimeInterval(86400 * 5), // 5 days from now
            requestedBy: "Verification Team",
            priority: .medium,
            options: ["Complete", "Remind Me", "Skip"],
            selectedOptionIndex: nil,
            countdown: nil // No countdown for non-urgent tasks
        )

        // NON-URGENT: Upload driver license photo - no countdown (self-paced onboarding)
        let task4 = OnboardingTask(
            id: UUID(),
            title: "Upload driver license photo",
            description: "Upload a clear photo of your driver's license to verify your identity.",
            dueDate: now.addingTimeInterval(86400 * 7), // 7 days from now
            requestedBy: "DriveBai Team",
            priority: .low,
            options: ["Upload Now", "Later", "Help"],
            selectedOptionIndex: nil,
            countdown: nil // No countdown for non-urgent tasks
        )

        var tasks: [OnboardingTask] = [task1, task2, task3, task4]
        tasks.sort { $0.priority < $1.priority }
        return tasks
    }

    /// Mock task with overdue countdown for testing
    static func mockOverdueTask() -> OnboardingTask {
        let now = Date()
        return OnboardingTask(
            id: UUID(),
            title: "Overdue: Return the car",
            description: "The rental period has ended. Please return the vehicle to the owner immediately.",
            dueDate: now.addingTimeInterval(-3600 * 2), // 2 hours ago
            requestedBy: "Owner",
            priority: .high,
            options: ["Contact Owner", "Get Directions", "Help"],
            selectedOptionIndex: nil,
            countdown: CountdownConfig(deadline: now.addingTimeInterval(-3600 * 2))
        )
    }
}

extension NotificationItem {
    static func mockNotifications() -> [NotificationItem] {
        let now = Date()

        let notif1 = NotificationItem(
            id: UUID(), type: .booking,
            title: "New booking request",
            body: "John D. wants to book your BMW X6 for 2 weeks starting March 15.",
            date: now.addingTimeInterval(-3600), isRead: false, relatedChatId: nil
        )
        let notif2 = NotificationItem(
            id: UUID(), type: .message,
            title: "New message",
            body: "Sarah M. sent you a message about the Tesla Model Y.",
            date: now.addingTimeInterval(-7200), isRead: false, relatedChatId: nil
        )
        let notif3 = NotificationItem(
            id: UUID(), type: .payment,
            title: "Payment received",
            body: "You received $850 for your BMW X6 rental.",
            date: now.addingTimeInterval(-86400), isRead: true, relatedChatId: nil
        )
        let notif4 = NotificationItem(
            id: UUID(), type: .reminder,
            title: "Rental ending soon",
            body: "The current rental of your Mercedes GLE ends in 3 days.",
            date: now.addingTimeInterval(-86400 * 2), isRead: true, relatedChatId: nil
        )
        let notif5 = NotificationItem(
            id: UUID(), type: .system,
            title: "Profile update required",
            body: "Please update your profile photo to improve trust with renters.",
            date: now.addingTimeInterval(-86400 * 3), isRead: true, relatedChatId: nil
        )

        let notifications: [NotificationItem] = [notif1, notif2, notif3, notif4, notif5]
        return notifications
    }
}
