import Foundation
import os
#if canImport(ActivityKit)
import ActivityKit
#endif

// MARK: - PickupLiveActivityManager
//
// Owns the lifecycle of the pickup-deadline Live Activity. Singleton because
// only one source of truth can hold the `[UUID: Activity]` map; multiple
// instances would race.
//
// Triggering philosophy:
//   - `startOrUpdate(...)` is idempotent. Call it as often as you like from
//     the existing data-load paths (payment-completed, Today fetch,
//     WebSocket-driven refetch). The first call starts an Activity; every
//     subsequent call with the same leaseRequestId updates the existing one
//     via `.update(.init(state:...))`.
//   - The Today reconciler calls `reconcile(activeLeaseIds:)` after every
//     `fetchKeyHandovers` to end Activities for leases that are no longer
//     in an awaiting-pickup state (terminal: pickupConfirmed, expired_refunded,
//     cancelled, declined). Single choke point keeps the lifecycle clean
//     without intercepting every individual WebSocket event.
//
// No background timers, no remote pushes, no UserDefaults sharing. All
// state lives in-process; on app launch we recover any leftover Activities
// from `Activity<PickupActivityAttributes>.activities` so the manager
// resumes ownership of them.

@available(iOS 16.1, *)
@MainActor
public final class PickupLiveActivityManager {

    // MARK: - Singleton

    public static let shared = PickupLiveActivityManager()
    private init() {}

    // MARK: - State

    /// Tracked Activities keyed by leaseRequestId — the dedup key. We use
    /// the lease id (not the activity id) because callers learn about
    /// lease lifecycle events, not ActivityKit ids.
    private var activitiesByLease: [UUID: Activity<PickupActivityAttributes>] = [:]

    /// Per-lease start anchor. Captured at PaymentSheet.completed; persisted
    /// in memory so subsequent .update calls keep the same progress origin
    /// even if the trigger came from a later code path (Today reconciler
    /// after WebSocket extension, etc.).
    private var startedAtByLease: [UUID: Date] = [:]

    private let log = Logger(subsystem: "com.drivebai-ios.app", category: "PickupActivity")

    // MARK: - Public API

    /// Called once on app launch (from DriveBaiApp.init or first foreground)
    /// to recover any Activities that survived a process restart. Without
    /// this, we'd leak Activities — they'd remain visible on the Lock
    /// Screen until their staleDate, but we'd be unable to end or update
    /// them on user actions.
    public func restoreActivities() {
        for activity in Activity<PickupActivityAttributes>.activities {
            let leaseId = activity.attributes.leaseRequestId
            activitiesByLease[leaseId] = activity
            // Best-effort start anchor recovery. We can't know the original
            // capture time across launches, so we use the activity's
            // current contentState (which the manager always writes with a
            // sane startedAt). On the first .update after restore, the
            // anchor stays correct.
            startedAtByLease[leaseId] = activity.content.state.startedAt
            log.info("restored activity for lease \(leaseId.uuidString, privacy: .public)")
        }
    }

