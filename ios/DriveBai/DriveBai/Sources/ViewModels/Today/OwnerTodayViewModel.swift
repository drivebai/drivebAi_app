import Foundation
import Combine

// MARK: - Owner Today ViewModel
// ============================================================
// WHERE TO TOGGLE hasListings / tasks TO DEMO STATES:
// ------------------------------------------------------------
// 1. Set `hasListings = false` to show empty listing state
// 2. Set `hasListings = true` to show listing cards
// 3. Set `tasks = []` to show "All done, enjoy your day!" state
// 4. Set `tasks = OnboardingTask.mockTasks()` to show task cards
// ============================================================

@MainActor
final class OwnerTodayViewModel: ObservableObject {
    // MARK: - Published Properties

    /// Toggle this to switch between empty and filled listing states
    @Published var hasListings: Bool = true

    /// Current listings (only shown when hasListings is true)
    @Published var listings: [ListingSummary] = []

    /// Tasks to complete (empty array shows "All done" state)
    @Published var tasks: [OnboardingTask] = []

    /// Notifications for the bell icon (real data from backend)
    @Published var notifications: [NotificationItem] = []

    /// Unread notification count returned by the API
    @Published var unreadNotificationCount: Int = 0

    /// Current time for countdown calculations
    @Published var currentTime: Date = Date()

    /// Unread actions dot on bell (from today/actions endpoint)
    @Published var hasUnreadActions: Bool = false

    @Published var isLoadingActions = false
    @Published var actionsError: String?

    /// Active key-handover tasks (pending / awaiting driver) for this user
    @Published var keyHandovers: [KeyHandover] = []

    /// ID of the handover currently being confirmed (drives the card's busy state)
    @Published var submittingHandoverId: UUID?

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
        Task {
            await fetchActions()
            await fetchNotifications()
            await fetchKeyHandovers()
        }
    }

    deinit {
        timerCancellable?.cancel()
        wsCancellables.removeAll()
    }

    // MARK: - Data Loading

    func loadMockData() {
        if hasListings {
            listings = ListingSummary.mockListings()
        } else {
            listings = []
        }
    }

    func fetchNotifications() async {
        do {
            let response = try await apiClient.fetchNotifications()
            notifications = response.notifications.map { $0.toNotificationItem() }
            unreadNotificationCount = response.unreadCount
        } catch {
            #if DEBUG
            print("[OwnerTodayVM] fetchNotifications error: \(error)")
            #endif
        }
    }

    func markNotificationRead(_ id: UUID) async {
        _ = try? await apiClient.markNotificationRead(id: id)
        if let idx = notifications.firstIndex(where: { $0.id == id }), !notifications[idx].isRead {
            let n = notifications[idx]
            notifications[idx] = NotificationItem(id: n.id, type: n.type, title: n.title,
                body: n.body, date: n.date, isRead: true, relatedChatId: n.relatedChatId)
            unreadNotificationCount = max(0, unreadNotificationCount - 1)
        }
    }

    func markAllNotificationsRead() async {
        _ = try? await apiClient.markAllNotificationsRead()
        notifications = notifications.map { n in
            NotificationItem(id: n.id, type: n.type, title: n.title,
                body: n.body, date: n.date, isRead: true, relatedChatId: n.relatedChatId)
        }
        unreadNotificationCount = 0
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
            print("[OwnerTodayVM] fetchActions error: \(error)")
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
        await fetchNotifications()
        await fetchKeyHandovers()
    }

    // MARK: - Key Handovers

    func fetchKeyHandovers() async {
        do {
            let response = try await apiClient.fetchKeyHandoversToday()
            keyHandovers = response.keyHandovers.map { $0.toDomain() }
        } catch {
            #if DEBUG
            print("[OwnerTodayVM] fetchKeyHandovers error: \(error)")
            #endif
        }
    }

    /// Confirm the handover for the current viewer (owner → handed over, driver → received).
    func confirmHandover(_ handover: KeyHandover) {
        guard submittingHandoverId == nil else { return }
        submittingHandoverId = handover.id
        Task {
            defer { submittingHandoverId = nil }
            do {
                if handover.viewerRole == .owner {
                    _ = try await apiClient.ownerConfirmKeyHandover(id: handover.id)
                } else {
                    _ = try await apiClient.driverConfirmKeyHandover(id: handover.id)
                }
            } catch {
                #if DEBUG
                print("[OwnerTodayVM] confirmHandover error: \(error)")
                #endif
            }
            // Re-sync with the server (handles success, expiry, and races uniformly).
            await fetchKeyHandovers()
        }
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

        ws.notificationCreatedPublisher
            .receive(on: RunLoop.main)
            .sink { [weak self] unreadCount in
                guard let self else { return }
                self.unreadNotificationCount = unreadCount
                Task { await self.fetchNotifications() }
            }
            .store(in: &wsCancellables)

        ws.keyHandoverUpdatedPublisher
            .sink { [weak self] in
                Task { await self?.fetchKeyHandovers() }
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

    /// Respond to a backend action (Approve/Decline)
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
                print("[OwnerTodayVM] respondToAction error: \(error)")
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

}
