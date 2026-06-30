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

    /// Active vehicle-return rows for this owner (driver-initiated waiting
    /// confirm / disputed / freshly-completed).
    @Published var vehicleReturns: [VehicleReturn] = []

    /// ID of the return currently being acted on (drives the card's busy state).
    @Published var submittingReturnId: UUID?

    /// Locally-dismissed return ids; same client-only pattern as the
    /// driver-side VM.
    private var dismissedReturnIds: Set<UUID> = []

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
            await fetchVehicleReturns()
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
        await fetchVehicleReturns()
    }

    // MARK: - Key Handovers

    func fetchKeyHandovers() async {
        do {
            let response = try await apiClient.fetchKeyHandoversToday()
            keyHandovers = response.keyHandovers.map { $0.toDomain() }
            // Mirror the driver-side Live Activity reconciler so owners
            // also get a Lock Screen / Dynamic Island countdown for
            // "Driver picking up your car". Same single-choke-point pattern.
            syncLiveActivities()
        } catch {
            #if DEBUG
            print("[OwnerTodayVM] fetchKeyHandovers error: \(error)")
            #endif
        }
    }

    /// Updates the iOS Live Activity tracker to match `keyHandovers`. Idempotent.
    private func syncLiveActivities() {
        guard #available(iOS 16.1, *) else { return }
        let active = keyHandovers.filter { $0.isAwaitingPickupConfirmation }
        for handover in active {
            PickupLiveActivityManager.shared.startOrUpdate(for: handover)
        }
        let activeLeaseIds = Set(active.map { $0.leaseRequestId })
        PickupLiveActivityManager.shared.reconcile(activeLeaseIds: activeLeaseIds)
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

    /// Owner adds `minutes` to the pickup deadline backing this handover.
    /// Backend guards against double-extend / cap / race-with-scanner; we just
    /// fire-and-refetch so the timer + button-disabled state reflect truth.
    func extendPickup(handover: KeyHandover, minutes: Int) {
        Task {
            do {
                _ = try await apiClient.extendPickupDeadline(leaseRequestId: handover.leaseRequestId, minutes: minutes)
            } catch {
                #if DEBUG
                print("[OwnerTodayVM] extendPickup error: \(error)")
                #endif
            }
            await fetchKeyHandovers()
        }
    }

    /// "Got it" on a terminal refunded card. Optimistically removes the
    /// card; on backend failure we refetch so the row reappears.
    func dismissHandover(_ handover: KeyHandover) {
        let originalIndex = keyHandovers.firstIndex(where: { $0.id == handover.id })
        keyHandovers.removeAll { $0.id == handover.id }

        Task {
            do {
                _ = try await apiClient.dismissKeyHandover(id: handover.id)
            } catch {
                #if DEBUG
                print("[OwnerTodayVM] dismissHandover error: \(error)")
                #endif
                // Restore at the original slot so card order doesn't jump.
                if let i = originalIndex,
                   !self.keyHandovers.contains(where: { $0.id == handover.id }) {
                    self.keyHandovers.insert(handover, at: min(i, self.keyHandovers.count))
                }
                await fetchKeyHandovers()
            }
        }
    }

    // MARK: - Vehicle Returns

    /// Pull active vehicle-return rows for the owner. Failure-silent — the
    /// list is purely additive on top of the Today tab and shouldn't paint
    /// errors over the rest of the screen.
    func fetchVehicleReturns() async {
        do {
            let response = try await apiClient.fetchVehicleReturnsToday()
            let incoming = response.vehicleReturns.map { $0.toDomain() }
            vehicleReturns = incoming.filter { !dismissedReturnIds.contains($0.id) }
        } catch {
            #if DEBUG
            print("[OwnerTodayVM] fetchVehicleReturns error: \(error)")
            #endif
        }
    }

    /// Single entry-point the card's "primary" CTA hits — dispatches by
    /// viewerRole so a user who is the owner on one lease AND the driver
    /// on another can't accidentally invoke the wrong API (e.g. tapping
    /// "Undo return" on a driver-perspective card from the Owner tab).
    func actOnVehicleReturn(_ vReturn: VehicleReturn) {
        switch vReturn.viewerRole {
        case .driver: cancelVehicleReturn(vReturn)
        case .owner:  confirmVehicleReturn(vReturn)
        }
    }

    /// Driver-perspective undo — kept on the Owner VM too because the
    /// underlying `vehicleReturns` array is the union of both roles for
    /// this user. See `actOnVehicleReturn` for dispatch.
    func cancelVehicleReturn(_ vReturn: VehicleReturn) {
        guard submittingReturnId == nil else { return }
        submittingReturnId = vReturn.id
        Task {
            defer { submittingReturnId = nil }
            do {
                _ = try await apiClient.cancelVehicleReturn(returnId: vReturn.id)
            } catch {
                #if DEBUG
                print("[OwnerTodayVM] cancelVehicleReturn error: \(error)")
                #endif
            }
            await fetchVehicleReturns()
        }
    }

    /// Owner confirms receipt; backend immediately moves to the refund
    /// pipeline. We just refetch to pick up the new state.
    func confirmVehicleReturn(_ vReturn: VehicleReturn) {
        guard submittingReturnId == nil else { return }
        submittingReturnId = vReturn.id
        Task {
            defer { submittingReturnId = nil }
            do {
                _ = try await apiClient.confirmVehicleReturn(returnId: vReturn.id)
            } catch {
                #if DEBUG
                print("[OwnerTodayVM] confirmVehicleReturn error: \(error)")
                #endif
            }
            await fetchVehicleReturns()
        }
    }

    /// Owner files a dispute. `reason` is the 5-500 char string the
    /// support team will read. Returns the failure message on error so
    /// the dispute sheet can keep itself up with a spinner during the
    /// round-trip and only auto-dismiss after the server accepts.
    /// Returns nil on success.
    func disputeVehicleReturn(_ vReturn: VehicleReturn, reason: String) async -> String? {
        guard submittingReturnId == nil else { return nil }
        submittingReturnId = vReturn.id
        defer { submittingReturnId = nil }
        do {
            _ = try await apiClient.disputeVehicleReturn(returnId: vReturn.id, reason: reason)
            await fetchVehicleReturns()
            return nil
        } catch {
            #if DEBUG
            print("[OwnerTodayVM] disputeVehicleReturn error: \(error)")
            #endif
            await fetchVehicleReturns()
            return (error as? APIError)?.errorDescription ?? "Couldn't submit dispute. Please try again."
        }
    }

    /// Local-only dismiss on a terminal return card.
    func dismissVehicleReturn(_ vReturn: VehicleReturn) {
        dismissedReturnIds.insert(vReturn.id)
        vehicleReturns.removeAll { $0.id == vReturn.id }
    }

    // MARK: - Countdown Timer

    private func startCountdownTimer() {
        // 1 Hz tick so countdown badges that show seconds (under one hour
        // remaining) actually count down on screen. The timer only fires
        // while the VM is alive and only touches `currentTime`.
        timerCancellable = Timer.publish(every: 1, on: .main, in: .common)
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

        // Today key-handovers depend on BOTH handover state changes AND lease
        // state changes (the card's pickup countdown + extension fields come
        // from the lease). Merging the two publishers here closes the gap
        // where the owner extends the deadline and the driver's Today timer
        // would otherwise wait for the 60s tick. Combine's default merging
        // already collapses bursts, so no extra debounce is needed.
        ws.keyHandoverUpdatedPublisher
            .merge(with: ws.leaseRequestUpdatedPublisher)
            .sink { [weak self] in
                Task { await self?.fetchKeyHandovers() }
            }
            .store(in: &wsCancellables)

        // Vehicle-return updates need to refresh both the return list and the
        // lease list (the lease's `vehicle_returned_at` flips on completion).
        ws.vehicleReturnUpdatedPublisher
            .merge(with: ws.leaseRequestUpdatedPublisher)
            .sink { [weak self] in
                Task { await self?.fetchVehicleReturns() }
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
