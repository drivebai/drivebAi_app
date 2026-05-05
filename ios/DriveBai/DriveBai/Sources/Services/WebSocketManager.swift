import Foundation
import Combine

enum WebSocketConnectionState: Equatable {
    case disconnected
    case connecting
    case connected
    case reconnecting(attempt: Int)
}

@MainActor
final class WebSocketManager: ObservableObject {
    static let shared = WebSocketManager()

    @Published private(set) var connectionState: WebSocketConnectionState = .disconnected

    // Event publishers for ViewModels
    let newMessagePublisher = PassthroughSubject<ChatMessageAPIResponse, Never>()
    let requestCreatedPublisher = PassthroughSubject<ChatRequestAPIResponse, Never>()
    let requestUpdatedPublisher = PassthroughSubject<ChatRequestAPIResponse, Never>()

    // Lease request events — triggers Today tab refresh
    let leaseRequestCreatedPublisher = PassthroughSubject<Void, Never>()
    let leaseRequestUpdatedPublisher = PassthroughSubject<Void, Never>()

    // Notification events — payload is the new unread count
    let notificationCreatedPublisher = PassthroughSubject<Int, Never>()

    private var webSocketTask: URLSessionWebSocketTask?
    private let session: URLSession = .shared
    private let keychain: KeychainService = .shared

    private var reconnectAttempt = 0
    private let maxReconnectAttempts = 10
    private let baseReconnectDelay: TimeInterval = 1.0
    private var reconnectTask: Task<Void, Never>?

    // Polling fallback
    private var pollingTask: Task<Void, Never>?
    private var usePollingFallback = false

    private let wsBaseURL = AppConfig.wsBaseURL

    private let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { decoder in
            let container = try decoder.singleValueContainer()
            let str = try container.decode(String.self)
            guard let date = ISO8601DateParser.parse(str) else {
                throw DecodingError.dataCorruptedError(in: container, debugDescription: "Cannot decode date: \(str)")
            }
            return date
        }
        return d
    }()

    private init() {}

    // MARK: - Connection Lifecycle

    func connect() {
        guard connectionState == .disconnected else { return }
        guard let token = keychain.getAccessToken() else { return }

        connectionState = .connecting
        reconnectAttempt = 0

        guard let url = URL(string: "\(wsBaseURL)?token=\(token)") else { return }

        let task = session.webSocketTask(with: url)
        task.resume()
        webSocketTask = task
        connectionState = .connected

        receiveMessage()

        #if DEBUG
        print("[WebSocket] Connected")
        #endif
    }

    func disconnect() {
        reconnectTask?.cancel()
        pollingTask?.cancel()
        webSocketTask?.cancel(with: .goingAway, reason: nil)
        webSocketTask = nil
        connectionState = .disconnected
        usePollingFallback = false
    }

    // MARK: - Receive Loop

    private func receiveMessage() {
        webSocketTask?.receive { [weak self] result in
            Task { @MainActor in
                guard let self = self else { return }
                switch result {
                case .success(let message):
                    self.handleWebSocketMessage(message)
                    self.receiveMessage()
                case .failure(let error):
                    #if DEBUG
                    print("[WebSocket] Receive error: \(error)")
                    #endif
                    self.handleDisconnection()
                }
            }
        }
    }

    private func handleWebSocketMessage(_ message: URLSessionWebSocketTask.Message) {
        let data: Data
        switch message {
        case .string(let text):
            data = Data(text.utf8)
        case .data(let d):
            data = d
        @unknown default:
            return
        }

        guard let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let type = json["type"] as? String,
              let payloadData = try? JSONSerialization.data(withJSONObject: json["payload"] ?? [:])
        else { return }

        switch type {
        case "new_message":
            if let msg = try? decoder.decode(ChatMessageAPIResponse.self, from: payloadData) {
                newMessagePublisher.send(msg)
            }
        case "request_created":
            if let req = try? decoder.decode(ChatRequestAPIResponse.self, from: payloadData) {
                requestCreatedPublisher.send(req)
            }
        case "request_updated":
            if let req = try? decoder.decode(ChatRequestAPIResponse.self, from: payloadData) {
                requestUpdatedPublisher.send(req)
            }
        case "lease_request_created":
            leaseRequestCreatedPublisher.send()
        case "lease_request_updated":
            leaseRequestUpdatedPublisher.send()
        case "notification_created":
            // Payload: { "unread_count": Int }
            if let unread = (try? JSONSerialization.jsonObject(with: payloadData) as? [String: Int])?["unread_count"] {
                notificationCreatedPublisher.send(unread)
            } else {
                notificationCreatedPublisher.send(1)
            }
        default:
            #if DEBUG
            print("[WebSocket] Unknown event type: \(type)")
            #endif
        }
    }

    // MARK: - Reconnection

    private func handleDisconnection() {
        connectionState = .disconnected
        webSocketTask = nil

        guard reconnectAttempt < maxReconnectAttempts else {
            #if DEBUG
            print("[WebSocket] Max reconnect attempts reached, falling back to polling")
            #endif
            startPollingFallback()
            return
        }

        reconnectAttempt += 1
        let delay = baseReconnectDelay * pow(2.0, Double(reconnectAttempt - 1))
        let jitter = Double.random(in: 0...1)
        let totalDelay = delay + jitter

        connectionState = .reconnecting(attempt: reconnectAttempt)

        reconnectTask = Task {
            try? await Task.sleep(nanoseconds: UInt64(totalDelay * 1_000_000_000))
            guard !Task.isCancelled else { return }
            self.connect()
        }
    }

    // MARK: - Polling Fallback

    private func startPollingFallback() {
        usePollingFallback = true
        pollingTask = Task {
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 5_000_000_000) // 5s
                guard !Task.isCancelled else { break }
                await ChatsListViewModel.shared.fetchChats()
            }
        }
    }

    // MARK: - Cleanup

    func clearAll() {
        disconnect()
    }
}
