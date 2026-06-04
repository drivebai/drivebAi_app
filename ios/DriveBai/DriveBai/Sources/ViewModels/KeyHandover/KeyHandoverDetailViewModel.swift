import Foundation
import Combine

@MainActor
final class KeyHandoverDetailViewModel: ObservableObject {
    @Published var handover: KeyHandover
    @Published var isSubmitting = false
    @Published var error: String?
    @Published var currentTime: Date = Date()

    private let apiClient: APIClientProtocol
    private var timerCancellable: AnyCancellable?
    private var wsCancellables = Set<AnyCancellable>()

    init(handover: KeyHandover, apiClient: APIClientProtocol = APIClient.shared) {
        self.handover = handover
        self.apiClient = apiClient
        startTimer()
        subscribe()
    }

    deinit {
        timerCancellable?.cancel()
        wsCancellables.removeAll()
    }

    func reload() async {
        do {
            let model = try await apiClient.fetchKeyHandover(id: handover.id)
            handover = model.toDomain()
        } catch {
            #if DEBUG
            print("[KeyHandoverDetailVM] reload error: \(error)")
            #endif
        }
    }

    func confirm() {
        guard !isSubmitting else { return }
        isSubmitting = true
        error = nil
        let current = handover
        Task {
            defer { isSubmitting = false }
            do {
                let updated: KeyHandoverAPIModel
                if current.viewerRole == .owner {
                    updated = try await apiClient.ownerConfirmKeyHandover(id: current.id)
                } else {
                    updated = try await apiClient.driverConfirmKeyHandover(id: current.id)
                }
                handover = updated.toDomain()
            } catch {
                self.error = error.localizedDescription
                await reload()
            }
        }
    }

    private func startTimer() {
        timerCancellable = Timer.publish(every: 1, on: .main, in: .common)
            .autoconnect()
            .sink { [weak self] date in self?.currentTime = date }
    }

    private func subscribe() {
        WebSocketManager.shared.keyHandoverUpdatedPublisher
            .sink { [weak self] in Task { await self?.reload() } }
            .store(in: &wsCancellables)
    }
}
