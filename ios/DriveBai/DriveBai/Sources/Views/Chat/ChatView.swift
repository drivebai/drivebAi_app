import SwiftUI
import PhotosUI
import UniformTypeIdentifiers
import StripePaymentSheet

struct ChatView: View {
    let chatId: UUID
    let currentUserId: UUID
    let counterpartyId: UUID
    let counterpartyName: String

    @StateObject private var viewModel: ChatViewModel
    @EnvironmentObject private var authStore: AuthStore
    /// Observed so the illustrative Requests dot appears and self-clears in
    /// step with the chatSegments coach mark. Read-only — no tour is started
    /// or mutated from here.
    @ObservedObject private var tourCoordinator = ProductTourCoordinator.shared
    @State private var showRequestComposer = false
    @State private var showDetails = false
    @State private var showProfile = false
    @State private var showPaymentSheet = false
    @State private var paymentIntentResponse: PaymentIntentAPIResponse?
    @State private var paymentLeaseRequestId: UUID?
    @State private var adjustPriceLeaseRequest: LeaseRequest?
    /// Owner-side vehicle-return dispute target. Bound to a sheet so the
    /// owner can type the reason before we round-trip to the backend.
    @State private var disputeVehicleReturnForLease: LeaseRequest?

    // Purchase-flow sheet state — driven by PurchaseRequestCardView taps.
    @State private var billOfSalePurchase: PurchaseRequest?
    @State private var billOfSaleSeed: BillOfSale?
    @State private var billOfSaleInitialStep: BoSStep?
    @State private var choosePaymentPurchase: PurchaseRequest?
    @State private var inspectPurchase: PurchaseRequest?
    @State private var scheduleHandoverPurchase: PurchaseRequest?

    // Plus-menu / attachment / accident state
    @State private var showPlusMenu = false
    @State private var pendingPlusAction: ChatPlusAction?
    @State private var showAccidentReport = false
    @State private var showPhotoPicker = false
    @State private var photoPickerItems: [PhotosPickerItem] = []
    @State private var showFileImporter = false
    @State private var relatedCarId: UUID?

    /// Tab to focus when the view appears. Default `nil` keeps the
    /// ViewModel's own default (.messages). Pass `.requests` from surfaces
    /// that exist to surface a pending lease request — e.g. the owner
    /// Today "Go to requests" button.
    private let initialTab: ChatTab?

    init(
        chatId: UUID,
        currentUserId: UUID,
        counterpartyId: UUID,
        counterpartyName: String,
        initialTab: ChatTab? = nil
    ) {
        self.chatId = chatId
        self.currentUserId = currentUserId
        self.counterpartyId = counterpartyId
        self.counterpartyName = counterpartyName
        self.initialTab = initialTab
        _viewModel = StateObject(wrappedValue: ChatViewModel(chatId: chatId, currentUserId: currentUserId))
    }

    /// Purely illustrative red dot shown next to the Requests segment ONLY
    /// while the chatSegments coach mark is on its "dot" step, so the user
    /// sees exactly what the real attention dot looks like. It is derived live
    /// from the coordinator, so it appears when that step is reached and
    /// self-clears the instant the step advances. It never sets unread /
    /// attention state, never mutates the ChatViewModel, and never touches
    /// backend counts. If the real Requests badge is already showing it wins —
    /// the single `showBadge` Circle draws once regardless.
    private var tutorialRequestsDot: Bool {
        tourCoordinator.activeTour == .chatSegments
            && tourCoordinator.currentStep?.id == "dot"
    }