    /// Start a new Activity OR update the existing one for this lease.
    ///
    /// - Parameters:
    ///   - leaseRequestId: dedup key.
    ///   - chatId / carTitle / viewerRole: static attributes (frozen at first call).
    ///   - deadline: the lease's pickup_deadline_at. Drives both the
    ///     `Text(timerInterval:)` readout and the progress denominator.
    ///   - startedAtIfNew: device-clock timestamp to anchor the progress bar.
    ///     ONLY used when starting a fresh Activity; if an Activity already
    ///     exists for this lease we keep its existing anchor (so the
    ///     progress bar doesn't snap to "0% elapsed" when the owner extends).
    ///     If nil, we fall back to `deadline - 120m`.
    public func startOrUpdate(
        leaseRequestId: UUID,
        chatId: UUID?,
        carTitle: String,
        viewerRole: PickupActivityAttributes.ViewerRole,
        deadline: Date,
        startedAtIfNew: Date?
    ) {
        // Respect user settings. If the user disabled Live Activities for
        // DriveBai (or globally) we no-op silently — never raise an error
        // in the host app.
        guard ActivityAuthorizationInfo().areActivitiesEnabled else {
            log.debug("Live Activities disabled by user; no-op")
            return
        }

        // Defensive: if the deadline is already in the past, don't start a
        // brand new Activity (it would render "0:00" forever). The Today
        // reconciler will end any stale Activity on the next pass.
        if activitiesByLease[leaseRequestId] == nil, deadline <= Date() {
            log.debug("skipping start for already-past deadline lease \(leaseRequestId.uuidString, privacy: .public)")
            return
        }

        if let existing = activitiesByLease[leaseRequestId] {
            // Update path. Resolve the progress-bar anchor:
            //   - If the caller passed `startedAtIfNew`, prefer it. That
            //     means the caller has a more precise timestamp than what
            //     we currently hold (e.g. ChatView.handlePaymentResult
            //     captured `Date()` at Stripe success, but a cold-launch
            //     reconciler had already started the activity with the
            //     fallback `deadline - 120m` anchor). Preferring the
            //     caller value also collapses the multi-second drift
            //     between the inferred and real payment moments.
            //   - Else keep whatever we already had (in-memory map, then
            //     the activity's own contentState as a last-resort
            //     restore-after-launch fallback).
            let rawAnchor: Date = startedAtIfNew
                ?? startedAtByLease[leaseRequestId]
                ?? existing.content.state.startedAt
            // Validate: ProgressView(timerInterval:) requires startedAt < deadline.
            // If a corrupt restore or clock skew produces an inverted interval,
            // the widget's fractionCompleted goes nil and the progress bar
            // renders zero-width (invisible). Clamp the anchor to at most
            // `deadline - 1s` so the bar always has a valid forward interval
            // to chew on. Worst case the bar reads "100%" and the timer reads
            // "0:00" — both visible, both correct given the bad state.
            let anchor: Date = min(rawAnchor, deadline.addingTimeInterval(-1))
            startedAtByLease[leaseRequestId] = anchor
            let newState = PickupActivityAttributes.ContentState(
                deadline: deadline,
                startedAt: anchor,
                phase: .active
            )
            Task {
                await existing.update(
                    ActivityContent(
                        state: newState,
                        staleDate: deadline.addingTimeInterval(15 * 60)
                    )
                )
            }
            log.info("updated activity for lease \(leaseRequestId.uuidString, privacy: .public)")
            return
        }

        // Start path. Same defensive clamp as the update path — if a
        // caller hands us a startedAt at or past the deadline (clock
        // skew, replay attack, future bug), reel it back so the
        // ProgressView interval is never inverted.
        let rawAnchor = startedAtIfNew ?? deadline.addingTimeInterval(-120 * 60)
        let anchor = min(rawAnchor, deadline.addingTimeInterval(-1))
        startedAtByLease[leaseRequestId] = anchor

        let attributes = PickupActivityAttributes(
            leaseRequestId: leaseRequestId,
            chatId: chatId,
            carTitle: carTitle,
            viewerRole: viewerRole
        )
        let state = PickupActivityAttributes.ContentState(
            deadline: deadline,
            startedAt: anchor,
            phase: .active
        )

        do {
            let activity = try Activity<PickupActivityAttributes>.request(
                attributes: attributes,
                content: ActivityContent(
                    state: state,
                    // Stale 15 min past the deadline so the system greys
                    // the activity out even if our reconciler is slow.
                    // The Today VM will end it for real on the next
                    // WebSocket-driven refetch.
                    staleDate: deadline.addingTimeInterval(15 * 60)
                ),
                pushType: nil // local-only updates for MVP
            )
            activitiesByLease[leaseRequestId] = activity
            log.info("started activity for lease \(leaseRequestId.uuidString, privacy: .public)")
        } catch {
            // Common error: hit the per-app concurrent-activities limit
            // (currently ~8). MVP has at most one active rental per driver
            // so this should never fire in practice; logging it is enough.
            log.error("activity.request failed for lease \(leaseRequestId.uuidString, privacy: .public): \(error.localizedDescription, privacy: .public)")
        }
    }

