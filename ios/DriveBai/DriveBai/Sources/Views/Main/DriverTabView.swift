import SwiftUI

struct DriverTabView: View {
    @EnvironmentObject private var authStore: AuthStore
    @EnvironmentObject private var supportInboxStore: SupportInboxStore
    @EnvironmentObject private var deepLinkRouter: DeepLinkRouter
    @ObservedObject private var chatsVM = ChatsListViewModel.shared
    @ObservedObject private var tour = ProductTourCoordinator.shared

    /// Tag of the Discover tab. Named so the tour redirect below doesn't rely
    /// on a bare literal matching the `.tag(...)` order.
    private static let discoverTabIndex = 1

    @State private var selectedTab = 0
    /// Drives the universal Help & Support sheet (QA pt 0). One tap from every
    /// tab, in both driver and owner modes.
    @State private var showSupport = false

    var body: some View {
        TabView(selection: $selectedTab) {
            // Today Tab - Driver's dashboard with rentals and tasks
            DriverTodayView(selectedTab: $selectedTab)
                .tabItem {
                    Label("Today", systemImage: "house.fill")
                }
                .tag(0)

            // Discover Tab - Find available cars
            DiscoverView()
                .tabItem {
                    Label("Discover", systemImage: "car.2.fill")
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
        // Product-tour coach-mark overlay. Installed on the TabView root so the
        // welcome card, the geometric tab-bar spotlights and any coach-mark on a
        // pushed screen (Discover detail, Chat) all render above the tabs. The
        // native bar, its badges and the deep-link handlers below are untouched.
        .onboardingOverlayHost(tour)
        // Announce the active role so the coordinator can (eventually) run the
        // driver tab walk. For a fresh signup this is a no-op until the welcome
        // card finishes, which then chains into the tab walk.
        .task { tour.handle(.roleActivated(.driver)) }
        // The tab walk ends on "Explore Discover" — land the driver there so the
        // first-Discover coach-marks can pick up when that screen appears.
        // Only on completion: a user who tapped "Skip tour" asked to be left
        // alone, not moved to a tab they never chose.
        .onChange(of: tour.activeTour) { oldValue, newValue in
            guard oldValue == .driverTabs, newValue == nil,
                  tour.progress[.driverTabs] == .completed else { return }
            selectedTab = Self.discoverTabIndex
        }
        // Live Activity tap routes here via DeepLinkRouter. Switch to the
        // Today tab so DriverTodayView (which owns the keyHandover list and
        // the existing ChatView navigation machinery) can finish the
        // routing in one place.
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

// MARK: - Support Floating Button

/// Persistent circular Help button that floats above the tab bar. Shared by
/// `DriverTabView` and `OwnerTabView` (QA pt 0) so support is one tap from
/// every tab in both modes. Shows a small badge when the support inbox has
/// unread replies. Presentation + routing live with the caller (a sheet over
/// the existing `SupportChatView`).
struct SupportFloatingButton: View {
    let unreadCount: Int
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: "questionmark.circle.fill")
                .font(.system(size: 40))
                .symbolRenderingMode(.palette)
                .foregroundStyle(.white, Color.driveBaiPrimary)
                .shadow(color: .black.opacity(0.18), radius: 6, x: 0, y: 2)
                .overlay(alignment: .topTrailing) {
                    if unreadCount > 0 {
                        Circle()
                            .fill(Color.red)
                            .frame(width: 12, height: 12)
                            .overlay(Circle().stroke(Color(.systemBackground), lineWidth: 2))
                            .offset(x: 1, y: -1)
                    }
                }
        }
        .accessibilityLabel("Help and support")
        .padding(.trailing, 16)
        // Lift clear of the tab bar so the button never sits on the tab items.
        .padding(.bottom, 72)
    }
}

#Preview {
    DriverTabView()
        .environmentObject(AuthStore.shared)
}
