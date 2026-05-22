import Foundation
import Combine

@MainActor
final class SupportChatViewModel: ObservableObject {
    @Published private(set) var messages: [SupportMessage] = []
    @Published private(set) var chatId: UUID? = nil
    @Published private(set) var isLoading = false
    @Published private(set) var isSending = false
    @Published private(set) var error: String? = nil
    @Published var draft: String = ""

    private var cancellables = Set<AnyCancellable>()

    init() {
        subscribeToWebSocket()
    }

    // MARK: - Load / Create

    func loadOrCreate() async {
        guard !isLoading else { return }
        isLoading = true
        error = nil
        do {
            let chat = try await APIClient.shared.getOrCreateSupportChat()
            chatId = chat.id
            let apiMessages = try await APIClient.shared.fetchSupportMessages(chatId: chat.id)
            messages = apiMessages.map { $0.toSupportMessage() }
            try? await APIClient.shared.markSupportChatRead(chatId: chat.id)
        } catch {
            self.error = error.localizedDescription
        }
        isLoading = false
    }

    // MARK: - Send

    func sendMessage() async {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, let chatId else { return }
        draft = ""
        isSending = true

        // Optimistic message
        let optimistic = SupportMessage(
            id: UUID(),
            supportChatId: chatId,
            senderId: UUID(), // filled in by server; locally irrelevant
            senderKind: .user,
            body: text,
            createdAt: Date()
        )
        messages.append(optimistic)

        do {
            let saved = try await APIClient.shared.sendSupportMessage(chatId: chatId, body: text)
            // Replace the optimistic message
            if let idx = messages.firstIndex(where: { $0.id == optimistic.id }) {
                messages[idx] = saved.toSupportMessage()
            }
        } catch {
            // Roll back optimistic message
            messages.removeAll { $0.id == optimistic.id }
            self.error = "Failed to send: \(error.localizedDescription)"
        }
        isSending = false
    }

    func markRead() async {
        guard let chatId else { return }
        try? await APIClient.shared.markSupportChatRead(chatId: chatId)
    }

    // MARK: - WebSocket

    private func subscribeToWebSocket() {
        WebSocketManager.shared.supportMessageCreatedPublisher
            .receive(on: RunLoop.main)
            .sink { [weak self] apiMsg in
                guard let self else { return }
                // Only handle messages for our chat
                guard self.chatId == apiMsg.supportChatId else { return }
                let msg = apiMsg.toSupportMessage()
                // Deduplicate (WS may race with HTTP response)
                guard !self.messages.contains(where: { $0.id == msg.id }) else { return }
                self.messages.append(msg)
            }
            .store(in: &cancellables)
    }
}
