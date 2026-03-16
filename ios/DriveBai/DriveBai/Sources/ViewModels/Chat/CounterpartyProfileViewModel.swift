import Foundation

@MainActor
final class CounterpartyProfileViewModel: ObservableObject {
    let userId: UUID

    @Published var profile: CounterpartyProfile?
    @Published var isLoading = false
    @Published var error: String?

    private let apiClient: APIClient

    init(userId: UUID, apiClient: APIClient = .shared) {
        self.userId = userId
        self.apiClient = apiClient
    }

    func loadProfile() async {
        isLoading = true
        error = nil
        do {
            let response = try await apiClient.fetchCounterpartyProfile(userId: userId)
            profile = response.toProfile()
        } catch let apiError as APIError {
            error = apiError.errorDescription
        } catch {
            self.error = error.localizedDescription
        }
        isLoading = false
    }
}
