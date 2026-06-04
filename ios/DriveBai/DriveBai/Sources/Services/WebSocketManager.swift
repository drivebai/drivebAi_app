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

    // Key handover events (created / confirmed / completed / expired) — triggers Today refresh
    let keyHandoverUpdatedPublisher = PassthroughSubject<Void, Never>()

    // Support chat events — admin reply arrives in real-time
    let supportMessageCreatedPublisher = PassthroughSubject<SupportMessageAPIResponse, Never>()

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

    // Last receive-layer error code; used to decide whether to skip token refresh.
    // -1011 (badServerResponse) means a non-101 from the server — not an auth issue.
    private var lastFailureCode: Int? = nil

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
        connectionState = .connecting

        Task { @MainActor in
            // Run a plain-HTTP preflight only on fresh connects (reconnectAttempt == 0)
            // to diagnose redirects / auth failures without spamming logs on retries.
            #if DEBUG
            await runPreflight()
            #endif

            // Only refresh token when the previous failure was NOT a WS-handshake
            // error (-1011 = badServerResponse). A -1011 after a working HTTP API call
            // means the issue is server/proxy-side, not token expiry.
            // Unconditionally refreshing on -1011 just burns the token endpoint.
            let isBadHandshake = lastFailureCode == URLError.Code.badServerResponse.rawValue
            if !isBadHandshake {
                await APIClient.shared.ensureFreshToken()
            }
            lastFailureCode = nil

            guard let token = keychain.getAccessToken() else {
                connectionState = .disconnected
                return
            }
            // wsBaseURL is "wss://" for Fly and "ws://" for local.
            // NSError displays these as "https://"/"http://" — that is an iOS
            // NSURLErrorFailingURLStringErrorKey normalisation, not a code bug here.
            guard let url = URL(string: "\(wsBaseURL)?token=\(token)") else {
                connectionState = .disconnected
                return
            }
            #if DEBUG
            let masked = String(token.prefix(8)) + "…" + String(token.suffix(4))
            print("[WS] Opening → scheme:\(url.scheme ?? "?") token:\(masked)")
            #endif
            let task = session.webSocketTask(with: url)
            task.resume()
            webSocketTask = task
            // connectionState advances to .connected only on the first successful
            // receive (see receiveMessage). Setting it here would be premature and
            // would hide the real -1011 failure in logs.
            receiveMessage()
        }
    }

    func disconnect() {
        reconnectTask?.cancel()
        pollingTask?.cancel()
        webSocketTask?.cancel(with: .goingAway, reason: nil)
        webSocketTask = nil
        connectionState = .disconnected
        usePollingFallback = false
        reconnectAttempt = 0
        lastFailureCode = nil
    }

    /// Call when the app returns to foreground or any time the connection may have silently dropped.
    func reconnectIfNeeded() {
        switch connectionState {
        case .connected, .connecting:
            return
        default:
            reconnectTask?.cancel()
            pollingTask?.cancel()
            usePollingFallback = false
            reconnectAttempt = 0
            lastFailureCode = nil
            connectionState = .disconnected
            connect()
        }
    }

    // MARK: - Receive Loop

    private func receiveMessage() {
        webSocketTask?.receive { [weak self] result in
            Task { @MainActor in
                guard let self = self else { return }
                switch result {
                case .success(let message):
                    // Handshake confirmed — promote state and reset retry counter here,
                    // not inside connect(), so the counter only resets on real success.
                    if self.connectionState != .connected {
                        self.connectionState = .connected
                        self.reconnectAttempt = 0
                        #if DEBUG
                        print("[WS] Handshake confirmed — live")
                        #endif
                    }
                    self.handleWebSocketMessage(message)
                    self.receiveMessage()
                case .failure(let error):
                    let e = error as NSError
                    self.lastFailureCode = e.code
                    #if DEBUG
                    print("[WS] Receive failed: \(e.localizedDescription) (domain:\(e.domain) code:\(e.code))")
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
        case "key_handover_created", "key_handover_owner_confirmed", "key_handover_completed", "key_handover_expired":
            keyHandoverUpdatedPublisher.send()
        case "notification_created":
            // Payload: { "unread_count": Int }
            if let unread = (try? JSONSerialization.jsonObject(with: payloadData) as? [String: Int])?["unread_count"] {
                notificationCreatedPublisher.send(unread)
            } else {
                notificationCreatedPublisher.send(1)
            }
        case "support_message_created":
            if let msg = try? decoder.decode(SupportMessageAPIResponse.self, from: payloadData) {
                supportMessageCreatedPublisher.send(msg)
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
            // Reset state so connect()'s guard passes after the backoff delay.
            self.connectionState = .disconnected
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

    // MARK: - Debug Preflight

    // Runs a plain HTTPS GET to the WS endpoint before the first connection attempt
    // so we can see actual HTTP status codes, redirects, and response bodies.
    // Only runs when reconnectAttempt == 0 (fresh connect, not a retry) to avoid
    // spamming logs during the reconnect backoff loop.
    #if DEBUG
    @MainActor
    private func runPreflight() async {
        guard reconnectAttempt == 0 else { return }
        guard let token = keychain.getAccessToken() else { return }
        // Convert wss:// → https:// (or ws:// → http://) for a plain HTTP request.
        let httpURLString = wsBaseURL
            .replacingOccurrences(of: "wss://", with: "https://")
            .replacingOccurrences(of: "ws://", with: "http://")
        guard let url = URL(string: "\(httpURLString)?token=\(token)") else { return }

        // Use a one-shot session whose delegate blocks redirects so we can see
        // 3xx status + Location header explicitly (URLSessionWebSocketTask does NOT
        // follow redirects, so any redirect is a root cause of -1011).
        let delegate = WSPreflightDelegate()
        let cfg = URLSessionConfiguration.ephemeral
        let preflightSession = URLSession(configuration: cfg, delegate: delegate, delegateQueue: nil)
        defer { preflightSession.invalidateAndCancel() }

        do {
            let (data, response) = try await preflightSession.data(from: url)
            if let http = response as? HTTPURLResponse {
                let body = String(data: data.prefix(200), encoding: .utf8) ?? "<binary \(data.count)b>"
                print("[WS Preflight] status=\(http.statusCode)")
                print("[WS Preflight] body: \(body)")
            }
        } catch let e as NSError where e.code == NSURLErrorCancelled {
            // Redirect was blocked by the delegate — details already printed there.
            print("[WS Preflight] → redirect intercepted (see above); this will cause -1011 on real WS connect")
        } catch {
            print("[WS Preflight] error: \(error)")
        }
    }

    private final class WSPreflightDelegate: NSObject, URLSessionTaskDelegate {
        func urlSession(
            _ session: URLSession,
            task: URLSessionTask,
            willPerformHTTPRedirection response: HTTPURLResponse,
            newRequest request: URLRequest,
            completionHandler: @escaping (URLRequest?) -> Void
        ) {
            let loc = response.value(forHTTPHeaderField: "Location") ?? "—"
            print("[WS Preflight] ⚠ REDIRECT \(response.statusCode) → \(request.url?.absoluteString ?? "?")")
            print("[WS Preflight] Location: \(loc)")
            completionHandler(nil)  // block redirect; throws .cancelled to caller
        }
    }
    #endif
}
