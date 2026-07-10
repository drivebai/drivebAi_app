import Foundation

// MARK: - Checklist UI state

/// Collapse / dismiss flags for a role's Getting-Started checklist. Persisted in
/// the same per-user cache blob so it survives relaunch (but not reinstall —
/// this is UI chrome, not authoritative progress).
struct ChecklistUIState: Codable, Equatable {
    var collapsed: Bool = false
    var dismissed: Bool = false

    static let expanded = ChecklistUIState(collapsed: false, dismissed: false)
}

// MARK: - Domain milestones

/// Things the user has actually *done* in the product.
///
/// Deliberately separate from `TourStatus`. Checklist rows are bound to these,
/// never to tour progress: seeing (or replaying) a coach mark must never tick a
/// box that claims you browsed a car or sent a request. Each milestone is
/// recorded from a real domain event — a screen the user opened, a POST that
/// succeeded — and never from a coach card's button.
enum TourMilestone: String, Codable, CaseIterable {
    /// Opened a car's detail screen.
    case viewedCarDetail
    /// A lease-request POST succeeded.
    case sentLeaseRequest
    /// Actually looked at the Requests segment of a chat.
    case openedRequestsTab
}

// MARK: - Persistence protocol

/// Adapter behind which the coordinator reads/writes tour progress. Backed by
/// the server table (authoritative) with a per-user `UserDefaults` cache for
/// instant/optimistic and offline starts. `Sendable` so the `@MainActor`
/// coordinator can hop off the main actor for the async cache/network work.
protocol TourPersistence: AnyObject, Sendable {
    /// Namespaces the cache to the signed-in user. Must be set before use;
    /// clears in-memory scoping when `nil` (logout).
    func setUser(_ id: UUID?)

    /// Fast local read of the cache (no network). Empty when nothing cached.
    func loadCache() async -> [TourKey: TourStatus]

    /// GET the server rows and reconcile (server wins), updating the cache.
    /// Returns the merged map, or `nil` if the round-trip failed (cache kept).
    func reconcileFromServer() async -> [TourKey: TourStatus]?

    /// Write-through upsert: cache immediately, then PUT best-effort. `role` is
    /// derived from `key.role` by the caller.
    func upsert(_ key: TourKey, status: TourStatus, lastStep: String?) async

    /// DEBUG reset: clear the local cache and delete server rows for the caller.
    func clearAll() async

    // Checklist chrome (local only).
    func checklistUIState(role: TourRole) -> ChecklistUIState
    func setChecklistUIState(_ state: ChecklistUIState, role: TourRole)

    // Domain milestones (local only).
    func milestones() -> Set<TourMilestone>
    func recordMilestone(_ milestone: TourMilestone)

    /// Whether this account was ever seen as a genuinely new signup. Persisted
    /// so contextual tours survive relaunch rather than dying with the session.
    func isNewUser() -> Bool
    func setIsNewUser(_ value: Bool)
}

// MARK: - Cache blob

/// The single cached document per user. `tour_key` globally implies its role
/// (`TourKey.role`), so progress can be a flat map with no role bucketing.
private struct TourCacheBlob: Codable {
    var progress: [String: String] = [:]        // tourKey.raw : status.raw
    var lastStep: [String: String] = [:]        // tourKey.raw : stepId
    var checklist: [String: ChecklistUIState] = [:] // role.raw : state
    var milestones: [String] = []               // TourMilestone.raw
    var isNewUser: Bool = false
}

// MARK: - OnboardingProgressService

/// Concrete `TourPersistence` — the "OnboardingProgressService" the coordinator
/// is wired with. Keeps a single `UserDefaults` blob keyed by user id (not 50
/// flags) and syncs to the `/me/onboarding-progress` endpoints. All mutable
/// state is guarded by a lock so it is safe to touch off the main actor.
final class OnboardingProgressService: TourPersistence, @unchecked Sendable {

    private let defaults: UserDefaults
    private let api: APIClient
    private let lock = NSLock()
    private var userId: UUID?

    init(defaults: UserDefaults = .standard, api: APIClient = .shared) {
        self.defaults = defaults
        self.api = api
    }

    // MARK: User scoping

    func setUser(_ id: UUID?) {
        lock.lock(); defer { lock.unlock() }
        userId = id
    }

    private func currentUserId() -> UUID? {
        lock.lock(); defer { lock.unlock() }
        return userId
    }

    private func cacheKey(for id: UUID) -> String {
        "productTour.cache.\(id.uuidString)"
    }

    // MARK: Blob read/write

    private func readBlob() -> TourCacheBlob {
        guard let id = currentUserId(),
              let data = defaults.data(forKey: cacheKey(for: id)),
              let blob = try? JSONDecoder().decode(TourCacheBlob.self, from: data)
        else { return TourCacheBlob() }
        return blob
    }

