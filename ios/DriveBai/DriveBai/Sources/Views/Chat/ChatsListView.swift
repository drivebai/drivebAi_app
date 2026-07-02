import SwiftUI

struct ChatsListView: View {
    @StateObject private var viewModel = ChatsListViewModel.shared
    @EnvironmentObject private var authStore: AuthStore
    @EnvironmentObject private var deepLinkRouter: DeepLinkRouter

    /// Owns the NavigationStack path so a push tap on a `purchase_*`
    /// notification can programmatically append a ChatView destination.
    /// Mirrors the DeepLinkPickupTarget pattern in DriverTodayView.
    @State private var purchaseNavTarget: PurchaseChatNavTarget?

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
            // Purchase push tap. When we can resolve the chat summary
            // (either already loaded or after the fetchChats above lands),
            // push the ChatView with the Requests tab pre-selected so the
            // user lands directly on the purchase-request card.
            .navigationDestination(item: $purchaseNavTarget) { target in
                if let user = authStore.state.user {
                    ChatView(
                        chatId: target.chatID,
                        currentUserId: user.id,
                        counterpartyId: target.counterpartyId,
                        counterpartyName: target.counterpartyName,
                        initialTab: .requests
                    )
                }
            }
            .onChange(of: deepLinkRouter.pendingPurchaseChat) { _, next in
                consumePendingPurchaseChat(next)
            }
            .onChange(of: viewModel.chats) { _, _ in
                // Cold-launch case: the push landed before fetchChats
                // populated the list. Re-attempt the resolution when the
                // chats array changes.
                if let pending = deepLinkRouter.pendingPurchaseChat {
                    consumePendingPurchaseChat(pending)
                }
            }
            .onAppear {
                // Re-appear after a tab switch (Chats tab was previously
                // dormant): retry the pending purchase-chat resolution now
                // that our chats list is likely populated.
                if let pending = deepLinkRouter.pendingPurchaseChat {
                    consumePendingPurchaseChat(pending)
                }
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

    /// Resolve the pending `PendingPurchaseChat` against the loaded chats
    /// list and push a ChatView(initialTab: .requests). No-op if the chat
    /// summary isn't loaded yet — the `onChange(of: viewModel.chats)`
    /// hook retries when the list next changes.
    private func consumePendingPurchaseChat(_ pending: PendingPurchaseChat?) {
        guard let pending else { return }
        guard let chat = viewModel.chats.first(where: { $0.id == pending.chatID }) else {
            // Not yet loaded — leave the pending payload in the router so
            // we retry on next chats-array change.
            return
        }
        purchaseNavTarget = PurchaseChatNavTarget(
            chatID: pending.chatID,
            counterpartyId: chat.counterpartyId,
            counterpartyName: chat.counterpartyName,
            purchaseRequestID: pending.purchaseRequestID
        )
        deepLinkRouter.clearPendingPurchaseChat()
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

// MARK: - Purchase-chat nav target

/// Resolved destination for a `purchase_*` push tap. Carries the
/// counterparty details looked up from the ChatSummary so ChatView can be
/// constructed with the same signature as the tap-in-list path.
private struct PurchaseChatNavTarget: Hashable, Identifiable {
    let chatID: UUID
    let counterpartyId: UUID
    let counterpartyName: String
    let purchaseRequestID: UUID
    var id: UUID { chatID }
}
