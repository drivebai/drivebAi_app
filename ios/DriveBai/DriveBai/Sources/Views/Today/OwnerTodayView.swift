import SwiftUI

/// Today tab view for Car Owners
/// Displays active listings, tasks to complete, and notifications bell
struct OwnerTodayView: View {
    @StateObject private var viewModel = OwnerTodayViewModel()
    @StateObject private var carsStore = OwnerCarsStore.shared
    @EnvironmentObject private var authStore: AuthStore
    @EnvironmentObject private var deepLinkRouter: DeepLinkRouter
    @State private var showNotifications = false
    @State private var showCreateListing = false
    @State private var selectedCarId: UUID?
    @State private var navigateToChatTask: OnboardingTask?
    @State private var showChat = false
    @State private var notificationChatId: UUID?
    @State private var showNotificationChat = false
    @State private var selectedHandover: KeyHandover?
    /// Lease-pickup deep-link target (owner side mirror of the driver-side
    /// machinery in DriverTodayView). Driven by DeepLinkRouter.pendingLeasePickupId.
    @State private var deepLinkPickupChat: DeepLinkPickupTarget?
    /// Vehicle return the owner currently wants to dispute. Bound to a
    /// modal sheet so the textfield input is captured before the API call.
    @State private var disputeTarget: VehicleReturn?

