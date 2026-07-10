import Foundation
import SwiftUI // UserInterfaceSizeClass, used by the tab-bar geometry self-tests

// MARK: - Pure tour logic
//
// All the branching that decides *whether* a tour may start, *which* tour an
// event maps to, and *how* step transitions move — extracted into plain,
// UIKit-free value types so they can be exercised by unit tests (or the DEBUG
// self-test below) without a running app, a coordinator, or SwiftUI.

// MARK: Global suppressors snapshot

/// A snapshot of the global gates evaluated before any tour may auto-start.
/// Captured from live singletons at call time by the coordinator, or built by
/// hand in tests.
struct TourGlobalState: Equatable {
    /// `authStore.state == .authenticated`.
    var isAuthenticated: Bool
    /// `user.needsOnboarding` — the signup flow is still incomplete.
    var needsOnboarding: Bool
    /// Any `DeepLinkRouter` pending flag is set (reset password, lease pickup,
    /// chat tap, purchase chat/tap, deep-link error).
    var hasPendingDeepLink: Bool

    /// True when every global suppressor is clear and auto-starts are allowed.
    var allowsAutoStart: Bool {
        isAuthenticated && !needsOnboarding && !hasPendingDeepLink
    }

    static let clear = TourGlobalState(
        isAuthenticated: true,
        needsOnboarding: false,
        hasPendingDeepLink: false
    )
}

// MARK: Eligibility

enum TourEligibility {

    /// Whether `tour` may **auto-start** right now.
    ///
    /// True iff, in order:
    ///  1. zero-progress rule — never started/completed/skipped before,
    ///  2. role match — the tour's role is `.shared` or equals the active role,
    ///  3. legacy cutoff — the account is a new user (signed up in-app, or
    ///     created on/after the rollout date); existing accounts never
    ///     auto-start and must opt in via Profile → Replay,
    ///  4. global suppressors are clear,
    ///  5. priority — no active tour of equal-or-higher priority blocks it
    ///     (a strictly higher-priority candidate may pre-empt a lower one).
    static func canAutoStart(
        tour: TourKey,
        progress: [TourKey: TourStatus],
        activeRole: TourRole,
        isNewUser: Bool,
        global: TourGlobalState,
        activeTour: TourKey?
    ) -> Bool {
        // (1) zero-progress
        guard progress[tour] == nil else { return false }
        // (2) role match
        guard tour.role == .shared || tour.role == activeRole else { return false }
        // (3) legacy cutoff
        guard isNewUser else { return false }
        // (4) global suppressors
        guard global.allowsAutoStart else { return false }
        // (5) priority / dedupe against the in-flight tour
        if let active = activeTour {
            if active == tour { return false }
            // A candidate can only interrupt a strictly lower-priority tour.
            if tour.priority <= active.priority { return false }
        }
        return true
    }
}

// MARK: Event → candidate tour

enum TourEventRouter {

    /// Maps a domain event to the tour it would (attempt to) start. Returns
    /// `nil` for events that only mutate context / advance an existing tour and
    /// have no candidate of their own.
    static func candidate(for event: TourEvent, activeRole: TourRole) -> TourKey? {
        switch event {
        case .signupCompleted:        return .sharedWelcome
        case .roleActivated(let r):   return TourCatalogue.tabsTour(for: r)
        case .discoverAppeared:       return .driverFirstDiscover
        case .carDetailOpened:        return .driverCarDetail
        case .willSendLeaseRequest:   return .driverPreRequest
        case .leaseRequestCreated:    return .driverPostRequest
        // Zero cars is a *My Cars* moment, not a wizard moment: the wizard's
        // controls don't exist yet, so its coach marks would point at nothing.
        case .ownerCarCountZero:      return .ownerFirstCar
        case .listingWizardOpened:    return .ownerFirstListing
        case .listingSubmitted:       return .ownerFirstListing
        case .chatWithRequestOpened:  return .chatSegments
        case .firstTodayActionPresent:return .firstTodayAction
        case .roleSwitched:           return .roleSwitch
        case .buyThisCarTapped:       return .purchaseIntro
        case .bosOpened:              return .bosIntro
        case .bothSignaturesPresent:  return .signatureLock
        case .inspectionAvailable:    return .inspectionIntro
        }
    }
}

// MARK: Step transitions

/// The outcome of advancing one step.
enum TourAdvance: Equatable {
    /// Move to this (0-based) step index.
    case move(to: Int)
    /// The tour is finished — no more steps.
    case finish
}

enum TourReducer {

    /// Advance from `stepIndex` within a tour of `stepCount` visible steps.
    static func advance(stepIndex: Int, stepCount: Int) -> TourAdvance {
        let next = stepIndex + 1
        return next < stepCount ? .move(to: next) : .finish
    }

    /// Whether a status transition is allowed to write over an existing one.
    /// Terminal states (`completed`/`skipped`) are never downgraded back to
    /// `inProgress` by an auto-start; only an explicit replay may do that.
    static func mayOverwrite(existing: TourStatus?, with new: TourStatus) -> Bool {
        guard let existing else { return true }
        if existing.isTerminal && new == .inProgress { return false }
        return true
    }
}

// MARK: - DEBUG self-test
//
// No unit-test target exists in this project, so these pure functions are
// exercised by an assert-based self-test that can be invoked from the debug
// reset row. It documents the intended semantics as executable examples.

#if DEBUG
enum ProductTourSelfTest {

