import SwiftUI

struct MainTabView: View {
    @EnvironmentObject private var authStore: AuthStore
    @ObservedObject private var chatsVM = ChatsListViewModel.shared

    @State private var selectedTab = 0
    @State private var showAuthFlow = false

    var body: some View {
        TabView(selection: $selectedTab) {
            // Today Tab (placeholder)
            TodayView()
                .tabItem {
                    Label("Today", systemImage: "calendar")
                }
                .tag(0)

            // Discover Tab
            DiscoverView()
                .tabItem {
                    Label("Discover", systemImage: "magnifyingglass")
                }
                .tag(1)

            // Chats Tab
            ChatsListView()
                .tabItem {
                    Label("Chats", systemImage: "message")
                }
                .tag(2)
                .badge(chatsVM.totalUnreadCount)

            // Profile Tab
            ProfileView(showAuthFlow: $showAuthFlow)
                .tabItem {
                    Label("Profile", systemImage: "person")
                }
                .tag(3)
        }
        .tint(.driveBaiPrimary)
        .sheet(isPresented: $showAuthFlow) {
            LoginView(showDismissButton: true)
                .environmentObject(authStore)
        }
        .onChange(of: authStore.state) { oldValue, newValue in
            // Dismiss auth flow when user becomes authenticated
            if newValue.isAuthenticated {
                showAuthFlow = false
            }
        }
    }
}

// MARK: - Today View (Placeholder)

struct TodayView: View {
    @EnvironmentObject private var authStore: AuthStore

    var body: some View {
        NavigationStack {
            VStack {
                if authStore.state.isAuthenticated {
                    ScrollView {
                        VStack(alignment: .leading, spacing: 24) {
                            // Active Listings Header
                            HStack {
                                Circle()
                                    .fill(Color.driveBaiPrimary)
                                    .frame(width: 8, height: 8)
                                Text("Active listings")
                                    .font(.subheadline)
                                    .foregroundColor(.secondary)
                            }
                            .padding(.horizontal)
                            .padding(.top, 16)

                            // Placeholder content
                            VStack(spacing: 16) {
                                Text("No active listings")
                                    .font(.headline)
                                Text("Start by creating your first listing")
                                    .font(.subheadline)
                                    .foregroundColor(.secondary)

                                Button("Create Listing") {}
                                    .buttonStyle(DriveBaiButtonStyle())
                                    .padding(.horizontal, 48)
                            }
                            .frame(maxWidth: .infinity)
                            .padding(.top, 48)
                        }
                    }
                } else {
                    VStack(spacing: 24) {
                        Spacer()
                        Image(systemName: "calendar.badge.plus")
                            .font(.system(size: 60))
                            .foregroundColor(.gray)
                        Text("Sign in to see your schedule")
                            .font(.headline)
                        Text("Track your bookings and manage your calendar")
                            .font(.subheadline)
                            .foregroundColor(.secondary)
                            .multilineTextAlignment(.center)
                            .padding(.horizontal, 32)
                        Spacer()
                    }
                }
            }
            .navigationTitle("Today")
        }
    }
}

#Preview {
    MainTabView()
        .environmentObject(AuthStore.shared)
}
