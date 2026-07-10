import SwiftUI

/// Today tab view for Drivers
/// Displays active rentals, quick actions/reminders, and notifications bell
struct DriverTodayView: View {
    @StateObject private var viewModel = DriverTodayViewModel()
    @EnvironmentObject private var authStore: AuthStore
    @EnvironmentObject private var deepLinkRouter: DeepLinkRouter
    @ObservedObject private var tour = ProductTourCoordinator.shared
    /// Local mirror of the Getting-started card's collapse state (persisted
    /// best-effort into the tour cache).
    @State private var checklistCollapsed = false
    @State private var showNotifications = false
    @State private var navigateToChatTask: OnboardingTask?
    @State private var showChat = false
    @State private var notificationChatId: UUID?
    @State private var showNotificationChat = false
    @State private var selectedHandover: KeyHandover?
    /// Lease-pickup deep-link target. Filled when the user taps the
    /// pickup Live Activity (DeepLinkRouter.pendingLeasePickupId fires)
    /// AND we find a matching handover with a chatId. Drives a dedicated
    /// navigationDestination — separate from the Today-card path so the
    /// two never race.
    @State private var deepLinkPickupChat: DeepLinkPickupTarget?

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

                    // Section 1: Active Listings (rentals for driver)
                    activeListingsSection

                    // Section: Key Handover (shown only when active)
                    keyHandoverSection

                    // Section: Vehicle Return (shown only when active)
                    vehicleReturnSection

                    // Section: Purchase (buyer-side cards)
                    purchaseSection

                    // Section 2: Quick Actions and Reminders
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
                    onMarkAllRead: nil
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
            .navigationDestination(isPresented: $showChat) {
                if let task = navigateToChatTask,
                   let chatId = task.chatId,
                   let userId = authStore.state.user?.id {
                    ChatView(
                        chatId: chatId,
                        currentUserId: userId,
                        counterpartyId: task.counterpartyId ?? UUID(),
                        counterpartyName: task.counterpartyName ?? task.requestedBy,
                        // Every lease-card action (payment, price review,
                        // owner-side lease_request that's leaked into the
                        // driver feed) points at the LeaseRequestCardView
                        // in Chat → Requests, so land directly on that tab.
                        // Any other Today action (generic chat request,
                        // etc.) keeps the default Messages-first behaviour.
                        initialTab: (task.requestType == "lease_payment"
                                     || task.requestType == "lease_price_review"
                                     || task.requestType == "lease_request") ? .requests : nil
                    )
                }
            }
            .navigationDestination(item: $selectedHandover) { handover in
                KeyHandoverDetailView(handover: handover)
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
                // Handle the case where the deep link arrives before
                // fetchKeyHandovers has populated the list (cold launch).
                // Re-attempt the lookup once handovers land.
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
                checklistCollapsed = tour.checklistUIState(role: .driver).collapsed
                async let actionsTask: () = viewModel.fetchActions()
                async let notifTask: () = viewModel.fetchNotifications()
                // Refresh documents so the "Add your driver's license" checklist
                // row reflects real upload state, not a stale/empty cache.
                async let docsTask: () = authStore.fetchDocuments()
                _ = await (actionsTask, notifTask, docsTask)
                viewModel.markActionsSeen()
                if !viewModel.tasks.isEmpty { tour.handle(.firstTodayActionPresent) }
            }
            .onChange(of: viewModel.tasks.isEmpty) { _, isEmpty in
                if !isEmpty { tour.handle(.firstTodayActionPresent) }
            }
            .onChange(of: checklistCollapsed) { _, collapsed in
                tour.setChecklistCollapsed(collapsed, role: .driver)
            }
            .refreshable {
                await viewModel.refresh()
            }
        }
    }

    // MARK: - Your Rental Section

    private var activeListingsSection: some View {
        VStack(alignment: .leading, spacing: TodayLayout.headerSpacing) {
            // Section header
            Text("Your rental")
                .font(TodayLayout.sectionTitleFont)
                .foregroundColor(.primary)
                .padding(.horizontal, TodayLayout.horizontalPadding)

            // Listings content
            if viewModel.hasListings && !viewModel.listings.isEmpty {
                // Horizontally scrollable listing cards
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: TodayLayout.cardSpacing) {
                        ForEach(viewModel.listings) { listing in
                            ListingCard(
                                listing: listing,
                                onChatTap: {
                                    print("Chat tapped for: \(listing.title)")
                                },
                                onOptionSelect: { index in
                                    print("Option \(index) selected for: \(listing.title)")
                                }
                            )
                            .frame(width: 280)
                        }
                    }
                    .padding(.horizontal, TodayLayout.horizontalPadding)
                }
            } else {
                // Empty state for driver
                EmptyListingCard(isOwner: false) {
                    print("Discover rental tapped - navigate to discover flow")
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
                            onDispute: nil,
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
                Text("Buy the car")
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
                                    counterpartyId: purchase.sellerId,
                                    counterpartyName: purchase.sellerName
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
            Text("Quick actions and reminders")
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
                VStack(spacing: TodayLayout.cardSpacing) {
                    driverChecklistCard
                    AllDoneView()
                }
                .padding(.horizontal, TodayLayout.horizontalPadding)
            } else {
                VStack(spacing: TodayLayout.cardSpacing) {
                    ForEach(viewModel.tasks) { task in
                        // Cards that need to bounce into the chat's
                        // Requests tab collapse to a single "Review request"
                        // CTA — the actual accept/decline/pay happens
                        // inside the LeaseRequestCardView there, not
                        // duplicated on Today.
                        //
                        // `lease_request` is owner-side, but the backend's
                        // today/actions feed surfaces every action for the
                        // current user regardless of which Today tab is on
                        // screen; without this collapse a user who is in
                        // driver mode but is the owner on some lease sees
                        // an inline Approve/Decline pair here instead of
                        // being routed to the single source of truth in
                        // Chats → Requests.
                        let isChatHandoff = task.requestType == "lease_payment"
                            || task.requestType == "lease_price_review"
                            || task.requestType == "lease_request"
                        let displayTask = isChatHandoff
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
                                if isChatHandoff {
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
                        // Coach-mark anchor for `first_today_action_v1` — only
                        // the first (top) card is spotlighted.
                        .onboardingTargetIf(task.id == viewModel.tasks.first?.id, .todayFirstCard)
                    }
                }
                .padding(.horizontal, TodayLayout.horizontalPadding)
            }
        }
    }

    // MARK: - Getting-started checklist

    /// Driver "Getting started" card, shown in the empty Quick-actions region
    /// until every step is real-domain complete or the owner dismisses it.
    /// Rows are bound to genuine signals: the license upload, and the
    /// discover/request/where-to-pay milestones the coordinator records as the
    /// matching real events fire.
    @ViewBuilder
    private var driverChecklistCard: some View {
        if !tour.checklistUIState(role: .driver).dismissed {
            ChecklistCard.driver(
                hasLicense: authStore.hasRequiredDocuments(),
                browsedCars: tour.hasMilestone(.viewedCarDetail),
                sentRequest: tour.hasMilestone(.sentLeaseRequest),
                foundWhereToPay: tour.hasMilestone(.openedRequestsTab),
                collapsed: $checklistCollapsed,
                onDismiss: { tour.dismissChecklist(role: .driver) }
            )
        }
    }
}

