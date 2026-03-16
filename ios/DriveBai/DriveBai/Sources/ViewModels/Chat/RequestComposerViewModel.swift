import Foundation

@MainActor
final class RequestComposerViewModel: ObservableObject {
    let chatId: UUID
    let userRole: UserRole

    @Published var input = CreateRequestInput()
    @Published var isSubmitting = false
    @Published var error: String?
    @Published var didSubmit = false

    private let apiClient: APIClient

    var availableRequestTypes: [RequestType] {
        RequestType.allCases.filter { $0.allowedRoles.contains(userRole) }
    }

    init(chatId: UUID, userRole: UserRole, apiClient: APIClient = .shared) {
        self.chatId = chatId
        self.userRole = userRole
        self.apiClient = apiClient
    }

    func submit() async {
        guard input.isValid else { return }
        isSubmitting = true
        error = nil

        do {
            let request = CreateChatRequestAPIRequest(
                type: input.type.rawValue,
                title: input.title,
                description: input.description,
                amount: input.amount,
                currency: input.amount != nil ? input.currency : nil
            )
            _ = try await apiClient.createChatRequest(chatId: chatId, request: request)
            didSubmit = true
        } catch let apiError as APIError {
            error = apiError.errorDescription
        } catch {
            self.error = error.localizedDescription
        }

        isSubmitting = false
    }
}
