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

    /// Stripe Connect embedded onboarding launch. The backend (account
    /// creation, account sessions, transfers) is live, but the StripeConnect
    /// SDK module ships in a follow-up batch — until then the Earnings &
    /// payouts screen shows status and explains, and the launch button is
    /// disabled with "available in the next update". Flip to true in the
    /// SDK batch.
    static let payoutOnboardingEnabled = false

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
