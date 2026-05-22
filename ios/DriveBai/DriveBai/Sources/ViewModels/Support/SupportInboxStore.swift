import Foundation
import Combine

/// Tracks how many unread admin support messages the user has not yet seen.
/// Lives for the lifetime of the app; created once in DriveBaiApp and injected
/// as an EnvironmentObject so ProfileView, DriverTabView, and OwnerTabView can
/// observe it without polling.
@MainActor
final class SupportInboxStore: ObservableObject {
    static let shared = SupportInboxStore()

    @Published private(set) var unreadCount: Int = 0
    @Published private(set) var chatId: UUID? = nil

    /// True while SupportChatView is on screen — prevents double-counting.
    var isSupportChatVisible: Bool = false

    private var cancellables = Set<AnyCancellable>()
    private var seenMessageIds = Set<UUID>()

    private init() {
        WebSocketManager.shared.supportMessageCreatedPublisher
            .receive(on: RunLoop.main)
            .sink { [weak self] msg in
                self?.handleIncoming(msg)
            }
            .store(in: &cancellables)
    }

    // MARK: - API

    func refresh() async {
        do {
            let chat = try await APIClient.shared.getOrCreateSupportChat()
            chatId = chat.id
            unreadCount = chat.unreadCount
        } catch {
            // Non-fatal — badge just won't update until next refresh
        }
    }

    func markRead() async {
        guard let chatId else { return }
        unreadCount = 0
        seenMessageIds.removeAll()
        try? await APIClient.shared.markSupportChatRead(chatId: chatId)
    }

    // MARK: - Private

    private func handleIncoming(_ msg: SupportMessageAPIResponse) {
        guard msg.senderKind == "admin" else { return }
        guard !seenMessageIds.contains(msg.id) else { return }
        seenMessageIds.insert(msg.id)

        if isSupportChatVisible {
            // Chat is open — mark read in background; don't bump badge
            Task { await markRead() }
        } else {
            unreadCount += 1
        }
    }
}
