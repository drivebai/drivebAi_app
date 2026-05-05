import Foundation

enum AppConfig {

    enum BackendEnvironment {
        case local
        case flyTeam
    }

    #if DEBUG
    static var current: BackendEnvironment = .flyTeam
    #else
    static let current: BackendEnvironment = .flyTeam
    #endif

    static var apiBaseURL: URL {
        switch current {
        case .local:
            return URL(string: "http://localhost:8080/api/v1/")!
        case .flyTeam:
            return URL(string: "https://drivebai-api-team.fly.dev/api/v1/")!
        }
    }

    static var serverBaseURL: URL {
        switch current {
        case .local:
            return URL(string: "http://localhost:8080")!
        case .flyTeam:
            return URL(string: "https://drivebai-api-team.fly.dev")!
        }
    }

    static var wsBaseURL: String {
        switch current {
        case .local:
            return "ws://localhost:8080/api/v1/ws"
        case .flyTeam:
            return "wss://drivebai-api-team.fly.dev/api/v1/ws"
        }
    }

    /// True when running on a development/TestFlight build — tells the backend
    /// to use the APNs sandbox gateway instead of production.
    static var apnsSandbox: Bool {
        #if DEBUG
        return true
        #else
        return false
        #endif
    }
}
