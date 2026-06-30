import Foundation
import UIKit
import UserNotifications

/// Central coordinator for push notification lifecycle:
/// - Owns the `UNUserNotificationCenter` delegate so we can surface banners
///   in the foreground and route taps in the background / from a cold start.
/// - Bridges `UIApplicationDelegate` callbacks (didRegister, didReceiveRemote)
///   to the rest of the app via `NotificationCenter`.
///
/// Installed exactly once by `AppDelegate.application(_:didFinishLaunching…)`
/// — independent of auth state, so a cold-launch tap on a push always has a
/// delegate set before the OS forwards the notification.
final class PushNotificationCoordinator: NSObject, ObservableObject {
    static let shared = PushNotificationCoordinator()

    /// Key for the last hex-encoded APNs device token. Persisted so that
    /// `AuthStore.logout()` can call `deleteDeviceToken` even after the in-
    /// memory token has been dropped, and so we can reconcile against server
    /// state if registration succeeded but the API call failed.
    static let lastDeviceTokenKey = "lastDeviceToken"

    /// Current OS-level push-permission state. Published so SwiftUI views
    /// (e.g. NotificationsView) can render an in-app "Push is off — open
    /// Settings" affordance when the user denied the prompt at first run.
    /// Stays `.notDetermined` until the first `refreshAuthorizationStatus()`
    /// call (App lifecycle + view onAppear).
    @MainActor @Published private(set) var authorizationStatus: UNAuthorizationStatus = .notDetermined

    private override init() { super.init() }

    /// Reads the OS authorization status and publishes any change. Cheap
    /// (single XPC), safe to call from .onAppear / app-foreground hooks.
    @MainActor
    func refreshAuthorizationStatus() async {
        let settings = await UNUserNotificationCenter.current().notificationSettings()
        if settings.authorizationStatus != authorizationStatus {
            authorizationStatus = settings.authorizationStatus
        }
    }

    /// Wire `UNUserNotificationCenter.delegate` to self and handle the cold-
    /// start case where the user tapped a notification while the app was
    /// terminated. Safe to call multiple times.
    func install(launchOptions: [UIApplication.LaunchOptionsKey: Any]?) {
        UNUserNotificationCenter.current().delegate = self

        // Cold-start tap: iOS hands us the notification payload via launch
        // options. The user-tapped path still fires `didReceive` afterwards,
        // but we keep this as a belt-and-suspenders dispatch in case the
        // OS doesn't replay the response for some reason.
        if let userInfo = launchOptions?[.remoteNotification] as? [AnyHashable: Any] {
            DispatchQueue.main.async {
                DeepLinkRouter.shared.route(notificationUserInfo: userInfo)
            }
        }
    }
}

// MARK: - UNUserNotificationCenterDelegate

extension PushNotificationCoordinator: UNUserNotificationCenterDelegate {
    /// Foreground display — show the banner and play the sound even while
    /// the app is open so users don't miss time-sensitive messages.
    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        // Refresh in-app stores so the bell badge stays in sync as a fallback
        // when the WebSocket is not connected. WS remains the primary path.
        NotificationCenter.default.post(name: .didReceivePushNotification,
                                        object: notification.request.content.userInfo)
        completionHandler([.banner, .list, .sound, .badge])
    }

    /// Background / locked-screen tap — route through DeepLinkRouter so the
    /// in-app bell tap and push tap converge on the same destination.
    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse,
        withCompletionHandler completionHandler: @escaping () -> Void
    ) {
        let userInfo = response.notification.request.content.userInfo
        DispatchQueue.main.async {
            DeepLinkRouter.shared.route(notificationUserInfo: userInfo)
            completionHandler()
        }
    }
}

// MARK: - Permission + registration helpers

extension PushNotificationCoordinator {
    /// Requests APNs authorization on first run only. On subsequent launches
    /// we re-register silently so the device token stays current — APNs may
    /// rotate it on restore-from-backup or major OS upgrades.
    @MainActor
    func requestAuthorizationIfNeeded() async {
        let center = UNUserNotificationCenter.current()
        let settings = await center.notificationSettings()
        // Publish the pre-request status first so an immediately-rendering
        // NotificationsView already knows where we stand.
        authorizationStatus = settings.authorizationStatus

        switch settings.authorizationStatus {
        case .notDetermined:
            do {
                let granted = try await center.requestAuthorization(options: [.alert, .sound, .badge])
                if granted {
                    UIApplication.shared.registerForRemoteNotifications()
                }
                // Pull the now-resolved status (granted OR denied) back into
                // the published property so any in-app "push is off"
                // affordance flips state immediately.
                let post = await center.notificationSettings()
                authorizationStatus = post.authorizationStatus
            } catch {
                #if DEBUG
                print("[Push] requestAuthorization error: \(error)")
                #endif
            }
        case .authorized, .provisional, .ephemeral:
            UIApplication.shared.registerForRemoteNotifications()
        case .denied:
            break
        @unknown default:
            break
        }
    }

    /// Registers the device token with the backend, retrying transient
    /// failures with exponential backoff. Clears the persisted token only
    /// after the server confirms `{ok:true}` so we don't drop it on a
    /// flaky network.
    func registerTokenWithBackend(_ token: String) {
        UserDefaults.standard.set(token, forKey: Self.lastDeviceTokenKey)

        Task.detached {
            let delays: [UInt64] = [0, 1_000_000_000, 3_000_000_000] // 0s, 1s, 3s
            for (attempt, delay) in delays.enumerated() {
                if delay > 0 { try? await Task.sleep(nanoseconds: delay) }
                do {
                    _ = try await APIClient.shared.registerDeviceToken(
                        token: token,
                        sandbox: AppConfig.apnsSandbox
                    )
                    #if DEBUG
                    print("[Push] Registered device token with backend (attempt \(attempt + 1))")
                    #endif
                    return
                } catch {
                    #if DEBUG
                    print("[Push] registerDeviceToken attempt \(attempt + 1) failed: \(error)")
                    #endif
                }
            }
        }
    }

    /// Best-effort unregister — used on explicit logout. Failures are
    /// non-fatal because the server will also prune on 410.
    static func unregisterLastDeviceTokenIfPossible() async {
        guard let token = UserDefaults.standard.string(forKey: lastDeviceTokenKey),
              !token.isEmpty else { return }
        _ = try? await APIClient.shared.deleteDeviceToken(token: token)
        UserDefaults.standard.removeObject(forKey: lastDeviceTokenKey)
    }
}

// MARK: - Notification names

extension NSNotification.Name {
    /// Fired when a remote push arrives in the foreground (via
    /// `willPresent`). Listeners can refresh in-app stores; routing on tap
    /// is handled separately by `DeepLinkRouter`.
    static let didReceivePushNotification = NSNotification.Name("didReceivePushNotification")
}