    /// Convenience overload for the chat lease-request flow.
    /// (Internal access — consumes the app-target-only `LeaseRequest`.)
    func startOrUpdate(
        for leaseRequest: LeaseRequest,
        viewerRole: PickupActivityAttributes.ViewerRole,
        startedAtIfNew: Date? = nil
    ) {
        guard let deadline = leaseRequest.pickupDeadlineAt else { return }
        guard leaseRequest.isAwaitingPickupConfirmation else { return }
        startOrUpdate(
            leaseRequestId: leaseRequest.id,
            chatId: leaseRequest.chatId,
            carTitle: leaseRequest.carTitle,
            viewerRole: viewerRole,
            deadline: deadline,
            startedAtIfNew: startedAtIfNew
        )
    }

    /// Convenience overload for the Today key-handover flow.
    /// (Internal access — consumes the app-target-only `KeyHandover`.)
    func startOrUpdate(for handover: KeyHandover) {
        guard handover.isAwaitingPickupConfirmation,
              let deadline = handover.pickupDeadlineAt else { return }
        startOrUpdate(
            leaseRequestId: handover.leaseRequestId,
            chatId: handover.chatId,
            carTitle: handover.carTitle,
            viewerRole: handover.viewerRole == .owner ? .owner : .driver,
            deadline: deadline,
            startedAtIfNew: nil  // Today-side never has a fresh device-clock anchor
        )
    }

    /// End an Activity for a specific lease. Flips to a terminal phase
    /// briefly so the user sees "Pickup confirmed" / "Pickup expired" on
    /// the Lock Screen before the system dismisses the card.
    public func end(leaseRequestId: UUID, reason: EndReason) {
        guard let activity = activitiesByLease[leaseRequestId] else { return }

        let finalPhase: PickupActivityAttributes.ContentState.Phase = {
            switch reason {
            case .pickupConfirmed: return .pickupConfirmed
            case .expired:         return .expired
            case .cancelled:       return .cancelled
            }
        }()

        let lastState = activity.content.state
        let finalContent = PickupActivityAttributes.ContentState(
            deadline: lastState.deadline,
            startedAt: lastState.startedAt,
            phase: finalPhase
        )

        activitiesByLease.removeValue(forKey: leaseRequestId)
        startedAtByLease.removeValue(forKey: leaseRequestId)

        Task {
            // .immediate keeps the terminal copy on screen for a couple of
            // seconds before iOS dismisses it — gives the user time to
            // see "Pickup confirmed" rather than the card just vanishing.
            await activity.end(
                ActivityContent(state: finalContent, staleDate: nil),
                dismissalPolicy: .default
            )
        }
        log.info("ended activity for lease \(leaseRequestId.uuidString, privacy: .public) reason=\(String(describing: reason), privacy: .public)")
    }

    public enum EndReason {
        case pickupConfirmed
        case expired
        case cancelled
    }

    /// Reconciler. Called from the Today VM after every `fetchKeyHandovers`.
    /// For every tracked Activity whose leaseRequestId is NOT in
    /// `activeLeaseIds`, end it with `.expired`. Use this in place of
    /// intercepting individual WebSocket events for terminal transitions
    /// (expired_refunded / cancelled / declined / pickup_confirmed).
    public func reconcile(activeLeaseIds: Set<UUID>) {
        let stale = activitiesByLease.keys.filter { !activeLeaseIds.contains($0) }
        for leaseId in stale {
            end(leaseRequestId: leaseId, reason: .expired)
        }
    }

    // MARK: - Testing helpers

    /// Number of currently-tracked Activities. Useful in adversarial tests
    /// and for debug overlays — not part of the production UI.
    public var trackedActivityCount: Int { activitiesByLease.count }
}
