import SwiftUI

struct OwnerTabView: View {
    @EnvironmentObject private var authStore: AuthStore
    @EnvironmentObject private var supportInboxStore: SupportInboxStore
    @ObservedObject private var chatsVM = ChatsListViewModel.shared

    @State private var selectedTab = 0

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