    @discardableResult
    static func runAll() -> Bool {
        var ok = true
        func check(_ cond: Bool, _ name: String) {
            if !cond { ok = false; print("[ProductTourSelfTest] FAIL: \(name)") }
        }

        // Eligibility: happy path.
        check(TourEligibility.canAutoStart(
            tour: .driverTabs, progress: [:], activeRole: .driver,
            isNewUser: true, global: .clear, activeTour: nil), "driverTabs eligible")

        // Zero-progress rule: a completed tour never re-fires.
        check(!TourEligibility.canAutoStart(
            tour: .driverTabs, progress: [.driverTabs: .completed], activeRole: .driver,
            isNewUser: true, global: .clear, activeTour: nil), "completed suppresses")
        check(!TourEligibility.canAutoStart(
            tour: .driverTabs, progress: [.driverTabs: .skipped], activeRole: .driver,
            isNewUser: true, global: .clear, activeTour: nil), "skipped suppresses")

        // Legacy cutoff: pre-rollout users never auto-start.
        check(!TourEligibility.canAutoStart(
            tour: .driverTabs, progress: [:], activeRole: .driver,
            isNewUser: false, global: .clear, activeTour: nil), "legacy blocked")

        // Role match: an owner tour is not eligible for a driver.
        check(!TourEligibility.canAutoStart(
            tour: .ownerTabs, progress: [:], activeRole: .driver,
            isNewUser: true, global: .clear, activeTour: nil), "role mismatch blocked")
        // Shared tour eligible in any role.
        check(TourEligibility.canAutoStart(
            tour: .sharedWelcome, progress: [:], activeRole: .owner,
            isNewUser: true, global: .clear, activeTour: nil), "shared eligible for owner")

        // Global suppressors.
        check(!TourEligibility.canAutoStart(
            tour: .driverTabs, progress: [:], activeRole: .driver, isNewUser: true,
            global: TourGlobalState(isAuthenticated: false, needsOnboarding: false, hasPendingDeepLink: false),
            activeTour: nil), "unauthenticated blocked")
        check(!TourEligibility.canAutoStart(
            tour: .driverTabs, progress: [:], activeRole: .driver, isNewUser: true,
            global: TourGlobalState(isAuthenticated: true, needsOnboarding: true, hasPendingDeepLink: false),
            activeTour: nil), "needsOnboarding blocked")
        check(!TourEligibility.canAutoStart(
            tour: .driverTabs, progress: [:], activeRole: .driver, isNewUser: true,
            global: TourGlobalState(isAuthenticated: true, needsOnboarding: false, hasPendingDeepLink: true),
            activeTour: nil), "deeplink blocked")

        // Priority: post-request (100) pre-empts an active lower tour (50)…
        check(TourEligibility.canAutoStart(
            tour: .driverPostRequest, progress: [:], activeRole: .driver, isNewUser: true,
            global: .clear, activeTour: .driverTabs), "high priority pre-empts")
        // …but a low-priority tour cannot pre-empt an active equal-priority one.
        check(!TourEligibility.canAutoStart(
            tour: .driverFirstDiscover, progress: [:], activeRole: .driver, isNewUser: true,
            global: .clear, activeTour: .driverTabs), "equal priority does not pre-empt")

        // Transitions.
        check(TourReducer.advance(stepIndex: 0, stepCount: 4) == .move(to: 1), "advance mid")
        check(TourReducer.advance(stepIndex: 3, stepCount: 4) == .finish, "advance to finish")
        check(TourReducer.advance(stepIndex: 0, stepCount: 1) == .finish, "single-step finishes")

        // Overwrite rule.
        check(TourReducer.mayOverwrite(existing: nil, with: .inProgress), "nil overwritable")
        check(!TourReducer.mayOverwrite(existing: .completed, with: .inProgress), "no downgrade")
        check(TourReducer.mayOverwrite(existing: .inProgress, with: .completed), "progress→complete ok")

        // Event routing.
        check(TourEventRouter.candidate(for: .leaseRequestCreated, activeRole: .driver) == .driverPostRequest, "lease routes post-request")
        check(TourEventRouter.candidate(for: .roleActivated(.owner), activeRole: .driver) == .ownerTabs, "roleActivated routes owner tabs")
        // Zero cars is a My Cars moment; the wizard tour needs the wizard open.
        check(TourEventRouter.candidate(for: .ownerCarCountZero, activeRole: .owner) == .ownerFirstCar, "zero cars routes first-car")
        check(TourEventRouter.candidate(for: .listingWizardOpened, activeRole: .owner) == .ownerFirstListing, "wizard open routes listing tour")

        // Screen-driven: only the listing wizard tour hands stepping to its screen.
        check(TourKey.ownerFirstListing.isScreenDriven, "listing tour is screen-driven")
        check(!TourKey.ownerFirstCar.isScreenDriven, "first-car tour is not screen-driven")
        check(!TourKey.driverTabs.isScreenDriven, "tab walk is not screen-driven")

        // Tab-bar geometry is refused on layouts we cannot describe.
        check(TabBarSpotlightRect.supportsGeometricSpotlight(
            horizontalSizeClass: .compact, containerSize: CGSize(width: 390, height: 844)),
            "compact portrait supports tab spotlight")
        check(!TabBarSpotlightRect.supportsGeometricSpotlight(
            horizontalSizeClass: .regular, containerSize: CGSize(width: 1024, height: 1366)),
            "regular width refuses tab spotlight")
        check(!TabBarSpotlightRect.supportsGeometricSpotlight(
            horizontalSizeClass: .compact, containerSize: CGSize(width: 844, height: 390)),
            "landscape refuses tab spotlight")

        print("[ProductTourSelfTest] \(ok ? "all passed" : "FAILURES above")")
        return ok
    }
}
#endif
