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

    /// Driver onboarding documents (license) shared into this chat through
    /// one or more lease requests. Surfaced to the OWNER side of the chat
    /// only — `vehicleDocuments` is the matching driver-side payload.
    @Published var sharedDocuments: [SharedDocumentAPIResponse] = []
    /// Vehicle/car documents (registration, insurance, …) the owner uploaded
    /// for the listing this chat is about. Surfaced to the DRIVER side only.
    @Published var vehicleDocuments: [VehicleDocumentAPIResponse] = []
    @Published var isLoadingSharedDocuments = false

    @Published var messageText = ""
    @Published var selectedTab: ChatTab = .messages
    @Published var error: String?

    /// One-shot flag the View flips after consuming `ChatView(initialTab:)`.
    /// Stops the override from re-applying on every nav-stack re-appear,
    /// which would otherwise stomp the user's later tab picks.
    var didApplyInitialTab = false

    /// True while an attachment is uploading. Used to disable the + button so
    /// the user can't double-fire a picker mid-upload.
    @Published var isUploadingAttachment = false

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

        // On any lease status flip we also refresh shared documents. This
        // matters for the driver side: vehicle documents (registration /
        // insurance / …) are gated by `HasAcceptedLeaseForChat` on the
        // backend, so they appear the moment the owner taps Accept and
        // disappear again if the rental terminates (declined / cancelled /
        // expired_refunded). Refreshing here keeps the chat surface in sync
        // without forcing the user to leave and re-enter the screen.
        WebSocketManager.shared.leaseRequestUpdatedPublisher
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in
                Task { [weak self] in
                    await self?.loadLeaseRequests()
                    await self?.loadSharedDocuments()
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
            senderId: currentUserId, senderName: "Me", senderKind: "user",
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
                    senderId: currentUserId, senderName: "Me", senderKind: "user",
                    direction: .sent, messageType: "text",
                    body: text, attachments: [], createdAt: Date(),
                    status: .failed(error.localizedDescription)
                )
            }
        }
    }

    /// A single file the user has picked but not yet uploaded.
    struct PendingAttachment {
        let data: Data
        let filename: String
        let mimeType: String
    }

    /// Uploads a single attachment. Convenience wrapper over `sendAttachments`
    /// kept for the file-picker path (one PDF/doc at a time).
    func sendAttachment(data: Data, filename: String, mimeType: String) async {
        await sendAttachments([PendingAttachment(data: data, filename: filename, mimeType: mimeType)])
    }

    /// Uploads N attachments. Optimistic bubbles are appended UP FRONT, in
    /// picked order, so the user sees the full batch immediately; uploads then
    /// run sequentially against the existing per-message idempotency contract.
    /// A per-item failure marks that one bubble `.failed`; the rest still
    /// upload.
    func sendAttachments(_ items: [PendingAttachment]) async {
        guard !items.isEmpty else { return }
        guard !isUploadingAttachment else { return }
        isUploadingAttachment = true
        defer { isUploadingAttachment = false }

        // 1. Append every optimistic bubble up front so the picked order is
        //    visually preserved before any network call has returned.
        let clientIds: [UUID] = items.map { _ in UUID() }
        for (idx, item) in items.enumerated() {
            appendOptimisticAttachment(
                clientId: clientIds[idx],
                data: item.data,
                filename: item.filename,
                mimeType: item.mimeType
            )
        }

        // 2. Upload sequentially against the existing endpoint. The backend's
        //    idempotency key is per (chat, sender, clientMessageId), so even if
        //    the user re-picks the same image twice it lands as two distinct
        //    bubbles (different clientIds), which matches user expectation.
        for (idx, item) in items.enumerated() {
            await uploadAttachment(
                clientId: clientIds[idx],
                data: item.data,
                filename: item.filename,
                mimeType: item.mimeType
            )
        }
    }

    private func appendOptimisticAttachment(clientId: UUID, data: Data, filename: String, mimeType: String) {
        let kind: AttachmentType =
            mimeType.hasPrefix("image/") ? .image :
            mimeType.hasPrefix("video/") ? .video : .document
        let placeholder = ChatAttachment(
            id: clientId, kind: kind, filename: filename,
            fileURL: "", fileSize: data.count, mimeType: mimeType
        )
        let optimistic = ChatMessage(
            id: clientId, clientMessageId: clientId, chatId: chatId,
            senderId: currentUserId, senderName: "Me", senderKind: "user",
            direction: .sent, messageType: "attachment",
            body: filename, attachments: [placeholder], createdAt: Date(),
            status: .sending
        )
        messages.append(optimistic)
    }

    private func uploadAttachment(clientId: UUID, data: Data, filename: String, mimeType: String) async {
        do {
            let response = try await apiClient.uploadChatAttachment(
                chatId: chatId, fileData: data, filename: filename, mimeType: mimeType,
                clientMessageId: clientId
            )
            // Replace optimistic with server-derived message (same clientId).
            if let idx = messages.firstIndex(where: { $0.clientMessageId == clientId }) {
                messages[idx] = response.toChatMessage(currentUserId: currentUserId)
            }
        } catch {
            if let idx = messages.firstIndex(where: { $0.clientMessageId == clientId }) {
                messages[idx].status = .failed(error.localizedDescription)
            }
            self.error = "Failed to upload attachment: \(error.localizedDescription)"
        }
    }

    private func handleIncomingMessage(_ apiMsg: ChatMessageAPIResponse) {
        let msg = apiMsg.toChatMessage(currentUserId: currentUserId)

        // If we already have a local row with the same clientMessageId, the
        // server has confirmed something the sender already optimistically
        // appended. If the local row is still `.sending` (no HTTP response yet)
        // or `.failed` (HTTP response lost mid-flight but the server actually
        // committed), REPLACE it with the server-confirmed message so the bubble
        // is reconciled instead of staying stuck. If it's already `.sent` (HTTP
        // round-trip succeeded first), skip — the WS event is redundant.
        if let idx = messages.firstIndex(where: { $0.clientMessageId == msg.clientMessageId }) {
            if case .sent = messages[idx].status { return }
            messages[idx] = msg
            return
        }

        // Plain dedup by id (covers messages without a clientMessageId).
        if messages.contains(where: { $0.id == msg.id }) { return }
        messages.append(msg)
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

    /// Red-dot signal on the in-chat "Requests" segmented tab. True when the
    /// current user has a lease request that needs their action.
    ///
    ///   - Owner: at least one lease request is still `requested` (waiting on
    ///     them to Accept/Decline).
    ///   - Driver:
    ///       • a request the owner has accepted is awaiting payment
    ///         (`driverCanPay` — covers fresh accepts and failed/canceled
    ///         retries), OR
    ///       • the rental is paid and pickup confirmation is still pending
    ///         (driver must tap "I've picked up the car" before the deadline).
    ///
    /// Suppressed while the user is already on the Requests tab — the action
    /// card itself is visible there, so a persistent dot on the tab label
    /// would be redundant. Auto-clears the moment the underlying status flips
    /// (action taken, owner declined, payment succeeded, pickup confirmed, …)
    /// because `leaseRequests` is updated in place by every mutation path.
    var hasRequestsAttentionBadge: Bool {
        if selectedTab == .requests { return false }
        return leaseRequests.contains { lr in
            if lr.ownerId == currentUserId, lr.status == .requested {
                return true
            }
            if lr.driverId == currentUserId {
                // Driver must review a price change before payment — Pay
                // Now is held, so this is the top-priority signal.
                if lr.driverShouldReviewPrice { return true }
                if lr.driverCanPay { return true }
                if lr.isAwaitingPickupConfirmation { return true }
            }
            return false
        }
    }

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

    /// Fetches the role-aware document payload for this chat. The backend
    /// fills `driver_documents` for the OWNER and `vehicle_documents` for
    /// the DRIVER — the empty array is populated for the wrong-side viewer
    /// so the UI doesn't need to do extra gating. Silent failure: any
    /// error is logged and the sections simply stay empty.
    func loadSharedDocuments() async {
        isLoadingSharedDocuments = true
        defer { isLoadingSharedDocuments = false }
        do {
            let response = try await apiClient.fetchSharedDocuments(chatId: chatId)
            sharedDocuments = response.driverDocumentsOrLegacy
            vehicleDocuments = response.vehicleDocumentsOrEmpty
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

    /// Driver: accept the owner's adjusted price so Pay Now reappears.
    /// On success the LR's `priceChangePending` flips false and the card
    /// renders the existing Pay Now flow on the next SwiftUI tick.
    func acceptPriceChange(id: UUID) async {
        do {
            let response = try await apiClient.acceptLeasePriceChange(id: id)
            let updated = response.toLeaseRequest()
            if let idx = leaseRequests.firstIndex(where: { $0.id == id }) {
                leaseRequests[idx] = updated
            }
        } catch {
            self.error = describeError(error)
        }
    }

    /// Driver: decline the owner's adjusted price. The lease is cancelled
    /// server-side (car unreserved, any pending PaymentIntent voided), so
    /// the card transitions to its cancelled terminal state.
    func declinePriceChange(id: UUID) async {
        do {
            let response = try await apiClient.declineLeasePriceChange(id: id)
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

    /// Owner: undo an Accept while no payment is in flight. Backend refuses
    /// (409) once the lease moves past `accepted`, so the iOS error banner
    /// flashes "the rental is already in progress" — which is the right
    /// thing to show the owner.
    func rescindAcceptedLeaseRequest(id: UUID) async {
        do {
            let response = try await apiClient.rescindAcceptedLeaseRequest(id: id)
            let updated = response.toLeaseRequest()
            if let idx = leaseRequests.firstIndex(where: { $0.id == id }) {
                leaseRequests[idx] = updated
            }
        } catch {
            self.error = describeError(error)
        }
    }

    /// Driver-only: confirms in-person pickup before the PICKUP_DEADLINE_MINUTES
    /// window elapses. Backend marks pickup_confirmed_at, stops the expiry
    /// scanner from picking the row up, and broadcasts to both sides.
    func confirmPickup(id: UUID) async {
        do {
            let response = try await apiClient.confirmPickup(leaseRequestId: id)
            let updated = response.toLeaseRequest()
            if let idx = leaseRequests.firstIndex(where: { $0.id == id }) {
                leaseRequests[idx] = updated
            }
        } catch {
            self.error = describeError(error)
        }
    }

    /// Owner-only: pushes `pickup_deadline_at` out by `minutes` (one of
    /// LeaseRequest.allowedPickupExtensionMinutes). The backend response is
    /// authoritative — it bumps the local row's deadline and the timer in
    /// the lease card picks the new value up on the next tick.
    func extendPickupDeadline(id: UUID, minutes: Int) async {
        do {
            let response = try await apiClient.extendPickupDeadline(leaseRequestId: id, minutes: minutes)
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
                    pickupDeadlineAt: lr.pickupDeadlineAt,
                    pickupConfirmedAt: lr.pickupConfirmedAt,
                    refundId: lr.refundId,
                    refundedAt: lr.refundedAt,
                    refundStatus: lr.refundStatus,
                    pickupExtensionTotalMinutes: lr.pickupExtensionTotalMinutes,
                    pickupExtensionCount: lr.pickupExtensionCount,
                    pickupExtensionRemainingMinutes: lr.pickupExtensionRemainingMinutes,
                    pickupLastExtendedAt: lr.pickupLastExtendedAt,
                    priceChangePending: lr.priceChangePending,
                    previousOfferedWeeklyPrice: lr.previousOfferedWeeklyPrice,
                    priceChangeActedAt: lr.priceChangeActedAt,
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
                    pickupDeadlineAt: lr.pickupDeadlineAt,
                    pickupConfirmedAt: lr.pickupConfirmedAt,
                    refundId: lr.refundId,
                    refundedAt: lr.refundedAt,
                    refundStatus: lr.refundStatus,
                    pickupExtensionTotalMinutes: lr.pickupExtensionTotalMinutes,
                    pickupExtensionCount: lr.pickupExtensionCount,
                    pickupExtensionRemainingMinutes: lr.pickupExtensionRemainingMinutes,
                    pickupLastExtendedAt: lr.pickupLastExtendedAt,
                    priceChangePending: lr.priceChangePending,
                    previousOfferedWeeklyPrice: lr.previousOfferedWeeklyPrice,
                    priceChangeActedAt: lr.priceChangeActedAt,
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
