import Foundation
import Combine

enum ChatTab: String, CaseIterable {
    case messages = "Messages"
    case requests = "Requests"
}

@MainActor
final class ChatViewModel: ObservableObject {
    let chatId: UUID
    let currentUserId: UUID

    @Published var messages: [ChatMessage] = []
    @Published var isLoadingMessages = false
    @Published var hasMoreMessages = true
    private var nextCursor: String?

    @Published var requests: [ChatRequest] = []
    @Published var isLoadingRequests = false

    @Published var leaseRequests: [LeaseRequest] = []
    @Published var isLoadingLeaseRequests = false

    /// Driver onboarding documents shared into this chat through one or more
    /// lease requests. Deduplicated by document. Only the car owner should
    /// render these in the UI — the view decides via `isOwnerOfChat`.
    @Published var sharedDocuments: [SharedDocumentAPIResponse] = []
    @Published var isLoadingSharedDocuments = false

    @Published var messageText = ""
    @Published var selectedTab: ChatTab = .messages
    @Published var error: String?

    private let apiClient: APIClient
    private var cancellables = Set<AnyCancellable>()

    init(chatId: UUID, currentUserId: UUID, apiClient: APIClient = .shared) {
        self.chatId = chatId
        self.currentUserId = currentUserId
        self.apiClient = apiClient
        subscribeToWebSocket()
    }

    private func subscribeToWebSocket() {
        WebSocketManager.shared.newMessagePublisher
            .receive(on: RunLoop.main)
            .filter { [weak self] msg in msg.chatId == self?.chatId }
            .sink { [weak self] msg in
                self?.handleIncomingMessage(msg)
            }
            .store(in: &cancellables)

        WebSocketManager.shared.requestCreatedPublisher
            .receive(on: RunLoop.main)
            .filter { [weak self] req in req.chatId == self?.chatId }
            .sink { [weak self] req in
                let request = req.toChatRequest()
                if !(self?.requests.contains(where: { $0.id == request.id }) ?? true) {
                    self?.requests.insert(request, at: 0)
                }
            }
            .store(in: &cancellables)

        WebSocketManager.shared.requestUpdatedPublisher
            .receive(on: RunLoop.main)
            .filter { [weak self] req in req.chatId == self?.chatId }
            .sink { [weak self] req in
                let request = req.toChatRequest()
                if let idx = self?.requests.firstIndex(where: { $0.id == request.id }) {
                    self?.requests[idx] = request
                }
            }
            .store(in: &cancellables)

        // When a new lease request arrives in this chat, the backend has also
        // snapshotted the driver's documents into the shared-docs table. Refresh
        // both so the owner sees the Driver Documents section without a reload.
        WebSocketManager.shared.leaseRequestCreatedPublisher
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in
                Task { [weak self] in
                    await self?.loadLeaseRequests()
                    await self?.loadSharedDocuments()
                }
            }
            .store(in: &cancellables)

