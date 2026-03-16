import SwiftUI

struct ChatsListView: View {
    @StateObject private var viewModel = ChatsListViewModel.shared
    @EnvironmentObject private var authStore: AuthStore

    var body: some View {
        NavigationStack {
            Group {
                if viewModel.isLoading && viewModel.chats.isEmpty {
                    ProgressView()
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if viewModel.filteredChats.isEmpty {
                    emptyState
                } else {
                    chatsList
                }
            }
            .navigationTitle("Chats")
            .searchable(text: $viewModel.searchText, prompt: "Search chats")
            .refreshable {
                await viewModel.fetchChats()
            }
            .task {
                await viewModel.fetchChats()
            }
        }
    }

    private var chatsList: some View {
        List {
            ForEach(viewModel.filteredChats) { chat in
                NavigationLink(value: chat) {
                    ChatRowView(chat: chat)
                }
            }
            .onDelete { indexSet in
                Task {
                    for index in indexSet {
                        let chat = viewModel.filteredChats[index]
                        await viewModel.archiveChat(chat.id)
                    }
                }
            }
        }
        .listStyle(.plain)
        .navigationDestination(for: ChatSummary.self) { chat in
            if let user = authStore.state.user {
                ChatView(
                    chatId: chat.id,
                    currentUserId: user.id,
                    counterpartyId: chat.counterpartyId,
                    counterpartyName: chat.counterpartyName
                )
            }
        }
    }

    private var emptyState: some View {
        VStack(spacing: 24) {
            Spacer()
            Image(systemName: "message.fill")
                .font(.system(size: 60))
                .foregroundColor(.gray.opacity(0.5))
            Text("No chats yet")
                .font(.headline)
            Text("Start a conversation by contacting a car owner or driver")
                .font(.subheadline)
                .foregroundColor(.secondary)
                .multilineTextAlignment(.center)
                .padding(.horizontal, 32)
            Spacer()
        }
    }
}
