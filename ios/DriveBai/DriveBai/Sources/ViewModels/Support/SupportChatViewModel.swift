import Foundation
import Combine

@MainActor
final class SupportChatViewModel: ObservableObject {
    @Published private(set) var messages: [SupportMessage] = []
    @Published private(set) var chatId: UUID? = nil
    @Published private(set) var isLoading = false
    @Published private(set) var isSending = false
    @Published private(set) var isUploadingAttachment = false
    @Published private(set) var error: String? = nil
    /// Support has read every user message stamped at/before this time. Drives
    /// the "Seen by support" indicator.
    @Published private(set) var adminLastReadAt: Date? = nil
    @Published var draft: String = ""

    private var cancellables = Set<AnyCancellable>()

    /// The id of the last user message that support has seen — the only place
    /// the "Seen by support" indicator is shown (iMessage-style: under the most
    /// recent sent message, not every one).
    var seenIndicatorMessageID: UUID? {
        guard let seenAt = adminLastReadAt else { return nil }
        guard let last = messages.last(where: { $0.senderKind == .user }) else { return nil }
        return last.createdAt <= seenAt ? last.id : nil
    }

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
            adminLastReadAt = chat.adminLastReadAt
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
            attachments: [],
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

    // MARK: - Attachment

    /// Upload a photo/document into the thread. Unlike text, we don't render an
    /// optimistic bubble — the signed URL only exists after the round-trip — so
    /// we append the real message the server returns (deduped against the WS
    /// echo). The caption is whatever is currently in the draft field.
    func uploadAttachment(data: Data, filename: String, mimeType: String) async {
        guard let chatId, !isUploadingAttachment else { return }
        let caption = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        isUploadingAttachment = true
        do {
            let saved = try await APIClient.shared.uploadSupportAttachment(
                chatId: chatId, data: data, filename: filename, mimeType: mimeType, caption: caption
            )
            draft = ""
            let msg = saved.toSupportMessage()
            if !messages.contains(where: { $0.id == msg.id }) {
                messages.append(msg)
            }
        } catch {
            self.error = "Failed to send attachment: \(error.localizedDescription)"
        }
        isUploadingAttachment = false
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
                // A reply from support means they've read everything up to now —
                // advance the "Seen" watermark so the indicator updates live.
                if msg.isFromAdmin {
                    self.adminLastReadAt = max(self.adminLastReadAt ?? .distantPast, msg.createdAt)
                }
                // Deduplicate (WS may race with HTTP response)
                guard !self.messages.contains(where: { $0.id == msg.id }) else { return }
                self.messages.append(msg)
            }
            .store(in: &cancellables)
    }
}
