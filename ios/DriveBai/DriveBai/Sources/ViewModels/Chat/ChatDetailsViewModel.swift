import Foundation

@MainActor
final class ChatDetailsViewModel: ObservableObject {
    let chatId: UUID

    @Published var details: ChatDetails?
    @Published var documents: [ChatAttachment] = []
    @Published var media: [ChatAttachment] = []
    @Published var isLoading = false
    @Published var error: String?

    private let apiClient: APIClient

    init(chatId: UUID, apiClient: APIClient = .shared) {
        self.chatId = chatId
        self.apiClient = apiClient
    }

    func loadDetails() async {
        isLoading = true
        error = nil
        do {
            let response = try await apiClient.fetchChatDetails(chatId: chatId)
            details = response.toChatDetails()
        } catch let apiError as APIError {
            error = apiError.errorDescription
        } catch {
            self.error = error.localizedDescription
        }
        isLoading = false
    }

    func loadAttachments() async {
        do {
            let docsResponse = try await apiClient.fetchChatAttachments(chatId: chatId, kind: "document")
            documents = docsResponse.attachments.map { $0.toAttachment() }

            let mediaResponse = try await apiClient.fetchChatAttachments(chatId: chatId, kind: "media")
            media = mediaResponse.attachments.map { $0.toAttachment() }
        } catch {
            // Non-critical, don't overwrite main error
        }
    }

    func toggleAutoTranslation() async {
        guard let current = details else { return }
        do {
            let request = UpdateChatSettingsAPIRequest(
                autoTranslationEnabled: !current.autoTranslateEnabled,
                notificationsMuted: nil
            )
            let response = try await apiClient.updateChatSettings(chatId: chatId, request: request)
            details = response.toChatDetails()
        } catch {
            self.error = error.localizedDescription
        }
    }

    func toggleMuteNotifications() async {
        guard let current = details else { return }
        do {
            let request = UpdateChatSettingsAPIRequest(
                autoTranslationEnabled: nil,
                notificationsMuted: !current.notificationsMuted
            )
            let response = try await apiClient.updateChatSettings(chatId: chatId, request: request)
            details = response.toChatDetails()
        } catch {
            self.error = error.localizedDescription
        }
    }
}
