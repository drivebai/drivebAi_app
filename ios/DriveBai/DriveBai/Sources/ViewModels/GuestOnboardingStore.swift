import Foundation
import Combine

// MARK: - Guest Onboarding Store (device-local, no account)
//
// A guest has no account, so none of the account-scoped, server-backed
// onboarding machinery is available to them: TourProgressStore no-ops without
// a user id, and /me/onboarding-progress is behind AuthMiddleware. This store
// is the guest equivalent — a single small blob in UserDefaults, keyed by the
// device (not a user UUID), holding only convenience state for the guest
// onboarding surfaces:
//
//   • carDetailViewCount — how many car details this guest has opened, so the
//     engagement-earned nudge can fire only after real browsing.
//   • nudgeShown / nudgeDismissed — the nudge appears at most once per install
//     and, once dismissed, never again.
//   • hasEnteredEmailBefore — lets the Sign-in tab greet a first-time visitor
//     differently from someone returning to log in.
//
// Deliberately NOT the keychain (which can survive an uninstall): a fresh
// install is a fresh guest, and this state should reset with it. Persistence
// is best-effort convenience, never precious data — so a corrupt, older, or
// newer blob simply resets to defaults rather than crashing.
@MainActor
final class GuestOnboardingStore: ObservableObject {
    static let shared = GuestOnboardingStore()

    /// Versioned key: a breaking schema change bumps the key (…v2) and the old
    /// blob is abandoned rather than migrated. Within a version, additive
    /// fields decode with defaults (see Blob.init(from:)).
    private static let storageKey = "guestOnboarding.v1"

    /// Show the engagement nudge once the guest has opened at least this many
    /// car details — earned by browsing, never on launch.
    static let nudgeAfterCarViews = 4

    private let defaults: UserDefaults

    @Published private(set) var carDetailViewCount: Int
    @Published private(set) var nudgeShown: Bool
    @Published private(set) var nudgeDismissed: Bool
    @Published private(set) var hasEnteredEmailBefore: Bool

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        let blob = Self.load(from: defaults)
        self.carDetailViewCount = blob.carDetailViewCount
        self.nudgeShown = blob.nudgeShown
        self.nudgeDismissed = blob.nudgeDismissed
        self.hasEnteredEmailBefore = blob.hasEnteredEmailBefore
    }

    // MARK: - Derived

    /// The engagement nudge is due when the guest has browsed enough, hasn't
    /// seen it yet, and never dismissed it. One-shot by construction.
    var shouldOfferNudge: Bool {
        !nudgeDismissed && !nudgeShown && carDetailViewCount >= Self.nudgeAfterCarViews
    }

    // MARK: - Mutations

    func recordCarDetailView() {
        carDetailViewCount += 1
        persist()
    }

    func markNudgeShown() {
        guard !nudgeShown else { return }
        nudgeShown = true
        persist()
    }

    /// Permanent, one-tap dismissal — the nudge never returns on this install.
    func dismissNudge() {
        nudgeDismissed = true
        nudgeShown = true
        persist()
    }

    func markEnteredEmail() {
        guard !hasEnteredEmailBefore else { return }
        hasEnteredEmailBefore = true
        persist()
    }

    // MARK: - Persistence

    private func persist() {
        let blob = Blob(
            carDetailViewCount: carDetailViewCount,
            nudgeShown: nudgeShown,
            nudgeDismissed: nudgeDismissed,
            hasEnteredEmailBefore: hasEnteredEmailBefore
        )
        if let data = try? JSONEncoder().encode(blob) {
            defaults.set(data, forKey: Self.storageKey)
        }
    }

    private static func load(from defaults: UserDefaults) -> Blob {
        guard let data = defaults.data(forKey: storageKey),
              let blob = try? JSONDecoder().decode(Blob.self, from: data) else {
            return Blob.empty
        }
        return blob
    }

    // MARK: - Persisted shape

    private struct Blob: Codable {
        var schemaVersion: Int
        var carDetailViewCount: Int
        var nudgeShown: Bool
        var nudgeDismissed: Bool
        var hasEnteredEmailBefore: Bool

        static let empty = Blob(
            schemaVersion: 1,
            carDetailViewCount: 0,
            nudgeShown: false,
            nudgeDismissed: false,
            hasEnteredEmailBefore: false
        )

        init(
            schemaVersion: Int = 1,
            carDetailViewCount: Int,
            nudgeShown: Bool,
            nudgeDismissed: Bool,
            hasEnteredEmailBefore: Bool
        ) {
            self.schemaVersion = schemaVersion
            self.carDetailViewCount = carDetailViewCount
            self.nudgeShown = nudgeShown
            self.nudgeDismissed = nudgeDismissed
            self.hasEnteredEmailBefore = hasEnteredEmailBefore
        }

        // Additive-field tolerant: a future field decodes with a default, and
        // an older blob missing today's fields still loads. Any hard failure
        // bubbles up to load(), which falls back to .empty.
        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            schemaVersion = try c.decodeIfPresent(Int.self, forKey: .schemaVersion) ?? 1
            carDetailViewCount = try c.decodeIfPresent(Int.self, forKey: .carDetailViewCount) ?? 0
            nudgeShown = try c.decodeIfPresent(Bool.self, forKey: .nudgeShown) ?? false
            nudgeDismissed = try c.decodeIfPresent(Bool.self, forKey: .nudgeDismissed) ?? false
            hasEnteredEmailBefore = try c.decodeIfPresent(Bool.self, forKey: .hasEnteredEmailBefore) ?? false
        }
    }
}