    /// Get listings from OwnerCarsStore (single source of truth)
    private var listings: [ListingSummary] {
        carsStore.cars.map { $0.toListingSummary }
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: TodayLayout.sectionSpacing) {
                    // Custom header
                    TodayHeaderView(
                        title: "Today",
                        unreadCount: viewModel.unreadNotificationCount,
                        onBellTap: { showNotifications = true }
                    )

                    // Section 1: Active Listings
                    activeListingsSection

                    // Section: Key Handover (shown only when active)
                    keyHandoverSection

                    // Section: Vehicle Return (shown only when active)
                    vehicleReturnSection

                    // Section: Purchase requests (seller-side cards)
                    purchaseSection

                    // Section 2: Actions to Take
                    actionsSection
                }
                .padding(.vertical, TodayLayout.horizontalPadding)
            }
            .background(TodayLayout.pageBackground.ignoresSafeArea())
            .toolbar(.hidden, for: .navigationBar)
            .navigationDestination(isPresented: $showNotifications) {
                NotificationsView(
                    notifications: viewModel.notifications,
                    onOpen: { chatId in
                        guard let chatId else { return }
                        notificationChatId = chatId
                        showNotifications = false
                        DispatchQueue.main.asyncAfter(deadline: .now() + 0.35) {
                            showNotificationChat = true
                        }
                    },
                    onMarkRead: { id in Task { await viewModel.markNotificationRead(id) } },
                    onMarkAllRead: { Task { await viewModel.markAllNotificationsRead() } }
                )
            }
            .navigationDestination(isPresented: $showNotificationChat) {
                if let chatId = notificationChatId,
                   let user = authStore.state.user {
                    ChatView(
                        chatId: chatId,
                        currentUserId: user.id,
                        counterpartyId: UUID(),
                        counterpartyName: "Chat"
                    )
                }
            }
            .sheet(isPresented: $showCreateListing, onDismiss: {
                // Re-fetch cars when the sheet is dismissed to ensure fresh data
                // This ensures images appear immediately after creating a listing
                #if DEBUG
                print("[OwnerTodayView] Create listing sheet dismissed, refreshing cars...")
                #endif
                Task {
                    await carsStore.fetchCars()
                }
            }) {
                CreateListingFlowView()
            }
            .navigationDestination(item: $selectedCarId) { carId in
                CarDetailView(carId: carId)
            }
            .navigationDestination(isPresented: $showChat) {
                if let task = navigateToChatTask,
                   let chatId = task.chatId,
                   let userId = authStore.state.user?.id {
                    ChatView(
                        chatId: chatId,
                        currentUserId: userId,
                        counterpartyId: task.counterpartyId ?? UUID(),
                        counterpartyName: task.counterpartyName ?? task.requestedBy,
                        // Lease requests: jump straight to the Requests tab so
                        // the owner sees the Accept/Decline card on arrival
                        // instead of an empty Messages thread. Other action
                        // types (generic chat requests, etc.) keep the
                        // default Messages-first behaviour.
                        initialTab: task.requestType == "lease_request" ? .requests : nil
                    )
                }
            }
            .navigationDestination(item: $selectedHandover) { handover in
                KeyHandoverDetailView(handover: handover)
            }
            .sheet(item: $disputeTarget) { target in
                VehicleReturnDisputeSheet(vehicleReturn: target) { reason in
                    await viewModel.disputeVehicleReturn(target, reason: reason)
                }
            }
            .navigationDestination(item: $deepLinkPickupChat) { target in
                if let userId = authStore.state.user?.id {
                    ChatView(
                        chatId: target.chatId,
                        currentUserId: userId,
                        counterpartyId: target.counterpartyId,
                        counterpartyName: target.counterpartyName,
                        initialTab: .requests
                    )
                }
            }
            .onChange(of: deepLinkRouter.pendingLeasePickupId) { _, newId in
                handleLeasePickupDeepLink(newId)
            }
            .onChange(of: viewModel.keyHandovers) { _, _ in
                if let leaseId = deepLinkRouter.pendingLeasePickupId {
                    handleLeasePickupDeepLink(leaseId)
                }
            }
            .onChange(of: deepLinkRouter.pendingPurchaseTap) { _, newId in
                handlePurchaseDeepLink(newId)
            }
            .onChange(of: viewModel.purchaseRequests) { _, _ in
                if let id = deepLinkRouter.pendingPurchaseTap {
                    handlePurchaseDeepLink(id)
                }
            }
            .task {
                await carsStore.fetchCars()
                async let actionsTask: () = viewModel.fetchActions()
                async let notifTask: () = viewModel.fetchNotifications()
                _ = await (actionsTask, notifTask)
                viewModel.markActionsSeen()
            }
            .refreshable {
                await viewModel.refresh()
                await carsStore.fetchCars()
            }
        }
    }

    // MARK: - Active Listings Section

    private var activeListingsSection: some View {
        VStack(alignment: .leading, spacing: TodayLayout.headerSpacing) {
            // Section header
            Text("Active listings")
                .font(TodayLayout.sectionTitleFont)
                .foregroundColor(.primary)
                .padding(.horizontal, TodayLayout.horizontalPadding)

            // Listings content
            if !listings.isEmpty {
                // Horizontally scrollable listing cards with add button at end
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: TodayLayout.cardSpacing) {
                        ForEach(listings) { listing in
                            ListingCard(
                                listing: listing,
                                onTap: {
                                    selectedCarId = listing.id
                                },
                                onChatTap: {
                                    print("Chat tapped for: \(listing.title)")
                                },
                                onOptionSelect: { index in
                                    print("Option \(index) selected for: \(listing.title)")
                                }
                            )
                            .frame(
                                width: TodayLayout.activeListingCardWidth,
                                height: TodayLayout.activeListingCardHeight
                            )
                        }

                        // Add new listing button at end — same size as listing cards
                        AddListingButton {
                            showCreateListing = true
                        }
                        .frame(
                            width: TodayLayout.activeListingCardWidth,
                            height: TodayLayout.activeListingCardHeight
                        )
                    }
                    .padding(.horizontal, TodayLayout.horizontalPadding)
                }
            } else {
                // Empty state
                EmptyListingCard(isOwner: true) {
                    showCreateListing = true
                }
                .padding(.horizontal, TodayLayout.horizontalPadding)
            }
        }
    }

    // MARK: - Key Handover Section

    @ViewBuilder
    private var keyHandoverSection: some View {
        if !viewModel.keyHandovers.isEmpty {
            VStack(alignment: .leading, spacing: TodayLayout.headerSpacing) {
                Text("Key handover")
                    .font(TodayLayout.sectionTitleFont)
                    .foregroundColor(.primary)
                    .padding(.horizontal, TodayLayout.horizontalPadding)

                VStack(spacing: TodayLayout.cardSpacing) {
                    ForEach(viewModel.keyHandovers) { handover in
                        KeyHandoverCard(
                            handover: handover,
                            currentTime: viewModel.currentTime,
                            isSubmitting: viewModel.submittingHandoverId == handover.id,
                            onAct: { viewModel.confirmHandover(handover) },
                            onOpen: { selectedHandover = handover },
                            onExtendPickup: { minutes in
                                viewModel.extendPickup(handover: handover, minutes: minutes)
                            },
                            onDismiss: { viewModel.dismissHandover(handover) }
                        )
                    }
                }
                .padding(.horizontal, TodayLayout.horizontalPadding)
            }
        }
    }

    // MARK: - Vehicle Return Section

    @ViewBuilder
    private var vehicleReturnSection: some View {
        if !viewModel.vehicleReturns.isEmpty {
            VStack(alignment: .leading, spacing: TodayLayout.headerSpacing) {
                Text("Vehicle return")
                    .font(TodayLayout.sectionTitleFont)
                    .foregroundColor(.primary)
                    .padding(.horizontal, TodayLayout.horizontalPadding)

                VStack(spacing: TodayLayout.cardSpacing) {
                    ForEach(viewModel.vehicleReturns) { vReturn in
                        VehicleReturnCard(
                            vehicleReturn: vReturn,
                            currentTime: viewModel.currentTime,
                            isSubmitting: viewModel.submittingReturnId == vReturn.id,
                            onAct: { viewModel.actOnVehicleReturn(vReturn) },
                            // Dispute is owner-only; the card already hides the
                            // dispute control for driver-perspective rows, but
                            // we still no-op here for driver rows in case the
                            // card's gating ever changes.
                            onDispute: vReturn.viewerRole == .owner ? { disputeTarget = vReturn } : nil,
                            onOpen: {},
                            onDismiss: { viewModel.dismissVehicleReturn(vReturn) }
                        )
                    }
                }
                .padding(.horizontal, TodayLayout.horizontalPadding)
            }
        }
    }

    // MARK: - Purchase Section

    @ViewBuilder
    private var purchaseSection: some View {
        if !viewModel.purchaseRequests.isEmpty,
           let user = authStore.state.user {
            VStack(alignment: .leading, spacing: TodayLayout.headerSpacing) {
                Text("Purchase requests")
                    .font(TodayLayout.sectionTitleFont)
                    .foregroundColor(.primary)
                    .padding(.horizontal, TodayLayout.horizontalPadding)

                VStack(spacing: TodayLayout.cardSpacing) {
                    ForEach(viewModel.purchaseRequests) { purchase in
                        PurchaseTodayCard(
                            purchaseRequest: purchase,
                            currentUserId: user.id,
                            onOpen: {
                                deepLinkPickupChat = DeepLinkPickupTarget(
                                    chatId: purchase.chatId,
                                    counterpartyId: purchase.buyerId,
                                    counterpartyName: purchase.buyerName
                                )
                            }
                        )
                    }
                }
                .padding(.horizontal, TodayLayout.horizontalPadding)
            }
        }
    }

    // MARK: - Actions Section

    private var actionsSection: some View {
        VStack(alignment: .leading, spacing: TodayLayout.headerSpacing) {
            // Section header
            Text("Actions to take")
                .font(TodayLayout.sectionTitleFont)
                .foregroundColor(.primary)
                .padding(.horizontal, TodayLayout.horizontalPadding)

            // Helper text
            Text("Here you will see important notifications when you have active listings")
                .font(TodayLayout.helperTextFont)
                .foregroundColor(.secondary)
                .padding(.horizontal, TodayLayout.horizontalPadding)

            // Task cards or empty state
            if viewModel.tasks.isEmpty {
                AllDoneView()
                    .padding(.horizontal, TodayLayout.horizontalPadding)
            } else {
                VStack(spacing: TodayLayout.cardSpacing) {
                    ForEach(viewModel.tasks) { task in
                        // Owner side, lease_request action: collapse the
                        // inline Accept/Decline pair into a single
                        // "Review request" CTA that redirects to Chat →
                        // Requests tab. The Chat → Requests tab is the
                        // single source of truth for accept/decline;
                        // having both surfaces invited race conditions
                        // (e.g. accept here while the owner decided in
                        // chat) and made the action card busier than it
                        // needed to be. Same pattern as the key-handover
                        // Today card — Today only routes, the actual
                        // decision lives one tap away. Other action types
                        // (generic chat requests, etc.) keep their
                        // original two-option layout.
                        let displayTask = task.requestType == "lease_request"
                            ? task.withSingleOption("Review request")
                            : task
                        TaskCard(
                            task: displayTask,
                            currentTime: viewModel.currentTime,
                            onOpenTap: {
                                if task.isBackendAction {
                                    navigateToChatTask = task
                                    showChat = true
                                }
                            },
                            onOptionSelect: { index in
                                if task.requestType == "lease_request" {
                                    // Single CTA — always navigate.
                                    navigateToChatTask = task
                                    showChat = true
                                } else if task.isBackendAction {
                                    let action = index == 0 ? "accept" : "decline"
                                    viewModel.respondToAction(task: task, action: action)
                                } else {
                                    viewModel.selectOption(for: task.id, optionIndex: index)
                                }
                            }
                        )
                    }
                }
                .padding(.horizontal, TodayLayout.horizontalPadding)
            }
        }
    }
}

