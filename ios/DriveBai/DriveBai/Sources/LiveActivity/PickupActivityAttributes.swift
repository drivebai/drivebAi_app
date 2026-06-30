import Foundation
#if canImport(ActivityKit)
import ActivityKit
#endif

// MARK: - PickupActivityAttributes
//
// ActivityKit attributes for the 2-hour post-payment pickup countdown.
//
// This file must be a member of BOTH targets:
//   - DriveBai (host app) — the manager constructs and updates Activities
//   - DriveBaiWidgets (Live Activity extension) — the widget reads attributes
//     + content state in its ActivityConfiguration body.
//
// Designed to be tiny on the wire: ActivityKit serializes ContentState on
// every update and bills against a per-app size budget. Anything that
// doesn't change after start lives in the static `Attributes`, not the
// `ContentState`.

@available(iOS 16.1, *)
public struct PickupActivityAttributes: ActivityAttributes {

    // MARK: - ContentState (mutable; resent on every .update)

    public struct ContentState: Codable, Hashable {
        /// Absolute deadline. Drives the system-managed `Text(timerInterval:)`
        /// readout in the widget so SpringBoard ticks the digits per-second
        /// without waking the host app. Updated when the owner extends the
        /// pickup window.
        public let deadline: Date

        /// Anchor for the progress bar. We compute `progress = (now - startedAt) / (deadline - startedAt)`.
        /// Captured at PaymentSheet.completed for the precise moment of
        /// success; on cold-launch reconciliation we fall back to
        /// `deadline - 120 minutes` (the backend's PICKUP_DEADLINE_MINUTES
        /// constant) so the bar still has a reasonable origin.
        public let startedAt: Date

        /// Lifecycle phase. Drives the headline + tier color in the widget.
        /// `.active` is the normal pre-deadline state; the three terminal
        /// values are set briefly before the manager calls `.end(...)` so
        /// the user sees a final state before the activity dismisses.
        public let phase: Phase

        public enum Phase: String, Codable, Hashable {
            case active
            case pickupConfirmed
            case expired
            case cancelled
        }

        public init(deadline: Date, startedAt: Date, phase: Phase = .active) {
            self.deadline = deadline
            self.startedAt = startedAt
            self.phase = phase
        }
    }

    // MARK: - Static attributes (frozen at start; never resent)

    /// Backend lease id — also used as the de-dup key in the manager and as
    /// the path component of the `drivebai://lease/{id}/pickup` deep link.
    public let leaseRequestId: UUID

    /// Used to look the chat up on tap so we can route the user straight to
    /// Chat → Requests on activity open. Optional because some legacy lease
    /// rows may not have a chat (defensive — should always be present in
    /// practice).
    public let chatId: UUID?

    /// Car title for the headline, e.g. "2019 Toyota Corolla". Frozen at
    /// activity start; if the owner renames the listing mid-rental the
    /// activity keeps the original copy (UX-acceptable + cheaper to update).
    public let carTitle: String

    /// Role-aware headline tag. Drivers see "Pick up the car"; owners see
    /// "Driver picking up". Used in both the Lock Screen and Dynamic
    /// Island expanded views.
    public let viewerRole: ViewerRole

    public enum ViewerRole: String, Codable, Hashable {
        case driver
        case owner
    }

    public init(
        leaseRequestId: UUID,
        chatId: UUID?,
        carTitle: String,
        viewerRole: ViewerRole
    ) {
        self.leaseRequestId = leaseRequestId
        self.chatId = chatId
        self.carTitle = carTitle
        self.viewerRole = viewerRole
    }
}

// MARK: - Shared formatting helpers
//
// Tier thresholds mirror the in-app PickupCountdownView so the Live
// Activity, the Today card, and the Chat card all share the same visual
// language as the deadline approaches.

@available(iOS 16.1, *)
public enum PickupActivityTier: Equatable {
    case normal     // > 60 min
    case warning    // 15–60 min
    case critical   // < 15 min

    public init(remainingSeconds: TimeInterval) {
        let minutes = Int(remainingSeconds / 60)
        if minutes < 15 {
            self = .critical
        } else if minutes <= 60 {
            self = .warning
        } else {
            self = .normal
        }
    }

    /// SF Symbol that matches PickupCountdownView's tier icons.
    public var iconName: String {
        switch self {
        case .normal:   return "clock"
        case .warning:  return "clock.fill"
        case .critical: return "exclamationmark.triangle.fill"
        }
    }
}

/// Deep-link path the Live Activity uses when tapped. Centralized here so
/// the widget extension and the host app's DeepLinkRouter agree on the
/// exact URL format without sharing strings by copy-paste.
@available(iOS 16.1, *)
public enum PickupActivityDeepLink {
    public static let scheme = "drivebai"
    public static let host = "lease"
    public static let pickupAction = "pickup"

    public static func url(forLease leaseRequestId: UUID) -> URL? {
        URL(string: "\(scheme)://\(host)/\(leaseRequestId.uuidString)/\(pickupAction)")
    }
}