    var body: some View {
        VStack(spacing: 0) {
            // Tab picker. Custom segmented control instead of SwiftUI's
            // `.segmented` Picker so we can overlay an unread/attention dot
            // on the tab labels without fighting UIKit's segmented
            // renderer. Visually matches the native segmented look the
            // screen used before.
            ChatTabsBar(
                selection: $viewModel.selectedTab,
                requestsHasBadge: viewModel.hasRequestsAttentionBadge || tutorialRequestsDot,
                messagesHasBadge: viewModel.hasMessagesAttentionBadge
            )
            .padding(.horizontal)
            .padding(.vertical, 8)

            // Content
            switch viewModel.selectedTab {
            case .messages:
                messagesContent
            case .requests:
                requestsContent
            }
        }
        .onChange(of: viewModel.selectedTab) { _, tab in
            // Actually looking at Requests is the real "found where you pay"
            // event. Seeing the coach mark that describes it is not.
            if tab == .requests {
                ProductTourCoordinator.shared.recordMilestone(.openedRequestsTab)
            }
        }
        .navigationTitle(counterpartyName)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .navigationBarTrailing) {
                Menu {
                    Button { showProfile = true } label: {
                        Label("View Profile", systemImage: "person.circle")
                    }
                    Button { showDetails = true } label: {
                        Label("Chat Details", systemImage: "info.circle")
                    }
                } label: {
                    Image(systemName: "ellipsis.circle")
                }
            }
        }
        .sheet(isPresented: $showPlusMenu, onDismiss: handlePendingPlusAction) {
            ChatPlusMenuSheet { pendingPlusAction = $0 }
        }
        .sheet(isPresented: $showRequestComposer) {
            if let user = authStore.state.user {
                RequestComposerSheet(chatId: chatId, userRole: user.role) {
                    await viewModel.loadRequests()
                }
            }
        }
        .sheet(isPresented: $showAccidentReport) {
            AccidentReportView(relatedChatId: chatId, relatedCarId: relatedCarId)
                .environmentObject(authStore)
        }
        .photosPicker(
            isPresented: $showPhotoPicker,
            selection: $photoPickerItems,
            maxSelectionCount: 10,
            selectionBehavior: .ordered,
            matching: .images
        )
        .onChange(of: photoPickerItems) { _, items in
            guard !items.isEmpty else { return }
            Task { await handlePickedPhotos(items) }
        }
        .fileImporter(
            isPresented: $showFileImporter,
            allowedContentTypes: [.pdf, .image, .data, .text, .plainText, .rtf],
            allowsMultipleSelection: false
        ) { result in
            Task { await handlePickedFile(result) }
        }
        .sheet(item: $adjustPriceLeaseRequest) { leaseReq in
            AdjustPriceSheet(leaseRequest: leaseReq) { newPrice in
                Task { await viewModel.adjustLeasePrice(id: leaseReq.id, offeredWeeklyPrice: newPrice) }
            }
        }
        .sheet(item: $disputeVehicleReturnForLease) { leaseReq in
            if let vReturn = viewModel.vehicleReturnsByLease[leaseReq.id] {
                VehicleReturnDisputeSheet(vehicleReturn: vReturn) { reason in
                    await viewModel.disputeVehicleReturn(leaseRequestId: leaseReq.id, reason: reason)
                }
            }
        }
        .sheet(item: $billOfSalePurchase) { purchase in
            BillOfSaleFlowView(
                purchaseRequest: purchase,
                currentUserId: currentUserId,
                initialBoS: billOfSaleSeed,
                initialStep: billOfSaleInitialStep,
                onBoSUpdated: { bos in
                    viewModel.billOfSalesByPurchase[bos.purchaseRequestId] = bos
                },
                onPurchaseUpdated: { updated in
                    viewModel.upsertPurchaseRequest(updated)
                },
                externalBoSStream: viewModel.billOfSalesByPurchase[purchase.id]
            )
        }
        .sheet(item: $choosePaymentPurchase) { purchase in
            ChoosePaymentOptionView(
                purchaseRequest: purchase,
                onPurchaseUpdated: { updated in
                    viewModel.upsertPurchaseRequest(updated)
                },
                onDismiss: {
                    Task { await viewModel.refreshPurchase(id: purchase.id) }
                }
            )
        }
        .sheet(item: $inspectPurchase) { purchase in
            InspectionView(
                purchaseRequest: purchase,
                onPurchaseUpdated: { updated in
                    viewModel.upsertPurchaseRequest(updated)
                }
            )
        }
        .sheet(item: $scheduleHandoverPurchase) { purchase in
            ScheduleHandoverSheet(
                purchaseRequest: purchase,
                onSubmit: { scheduledAt, location, latitude, longitude in
                    await viewModel.scheduleHandover(
                        purchaseRequestId: purchase.id,
                        scheduledAt: scheduledAt,
                        location: location,
                        latitude: latitude,
                        longitude: longitude
                    )
                }
            )
        }
        .navigationDestination(isPresented: $showDetails) {
            ChatDetailsView(chatId: chatId)
        }
        .navigationDestination(isPresented: $showProfile) {
            CounterpartyProfileView(userId: counterpartyId)
        }
        .onAppear {
            // Apply the caller-requested initial tab once on first appear.
            // We don't overwrite the user's later picks, so re-appearing
            // (e.g. push/pop in the nav stack) keeps their last selection.
            // MUST run before the activelyReadingChatId decision below —
            // landing straight on Requests (owner Today "Go to requests")
            // is not "reading messages" and must keep the Chats-list badge
            // and server read-state intact.
            if let initial = initialTab, viewModel.didApplyInitialTab == false {
                viewModel.selectedTab = initial
                viewModel.didApplyInitialTab = true
            }
            // Landing directly on Requests (deep link, post-request navigation)
            // never fires `.onChange`, so record the milestone here too.
            if viewModel.selectedTab == .requests {
                ProductTourCoordinator.shared.recordMilestone(.openedRequestsTab)
            }
            // Only suppress list-badge increments while the Messages tab is
            // actually visible. Tab switches keep this in sync via the
            // ViewModel's selectedTab.didSet.
            if viewModel.selectedTab == .messages {
                ChatsListViewModel.shared.activelyReadingChatId = chatId
            }
        }
        .onDisappear {
            if ChatsListViewModel.shared.activelyReadingChatId == chatId {
                ChatsListViewModel.shared.activelyReadingChatId = nil
            }
        }
        .task {
            await viewModel.loadInitialMessages()
            async let reqTask: () = viewModel.loadRequests()
            async let leaseTask: () = viewModel.loadLeaseRequests()
            async let purchaseTask: () = viewModel.loadPurchaseRequests()
            async let sharedDocsTask: () = viewModel.loadSharedDocuments()
            async let carIdTask: () = loadRelatedCarId()
            _ = await (reqTask, leaseTask, purchaseTask, sharedDocsTask, carIdTask)
            // A chat that carries a lease/purchase request is the trigger for the
            // Messages-vs-Requests explainer. Deduped + suppressed while the
            // higher-priority post-request teach is running.
            if hasAnyRequests {
                ProductTourCoordinator.shared.handle(.chatWithRequestOpened)
            }
            // Tab-aware read gating: marks the chat read only when the user
            // landed on Messages; a Requests landing instead seeds the
            // Messages-tab unread dot from the server's unread count.
            // `.onAppear` has already applied `initialTab` by the time this
            // runs, so the check sees the real landing tab.
            await viewModel.handleChatOpened()
        }
        .alert("Error", isPresented: .constant(viewModel.error != nil)) {
            Button("OK") { viewModel.error = nil }
        } message: {
            if let error = viewModel.error {
                Text(error)
            }
        }
        .background {
            // Zero-size overlay that presents Stripe PaymentSheet natively (no double-modal)
            if showPaymentSheet,
               let response = paymentIntentResponse,
               let customerId = response.customerId,
               let ephemeralKey = response.ephemeralKeySecret {
                PaymentSheetPresenter(
                    clientSecret: response.paymentIntentClientSecret,
                    ephemeralKeySecret: ephemeralKey,
                    customerId: customerId,
                    publishableKey: response.publishableKey
                ) { result in
                    showPaymentSheet = false
                    handlePaymentResult(result)
                }
                .frame(width: 0, height: 0)
            }
        }
    }

    // MARK: - Messages Tab

    private var messagesContent: some View {
        VStack(spacing: 0) {
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(spacing: 2) {
                        if viewModel.hasMoreMessages {
                            ProgressView()
                                .padding()
                                .onAppear {
                                    Task { await viewModel.loadMoreMessages() }
                                }
                        }

                        ForEach(viewModel.messages) { message in
                            ChatBubbleView(
                                message: message,
                                onRetry: { failed in
                                    Task { await viewModel.retrySend(message: failed) }
                                }
                            )
                            .id(message.id)
                        }
                    }
                    .padding(.horizontal)
                }
                .defaultScrollAnchor(.bottom)
                .onChange(of: viewModel.messages.count) { _, _ in
                    if let lastId = viewModel.messages.last?.id {
                        withAnimation(.easeOut(duration: 0.2)) {
                            proxy.scrollTo(lastId, anchor: .bottom)
                        }
                    }
                }
            }

            // Input bar
            ChatInputBar(
                text: $viewModel.messageText,
                onSend: { Task { await viewModel.sendMessage() } },
                onRequestTap: { showPlusMenu = true }
            )
        }
    }

    // MARK: - Requests Tab

    private var hasAnyRequests: Bool {
        !viewModel.requests.isEmpty
            || !viewModel.leaseRequests.isEmpty
            || !viewModel.purchaseRequests.isEmpty
    }

    private var isLoadingAnyRequests: Bool {
        viewModel.isLoadingRequests
            || viewModel.isLoadingLeaseRequests
            || viewModel.isLoadingPurchaseRequests
    }

    /// The current user is the car owner in this chat if they appear as the
    /// ownerId on at least one lease request. This avoids needing an extra
    /// chat-details lookup just to decide whether to show driver docs.
    private var currentUserIsOwner: Bool {
        viewModel.leaseRequests.contains { $0.ownerId == currentUserId }
    }

    private var shouldShowDriverDocs: Bool {
        currentUserIsOwner && !viewModel.sharedDocuments.isEmpty
    }

    /// Driver side of the chat sees the LISTING's car documents, not the
    /// driver-onboarding docs. Owner already has full access to their own
    /// car documents from the car detail screen, so we don't re-surface
    /// them here.
    private var shouldShowVehicleDocs: Bool {
        !currentUserIsOwner && !viewModel.vehicleDocuments.isEmpty
    }

    private var requestsContent: some View {
        Group {
            if isLoadingAnyRequests && !hasAnyRequests {
                ProgressView()
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if !hasAnyRequests {
                VStack(spacing: 16) {
                    Spacer()
                    Image(systemName: "doc.text")
                        .font(.system(size: 40))
                        .foregroundColor(.gray.opacity(0.5))
                    Text("No requests yet")
                        .font(.headline)
                    Text("Create a request using the + button in Messages")
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                        .multilineTextAlignment(.center)
                    Spacer()
                }
                .padding()
            } else {
                ScrollView {
                    LazyVStack(spacing: 12) {
                        // Role-aware doc section. Owner sees the driver's
                        // license; driver sees the car's papers
                        // (registration, insurance, …). The two are
                        // mutually exclusive per chat — the user is on
                        // one side, not both.
                        if shouldShowDriverDocs {
                            DriverDocumentsSection(documents: viewModel.sharedDocuments)
                        } else if shouldShowVehicleDocs {
                            VehicleDocumentsSection(documents: viewModel.vehicleDocuments)
                        }

                        // Lease requests first
                        ForEach(viewModel.leaseRequests) { leaseReq in
                            leaseRequestCard(for: leaseReq)
                        }

                        // Purchase requests
                        ForEach(viewModel.purchaseRequests) { purchase in
                            PurchaseRequestCardView(
                                purchaseRequest: purchase,
                                currentUserId: currentUserId,
                                billOfSale: viewModel.billOfSalesByPurchase[purchase.id],
                                onAction: { action in
                                    handlePurchaseCardAction(purchase: purchase, action: action)
                                }
                            )
                        }

                        // Regular chat requests
                        ForEach(viewModel.requests) { request in
                            RequestCardView(
                                request: request,
                                currentUserId: currentUserId,
                                onAccept: {
                                    Task { await viewModel.respondToRequest(requestId: request.id, action: .accept) }
                                },
                                onDecline: {
                                    Task { await viewModel.respondToRequest(requestId: request.id, action: .decline) }
                                }
                            )
                        }
                    }
                    .padding()
                }
                .refreshable {
                    // Pull-to-refresh the Requests tab. WS updates already
                    // re-fetch automatically, but manual refresh is the
                    // escape hatch when an out-of-band change (admin edit,
                    // backfill, missed WS event) leaves the cached
                    // pickup_confirmed_at / vehicle_return state stale
                    // and the user is staring at the wrong CTA.
                    async let leases: () = viewModel.loadLeaseRequests()
                    async let reqs:   () = viewModel.loadRequests()
                    async let docs:   () = viewModel.loadSharedDocuments()
                    async let vrets:  () = viewModel.loadVehicleReturnsForChatLeases()
                    async let purs:   () = viewModel.loadPurchaseRequests()
                    _ = await (leases, reqs, docs, vrets, purs)
                }
            }
        }
    }

    // MARK: - Lease Request Card builder

    /// Builds the lease-request card for the Requests tab. Extracted from the
    /// inline `ForEach` so the type-checker can resolve it in reasonable time
    /// — `LeaseRequestCardView` has 17+ closure parameters which used to push
    /// the inferred type past the compiler's complexity limit.
    @ViewBuilder
    private func leaseRequestCard(for leaseReq: LeaseRequest) -> some View {
        LeaseRequestCardView(
            leaseRequest: leaseReq,
            currentUserId: currentUserId,
            onAccept: {
                Task { await viewModel.acceptLeaseRequest(id: leaseReq.id) }
            },
            onDecline: {
                Task { await viewModel.declineLeaseRequest(id: leaseReq.id) }
            },
            onPay: {
                Task { await handlePayment(for: leaseReq) }
            },
            onCancel: {
                Task { await viewModel.cancelLeaseRequest(id: leaseReq.id) }
            },
            onAdjustPrice: {
                adjustPriceLeaseRequest = leaseReq
            },
            onConfirmPickup: {
                Task { await viewModel.confirmPickup(id: leaseReq.id) }
            },
            onRescindAccept: {
                Task { await viewModel.rescindAcceptedLeaseRequest(id: leaseReq.id) }
            },
            onExtendPickup: { minutes in
                Task { await viewModel.extendPickupDeadline(id: leaseReq.id, minutes: minutes) }
            },
            onAcceptPriceChange: {
                Task { await viewModel.acceptPriceChange(id: leaseReq.id) }
            },
            onDeclinePriceChange: {
                Task { await viewModel.declinePriceChange(id: leaseReq.id) }
            },
            vehicleReturn: viewModel.vehicleReturnsByLease[leaseReq.id],
            onStartReturn: {
                Task { await viewModel.submitVehicleReturn(leaseRequestId: leaseReq.id) }
            },
            onCancelReturn: {
                Task { await viewModel.cancelVehicleReturn(leaseRequestId: leaseReq.id) }
            },
            onConfirmReturn: {
                Task { await viewModel.confirmVehicleReturn(leaseRequestId: leaseReq.id) }
            },
            onDisputeReturn: {
                disputeVehicleReturnForLease = leaseReq
            },
            isReturnSubmitting: viewModel.submittingVehicleReturnLeaseId == leaseReq.id,
            keyHandover: viewModel.keyHandoversByLease[leaseReq.id]
        )
    }

    // MARK: - Plus menu / attachments

    private func handlePendingPlusAction() {
        guard let action = pendingPlusAction else { return }
        pendingPlusAction = nil
        switch action {
        case .createRequest:  showRequestComposer = true
        case .reportAccident: showAccidentReport = true
        case .attachPhoto:    showPhotoPicker = true
        case .attachFile:     showFileImporter = true
        }
    }

    private func loadRelatedCarId() async {
        if let details = try? await APIClient.shared.fetchChatDetails(chatId: chatId) {
            relatedCarId = details.car.id
        }
    }

    private func handlePickedPhotos(_ items: [PhotosPickerItem]) async {
        // Clear selection up front so the picker can re-open immediately even
        // while uploads are still running in the background.
        defer { photoPickerItems = [] }

        // Decode each item to (data, filename, mime). itemIdentifier is a
        // PhotoKit asset id with slashes, so we mint our own UUID-based
        // filename and derive the MIME + extension from the picker item's
        // supportedContentTypes (HEIC/PNG/GIF correct, not always JPEG).
        var attachments: [ChatViewModel.PendingAttachment] = []
        for item in items {
            guard let data = try? await item.loadTransferable(type: Data.self) else { continue }
            let utt = item.supportedContentTypes.first
            let mime = utt?.preferredMIMEType ?? "image/jpeg"
            let ext = utt?.preferredFilenameExtension ?? "jpg"
            let filename = "photo-\(UUID().uuidString.prefix(8)).\(ext)"
            attachments.append(ChatViewModel.PendingAttachment(data: data, filename: filename, mimeType: mime))
        }
        guard !attachments.isEmpty else { return }
        await viewModel.sendAttachments(attachments)
    }

    private func handlePickedFile(_ result: Result<[URL], Error>) async {
        switch result {
        case .failure(let err):
            viewModel.error = err.localizedDescription
        case .success(let urls):
            guard let url = urls.first else { return }
            let didStart = url.startAccessingSecurityScopedResource()
            defer { if didStart { url.stopAccessingSecurityScopedResource() } }
            guard let data = try? Data(contentsOf: url) else {
                viewModel.error = "Couldn't read the selected file."
                return
            }
            let filename = url.lastPathComponent
            let mime = UTType(filenameExtension: url.pathExtension)?.preferredMIMEType
                ?? "application/octet-stream"
            await viewModel.sendAttachment(data: data, filename: filename, mimeType: mime)
        }
    }

    // MARK: - Purchase card actions

    private func handlePurchaseCardAction(purchase: PurchaseRequest, action: PurchaseCardAction) {
        switch action {
        case .sellerAccept:
            Task { await viewModel.acceptPurchaseRequest(id: purchase.id) }
        case .sellerDecline:
            Task { await viewModel.declinePurchaseRequest(id: purchase.id) }
        case .buyerCancel:
            Task { await viewModel.cancelPurchaseRequest(id: purchase.id) }
        case .openBillOfSale:
            let bos = viewModel.billOfSalesByPurchase[purchase.id]
            billOfSaleSeed = bos
            billOfSaleInitialStep = nextIncompleteStep(
                bos: bos,
                purchase: purchase,
                isSeller: purchase.sellerId == currentUserId,
                isBuyer: purchase.buyerId == currentUserId
            )
            billOfSalePurchase = purchase
        case .buyerAuthorizePayment:
            choosePaymentPurchase = purchase
        case .sellerScheduleHandover:
            scheduleHandoverPurchase = purchase
        case .sellerMarkHandedOver:
            Task { await viewModel.confirmKeysHandedOver(purchaseRequestId: purchase.id) }
        case .buyerInspect:
            inspectPurchase = purchase
        }
    }

    /// Best-effort "which BoS step should the wizard open on" for the
    /// current user. Called from `openBillOfSale` to skip the user past
    /// steps they've already completed.
    private func nextIncompleteStep(
        bos: BillOfSale?,
        purchase: PurchaseRequest,
        isSeller: Bool,
        isBuyer: Bool
    ) -> BoSStep {
        guard let bos else { return .vehicle }
        if isSeller {
            if bos.vehicleMake.isEmpty || bos.vehicleModel.isEmpty || bos.vin.isEmpty {
                return .vehicle
            }
            if bos.termsConditions.isEmpty { return .sale }
            if bos.sellerName.isEmpty || bos.sellerAddress.isEmpty { return .seller }
            if !bos.sellerHasSigned { return .signature }
            return .review
        }
        if isBuyer {
            if bos.buyerName.isEmpty || bos.buyerAddress.isEmpty { return .buyer }
            if !bos.buyerHasSigned { return .signature }
            return .review
        }
        return .vehicle
    }

    private func handlePayment(for leaseRequest: LeaseRequest) async {
        guard let response = await viewModel.createPaymentIntent(leaseRequestId: leaseRequest.id) else { return }
        paymentIntentResponse = response
        paymentLeaseRequestId = leaseRequest.id
        showPaymentSheet = true
    }

    private func handlePaymentResult(_ result: PaymentSheetResult) {
        let leaseId = paymentLeaseRequestId
        paymentIntentResponse = nil
        paymentLeaseRequestId = nil

        switch result {
        case .completed:
            // Sync with Stripe (bypasses webhook) + poll for status update.
            // Capture the device-clock moment of success so the Live Activity
            // progress bar has a precise start anchor; pass it into the
            // manager as soon as `handlePaymentCompleted` has refreshed the
            // lease and `pickupDeadlineAt` is populated.
            if let leaseId = leaseId {
                let paidAt = Date()
                Task {
                    await viewModel.handlePaymentCompleted(leaseRequestId: leaseId)
                    if #available(iOS 16.1, *),
                       let lr = viewModel.leaseRequests.first(where: { $0.id == leaseId }),
                       lr.isAwaitingPickupConfirmation {
                        let role: PickupActivityAttributes.ViewerRole =
                            (currentUserId == lr.ownerId) ? .owner : .driver
                        PickupLiveActivityManager.shared.startOrUpdate(
                            for: lr,
                            viewerRole: role,
                            startedAtIfNew: paidAt
                        )
                    }
                }
            }
        case .canceled:
            // Refresh to show current state (payment may have been partially created)
            Task { await viewModel.loadLeaseRequests() }
        case .failed(let error):
            viewModel.error = "Payment failed: \(error.localizedDescription)"
            Task { await viewModel.loadLeaseRequests() }
        }
    }
}

