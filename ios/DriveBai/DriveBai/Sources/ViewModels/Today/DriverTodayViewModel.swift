import Foundation
import Combine

// MARK: - Driver Today ViewModel
// ============================================================
// WHERE TO TOGGLE hasListings / tasks TO DEMO STATES:
// ------------------------------------------------------------
// 1. Set `hasListings = false` to show empty listing state
// 2. Set `hasListings = true` to show active rental cards
// 3. Set `tasks = []` to show "All done, enjoy your day!" state
// 4. Set `tasks = OnboardingTask.mockDriverTasks()` to show task cards
// ============================================================

@MainActor
final class DriverTodayViewModel: ObservableObject {
    // MARK: - Published Properties

    /// Toggle this to switch between empty and filled listing states
    @Published var hasListings: Bool = false

    /// Current active rentals (only shown when hasListings is true)
    @Published var listings: [ListingSummary] = []

    /// Tasks to complete (empty array shows "All done" state)
    @Published var tasks: [OnboardingTask] = []

    /// Notifications for the bell icon
    @Published var notifications: [NotificationItem] = []

    /// Current time for countdown calculations
    @Published var currentTime: Date = Date()

    /// Unread actions dot on bell
    @Published var hasUnreadActions: Bool = false

    /// Number of unread notifications (combines mock notifs + real actions)
    var unreadNotificationCount: Int {
        let mockUnread = notifications.filter { !$0.isRead }.count
        return hasUnreadActions ? max(mockUnread, 1) : mockUnread
    }

    @Published var isLoadingActions = false
    @Published var actionsError: String?

    // MARK: - Private Properties

    private var timerCancellable: AnyCancellable?
    private var wsCancellables = Set<AnyCancellable>()
    private let apiClient: APIClientProtocol

    // MARK: - Initialization

    init(apiClient: APIClientProtocol = APIClient.shared) {
        self.apiClient = apiClient
        loadMockData()
        startCountdownTimer()
        subscribeToWebSocketEvents()
        Task { await fetchActions() }
    }

    deinit {
        timerCancellable?.cancel()
        wsCancellables.removeAll()
    }

    // MARK: - Data Loading

    func loadMockData() {
        if hasListings {
            listings = [
                ListingSummary(
                    id: UUID(),
                    title: "2023 Honda Accord Sport",
                    imageURL: nil,
                    weeklyPrice: 450,
                    rentedWeeks: 2,
                    totalEarned: 900,
                    status: .rented
                )
            ]
        } else {
            listings = []
        }
        notifications = NotificationItem.mockNotifications()
    }

    func fetchActions() async {
        isLoadingActions = true
        actionsError = nil
        do {
            let response = try await apiClient.fetchTodayActions()
            tasks = response.actions.map { $0.toOnboardingTask() }
            hasUnreadActions = response.hasUnreadActions
        } catch {
            #if DEBUG
            print("[DriverTodayVM] fetchActions error: \(error)")
            #endif
            actionsError = error.localizedDescription
        }
        isLoadingActions = false
    }

    /// Mark actions as seen (clears bell dot)
    func markActionsSeen() {
        guard hasUnreadActions else { return }
        hasUnreadActions = false
        Task {
            _ = try? await apiClient.markTodayActionsSeen()
        }
    }

    /// Refresh data (for pull-to-refresh)
    func refresh() async {
        loadMockData()
        await fetchActions()
    }

    // MARK: - Countdown Timer

    private func startCountdownTimer() {
        // Update every 60 seconds for countdown display (no seconds shown)
        timerCancellable = Timer.publish(every: 60, on: .main, in: .common)
            .autoconnect()
            .sink { [weak self] date in
                self?.currentTime = date
            }
    }

    // MARK: - WebSocket Events

    private func subscribeToWebSocketEvents() {
        let ws = WebSocketManager.shared
        ws.leaseRequestCreatedPublisher
            .merge(with: ws.leaseRequestUpdatedPublisher)
            .sink { [weak self] in
                Task { await self?.fetchActions() }
            }
            .store(in: &wsCancellables)
    }

    // MARK: - Task Actions

    /// Update selected option for a task
    func selectOption(for taskId: UUID, optionIndex: Int) {
        if let index = tasks.firstIndex(where: { $0.id == taskId }) {
            tasks[index].selectedOptionIndex = optionIndex
        }
    }

    /// Mark a task as completed (remove from list)
    func completeTask(_ taskId: UUID) {
        tasks.removeAll { $0.id == taskId }
    }

    /// Respond to a backend action (Accept/Decline)
    func respondToAction(task: OnboardingTask, action: String) {
        let requestId = task.id
        Task {
            do {
                if task.requestType == "lease_request" {
                    if action == "approve" {
                        _ = try await apiClient.acceptLeaseRequest(id: requestId)
                    } else {
                        _ = try await apiClient.declineLeaseRequest(id: requestId)
                    }
                } else if let chatId = task.chatId {
                    let request = RespondToRequestAPIRequest(action: action, note: nil)
                    _ = try await apiClient.respondToRequest(chatId: chatId, requestId: requestId, request: request)
                }
                tasks.removeAll { $0.id == requestId }
            } catch {
                #if DEBUG
                print("[DriverTodayVM] respondToAction error: \(error)")
                #endif
            }
        }
    }

    // MARK: - Listing Actions

    /// Toggle listing state for demo purposes
    func toggleListingState() {
        hasListings.toggle()
        loadMockData()
    }

    /// Clear all tasks to show "All done" state
    func clearAllTasks() {
        tasks = []
    }

    /// Reload tasks from backend
    func resetTasks() {
        Task { await fetchActions() }
    }

    // MARK: - Notification Actions

    func markNotificationAsRead(_ notificationId: UUID) {
        if let index = notifications.firstIndex(where: { $0.id == notificationId }) {
            let notification = notifications[index]
            notifications[index] = NotificationItem(
                id: notification.id,
                type: notification.type,
                title: notification.title,
                body: notification.body,
                date: notification.date,
                isRead: true
            )
        }
    }

    func markAllNotificationsAsRead() {
        notifications = notifications.map { notification in
            NotificationItem(
                id: notification.id,
                type: notification.type,
                title: notification.title,
                body: notification.body,
                date: notification.date,
                isRead: true
            )
        }
    }
}
