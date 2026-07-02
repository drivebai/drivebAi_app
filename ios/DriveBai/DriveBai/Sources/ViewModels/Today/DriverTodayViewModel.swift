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

    /// Active key-handover tasks (pending / awaiting confirmation) for this user
    @Published var keyHandovers: [KeyHandover] = []

    /// ID of the handover currently being confirmed (drives the card's busy state)
    @Published var submittingHandoverId: UUID?

    /// Active vehicle-return rows for this user (driver-initiated / awaiting
    /// owner / disputed / freshly-completed). Includes terminal rows until
    /// the user dismisses them — the Today card needs the "Got it" handshake
    /// to feel deliberate.
    @Published var vehicleReturns: [VehicleReturn] = []

    /// ID of the return currently being acted on (drives the card's busy state).
    @Published var submittingReturnId: UUID?

    /// Active purchase requests where the current user is the BUYER.  Empty
    /// on backends that don't yet ship the purchase endpoints (silent
    /// failure — see `fetchPurchaseRequests`).
    @Published var purchaseRequests: [PurchaseRequest] = []

    /// Locally-dismissed return ids — we filter these out client-side
    /// because the backend has no per-user dismiss endpoint for returns yet.
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
            await fetchPurchaseRequests()
        }
    }

    /// Fetches the current buyer's active purchase requests from the shared
    /// `/today/purchase-requests` endpoint.  Failures are logged and leave
    /// the list empty so an older backend doesn't paint the Today tab red.
    func fetchPurchaseRequests() async {
        do {
            let response = try await APIClient.shared.fetchPurchaseRequestsToday()
            purchaseRequests = response.purchaseRequests.map { $0.toDomain() }
        } catch {
            #if DEBUG
            print("[DriverTodayVM] fetchPurchaseRequests error: \(error)")
            #endif
        }
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
    }

    func fetchNotifications() async {
        do {
            let response = try await apiClient.fetchNotifications()
            notifications = response.notifications.map { $0.toNotificationItem() }
            unreadNotificationCount = response.unreadCount
        } catch {
            #if DEBUG
            print("[DriverTodayVM] fetchNotifications error: \(error)")
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
        await fetchNotifications()
        await fetchKeyHandovers()
        await fetchVehicleReturns()
        await fetchPurchaseRequests()
    }

    // MARK: - Key Handovers

    func fetchKeyHandovers() async {
        do {
            let response = try await apiClient.fetchKeyHandoversToday()
            keyHandovers = response.keyHandovers.map { $0.toDomain() }
            // Reconcile Live Activities against the freshly-fetched state.
            // This is the single choke-point that covers cold-launch (start
            // an activity for an already-paid lease), websocket-driven
            // extensions (update the existing activity), and every terminal
            // path (pickup confirmed, expired_refunded, cancelled —
            // anything that drops a handover out of isAwaitingPickupConfirmation
            // ends the activity automatically).
            syncLiveActivities()
        } catch {
            #if DEBUG
            print("[DriverTodayVM] fetchKeyHandovers error: \(error)")
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
                print("[DriverTodayVM] confirmHandover error: \(error)")
                #endif
            }
            // Re-sync with the server (handles success, expiry, and races uniformly).
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
                print("[DriverTodayVM] dismissHandover error: \(error)")
                #endif
                if let i = originalIndex,
                   !self.keyHandovers.contains(where: { $0.id == handover.id }) {
                    self.keyHandovers.insert(handover, at: min(i, self.keyHandovers.count))
                }
                await fetchKeyHandovers()
            }
        }
    }

    // MARK: - Vehicle Returns

    /// Pull the active vehicle-return rows for the current driver. Failures
    /// are swallowed (and just leave the list empty) so a backend not yet
    /// running the v2 endpoints doesn't paint the Today tab with a banner.
    func fetchVehicleReturns() async {
        do {
            let response = try await apiClient.fetchVehicleReturnsToday()
            let incoming = response.vehicleReturns.map { $0.toDomain() }
            vehicleReturns = incoming.filter { !dismissedReturnIds.contains($0.id) }
        } catch {
            #if DEBUG
            print("[DriverTodayVM] fetchVehicleReturns error: \(error)")
            #endif
        }
    }

    /// Driver-side initiator. Called from the Today card's "I returned the
    /// car" CTA OR from the chat-tab LeaseRequestCardView. Refreshes after
    /// the round-trip so both surfaces show the same state.
    func submitVehicleReturn(leaseRequestId: UUID) {
        Task {
            do {
                _ = try await apiClient.initiateVehicleReturn(leaseRequestId: leaseRequestId)
            } catch {
                #if DEBUG
                print("[DriverTodayVM] submitVehicleReturn error: \(error)")
                #endif
            }
            await fetchVehicleReturns()
        }
    }

    /// Single entry-point the card's "primary" CTA hits — dispatches by
    /// viewerRole so a user who is the driver on one lease AND the owner
    /// on another can't accidentally invoke the wrong API (e.g. tapping
    /// "Confirm return" on an owner-perspective card from the Driver tab).
    func actOnVehicleReturn(_ vReturn: VehicleReturn) {
        switch vReturn.viewerRole {
        case .driver: cancelVehicleReturn(vReturn)
        case .owner:  confirmVehicleReturn(vReturn)
        }
    }

    /// Driver-side undo within the 5-minute window. Backend enforces the
    /// window; on 409 we just refetch so the UI reflects the truth.
    func cancelVehicleReturn(_ vReturn: VehicleReturn) {
        guard submittingReturnId == nil else { return }
        submittingReturnId = vReturn.id
        Task {
            defer { submittingReturnId = nil }
            do {
                _ = try await apiClient.cancelVehicleReturn(returnId: vReturn.id)
            } catch {
                #if DEBUG
                print("[DriverTodayVM] cancelVehicleReturn error: \(error)")
                #endif
            }
            await fetchVehicleReturns()
        }
    }

    /// Owner-perspective confirm — kept on the Driver VM too because the
    /// underlying `vehicleReturns` array is the union of both roles for
    /// this user. See `actOnVehicleReturn` for dispatch.
    func confirmVehicleReturn(_ vReturn: VehicleReturn) {
        guard submittingReturnId == nil else { return }
        submittingReturnId = vReturn.id
        Task {
            defer { submittingReturnId = nil }
            do {
                _ = try await apiClient.confirmVehicleReturn(returnId: vReturn.id)
            } catch {
                #if DEBUG
                print("[DriverTodayVM] confirmVehicleReturn error: \(error)")
                #endif
            }
            await fetchVehicleReturns()
        }
    }

    /// "Got it" tap on a terminal return card — local-only because the
    /// backend has no per-user dismiss endpoint for returns. Persists for
    /// the lifetime of the view model.
    func dismissVehicleReturn(_ vReturn: VehicleReturn) {
        dismissedReturnIds.insert(vReturn.id)
        vehicleReturns.removeAll { $0.id == vReturn.id }
    }

    // MARK: - Countdown Timer

    private func startCountdownTimer() {
        // 1 Hz tick so the post-payment pickup countdown card (and any other
        // sub-hour task badges) visibly counts down second-by-second. The
        // timer only fires while the VM is alive, and `currentTime` is the
        // only field it touches, so the publish radius is tight.
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

        // Driver Today depends on BOTH handover state changes AND lease state
        // changes (the card's pickup countdown + extension fields come from
        // the lease). Without this merge an owner-side extend would not be
        // visible to the driver until the next 60s tick.
        ws.keyHandoverUpdatedPublisher
            .merge(with: ws.leaseRequestUpdatedPublisher)
            .sink { [weak self] in
                Task { await self?.fetchKeyHandovers() }
            }
            .store(in: &wsCancellables)

        // Vehicle-return updates also re-query lease state under the hood
        // (the lease's `vehicle_returned_at` flips on completion), so we
        // bundle the lease publisher in here too — keeps the Today tab in
        // sync without waiting for the next manual refresh.
        ws.vehicleReturnUpdatedPublisher
            .merge(with: ws.leaseRequestUpdatedPublisher)
            .sink { [weak self] in
                Task { await self?.fetchVehicleReturns() }
            }
            .store(in: &wsCancellables)

        ws.purchaseRequestUpdatedPublisher
            .sink { [weak self] in
                Task { await self?.fetchPurchaseRequests() }
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

}