// MARK: - Tabs Bar

/// In-chat segmented control with a small red attention dot next to the
/// tab labels — Requests when a lease/purchase needs the user's action,
/// Messages when the conversation has unread messages. Replaces
/// `Picker(.segmented)` so the dot can live inside the bar (a
/// UIKit-rendered Picker doesn't expose per-segment overlays).
/// Styled to match the native segmented look the screen used before.
private struct ChatTabsBar: View {
    @Binding var selection: ChatTab
    let requestsHasBadge: Bool
    let messagesHasBadge: Bool

    var body: some View {
        HStack(spacing: 4) {
            ForEach(ChatTab.allCases, id: \.self) { tab in
                tabButton(tab)
            }
        }
        .padding(4)
        .background(Color(.systemGray5))
        .clipShape(Capsule())
    }

    @ViewBuilder
    private func tabButton(_ tab: ChatTab) -> some View {
        let isSelected = selection == tab
        let showBadge = (tab == .requests && requestsHasBadge)
            || (tab == .messages && messagesHasBadge)
        Button {
            withAnimation(.easeInOut(duration: 0.15)) { selection = tab }
        } label: {
            // Inline label + dot — keeps the indicator visually attached to
            // the word "Requests" instead of pinned to the segment's edge.
            HStack(spacing: 6) {
                Text(tab.rawValue)
                    .font(.subheadline)
                    .fontWeight(isSelected ? .semibold : .regular)
                    .foregroundColor(.primary)

                if showBadge {
                    Circle()
                        .fill(Color.red)
                        .frame(width: 8, height: 8)
                        .overlay(
                            Circle()
                                .stroke(Color(.systemBackground), lineWidth: 1.5)
                        )
                        .transition(.scale.combined(with: .opacity))
                }
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 8)
            .background(
                Capsule()
                    .fill(isSelected ? Color(.systemBackground) : Color.clear)
            )
        }
        .buttonStyle(.plain)
        .accessibilityLabel(showBadge ? "\(tab.rawValue), needs attention" : tab.rawValue)
        .accessibilityAddTraits(isSelected ? .isSelected : [])
        // Coach-mark anchors: Messages vs Requests segments. Each tab maps to a
        // distinct target id so the two don't overwrite each other's anchor.
        .onboardingTarget(tab == .requests ? .chatRequestsSegment : .chatMessagesSegment)
    }
}
