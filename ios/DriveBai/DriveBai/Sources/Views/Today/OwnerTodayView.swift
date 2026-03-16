import SwiftUI

/// Today tab view for Car Owners
/// Displays active listings, tasks to complete, and notifications bell
struct OwnerTodayView: View {
    @StateObject private var viewModel = OwnerTodayViewModel()
    @StateObject private var carsStore = OwnerCarsStore.shared
    @EnvironmentObject private var authStore: AuthStore
    @State private var showNotifications = false
    @State private var showCreateListing = false
    @State private var selectedCarId: UUID?
    @State private var navigateToChatTask: OnboardingTask?
    @State private var showChat = false

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

                    // Section 2: Actions to Take
                    actionsSection
                }
                .padding(.vertical, TodayLayout.horizontalPadding)
            }
            .background(TodayLayout.pageBackground.ignoresSafeArea())
            .toolbar(.hidden, for: .navigationBar)
            .navigationDestination(isPresented: $showNotifications) {
                NotificationsView(notifications: viewModel.notifications)
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
                        counterpartyName: task.counterpartyName ?? task.requestedBy
                    )
                }
            }
            .task {
                await carsStore.fetchCars()
                await viewModel.fetchActions()
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
                        TaskCard(
                            task: task,
                            currentTime: viewModel.currentTime,
                            onOpenTap: {
                                if task.isBackendAction {
                                    navigateToChatTask = task
                                    showChat = true
                                }
                            },
                            onOptionSelect: { index in
                                if task.isBackendAction {
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

#Preview("Empty State") {
    OwnerTodayView()
}
