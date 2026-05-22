import SwiftUI

struct DriverTabView: View {
    @EnvironmentObject private var authStore: AuthStore
    @EnvironmentObject private var supportInboxStore: SupportInboxStore
    @ObservedObject private var chatsVM = ChatsListViewModel.shared

    @State private var selectedTab = 0

    var body: some View {
        TabView(selection: $selectedTab) {
            // Today Tab - Driver's dashboard with rentals and tasks
            DriverTodayView()
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
    }
}

#Preview {
    DriverTabView()
        .environmentObject(AuthStore.shared)
}