        WebSocketManager.shared.leaseRequestUpdatedPublisher
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in
                Task { [weak self] in
                    await self?.loadLeaseRequests()
                }
            }
            .store(in: &cancellables)
    }

    // MARK: - Messages

    func loadInitialMessages() async {
        guard !isLoadingMessages else { return }
        isLoadingMessages = true
        error = nil
        do {
            let response = try await apiClient.fetchMessages(chatId: chatId, cursor: nil, limit: 30)
            messages = response.messages.map { $0.toChatMessage(currentUserId: currentUserId) }.reversed()
            nextCursor = response.nextCursor
            hasMoreMessages = response.hasMore
        } catch {
            self.error = error.localizedDescription
        }
        isLoadingMessages = false
    }

    func loadMoreMessages() async {
        guard hasMoreMessages, !isLoadingMessages, let cursor = nextCursor else { return }
        isLoadingMessages = true
        do {
            let response = try await apiClient.fetchMessages(chatId: chatId, cursor: cursor, limit: 30)
            let older = response.messages.map { $0.toChatMessage(currentUserId: currentUserId) }.reversed()
            messages.insert(contentsOf: older, at: 0)
            nextCursor = response.nextCursor
            hasMoreMessages = response.hasMore
        } catch {
            self.error = error.localizedDescription
        }
        isLoadingMessages = false
    }

    func sendMessage() async {
        let text = messageText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }

        let clientId = UUID()
        let optimistic = ChatMessage(
            id: clientId, clientMessageId: clientId, chatId: chatId,
            senderId: currentUserId, senderName: "Me",
            direction: .sent, messageType: "text",
            body: text, attachments: [], createdAt: Date(), status: .sending
        )

        messages.append(optimistic)
        messageText = ""

        do {
            let request = SendMessageAPIRequest(body: text, clientMessageId: clientId)
            let response = try await apiClient.sendMessage(chatId: chatId, request: request)
            if let idx = messages.firstIndex(where: { $0.clientMessageId == clientId }) {
                messages[idx] = response.toChatMessage(currentUserId: currentUserId)
            }
        } catch {
            if let idx = messages.firstIndex(where: { $0.clientMessageId == clientId }) {
                messages[idx] = ChatMessage(
                    id: clientId, clientMessageId: clientId, chatId: chatId,
                    senderId: currentUserId, senderName: "Me",
                    direction: .sent, messageType: "text",
                    body: text, attachments: [], createdAt: Date(),
                    status: .failed(error.localizedDescription)
                )
            }
        }
    }

    private func handleIncomingMessage(_ apiMsg: ChatMessageAPIResponse) {
        let msg = apiMsg.toChatMessage(currentUserId: currentUserId)
        // Deduplicate by clientMessageId or id
        if !messages.contains(where: { $0.id == msg.id || $0.clientMessageId == msg.clientMessageId }) {
            messages.append(msg)
        }
    }

    // MARK: - Requests

    func loadRequests() async {
        isLoadingRequests = true
        do {
            let response = try await apiClient.fetchChatRequests(chatId: chatId)
            requests = response.requests.map { $0.toChatRequest() }
        } catch {
            self.error = error.localizedDescription
        }
        isLoadingRequests = false
    }

    func respondToRequest(requestId: UUID, action: RequestAction, note: String? = nil) async {
        do {
            let request = RespondToRequestAPIRequest(action: action.rawValue, note: note)
            let response = try await apiClient.respondToRequest(chatId: chatId, requestId: requestId, request: request)
            let updated = response.toChatRequest()
            if let idx = requests.firstIndex(where: { $0.id == requestId }) {
                requests[idx] = updated
            }
        } catch {
            self.error = error.localizedDescription
        }
    }

    // MARK: - Lease Requests

    func loadLeaseRequests() async {
        isLoadingLeaseRequests = true
        do {
            let response = try await apiClient.fetchLeaseRequests(chatId: chatId)
            leaseRequests = response.leaseRequests.map { $0.toLeaseRequest() }
        } catch {
            self.error = error.localizedDescription
        }
        isLoadingLeaseRequests = false
    }

    // MARK: - Shared Driver Documents

    /// Fetches the driver onboarding documents shared into this chat via lease
    /// requests. Silent failure — driver docs are auxiliary to the chat; any
    /// error is logged and the section simply stays empty.
    func loadSharedDocuments() async {
        isLoadingSharedDocuments = true
        defer { isLoadingSharedDocuments = false }
        do {
            let response = try await apiClient.fetchSharedDocuments(chatId: chatId)
            sharedDocuments = response.documents
        } catch {
            #if DEBUG
            print("[ChatViewModel] Failed to fetch shared documents: \(error)")
            #endif
        }
    }

    func acceptLeaseRequest(id: UUID) async {
        do {
            let response = try await apiClient.acceptLeaseRequest(id: id)
            let updated = response.toLeaseRequest()
            if let idx = leaseRequests.firstIndex(where: { $0.id == id }) {
                leaseRequests[idx] = updated
            }
        } catch {
            self.error = describeError(error)
        }
    }

    func declineLeaseRequest(id: UUID) async {
        do {
            let response = try await apiClient.declineLeaseRequest(id: id)
            let updated = response.toLeaseRequest()
            if let idx = leaseRequests.firstIndex(where: { $0.id == id }) {
                leaseRequests[idx] = updated
            }
        } catch {
            self.error = describeError(error)
        }
    }

    func cancelLeaseRequest(id: UUID) async {
        do {
            let response = try await apiClient.cancelLeaseRequest(id: id)
            let updated = response.toLeaseRequest()
            if let idx = leaseRequests.firstIndex(where: { $0.id == id }) {
                leaseRequests[idx] = updated
            }
        } catch {
            self.error = describeError(error)
        }
    }

    func adjustLeasePrice(id: UUID, offeredWeeklyPrice: Double) async {
        do {
            let response = try await apiClient.updateLeaseRequestPrice(id: id, offeredWeeklyPrice: offeredWeeklyPrice)
            let updated = response.toLeaseRequest()
            if let idx = leaseRequests.firstIndex(where: { $0.id == id }) {
                leaseRequests[idx] = updated
            }
        } catch {
            self.error = describeError(error)
        }
    }

    /// Returns the PaymentIntent client secret + ephemeral key + customer ID for PaymentSheet
    func createPaymentIntent(leaseRequestId: UUID) async -> PaymentIntentAPIResponse? {
        do {
            let response = try await apiClient.createPaymentIntent(leaseRequestId: leaseRequestId)
            // Refresh the lease request to show updated status
            await loadLeaseRequests()
            return response
        } catch {
            self.error = describeError(error)
            return nil
        }
    }

    /// Sync payment status with Stripe (fallback when webhook is delayed/misconfigured).
    /// Calls the backend sync endpoint, which queries Stripe directly and reconciles.
    func syncPaymentStatus(leaseRequestId: UUID) async {
        do {
            let response = try await apiClient.syncPaymentStatus(leaseRequestId: leaseRequestId)
            let updated = response.toLeaseRequest()
            if let idx = leaseRequests.firstIndex(where: { $0.id == leaseRequestId }) {
                leaseRequests[idx] = updated
            }
        } catch {
            // Sync failure is not critical — we'll still poll via loadLeaseRequests
            print("[ChatViewModel] syncPaymentStatus failed: \(error)")
        }
    }

    /// After PaymentSheet shows success, sync with backend and poll for status update.
    func handlePaymentCompleted(leaseRequestId: UUID) async {
        // 0. Optimistic UI: PaymentSheet only returns .completed when Stripe confirms success,
        //    so immediately show "Paid" locally while backend catches up.
        if let idx = leaseRequests.firstIndex(where: { $0.id == leaseRequestId }) {
            var lr = leaseRequests[idx]
            // Update the local payment status to succeeded so the card shows "Payment Complete"
            if let payment = lr.payment {
                let updatedPayment = PaymentSummary(
                    id: payment.id,
                    paymentIntentId: payment.paymentIntentId,
                    amount: payment.amount,
                    platformFeeAmount: payment.platformFeeAmount,
                    currency: payment.currency,
                    status: .succeeded
                )
                lr = LeaseRequest(
                    id: lr.id, chatId: lr.chatId, listingId: lr.listingId,
                    ownerId: lr.ownerId, driverId: lr.driverId,
                    driverName: lr.driverName, ownerName: lr.ownerName,
                    status: .paid, weeklyPrice: lr.weeklyPrice, offeredWeeklyPrice: lr.offeredWeeklyPrice,
                    totalAmount: lr.totalAmount,
                    currency: lr.currency, weeks: lr.weeks, message: lr.message,
                    carTitle: lr.carTitle, payment: updatedPayment,
                    createdAt: lr.createdAt, updatedAt: lr.updatedAt
                )
            } else {
                lr = LeaseRequest(
                    id: lr.id, chatId: lr.chatId, listingId: lr.listingId,
                    ownerId: lr.ownerId, driverId: lr.driverId,
                    driverName: lr.driverName, ownerName: lr.ownerName,
                    status: .paid, weeklyPrice: lr.weeklyPrice, offeredWeeklyPrice: lr.offeredWeeklyPrice,
                    totalAmount: lr.totalAmount,
                    currency: lr.currency, weeks: lr.weeks, message: lr.message,
                    carTitle: lr.carTitle, payment: lr.payment,
                    createdAt: lr.createdAt, updatedAt: lr.updatedAt
                )
            }
            leaseRequests[idx] = lr
        }

        // 1. Call sync endpoint to reconcile backend (queries Stripe directly)
        await syncPaymentStatus(leaseRequestId: leaseRequestId)

        // 2. Refresh from backend to get the authoritative state
        await loadLeaseRequests()

        // 3. If backend still hasn't caught up, retry after delays
        for delay in [2.0, 4.0] {
            if leaseRequests.first(where: { $0.id == leaseRequestId })?.status == .paid {
                return
            }
            try? await Task.sleep(for: .seconds(delay))
            await loadLeaseRequests()
        }
    }

    // MARK: - Read

    func markAsRead() async {
        _ = try? await apiClient.markChatRead(chatId: chatId)
    }

    // MARK: - Error Helpers

    private func describeError(_ error: Error) -> String {
        if let apiError = error as? APIError {
            switch apiError {
            case .serverError(let code, let message):
                // Show both code and message for actionable debugging
                return "\(message) [\(code)]"
            default:
                break
            }
        }
        return error.localizedDescription
    }
}