// MARK: - Conditional coach-mark target helper

private extension View {
    /// Apply `.onboardingTarget(id)` only when `condition` is true, so a list
    /// can spotlight a single element without every row overwriting the anchor.
    @ViewBuilder
    func onboardingTargetIf(_ condition: Bool, _ id: TourTargetID) -> some View {
        if condition { self.onboardingTarget(id) } else { self }
    }
}

// MARK: - Live Activity deep-link helpers

/// Synthetic target for the `drivebai://lease/{id}/pickup` deep link route.
/// Kept separate from `OnboardingTask` to avoid confusing the two paths —
/// they each have their own `navigationDestination`.
struct DeepLinkPickupTarget: Hashable, Identifiable {
    let chatId: UUID
    let counterpartyId: UUID
    let counterpartyName: String
    var id: UUID { chatId }
}

extension DriverTodayView {
    /// Resolve a pending lease-pickup deep link to a chat navigation. Safe
    /// to call repeatedly — if the matching handover hasn't loaded yet,
    /// the next `viewModel.keyHandovers` change will retry via the
    /// `onChange` hook in `body`.
    fileprivate func handleLeasePickupDeepLink(_ leaseId: UUID?) {
        guard let leaseId else { return }
        guard let handover = viewModel.keyHandovers.first(where: { $0.leaseRequestId == leaseId }),
              let chatId = handover.chatId else { return }
        // Driver-side: counterparty is the owner.
        deepLinkPickupChat = DeepLinkPickupTarget(
            chatId: chatId,
            counterpartyId: handover.ownerId,
            counterpartyName: handover.ownerName
        )
        deepLinkRouter.clearPendingLeasePickup()
    }

    /// Resolve a purchase-request deep link (from a push tap) to a chat
    /// destination.  Same retry pattern as the lease one — safe to call
    /// repeatedly until the purchase list is populated.
    fileprivate func handlePurchaseDeepLink(_ purchaseId: UUID?) {
        guard let purchaseId else { return }
        guard let purchase = viewModel.purchaseRequests.first(where: { $0.id == purchaseId }) else {
            return
        }
        deepLinkPickupChat = DeepLinkPickupTarget(
            chatId: purchase.chatId,
            counterpartyId: purchase.sellerId,
            counterpartyName: purchase.sellerName
        )
        deepLinkRouter.clearPendingPurchaseTap()
    }
}

#Preview("Empty Listings") {
    DriverTodayView()
}

#Preview("With Active Rental") {
    DriverTodayView()
}
