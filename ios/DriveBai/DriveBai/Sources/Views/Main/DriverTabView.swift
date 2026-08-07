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
    @State private var showSupportHub = false

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

            // Profile Tab. Support unread lives on the floating Support
            // button, not here — a count on Profile pointed at the wrong
            // surface (7/24 item 2).
            ProfileView(showAuthFlow: .constant(false))
                .tabItem {
                    Label("Profile", systemImage: "person.fill")
                }
                .tag(3)
        }
        .tint(.driveBaiPrimary)
        // Universal Help & Support entry point (QA pt 0). A persistent button
        // floating above the tab bar — reachable in one tap from every tab —
        // presents the existing SupportChatView, which creates/opens the
        // support conversation on first appearance. Routes to admins; no
        // hardcoded email.
        .overlay(alignment: .bottomTrailing) {
            SupportFloatingButton(unreadCount: supportInboxStore.unreadCount) {
                // Unread reply waiting → straight into the chat it lives in;
                // otherwise the hub chooser as usual (7/24 item 3b).
                if supportInboxStore.unreadCount > 0 {
                    showSupport = true
                } else {
                    showSupportHub = true
                }
            }
        }
        // Floating button opens the Help & Support hub (or the chat directly
        // when a reply is waiting); a support-message push also opens the
        // chat directly (below).
        .sheet(isPresented: $showSupportHub) {
            SupportHubView().environmentObject(supportInboxStore)
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
        // Push tap on a `support_message` (admin wrote to this user's
        // support chat) → present SupportChatView directly so the message
        // "pops up" ready to respond to.
        .onChange(of: deepLinkRouter.pendingSupportChatTap) { _, tapped in
            guard tapped else { return }
            showSupport = true
            deepLinkRouter.clearPendingSupportChatTap()
        }
    }
}

// MARK: - Support Floating Button

/// Persistent circular Help button that floats above the tab bar. Shared by
/// `DriverTabView` and `OwnerTabView` (QA pt 0) so support is one tap from
/// every tab in both modes. THE support-unread surface (7/24 item 2): shows
/// the unread count as a red pill and flips the circle to app-icon black
/// while replies are waiting (item 3a). Presentation + routing live with the
/// caller (a sheet over the existing `SupportChatView`).
struct SupportFloatingButton: View {
    let unreadCount: Int
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            ZStack {
                // Teal at rest; app-icon black (#000, same as the icon
                // background) while unread — the color IS the signal.
                Circle()
                    .fill(unreadCount > 0 ? Color.black : Color.driveBaiPrimary)
                    .frame(width: 56, height: 56)

                // The DriveBai mark (wing → "b"), template-tinted: white at
                // rest; on the black unread circle it wears the icon's own
                // lighter teal (#9ED7DD) so the button reads as the logo.
                //
                // Centred by its bounding box (no offset). Verified in the
                // real app via ImageRenderer: offset 0 gives the most EVEN
                // margins (left == right, top == bottom); any nudge toward the
                // pixel-mass centroid makes them less even. This mark resists
                // tighter optical centring — its dense mass (the "b" bowl,
                // lower-right) and its visual spread (the thin wing,
                // upper-left) sit on OPPOSITE diagonals, so translating toward
                // the mass only crowds the wing into an edge. Bbox-centre is
                // the best fit for the full mark; a compacter small-size
                // variant would be needed to balance it more tightly.
                //
                // Sized to 36 of the 56 circle for an even ring of breathing
                // room (was 40 — too large, nearly touching the edge).
                Image("SupportMark")
                    .renderingMode(.template)
                    .resizable()
                    .scaledToFit()
                    .foregroundColor(unreadCount > 0 ? .driveBaiIconTeal : .white)
                    .frame(width: 36, height: 36)
            }
            .shadow(color: .black.opacity(0.18), radius: 6, x: 0, y: 2)
            // The COUNT, not just a dot — this is where the tab bar's old
            // Profile badge moved to (7/24 item 2). Styled like a tab badge:
            // red pill, white number, 99+ cap.
            .overlay(alignment: .topTrailing) {
                if unreadCount > 0 {
                    Text(unreadCount > 99 ? "99+" : "\(unreadCount)")
                        .font(.caption2.weight(.bold))
                        .foregroundColor(.white)
                        .padding(.horizontal, 5)
                        .frame(minWidth: 18, minHeight: 18)
                        .background(Capsule().fill(Color.red))
                        .overlay(Capsule().stroke(Color(.systemBackground), lineWidth: 2))
                        .offset(x: 4, y: -4)
                }
            }
        }
        .accessibilityLabel(unreadCount > 0
            ? "Help and support, \(unreadCount) unread \(unreadCount == 1 ? "reply" : "replies")"
            : "Help and support")
        .padding(.trailing, 16)
        // Lift clear of the tab bar so the button never sits on the tab items.
        .padding(.bottom, 72)
    }
}

#Preview {
    DriverTabView()
        .environmentObject(AuthStore.shared)
}