// MARK: - Add Listing Button

/// Button shown at the end of the listings horizontal scroll to add a new listing
private struct AddListingButton: View {
    var onTap: () -> Void

    var body: some View {
        Button(action: onTap) {
            VStack {
                Spacer()
                Image(systemName: "plus")
                    .font(.system(size: 28, weight: .light))
                    .foregroundColor(TodayLayout.tealAccent)
                Spacer()
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(TodayLayout.tealAccentLight)
            .overlay(
                RoundedRectangle(cornerRadius: TodayLayout.cardCornerRadius)
                    .strokeBorder(
                        style: StrokeStyle(lineWidth: 2, dash: [8, 4])
                    )
                    .foregroundColor(TodayLayout.tealAccent.opacity(0.5))
            )
            .cornerRadius(TodayLayout.cardCornerRadius)
        }
        .buttonStyle(PlainButtonStyle())
    }
}

#Preview("With Listings") {
    OwnerTodayView()
}

extension OwnerTodayView {
    /// Owner-side mirror of DriverTodayView.handleLeasePickupDeepLink:
    /// look up the matching handover from the live key-handover list and
    /// route to Chat → Requests if we find one. The driver is the
    /// counterparty on this side.
    fileprivate func handleLeasePickupDeepLink(_ leaseId: UUID?) {
        guard let leaseId else { return }
        guard let handover = viewModel.keyHandovers.first(where: { $0.leaseRequestId == leaseId }),
              let chatId = handover.chatId else { return }
        deepLinkPickupChat = DeepLinkPickupTarget(
            chatId: chatId,
            counterpartyId: handover.driverId,
            counterpartyName: handover.driverName
        )
        deepLinkRouter.clearPendingLeasePickup()
    }

