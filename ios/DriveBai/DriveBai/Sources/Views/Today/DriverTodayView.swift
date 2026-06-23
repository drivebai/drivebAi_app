import SwiftUI

/// Today tab view for Drivers
/// Displays active rentals, quick actions/reminders, and notifications bell
struct DriverTodayView: View {
    @StateObject private var viewModel = DriverTodayViewModel()
    @EnvironmentObject private var authStore: AuthStore
    @State private var showNotifications = false
    @State private var navigateToChatTask: OnboardingTask?
    @State private var showChat = false
    @State private var notificationChatId: UUID?
    @State private var showNotificationChat = false
    @State private var selectedHandover: KeyHandover?

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
                        // Lease-payment AND lease-price-review cards both
                        // point at the lease card in Chat → Requests, so
                        // land directly on that tab. Any other Today
                        // action (generic chat request, etc.) keeps the
                        // default Messages-first behaviour.
                        initialTab: (task.requestType == "lease_payment" || task.requestType == "lease_price_review") ? .requests : nil
                    )
                }
            }
            .navigationDestination(item: $selectedHandover) { handover in
                KeyHandoverDetailView(handover: handover)
            }
            .task {
                async let actionsTask: () = viewModel.fetchActions()
                async let notifTask: () = viewModel.fetchNotifications()
                _ = await (actionsTask, notifTask)
                viewModel.markActionsSeen()
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
                AllDoneView()
                    .padding(.horizontal, TodayLayout.horizontalPadding)
            } else {
                VStack(spacing: TodayLayout.cardSpacing) {
                    ForEach(viewModel.tasks) { task in
                        // Driver-side cards that need to bounce into the
                        // chat's Requests tab (lease_payment OR the new
                        // lease_price_review) collapse to a single
                        // "Go to requests" CTA — the actual accept/decline
                        // happens inside the LeaseRequestCardView there,
                        // not duplicated on Today.
                        let isChatHandoff = task.requestType == "lease_payment" || task.requestType == "lease_price_review"
                        let displayTask = isChatHandoff
                            ? task.withSingleOption("Go to requests")
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
                    }
                }
                .padding(.horizontal, TodayLayout.horizontalPadding)
            }
        }
    }
}

#Preview("Empty Listings") {
    DriverTodayView()
}

#Preview("With Active Rental") {
    DriverTodayView()
}
