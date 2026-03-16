import Foundation
import SwiftUI

// MARK: - App Route

enum AppRoute: Equatable {
    case resetPassword(token: String)

    static func from(url: URL) -> AppRoute? {
        guard url.scheme == "drivebai" else { return nil }

        switch url.host {
        case "reset-password":
            // Extract token from query parameters
            guard let components = URLComponents(url: url, resolvingAgainstBaseURL: false),
                  let queryItems = components.queryItems,
                  let token = queryItems.first(where: { $0.name == "token" })?.value,
                  !token.isEmpty else {
                return nil
            }
            return .resetPassword(token: token)

        default:
            return nil
        }
    }
}

// MARK: - Deep Link Router

@MainActor
final class DeepLinkRouter: ObservableObject {
    static let shared = DeepLinkRouter()

    @Published var pendingRoute: AppRoute?
    @Published var showResetPassword = false
    @Published var resetPasswordToken: String?
    @Published var deepLinkError: String?

    private init() {}

    func handle(url: URL) {
        #if DEBUG
        print("[DeepLink] Received URL: \(url.absoluteString)")
        #endif

        guard let route = AppRoute.from(url: url) else {
            #if DEBUG
            print("[DeepLink] Could not parse route from URL")
            #endif
            deepLinkError = "Invalid link. Please request a new password reset."
            return
        }

        handleRoute(route)
    }

    func handleRoute(_ route: AppRoute) {
        switch route {
        case .resetPassword(let token):
            #if DEBUG
            print("[DeepLink] Navigating to reset password with token")
            #endif
            resetPasswordToken = token
            showResetPassword = true
            deepLinkError = nil
        }
    }

    func clearPendingRoute() {
        pendingRoute = nil
        showResetPassword = false
        resetPasswordToken = nil
    }

    func clearError() {
        deepLinkError = nil
    }
}