    /// Purchase deep-link mirror.  Seller-side: counterparty is the buyer.
    fileprivate func handlePurchaseDeepLink(_ purchaseId: UUID?) {
        guard let purchaseId else { return }
        guard let purchase = viewModel.purchaseRequests.first(where: { $0.id == purchaseId }) else {
            return
        }
        deepLinkPickupChat = DeepLinkPickupTarget(
            chatId: purchase.chatId,
            counterpartyId: purchase.buyerId,
            counterpartyName: purchase.buyerName
        )
        deepLinkRouter.clearPendingPurchaseTap()
    }
}

#Preview("Empty State") {
    OwnerTodayView()
}

// MARK: - Dispute Sheet

/// Lightweight modal sheet for the owner-side dispute flow. Validates the
/// 5-500 char reason locally so we never round-trip a definitely-invalid
/// payload to the backend.
///
/// onSubmit is async and returns the failure message on error (nil on
/// success). The sheet keeps itself up with a spinner during the call and
/// only auto-dismisses after the server accepts — otherwise the owner
/// would never see server-side rejections (rate limit, status race, etc.)
/// because the sheet would have already closed.
struct VehicleReturnDisputeSheet: View {
    let vehicleReturn: VehicleReturn
    let onSubmit: (String) async -> String?

    @Environment(\.dismiss) private var dismiss
    @State private var reason: String = ""
    @State private var isSubmitting = false
    @State private var errorMessage: String?

    private var trimmedReason: String {
        reason.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private var isValid: Bool {
        let count = trimmedReason.count
        return count >= 5 && count <= 500
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Text("Tell us what went wrong with the return. Our team will review and reach out within 24 hours.")
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                }

                Section("Reason") {
                    // TextEditor is the right control here — disputes are
                    // multi-line, and we want a visible character counter so
                    // owners know about the 500-char ceiling.
                    TextEditor(text: $reason)
                        .frame(minHeight: 120)
                        .disabled(isSubmitting)
                    Text("\(trimmedReason.count)/500")
                        .font(.caption)
                        .foregroundColor(trimmedReason.count > 500 ? .red : .secondary)
                        .frame(maxWidth: .infinity, alignment: .trailing)
                }

                if let errorMessage {
                    Section {
                        Text(errorMessage)
                            .font(.subheadline)
                            .foregroundColor(.red)
                    }
                }
            }
            .navigationTitle("Dispute return")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .disabled(isSubmitting)
                }
                ToolbarItem(placement: .confirmationAction) {
                    if isSubmitting {
                        ProgressView()
                    } else {
                        Button("Submit") {
                            isSubmitting = true
                            errorMessage = nil
                            let captured = trimmedReason
                            Task {
                                let err = await onSubmit(captured)
                                if let err {
                                    errorMessage = err
                                    isSubmitting = false
                                } else {
                                    dismiss()
                                }
                            }
                        }
                        .disabled(!isValid)
                    }
                }
            }
            .interactiveDismissDisabled(isSubmitting)
        }
    }
}
