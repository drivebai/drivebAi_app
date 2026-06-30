import Foundation
import SwiftUI

// MARK: - App Route

enum AppRoute: Equatable {
    case resetPassword(token: String)
    /// `drivebai://lease/{uuid}/pickup` — fired when the user taps the
    /// pickup-deadline Live Activity. Routes them to Chat → Requests for
    /// that lease so the "I've picked up the car" CTA is one tap away.
    case leasePickup(leaseId: UUID)

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

        case "lease":
            // Expected shape: drivebai://lease/{uuid}/{action}
            // path components include the leading "/" so we drop the empty first.
            let parts = url.pathComponents.filter { $0 != "/" }
            guard parts.count >= 2,
                  let leaseId = UUID(uuidString: parts[0]),
                  parts[1] == "pickup" else {
                return nil
            }
            return .leasePickup(leaseId: leaseId)

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
    /// Set by the leasePickup route. Tab views observe this and navigate
    /// the user to Chat → Requests for the named lease. Cleared once the
    /// navigation has been kicked off so a second tap of the same Live
    /// Activity also triggers (otherwise SwiftUI's @Published equality
    /// check would swallow it).
    @Published var pendingLeasePickupId: UUID?

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
        case .leasePickup(let leaseId):
            #if DEBUG
            print("[DeepLink] Lease pickup tap for \(leaseId)")
            #endif
            // Force a change-event even if the user taps the same activity
            // twice in a row — set to nil first so SwiftUI re-publishes.
            pendingLeasePickupId = nil
            pendingLeasePickupId = leaseId
            deepLinkError = nil
        }
    }

    /// Called by a tab view once it has acted on `pendingLeasePickupId`,
    /// so a subsequent navigation away + back doesn't re-trigger the
    /// same route.
    func clearPendingLeasePickup() {
        pendingLeasePickupId = nil
    }

    func clearPendingRoute() {
        pendingRoute = nil
        showResetPassword = false
        resetPasswordToken = nil
        pendingLeasePickupId = nil
    }

    func clearError() {
        deepLinkError = nil
    }

    // MARK: - Push notification routing

    /// Routes a tap on a remote push notification to the same destination
    /// the in-app bell would reach. The payload shape mirrors the keys the
    /// backend emits in `handlers/notifications.go::buildPushRequest`:
    ///
    /// - `type=lease_request | payment | key_handover` + `lease_request_id`
    ///   → Today tab, lease detail (driven via `pendingLeasePickupId`
    ///   which both tab views already observe).
    /// - `type=chat_message` + `chat_id` → Chat tab → push the chat.
    /// - Anything else → no-op, the OS already foregrounded the app.
    ///
    /// Always safe to call on the main actor; missing keys never crash.
    func route(notificationUserInfo userInfo: [AnyHashable: Any]) {
        let type = (userInfo["type"] as? String) ?? ""

        switch type {
        case "lease_request", "payment", "key_handover":
            if let raw = userInfo["lease_request_id"] as? String,
               let leaseId = UUID(uuidString: raw) {
                // Reuse the same channel the Live Activity pickup tap uses
                // so both DriverTabView and OwnerTabView (which already
                // observe `pendingLeasePickupId`) switch to the Today tab.
                pendingLeasePickupId = nil
                pendingLeasePickupId = leaseId
            }
            pendingChatTap = nil
        case "chat_message":
            if let raw = userInfo["chat_id"] as? String,
               let chatId = UUID(uuidString: raw) {
                pendingChatTap = nil
                pendingChatTap = chatId
            }
        default:
            // `system` and unknown types: foregrounding the app is enough.
            break
        }
    }

    /// Set by `route(notificationUserInfo:)` when a `chat_message` push is
    /// tapped. Tab views observe this and switch to the Chats tab + push
    /// the corresponding ChatView. Cleared after navigation via
    /// `clearPendingChatTap()` (mirrors the leasePickup pattern).
    @Published var pendingChatTap: UUID?

    func clearPendingChatTap() {
        pendingChatTap = nil
    }
}