    private func writeBlob(_ blob: TourCacheBlob) {
        guard let id = currentUserId() else { return }
        if let data = try? JSONEncoder().encode(blob) {
            defaults.set(data, forKey: cacheKey(for: id))
        }
    }

    private func progressMap(from blob: TourCacheBlob) -> [TourKey: TourStatus] {
        var out: [TourKey: TourStatus] = [:]
        for (rawKey, rawStatus) in blob.progress {
            if let key = TourKey(rawValue: rawKey), let status = TourStatus(rawValue: rawStatus) {
                out[key] = status
            }
        }
        return out
    }

    // MARK: TourPersistence

    func loadCache() async -> [TourKey: TourStatus] {
        progressMap(from: readBlob())
    }

    func reconcileFromServer() async -> [TourKey: TourStatus]? {
        guard currentUserId() != nil else { return nil }
        do {
            let rows = try await api.fetchOnboardingProgress()
            var blob = readBlob()
            for row in rows {
                // Server wins on conflict.
                blob.progress[row.tourKey] = row.status
                if let last = row.lastStepKey {
                    blob.lastStep[row.tourKey] = last
                }
            }
            writeBlob(blob)
            return progressMap(from: blob)
        } catch {
            return nil
        }
    }

    func upsert(_ key: TourKey, status: TourStatus, lastStep: String?) async {
        // 1) Cache immediately (write-through).
        var blob = readBlob()
        blob.progress[key.rawValue] = status.rawValue
        if let lastStep { blob.lastStep[key.rawValue] = lastStep }
        writeBlob(blob)

        // 2) Best-effort PUT. A failure is retried from the cache on next launch.
        do {
            try await api.putOnboardingProgress(
                role: key.role, tourKey: key, status: status, lastStepKey: lastStep
            )
        } catch {
            #if DEBUG
            print("[OnboardingProgressService] PUT failed for \(key.rawValue): \(error)")
            #endif
        }
    }

    func clearAll() async {
        // Local wipe first so the UI reflects the reset even offline.
        if let id = currentUserId() {
            defaults.removeObject(forKey: cacheKey(for: id))
        }
        do {
            try await api.deleteOnboardingProgress()
        } catch {
            #if DEBUG
            print("[OnboardingProgressService] DELETE failed: \(error)")
            #endif
        }
    }

    func checklistUIState(role: TourRole) -> ChecklistUIState {
        readBlob().checklist[role.rawValue] ?? .expanded
    }

    func setChecklistUIState(_ state: ChecklistUIState, role: TourRole) {
        var blob = readBlob()
        blob.checklist[role.rawValue] = state
        writeBlob(blob)
    }

    func milestones() -> Set<TourMilestone> {
        Set(readBlob().milestones.compactMap(TourMilestone.init(rawValue:)))
    }

    func recordMilestone(_ milestone: TourMilestone) {
        var blob = readBlob()
        guard !blob.milestones.contains(milestone.rawValue) else { return }
        blob.milestones.append(milestone.rawValue)
        writeBlob(blob)
    }

    func isNewUser() -> Bool { readBlob().isNewUser }

    func setIsNewUser(_ value: Bool) {
        var blob = readBlob()
        guard blob.isNewUser != value else { return }
        blob.isNewUser = value
        writeBlob(blob)
    }
}

// MARK: - In-memory persistence (previews / tests)

/// Non-networking `TourPersistence` for SwiftUI previews and unit tests.
final class InMemoryTourPersistence: TourPersistence, @unchecked Sendable {
    private let lock = NSLock()
    private var progress: [TourKey: TourStatus] = [:]
    private var checklist: [TourRole: ChecklistUIState] = [:]
    private var recorded: Set<TourMilestone> = []
    private var newUser: Bool = false

    init(progress: [TourKey: TourStatus] = [:]) {
        self.progress = progress
    }

    func setUser(_ id: UUID?) {}

    // `withLock` (vs bare lock/unlock) is safe to call from `async` contexts.
    func loadCache() async -> [TourKey: TourStatus] {
        lock.withLock { progress }
    }

    func reconcileFromServer() async -> [TourKey: TourStatus]? { nil }

    func upsert(_ key: TourKey, status: TourStatus, lastStep: String?) async {
        lock.withLock { progress[key] = status }
    }

    func clearAll() async {
        lock.withLock { progress.removeAll() }
    }

    func checklistUIState(role: TourRole) -> ChecklistUIState {
        lock.withLock { checklist[role] ?? .expanded }
    }

    func setChecklistUIState(_ state: ChecklistUIState, role: TourRole) {
        lock.withLock { checklist[role] = state }
    }

    func milestones() -> Set<TourMilestone> {
        lock.withLock { recorded }
    }

    func recordMilestone(_ milestone: TourMilestone) {
        lock.withLock { _ = recorded.insert(milestone) }
    }

    func isNewUser() -> Bool { lock.withLock { newUser } }

    func setIsNewUser(_ value: Bool) { lock.withLock { newUser = value } }
}
