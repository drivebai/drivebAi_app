import SwiftUI

struct OwnerTabView: View {
    @EnvironmentObject private var authStore: AuthStore
    @EnvironmentObject private var supportInboxStore: SupportInboxStore
    @EnvironmentObject private var deepLinkRouter: DeepLinkRouter
    @ObservedObject private var chatsVM = ChatsListViewModel.shared
    @ObservedObject private var tour = ProductTourCoordinator.shared

    /// Tag of the My Cars tab. Named so the tour redirect below doesn't rely on
    /// a bare literal matching the `.tag(...)` order.
    private static let myCarsTabIndex = 1

    @State private var selectedTab = 0
    /// Drives the universal Help & Support sheet (QA pt 0). One tap from every
    /// tab, in both driver and owner modes.
    @State private var showSupport = false

    var body: some View {
        TabView(selection: $selectedTab) {
            // Today Tab - Owner's dashboard with listings and tasks
            OwnerTodayView()
                .tabItem {
                    Label("Today", systemImage: "house.fill")
                }
                .tag(0)

            // My Cars Tab - Manage listings
            OwnerMyCarsView()
                .tabItem {
                    Label("My cars", systemImage: "car.2.fill")
                }
                .tag(1)

            // Chats Tab
            ChatsListView()
                .tabItem {
                    Label("Chats", systemImage: "message.fill")
                }
                .tag(2)
                .badge(chatsVM.totalUnreadCount)

            // Profile Tab
            ProfileView(showAuthFlow: .constant(false))
                .tabItem {
                    Label("Profile", systemImage: "person.fill")
                }
                .tag(3)
                .badge(supportInboxStore.unreadCount)
        }
        .tint(.driveBaiPrimary)
        // Universal Help & Support entry point (QA pt 0). A persistent button
        // floating above the tab bar — reachable in one tap from every tab —
        // presents the existing SupportChatView, which creates/opens the
        // support conversation on first appearance. Routes to admins; no
        // hardcoded email.
        .overlay(alignment: .bottomTrailing) {
            SupportFloatingButton(unreadCount: supportInboxStore.unreadCount) {
                showSupport = true
            }
        }
        .sheet(isPresented: $showSupport, onDismiss: {
            supportInboxStore.isSupportChatVisible = false
            Task { await supportInboxStore.markRead() }
        }) {
            SupportChatView()
                .environmentObject(authStore)
                .environmentObject(supportInboxStore)
        }
        // Product-tour coach-mark overlay (see DriverTabView for the rationale).
        .onboardingOverlayHost(tour)
        .task { tour.handle(.roleActivated(.owner)) }
        // The owner tab walk ends on "List a car" — land the owner on My cars,
        // whose zero-car empty state carries the "List your first vehicle"
        // checklist + primer. Only on completion: skipping the tour must not
        // move the user to a tab they never chose.
        .onChange(of: tour.activeTour) { oldValue, newValue in
            guard oldValue == .ownerTabs, newValue == nil,
                  tour.progress[.ownerTabs] == .completed else { return }
            selectedTab = Self.myCarsTabIndex
        }
        .onChange(of: deepLinkRouter.pendingLeasePickupId) { _, newId in
            guard newId != nil else { return }
            selectedTab = 0
        }
        // Push tap on a chat_message → switch to Chats tab. The chat row
        // tap routes the rest of the way; deeper push-into-chat navigation
        // is a follow-up since ChatsListView owns its own NavigationStack.
        .onChange(of: deepLinkRouter.pendingChatTap) { _, newId in
            guard newId != nil else { return }
            selectedTab = 2
            deepLinkRouter.clearPendingChatTap()
        }
        // Push tap on any `purchase_*` push → switch to Chats tab so
        // ChatsListView can pick up `pendingPurchaseChat` and push into
        // ChatView(initialTab: .requests). Mirrors the pendingChatTap /
        // pendingLeasePickupId observers above — the tab view owns tab
        // selection only; navigation lives with the view that owns the
        // NavigationStack.
        .onChange(of: deepLinkRouter.pendingPurchaseChat) { _, next in
            guard next != nil else { return }
            selectedTab = 2
        }
    }
}

// MARK: - My Cars View

struct MyCarsView: View {
    @EnvironmentObject private var authStore: AuthStore

    var body: some View {
        NavigationStack {
            VStack {
                // Empty state
                VStack(spacing: 24) {
                    Spacer()

                    Image(systemName: "car.fill")
                        .font(.system(size: 60))
                        .foregroundColor(.gray.opacity(0.5))

                    Text("No cars listed yet")
                        .font(.headline)

                    Text("Add your first car and start earning money by sharing it with trusted drivers")
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal, 32)

                    Button("Add Your First Car") {
                        // Navigate to add car flow
                    }
                    .buttonStyle(DriveBaiButtonStyle())
                    .padding(.horizontal, 48)

                    Spacer()
                }
            }
            .navigationTitle("My Cars")
            .toolbar {
                ToolbarItem(placement: .navigationBarTrailing) {
                    Button(action: {
                        // Add car
                    }) {
                        Image(systemName: "plus")
                    }
                }
            }
        }
    }
}

#Preview {
    OwnerTabView()
        .environmentObject(AuthStore.shared)
}
