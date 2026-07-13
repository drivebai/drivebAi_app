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

    /// Purchase requests attached to this chat.  The Requests tab renders
    /// these between the lease requests and the generic chat requests.
    @Published var purchaseRequests: [PurchaseRequest] = []
    @Published var isLoadingPurchaseRequests = false
    /// Cached BoS rows keyed by purchase-request id.  Fetched lazily —
    /// only when the wizard needs a seed.
    @Published var billOfSalesByPurchase: [UUID: BillOfSale] = [:]

    /// Active vehicle-return rows keyed by lease-request id. Powers the
    /// in-chat lease card so the same surface that drove pickup-confirmation
    /// now also drives "Start return" / "Confirm return" / status copy.
    /// We keep it map-shaped so the card render can look up its row in O(1)
    /// regardless of whether the row is in driver_initiated, owner_confirmed,
    /// disputed, or completed state.
    @Published var vehicleReturnsByLease: [UUID: VehicleReturn] = [:]
    @Published var isLoadingVehicleReturns = false
    /// Per-lease busy state for the chat-card CTAs.
    @Published var submittingVehicleReturnLeaseId: UUID?

    /// Active key handovers keyed by lease_request_id. Fetched from
    /// /key-handovers/today on chat load + refreshed on
    /// `key_handover_owner_confirmed` / `key_handover_completed` /
    /// `key_handover_created` WS events. Used to gate the driver's
    /// "I've picked up the car" CTA on `.ownerConfirmed` (mirrors the
    /// Today card's rule so both surfaces flip together).
    @Published var keyHandoversByLease: [UUID: KeyHandover] = [:]

    /// Driver onboarding documents (license) shared into this chat through
    /// one or more lease requests. Surfaced to the OWNER side of the chat
    /// only — `vehicleDocuments` is the matching driver-side payload.
    @Published var sharedDocuments: [SharedDocumentAPIResponse] = []
    /// Vehicle/car documents (registration, insurance, …) the owner uploaded
    /// for the listing this chat is about. Surfaced to the DRIVER side only.
    @Published var vehicleDocuments: [VehicleDocumentAPIResponse] = []
    @Published var isLoadingSharedDocuments = false

    @Published var messageText = ""
    @Published var selectedTab: ChatTab = .messages {
        didSet {
            guard oldValue != selectedTab else { return }
            handleTabChange()
        }
    }
    @Published var error: String?

    /// True when this chat has messages the current user hasn't viewed on
    /// the Messages tab yet. Server-derived — seeded from the GET /chats
    /// `unread_count` (which the backend computes from
    /// `chat_participants.last_read_at`) via `refreshUnreadState()` — and
    /// bumped live by WS `new_message` events that arrive while the user
    /// isn't looking at the conversation (Requests tab visible, or the chat
    /// pushed off-screen). Cleared the moment the Messages tab becomes
    /// visible, in the same breath as the POST /read stamp.
    @Published private(set) var hasUnreadMessages = false

    /// One-shot flag the View flips after consuming `ChatView(initialTab:)`.
    /// Stops the override from re-applying on every nav-stack re-appear,
    /// which would otherwise stomp the user's later tab picks.
    var didApplyInitialTab = false

    /// True while an attachment is uploading. Used to disable the + button so
    /// the user can't double-fire a picker mid-upload.
    @Published var isUploadingAttachment = false

    private let apiClient: APIClient
    private var cancellables = Set<AnyCancellable>()

    /// Tracks whether this VM has already seen the socket in `.connected`
    /// state. The first `.connected` emission is the normal open-path
    /// handshake (the `.task` HTTP loads cover it); only LATER ones are
    /// real reconnects whose outage may have swallowed `new_message`
    /// events, requiring a server-side resync.
    private var hasEverBeenConnected = false

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

        // Vehicle-return events on this chat's leases bump the local map so
        // the inline lease card flips between "Start return" → "Awaiting
        // owner" → "Returned" with no manual refresh. We don't filter by
        // chatId here because the WS payload is minimal; instead we refetch
        // for every lease this chat already knows about — cheap and correct.
        WebSocketManager.shared.vehicleReturnUpdatedPublisher
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in
                Task { [weak self] in await self?.loadVehicleReturnsForChatLeases() }
            }
            .store(in: &cancellables)

        // Purchase-request events (created / updated / payment / handover /
        // rejection).  We refetch the entire chat's purchase list on any
        // event so the card state is authoritative.
        WebSocketManager.shared.purchaseRequestUpdatedPublisher
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in
                Task { [weak self] in await self?.loadPurchaseRequests() }
            }
            .store(in: &cancellables)

        // BoS-only channel. On a targeted BoS update we refresh the
        // affected row in `billOfSalesByPurchase` so an open
        // BillOfSaleFlowView (or a PurchaseRequestCardView reading from
        // the cache) sees the counterparty's edits without needing a
        // full purchase-list reload. If the WS payload didn't carry an
        // id we fall back to refreshing every accepted purchase in this
        // chat — bounded and rare.
        WebSocketManager.shared.purchaseBillOfSaleUpdatedPublisher
            .receive(on: RunLoop.main)
            .sink { [weak self] purchaseID in
                Task { [weak self] in
                    guard let self else { return }
                    if let id = purchaseID {
                        await self.fetchBillOfSale(purchaseRequestId: id)
                    } else {
                        await self.refreshBillOfSaleCacheForChat()
                    }
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
                    await self?.loadVehicleReturnsForChatLeases()
                }
            }
            .store(in: &cancellables)

        // Key handover transitions (owner_confirmed / completed / expired)
        // flip the chat-card pickup CTA gate — refetch so the button appears
        // for the driver the instant the owner taps "I handed over the keys".
        WebSocketManager.shared.keyHandoverUpdatedPublisher
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in
                Task { [weak self] in
                    await self?.loadKeyHandoversForChatLeases()
                }
            }
            .store(in: &cancellables)

        // Reconnect resync. A dropped socket silently swallows `new_message`
        // events, which would leave both the local message list and the
        // Messages-tab unread dot stale. When the socket comes back after a
        // real drop (not the initial handshake), re-derive everything from
        // the server instead of trusting whatever WS state accumulated.
        //
        // Seeding: @Published replays the CURRENT state on subscribe. When
        // the socket is already connected at VM init, that replayed
        // `.connected` is the normal open-path handshake — consume it once
        // (seed false → else-branch). But when the chat is opened while the
        // socket is DOWN (backoff can reach minutes), the `.task` HTTP loads
        // finish first and any message arriving before the socket recovers
        // is never delivered — so the FIRST `.connected` after a cold-open
        // gap must resync too (seed true).
        hasEverBeenConnected = WebSocketManager.shared.connectionState != .connected
        WebSocketManager.shared.$connectionState
            .receive(on: RunLoop.main)
            .removeDuplicates()
            .sink { [weak self] state in
                guard let self, state == .connected else { return }
                if self.hasEverBeenConnected {
                    Task { [weak self] in await self?.resyncAfterReconnect() }
                } else {
                    self.hasEverBeenConnected = true
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

    /// Re-sends a bubble that previously landed in `.failed`. Reuses the
    /// ORIGINAL `clientMessageId` so the backend's per-(chat, sender,
    /// clientMessageId) idempotency key dedupes a re-POST whose first attempt
    /// actually committed but whose HTTP response was lost. No status gate:
    /// messaging stays available through and after the sale — this is purely a
    /// transient-failure recovery affordance. Text bubbles only (attachment
    /// bytes aren't retained after the optimistic append).
    func retrySend(message: ChatMessage) async {
        guard case .failed = message.status else { return }
        guard message.messageType == "text" else { return }
        let clientId = message.clientMessageId
        let text = message.body

        // Flip the existing bubble back to "sending" in place so the user sees
        // the spinner instead of a second bubble appearing.
        if let idx = messages.firstIndex(where: { $0.clientMessageId == clientId }) {
            messages[idx].status = .sending
        }

        do {
            let request = SendMessageAPIRequest(body: text, clientMessageId: clientId)
            let response = try await apiClient.sendMessage(chatId: chatId, request: request)
            if let idx = messages.firstIndex(where: { $0.clientMessageId == clientId }) {
                messages[idx] = response.toChatMessage(currentUserId: currentUserId)
            }
        } catch {
            if let idx = messages.firstIndex(where: { $0.clientMessageId == clientId }) {
                messages[idx].status = .failed(error.localizedDescription)
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

        // Unread bookkeeping. Own messages (echoes from another device —
        // the local-optimistic path already returned above) never light the
        // dot: direction is derived from senderId, so only counterparty /
        // system rows count, mirroring the backend's
        // `sender_id != $1` unread_count rule.
        guard msg.direction == .received else { return }
        if selectedTab == .messages, ChatsListViewModel.shared.activelyReadingChatId == chatId {
            // The user is watching the conversation right now — stamp
            // `last_read_at` so the server-side unread_count doesn't drift
            // upward while they sit in an open chat (the Chats-list badge
            // increment is already suppressed via activelyReadingChatId).
            Task { await markAsRead() }
        } else {
            // Requests tab visible, or the chat is buried under a pushed
            // screen: surface the dot and leave the read marker untouched.
            hasUnreadMessages = true
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

    // MARK: - Attention Badges

    /// Red-dot signal on the in-chat "Messages" segmented tab — the mirror
    /// of `hasRequestsAttentionBadge`. True when the conversation holds
    /// messages the user hasn't seen. Suppressed while the user is already
    /// on Messages: the thread itself is visible there (and arrival marks
    /// the chat read anyway), so a persistent dot would be redundant.
    var hasMessagesAttentionBadge: Bool {
        if selectedTab == .messages { return false }
        return hasUnreadMessages
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
        let leaseSignal = leaseRequests.contains { lr in
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
        if leaseSignal { return true }

        // Purchase-side signals: seller with a fresh offer, buyer waiting to
        // sign / pay / inspect.
        return purchaseRequests.contains { pr in
            if pr.sellerId == currentUserId, pr.status == .requested { return true }
            if pr.buyerId == currentUserId {
                switch pr.status {
                case .bosPendingBuyer, .bosSigned, .awaitingInspection:
                    return true
                default: return false
                }
            }
            if pr.sellerId == currentUserId {
                switch pr.status {
                case .bosPendingSeller, .paymentAuthorized, .handoverScheduled:
                    return true
                default: return false
                }
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
        // The vehicle-return cache is keyed by lease id, so reload it any
        // time the lease list changes shape. Cheap; bounded to one HTTP
        // call per lease, and most chats have exactly one lease row.
        await loadVehicleReturnsForChatLeases()
        // Same rationale for handovers: gate the chat-card pickup CTA on
        // owner-confirmed and stay in sync with the Today card without
        // duplicating fetch machinery per surface.
        await loadKeyHandoversForChatLeases()
    }

    /// Fetches the current user's active key handovers and indexes them by
    /// lease_request_id so LeaseRequestCardView can gate the pickup CTA on
    /// `.ownerConfirmed`. Best-effort — errors leave the cache empty (the
    /// CTA then stays in "Waiting for the owner…" mode, which is the safe
    /// default).
    func loadKeyHandoversForChatLeases() async {
        do {
            let resp = try await apiClient.fetchKeyHandoversToday()
            var map: [UUID: KeyHandover] = [:]
            for api in resp.keyHandovers {
                let kh = api.toDomain()
                map[kh.leaseRequestId] = kh
            }
            keyHandoversByLease = map
        } catch {
            #if DEBUG
            print("[ChatVM] loadKeyHandoversForChatLeases error: \(error)")
            #endif
        }
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
            // End the Lock Screen / Dynamic Island Live Activity immediately
            // — the user just confirmed in-app, so the outside-the-app
            // countdown is now misleading. The Today reconciler would
            // also catch this on the next websocket-driven refetch, but
            // explicit end here gives the snappiest UX.
            if #available(iOS 16.1, *) {
                PickupLiveActivityManager.shared.end(
                    leaseRequestId: id,
                    reason: .pickupConfirmed
                )
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

    // MARK: - Vehicle Returns

    /// Refresh the vehicle-return cache for every lease this chat currently
    /// knows about. Cheap-but-not-free; called on every WS event, on view
    /// appear, and after every return mutation.
    func loadVehicleReturnsForChatLeases() async {
        // Snapshot the lease ids up front so we're not racing the
        // `loadLeaseRequests` task that's about to mutate the list.
        let leaseIds = leaseRequests.map(\.id)
        guard !leaseIds.isEmpty else {
            vehicleReturnsByLease = [:]
            return
        }
        isLoadingVehicleReturns = true
        defer { isLoadingVehicleReturns = false }

        var updated: [UUID: VehicleReturn] = [:]
        for id in leaseIds {
            do {
                if let api = try await apiClient.fetchVehicleReturnForLease(leaseRequestId: id) {
                    updated[id] = api.toDomain()
                }
            } catch {
                #if DEBUG
                print("[ChatViewModel] fetchVehicleReturnForLease(\(id)) error: \(error)")
                #endif
            }
        }
        vehicleReturnsByLease = updated
    }

    /// Driver-side initiator from the chat lease card. Optimistic UX is
    /// handled by an immediate refetch — the backend is the source of truth
    /// and the WS broadcast also nudges OwnerTodayViewModel.
    func submitVehicleReturn(leaseRequestId: UUID) async {
        guard submittingVehicleReturnLeaseId == nil else { return }
        submittingVehicleReturnLeaseId = leaseRequestId
        defer { submittingVehicleReturnLeaseId = nil }
        do {
            let resp = try await apiClient.initiateVehicleReturn(leaseRequestId: leaseRequestId)
            vehicleReturnsByLease[leaseRequestId] = resp.toDomain()
        } catch {
            self.error = describeError(error)
        }
        // Lease row gets `vehicle_returned_at` once the flow finishes, so
        // refresh the lease list too — the LeaseRequestCardView reads from
        // it for some of its gating.
        await loadLeaseRequests()
    }

    /// Driver-side undo. Only valid within the 5-minute window; backend
    /// rejects with 409 once it lapses (we just refetch on error).
    func cancelVehicleReturn(leaseRequestId: UUID) async {
        guard let vReturn = vehicleReturnsByLease[leaseRequestId] else { return }
        guard submittingVehicleReturnLeaseId == nil else { return }
        submittingVehicleReturnLeaseId = leaseRequestId
        defer { submittingVehicleReturnLeaseId = nil }
        do {
            let resp = try await apiClient.cancelVehicleReturn(returnId: vReturn.id)
            vehicleReturnsByLease[leaseRequestId] = resp.toDomain()
        } catch {
            self.error = describeError(error)
            await loadVehicleReturnsForChatLeases()
        }
    }

    /// Owner-side confirm from inside the chat. Backend triggers the
    /// refund pipeline; the WS event will sync the Today tab on the other
    /// device.
    func confirmVehicleReturn(leaseRequestId: UUID) async {
        guard let vReturn = vehicleReturnsByLease[leaseRequestId] else { return }
        guard submittingVehicleReturnLeaseId == nil else { return }
        submittingVehicleReturnLeaseId = leaseRequestId
        defer { submittingVehicleReturnLeaseId = nil }
        do {
            let resp = try await apiClient.confirmVehicleReturn(returnId: vReturn.id)
            vehicleReturnsByLease[leaseRequestId] = resp.toDomain()
        } catch {
            self.error = describeError(error)
        }
        await loadLeaseRequests()
    }

    /// Owner-side dispute (called from the chat sheet, mirroring the Today
    /// dispute flow). The caller is expected to pass a trimmed 5-500 char
    /// `reason` — the backend re-validates.
    /// Returns the failure message on error so the dispute sheet can keep
    /// itself up with a spinner during the round-trip and only auto-dismiss
    /// after the server has actually accepted the dispute. Returns nil on
    /// success. Also still writes failures to `self.error` for callers that
    /// rely on the existing banner.
    func disputeVehicleReturn(leaseRequestId: UUID, reason: String) async -> String? {
        guard let vReturn = vehicleReturnsByLease[leaseRequestId] else {
            return "We couldn't find an active return for this lease."
        }
        guard submittingVehicleReturnLeaseId == nil else { return nil }
        submittingVehicleReturnLeaseId = leaseRequestId
        defer { submittingVehicleReturnLeaseId = nil }
        do {
            let resp = try await apiClient.disputeVehicleReturn(returnId: vReturn.id, reason: reason)
            vehicleReturnsByLease[leaseRequestId] = resp.toDomain()
            return nil
        } catch {
            let msg = describeError(error)
            self.error = msg
            return msg
        }
    }

    // MARK: - Purchase Requests

    /// Fetches the chat-scoped purchase requests.  Silent-failure on 404 /
    /// legacy backends that don't yet ship the endpoint, so older builds
    /// don't light up an error banner.
    func loadPurchaseRequests() async {
        isLoadingPurchaseRequests = true
        defer { isLoadingPurchaseRequests = false }
        do {
            let response = try await apiClient.fetchPurchaseRequestsForChat(chatId: chatId)
            purchaseRequests = response.purchaseRequests.map { $0.toDomain() }
        } catch {
            #if DEBUG
            print("[ChatViewModel] loadPurchaseRequests error: \(error)")
            #endif
        }
        // Populate BoS cache for every purchase that already has a row
        // server-side (accepted → bos_signed). The card and wizard both
        // read from this map, so hydrating it here means the sheet opens
        // with correct data on the very first tap.
        await refreshBillOfSaleCacheForChat()
    }

    /// Fetches (or refetches) the BoS row for a single purchase request
    /// and stores it in `billOfSalesByPurchase`. Silent on 404 — the
    /// backend returns 404 before the seller has accepted, which is the
    /// expected state during .requested/.declined/.cancelled.
    func fetchBillOfSale(purchaseRequestId: UUID) async {
        do {
            let response = try await apiClient.getBillOfSale(
                purchaseRequestId: purchaseRequestId
            )
            billOfSalesByPurchase[purchaseRequestId] = response.toDomain()
        } catch let apiError as APIError {
            if case .serverError(_, let msg) = apiError,
               msg.lowercased().contains("not found") {
                return
            }
            #if DEBUG
            print("[ChatViewModel] fetchBillOfSale(\(purchaseRequestId)) error: \(apiError)")
            #endif
        } catch {
            #if DEBUG
            print("[ChatViewModel] fetchBillOfSale(\(purchaseRequestId)) error: \(error)")
            #endif
        }
    }

    /// Refresh the BoS cache for every purchase in this chat that is
    /// past `requested`. Bounded to at most one HTTP call per open
    /// purchase; almost all chats have zero or one purchase request.
    func refreshBillOfSaleCacheForChat() async {
        let openStatuses: Set<PurchaseRequestStatus> = [
            .accepted, .bosPendingSeller, .bosPendingBuyer, .bosSigned,
            .paymentAuthorized, .handoverScheduled, .awaitingInspection,
            .inspectionAccepted, .inspectionRejected, .completed
        ]
        let targets = purchaseRequests.filter { openStatuses.contains($0.status) }
        for pr in targets {
            await fetchBillOfSale(purchaseRequestId: pr.id)
        }
    }

    func acceptPurchaseRequest(id: UUID) async {
        do {
            let response = try await apiClient.acceptPurchaseRequest(id: id)
            upsertPurchaseRequest(response.toDomain())
        } catch {
            self.error = describeError(error)
        }
    }

    func declinePurchaseRequest(id: UUID, reason: String? = nil) async {
        do {
            let response = try await apiClient.declinePurchaseRequest(id: id, reason: reason)
            upsertPurchaseRequest(response.toDomain())
        } catch {
            self.error = describeError(error)
        }
    }

    func cancelPurchaseRequest(id: UUID) async {
        do {
            let response = try await apiClient.cancelPurchaseRequest(id: id)
            upsertPurchaseRequest(response.toDomain())
        } catch {
            self.error = describeError(error)
        }
    }

    func confirmKeysHandedOver(purchaseRequestId: UUID) async {
        do {
            let response = try await apiClient.confirmKeysHandedOver(
                purchaseRequestId: purchaseRequestId
            )
            upsertPurchaseRequest(response.toDomain())
        } catch {
            self.error = describeError(error)
        }
    }

    func scheduleHandover(
        purchaseRequestId: UUID,
        scheduledAt: Date,
        location: String,
        latitude: Double?,
        longitude: Double?
    ) async {
        do {
            let response = try await apiClient.scheduleHandover(
                purchaseRequestId: purchaseRequestId,
                scheduledAt: scheduledAt,
                location: location,
                latitude: latitude,
                longitude: longitude
            )
            upsertPurchaseRequest(response.toDomain())
        } catch {
            self.error = describeError(error)
        }
    }

    func refreshPurchase(id: UUID) async {
        do {
            let response = try await apiClient.fetchPurchaseRequest(id: id)
            upsertPurchaseRequest(response.toDomain())
        } catch {
            #if DEBUG
            print("[ChatViewModel] refreshPurchase error: \(error)")
            #endif
        }
    }

    /// Insert-or-replace a purchase request in the local cache and re-sort
    /// by createdAt so the most recent one leads.
    func upsertPurchaseRequest(_ purchase: PurchaseRequest) {
        if let idx = purchaseRequests.firstIndex(where: { $0.id == purchase.id }) {
            purchaseRequests[idx] = purchase
        } else {
            purchaseRequests.insert(purchase, at: 0)
        }
    }

    // MARK: - Read

    func markAsRead() async {
        _ = try? await apiClient.markChatRead(chatId: chatId)
    }

    /// Tab-aware read bookkeeping, fired from `selectedTab.didSet`.
    ///
    /// Messages became visible → the user is now reading the thread, so
    /// stamp the server read marker (POST /read also drains the matching
    /// bell/APNs chat_message notifications backend-side), zero the local
    /// badges, and suppress Chats-list increments for live arrivals.
    ///
    /// Requests became visible → the user is explicitly NOT reading
    /// messages: stop claiming "actively reading" so a message arriving
    /// mid-dwell bumps both the Messages-tab dot and the Chats-list badge.
    private func handleTabChange() {
        switch selectedTab {
        case .messages:
            ChatsListViewModel.shared.activelyReadingChatId = chatId
            hasUnreadMessages = false
            ChatsListViewModel.shared.markChatRead(chatId)
            Task { await markAsRead() }
        case .requests:
            if ChatsListViewModel.shared.activelyReadingChatId == chatId {
                ChatsListViewModel.shared.activelyReadingChatId = nil
            }
        }
    }

    /// Open-path read gating, called once per appearance from ChatView's
    /// `.task` — AFTER `.onAppear` has applied `initialTab`, so the check
    /// reflects the tab the user actually landed on. Landing on Messages
    /// marks the chat read exactly like the old unconditional path; landing
    /// on Requests leaves `last_read_at` (and its bell/APNs cascade)
    /// untouched and instead seeds the Messages dot from the server-side
    /// unread count.
    func handleChatOpened() async {
        if selectedTab == .messages {
            hasUnreadMessages = false
            await markAsRead()
            ChatsListViewModel.shared.markChatRead(chatId)
        } else {
            await refreshUnreadState()
        }
    }

    /// Re-derives `hasUnreadMessages` from the server's read model — the
    /// per-chat `unread_count` in GET /chats, which the backend computes
    /// from `chat_participants.last_read_at`. Prefers the already-fetched
    /// ChatsListViewModel cache; falls back to refetching the list for
    /// deep-link opens where the Chats tab was never visited. Pass
    /// `forceRefetch` when local state can't be trusted (WS reconnect).
    func refreshUnreadState(forceRefetch: Bool = false) async {
        if !forceRefetch,
           let summary = ChatsListViewModel.shared.chats.first(where: { $0.id == chatId }) {
            hasUnreadMessages = summary.unreadCount > 0
            return
        }
        await ChatsListViewModel.shared.fetchChats()
        let unread = ChatsListViewModel.shared.chats
            .first(where: { $0.id == chatId })?.unreadCount ?? 0
        hasUnreadMessages = unread > 0
    }

    /// Server-side resync after a WS drop. Messages that arrived during the
    /// outage were never delivered on `newMessagePublisher`, so both the
    /// local thread and the unread dot may be stale.
    ///
    ///   - Messages tab visible on-screen → reload the thread so the missed
    ///     rows actually render, then stamp the read marker (matching what
    ///     the live-arrival path would have done).
    ///   - Otherwise → reload the thread silently and re-derive the dot
    ///     from the server's unread_count instead of trusting WS history.
    private func resyncAfterReconnect() async {
        await loadInitialMessages()
        if selectedTab == .messages,
           ChatsListViewModel.shared.activelyReadingChatId == chatId {
            hasUnreadMessages = false
            await markAsRead()
            ChatsListViewModel.shared.markChatRead(chatId)
        } else {
            await refreshUnreadState(forceRefetch: true)
        }
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
