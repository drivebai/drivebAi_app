import Foundation
import Combine

@MainActor
final class ChatsListViewModel: ObservableObject {
    static let shared = ChatsListViewModel()

    @Published var chats: [ChatSummary] = []
    @Published var filteredChats: [ChatSummary] = []
    @Published var searchText: String = ""
    @Published var isLoading = false
    @Published var error: String?
    @Published var totalUnreadCount = 0

    private let apiClient: APIClient
    private var cancellables = Set<AnyCancellable>()

    private init(apiClient: APIClient = .shared) {
        self.apiClient = apiClient
        setupSearchDebounce()
        subscribeToWebSocket()
    }

    private func setupSearchDebounce() {
        $searchText
            .debounce(for: .milliseconds(300), scheduler: RunLoop.main)
            .sink { [weak self] query in
                self?.applySearch(query)
            }
            .store(in: &cancellables)
    }

    private func subscribeToWebSocket() {
        WebSocketManager.shared.newMessagePublisher
            .receive(on: RunLoop.main)
            .sink { [weak self] message in
                guard let self = self else { return }
                if let idx = self.chats.firstIndex(where: { $0.id == message.chatId }) {
                    var updated = self.chats[idx]
                    updated = ChatSummary(
                        id: updated.id, carId: updated.carId, carTitle: updated.carTitle,
                        carCoverPhotoURL: updated.carCoverPhotoURL,
                        counterpartyId: updated.counterpartyId,
                        counterpartyName: updated.counterpartyName,
                        counterpartyAvatarURL: updated.counterpartyAvatarURL,
                        lastMessage: message.body,
                        lastMessageAt: message.createdAt,
                        unreadCount: updated.unreadCount + 1,
                        openRequestsCount: updated.openRequestsCount,
                        isArchived: updated.isArchived
                    )
                    self.chats[idx] = updated
                    self.totalUnreadCount += 1
                    self.applySearch(self.searchText)
                }
            }
            .store(in: &cancellables)
    }

    func fetchChats() async {
        isLoading = true
        error = nil
        do {
            let response = try await apiClient.fetchChats(archived: false)
            chats = response.chats.map { $0.toChatSummary() }
            totalUnreadCount = response.totalUnread
            applySearch(searchText)
        } catch let apiError as APIError {
            error = apiError.errorDescription
        } catch {
            self.error = error.localizedDescription
        }
        isLoading = false
    }

    func archiveChat(_ chatId: UUID) async {
        do {
            _ = try await apiClient.archiveChat(chatId: chatId, request: ArchiveChatAPIRequest(archived: true))
            chats.removeAll { $0.id == chatId }
            applySearch(searchText)
        } catch {
            self.error = error.localizedDescription
        }
    }

    func clearAll() {
        chats = []
        filteredChats = []
        searchText = ""
        totalUnreadCount = 0
        error = nil
    }

    private func applySearch(_ query: String) {
        if query.isEmpty {
            filteredChats = chats
        } else {
            let lower = query.lowercased()
            filteredChats = chats.filter {
                $0.counterpartyName.lowercased().contains(lower) ||
                $0.carTitle.lowercased().contains(lower)
            }
        }
    }
}
