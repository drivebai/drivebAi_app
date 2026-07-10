import Foundation
import Combine

// MARK: - Rollout

/// Legacy cutoff for `markNewUser(createdAt:)`.
///
/// The gate that actually runs today is the persisted signup flag: an account
/// earns `isNewUser` when it completes signup in-app, and that flag is stored
/// per account so contextual tours survive relaunch. This date is the second
/// gate, applied whenever a caller has a real account creation date to hand.
///
/// Either way a pre-rollout account never auto-starts anything and needs no
/// backfilled rows; it can still opt in via Profile → Replay.
enum ProductTourRollout {
    /// Product-tour launch date. Accounts created before this never auto-start.
    static let launchDate: Date = {
        var c = DateComponents()
        c.year = 2026; c.month = 7; c.day = 1
        c.timeZone = TimeZone(secondsFromGMT: 0)
        return Calendar(identifier: .gregorian).date(from: c) ?? .distantPast
    }()

    static func isNew(createdAt: Date?) -> Bool {
        guard let createdAt else { return false }
        return createdAt >= launchDate
    }
}

// MARK: - Contextual nuance

/// Per-session context the coordinator folds into step selection so view call
/// sites can stay argument-less. Set via `updateContext(_:)`.
struct TourContext: Equatable {
    /// The car currently open in detail is listed for sale → the `Or buy it`
    /// step and purchase tours become relevant.
    var carIsForSale: Bool = false
    /// The active Today/handover card has a resolvable pickup location → the
    /// `Get directions` step is shown.
    var hasPickupLocation: Bool = false
    /// The finalized Bill-of-Sale PDF exists → `pdf_ready_v1` may run after the
    /// signature-lock teach.
    var pdfReady: Bool = false
}

// MARK: - Coordinator

/// Single source of truth + policy engine for the product tour. Views raise
/// domain events through `handle(_:)`; the coordinator decides eligibility,
/// starts/advances tours, resolves priority, persists progress and drives the
/// overlay through its `@Published` state.
@MainActor
final class ProductTourCoordinator: ObservableObject {

    static let shared = ProductTourCoordinator(persistence: OnboardingProgressService())

    // Observed by the overlay host.
    @Published private(set) var activeTour: TourKey?
    @Published private(set) var stepIndex: Int = 0
    @Published private(set) var progress: [TourKey: TourStatus] = [:]

    /// A screen-driven tour is running but its current card has been dismissed;
    /// the overlay draws nothing until the screen advances the tour. Lets the
    /// user actually operate the screen the card was explaining.
    @Published private(set) var isStepDismissed: Bool = false

    /// New-user signal. Gate for all auto-starts; replay bypasses it. Defaults
    /// to `false` so nothing fires until a real account signal sets it.
    ///
    /// Persisted per account. Deriving it only from the live
    /// signup→completed transition made every contextual tour die at the end
    /// of the signup session: on the next launch the account already reads as
    /// onboarded, the flag never flipped, and nothing could auto-start again.
    var isNewUser: Bool = false {
        didSet {
            guard isNewUser, isNewUser != oldValue else { return }
            persistence.setIsNewUser(true)
        }
    }

    private(set) var activeRole: TourRole = .driver
    private(set) var context = TourContext()

    private let persistence: TourPersistence
    private var analytics: ProductTourAnalytics

    /// Snapshot of global suppressors. Overridable for tests; defaults to the
    /// live singletons. `@MainActor` because it reads main-actor stores.
    var globalStateProvider: @MainActor () -> TourGlobalState = {
        let auth = AuthStore.shared
        let dl = DeepLinkRouter.shared
        let pending = dl.showResetPassword
            || dl.resetPasswordToken != nil
            || dl.pendingLeasePickupId != nil
            || dl.pendingChatTap != nil
            || dl.pendingPurchaseTap != nil
            || dl.pendingPurchaseChat != nil
            || dl.deepLinkError != nil
        return TourGlobalState(
            isAuthenticated: auth.state.isAuthenticated,
            needsOnboarding: auth.needsOnboarding,
            hasPendingDeepLink: pending
        )
    }

    /// A lower-priority tour paused because a higher-priority one pre-empted it.
    private var paused: (tour: TourKey, stepIndex: Int)?

    /// Actions deferred behind an explainer, keyed by the tour that gates them.
    /// Drained in `endActiveTour` on completion *or* skip.
    private var pendingActions: [TourKey: () -> Void] = [:]

    /// Tours the user explicitly asked to replay that cannot run where they
    /// asked. They start the next time their own screen appears, bypassing the
    /// new-user gate — an explicit request is always allowed.
    private var armedTours: Set<TourKey> = []

    // Host arbitration: the overlay modifier is installed at multiple layers
    // (tab root + each sheet root). Only the frontmost registered host draws the
    // scrim so a sheet stacked over a tab root never double-dims.
    @Published private(set) var frontmostHostToken: Int = 0
    private var hostStack: [Int] = []
    private var hostSeq = 0

    init(persistence: TourPersistence, analytics: ProductTourAnalytics? = nil) {
        self.persistence = persistence
        if let analytics {
            self.analytics = analytics
        } else {
            #if DEBUG
            self.analytics = OSLogTourAnalytics()
            #else
            self.analytics = NoopTourAnalytics()
            #endif
        }
    }

    // MARK: Derived

    /// The steps for a tour after applying the current `TourContext`. Steps
    /// whose target is contextually absent are dropped here (never enqueued),
    /// so the overlay never points an arrow at a control that isn't on screen.
    func visibleSteps(for tour: TourKey) -> [TourStep] {
        TourCatalogue.steps(for: tour).filter { step in
            switch step.target {
            case .buyThisCarCTA:   return context.carIsForSale
            case .getDirectionsRow:return context.hasPickupLocation
            default:               return true
            }
        }
    }

    var currentStep: TourStep? {
        guard let tour = activeTour else { return nil }
        return visibleSteps(for: tour)[safe: stepIndex]
    }

    var stepCount: Int {
        guard let tour = activeTour else { return 0 }
        return visibleSteps(for: tour).count
    }

    /// 1-based index for "Step i of n". Only meaningful when `showsProgress`.
    var stepNumber: Int { stepIndex + 1 }

    /// Multi-step tours show "Step i of n"; single-card tours do not.
    var showsProgress: Bool { stepCount > 1 }

    // MARK: Lifecycle

    /// Load cache → GET reconcile. Call once after authentication. Also refreshes
    /// user scoping + the active role snapshot.
    func bootstrap() async {
        refreshUserContext()
        progress = await persistence.loadCache()
        if let merged = await persistence.reconcileFromServer() {
            progress = merged
        }
        milestones = persistence.milestones()
    }

    /// Refresh `activeRole`, `isNewUser` and cache scoping from the auth store.
    func refreshUserContext() {
        let auth = AuthStore.shared
        if let user = auth.state.user {
            persistence.setUser(user.id)
            activeRole = Self.tourRole(for: user.role)
            // Restore the per-account new-user flag written at signup. Without
            // this, every contextual tour is gated off from the second launch.
            if persistence.isNewUser() { isNewUser = true }
            milestones = persistence.milestones()
        } else {
            persistence.setUser(nil)
            milestones = []
        }
    }

    // MARK: Domain milestones

    /// Real things the user has done, used by the Getting-Started checklists.
    @Published private(set) var milestones: Set<TourMilestone> = []

    /// Record a milestone from a genuine domain event. Never call this from a
    /// coach-mark button — a checklist row must reflect the product, not the
    /// tutorial.
    func recordMilestone(_ milestone: TourMilestone) {
        guard !milestones.contains(milestone) else { return }
        persistence.recordMilestone(milestone)
        milestones.insert(milestone)
    }

    func hasMilestone(_ milestone: TourMilestone) -> Bool {
        milestones.contains(milestone)
    }

    static func tourRole(for role: UserRole) -> TourRole {
        switch role {
        case .driver:  return .driver
        case .carOwner:return .owner
        case .admin:   return .owner
        }
    }

    /// Mark the account new from its creation date. Only ever *sets* the flag:
    /// a legacy account must never be forced into a tour, and an account that
    /// already earned the flag at signup must not lose it because a later
    /// response omitted `created_at`.
    func markNewUser(createdAt: Date?) {
        if ProductTourRollout.isNew(createdAt: createdAt) { isNewUser = true }
    }

    // MARK: Context

    func updateContext(_ mutate: (inout TourContext) -> Void) {
        mutate(&context)
    }

    func setActiveRole(_ role: TourRole) { activeRole = role }

    // MARK: Host arbitration

    /// Called by an overlay host on appear; returns its token and marks it
    /// frontmost (the most recently appeared host wins).
    func registerHost() -> Int {
        hostSeq += 1
        hostStack.append(hostSeq)
        frontmostHostToken = hostStack.last ?? 0
        return hostSeq
    }

    /// Called by an overlay host on disappear; hands frontmost back to the
    /// previously-registered host (e.g. the tab root when a sheet dismisses).
    func unregisterHost(_ token: Int) {
        hostStack.removeAll { $0 == token }
        frontmostHostToken = hostStack.last ?? 0
    }

    // MARK: Checklist chrome
    //
    // Thin pass-throughs so the Getting-Started checklist's collapse / dismiss
    // state persists in the same per-user cache blob. Wave-2 binds these in the
    // Today / My Cars / Discover empty states and Profile → "Show setup checklist".

    func checklistUIState(role: TourRole) -> ChecklistUIState {
        persistence.checklistUIState(role: role)
    }

    func setChecklistCollapsed(_ collapsed: Bool, role: TourRole) {
        var state = persistence.checklistUIState(role: role)
        state.collapsed = collapsed
        persistence.setChecklistUIState(state, role: role)
    }

    func dismissChecklist(role: TourRole) {
        var state = persistence.checklistUIState(role: role)
        state.dismissed = true
        persistence.setChecklistUIState(state, role: role)
        objectWillChange.send()
    }

    /// Profile → "Show setup checklist": un-dismiss and re-expand it.
    func showChecklist(role: TourRole) {
        persistence.setChecklistUIState(.expanded, role: role)
        objectWillChange.send()
    }

    // MARK: Event ingestion

    /// Ingest a real domain event. Maps it to a candidate tour, applies special
    /// per-tour handling (wizard step sync, PDF chaining), then runs eligibility
    /// and starts if allowed. The only public entry point view code uses.
    func handle(_ event: TourEvent) {
        switch event {
        case .roleActivated(let role):
            activeRole = role
        case .roleSwitched(let role):
            activeRole = role
        default:
            break
        }

        // Special cases that touch an already-running tour rather than starting
        // a fresh one.
        switch event {
        case .listingSubmitted:
            advanceOwnerListingToSubmitted()
            return
        case .bothSignaturesPresent:
            handleBothSignatures()
            return
        default:
            break
        }

        guard let candidate = TourEventRouter.candidate(for: event, activeRole: activeRole) else {
            return
        }

        // `listingWizardOpened` should not restart a tour that is already the
        // active one (its cards are advanced by the wizard's own navigation).
        if event == .listingWizardOpened, activeTour == candidate { return }

        // An armed replay wins over the new-user gate: the user asked for it.
        if armedTours.contains(candidate) {
            forceStart(candidate)
            return
        }

        if eligibility(candidate) {
            start(candidate)
        }
    }

    /// Whether `tour` may start right now — either it auto-qualifies, or the
    /// user explicitly armed it via replay.
    func canStart(_ tour: TourKey) -> Bool {
        armedTours.contains(tour) || eligibility(tour)
    }

    /// Show `tour` and run `action` when it finishes; if the tour can't start
    /// (already seen, legacy user, another tour owns the screen), run `action`
    /// immediately.
    ///
    /// This is how an explainer *gates* the thing it explains. The pre-request
    /// card's button says "Send request", so the request must not already be
    /// in flight behind it — otherwise the tour narrates a decision the user
    /// never got to make, and its cards arrive in the wrong order.
    ///
    /// Skipping the tour still runs the action: the user asked for it.
    @discardableResult
    func startOrRun(_ tour: TourKey, then action: @escaping () -> Void) -> Bool {
        guard canStart(tour) else {
            action()
            return false
        }
        pendingActions[tour] = action
        if armedTours.contains(tour) {
            forceStart(tour)
        } else {
            start(tour)
        }
        guard activeTour == tour else {
            // `start` declined (no visible steps, or out-prioritised).
            pendingActions.removeValue(forKey: tour)
            action()
            return false
        }
        return true
    }

    /// Public eligibility check (auto-start rules). Mirrors `TourEligibility`.
    func eligibility(_ tour: TourKey) -> Bool {
        TourEligibility.canAutoStart(
            tour: tour,
            progress: progress,
            activeRole: activeRole,
            isNewUser: isNewUser,
            global: globalStateProvider(),
            activeTour: activeTour
        )
    }

    // MARK: Start / advance / end

    func start(_ tour: TourKey) {
        guard !visibleSteps(for: tour).isEmpty else { return }

        if let active = activeTour, active != tour {
            // Only a strictly higher-priority tour may take the screen, and it
            // parks the incumbent rather than discarding it. An equal-priority
            // tour must wait: silently replacing one would strand it
            // permanently at `.inProgress` and yank its card out mid-read.
            guard tour.priority > active.priority else { return }
            paused = (active, stepIndex)
        }

        activeTour = tour
        stepIndex = 0
        isStepDismissed = false
        setStatus(.inProgress, for: tour, lastStep: visibleSteps(for: tour).first?.id)
        analytics.tourStarted(tour)
        emitStepViewed()
    }

    func next() {
        guard let tour = activeTour else { return }

        // A screen-driven tour's card explains the page under it. "Got it"
        // dismisses the card and returns the screen to the user; the screen
        // decides when the next card appears (`syncScreenStep`). The terminal
        // card is the exception — it has no page to hand back.
        if tour.isScreenDriven, !isTerminalStep {
            isStepDismissed = true
            setStatus(.inProgress, for: tour, lastStep: currentStep?.id)
            return
        }

        switch TourReducer.advance(stepIndex: stepIndex, stepCount: stepCount) {
        case .move(to: let idx):
            stepIndex = idx
            isStepDismissed = false
            setStatus(.inProgress, for: tour, lastStep: currentStep?.id)
            emitStepViewed()
        case .finish:
            complete()
        }
    }

    /// True when the active tour is sitting on its last visible step.
    private var isTerminalStep: Bool {
        stepCount > 0 && stepIndex == stepCount - 1
    }

    /// Point a screen-driven tour at the step with `stepID` and show its card.
    ///
    /// Called by the screen being taught (the listing wizard) whenever its own
    /// page changes. A no-op when that tour isn't running, when the id is
    /// unknown, or when the user already saw that card — so paging back and
    /// forth doesn't replay explanations.
    func syncScreenStep(_ stepID: String, in tour: TourKey) {
        guard activeTour == tour, tour.isScreenDriven else { return }
        let steps = visibleSteps(for: tour)
        guard let index = steps.firstIndex(where: { $0.id == stepID }) else { return }
        guard index > stepIndex || (index == stepIndex && !isStepDismissed) else { return }
        stepIndex = index
        isStepDismissed = false
        setStatus(.inProgress, for: tour, lastStep: steps[index].id)
        emitStepViewed()
    }

    func skip() {
        guard let tour = activeTour else { return }
        analytics.tourSkipped(tour, atStep: currentStep?.id ?? "")
        setStatus(.skipped, for: tour, lastStep: currentStep?.id)
        endActiveTour(finished: tour)
    }

    func complete() {
        guard let tour = activeTour else { return }
        analytics.tourCompleted(tour)
        setStatus(.completed, for: tour, lastStep: visibleSteps(for: tour).last?.id)
        endActiveTour(finished: tour)
    }

    /// Whether `tour` can play on whatever screen the user is looking at now.
    ///
    /// A tour whose first card spotlights a specific control (the Discover
    /// search field, the wizard's VIN row, the Buy button) is meaningless from
    /// Profile — the control isn't there. Only a tour that opens with a
    /// target-less card can start anywhere.
    private func canPlayAnywhere(_ tour: TourKey) -> Bool {
        guard !tour.isScreenDriven else { return false }
        guard let first = visibleSteps(for: tour).first else { return false }
        return first.target == nil
    }

    /// Reset a single tour and start it, bypassing the new-user gate (an
    /// explicit user action is always allowed). Used by Profile → Replay.
    ///
    /// Returns `.armedForLater` when the tour belongs to a screen the user
    /// isn't on: it is queued and starts the moment that screen appears, rather
    /// than drawing cards that point at controls which don't exist here.
    @discardableResult
    func replay(_ tour: TourKey) -> TourReplayOutcome {
        guard canPlayAnywhere(tour) else {
            armedTours.insert(tour)
            progress[tour] = nil
            return .armedForLater
        }
        forceStart(tour)
        return .started
    }

    /// Start `tour` regardless of the new-user gate. Clears any armed flag.
    private func forceStart(_ tour: TourKey) {
        guard !visibleSteps(for: tour).isEmpty else { return }
        armedTours.remove(tour)
        progress[tour] = .inProgress
        Task { await persistence.upsert(tour, status: .inProgress, lastStep: visibleSteps(for: tour).first?.id) }
        paused = nil
        activeTour = tour
        stepIndex = 0
        isStepDismissed = false
        analytics.tourStarted(tour)
        emitStepViewed()
    }

    /// "Replay app tour" — welcome + the active role's tab walk.
    @discardableResult
    func replayWelcomeAndTabs() -> TourReplayOutcome {
        // Arm the tab walk so `chainAfter` can start it even for a legacy
        // account, which the new-user gate would otherwise refuse.
        if let tabs = TourCatalogue.tabsTour(for: activeRole) {
            progress[tabs] = nil
            armedTours.insert(tabs)
        }
        return replay(.sharedWelcome)
    }

    #if DEBUG
    /// Destructive: clear all local + server progress and re-bootstrap.
    func resetAll() async {
        await persistence.clearAll()
        progress = [:]
        milestones = []
        activeTour = nil
        stepIndex = 0
        isStepDismissed = false
        paused = nil
        pendingActions = [:]
        armedTours = []
        context = TourContext()
        await bootstrap()
    }
    #endif

    // MARK: Private helpers

    private func emitStepViewed() {
        guard let tour = activeTour, let step = currentStep else { return }
        analytics.stepViewed(tour, step: step.id, index: stepIndex)
    }

    private func setStatus(_ status: TourStatus, for tour: TourKey, lastStep: String?) {
        guard TourReducer.mayOverwrite(existing: progress[tour], with: status) else { return }
        progress[tour] = status
        Task { await persistence.upsert(tour, status: status, lastStep: lastStep) }
    }

    /// Tear down the active tour and resume a paused one or run a chained tour.
    private func endActiveTour(finished: TourKey) {
        activeTour = nil
        stepIndex = 0
        isStepDismissed = false

        // Release anything this tour was gating (completed or skipped alike).
        if let action = pendingActions.removeValue(forKey: finished) {
            action()
        }

        // Resume a pre-empted tour if any.
        if let resume = paused {
            paused = nil
            activeTour = resume.tour
            stepIndex = resume.stepIndex
            emitStepViewed()
            return
        }

        chainAfter(finished)
    }

    /// Chain follow-on tours (welcome → role tabs; role switch → role tabs;
    /// signature lock → the finalized-PDF teach once the document exists).
    private func chainAfter(_ finished: TourKey) {
        switch finished {
        case .sharedWelcome, .roleSwitch:
            // `canStart` (not `eligibility`) so a replayed welcome still chains
            // into the tab walk for an existing account.
            if let tabs = TourCatalogue.tabsTour(for: activeRole), canStart(tabs) {
                if armedTours.contains(tabs) { forceStart(tabs) } else { start(tabs) }
            }
        case .signatureLock:
            // Deferred rather than started alongside: both are priority 50, so
            // starting `pdfReady` while the lock card was still up used to
            // replace it mid-read and leave it stuck at `.inProgress`.
            if context.pdfReady, eligibility(.pdfReady) {
                start(.pdfReady)
            }
        default:
            break
        }
    }

    /// `listingSubmitted`: jump the owner listing tour to its terminal
    /// confirmation card (the fix for the silent-dismiss confusion), starting
    /// it first if it is eligible but not yet running.
    private func advanceOwnerListingToSubmitted() {
        let tour = TourKey.ownerFirstListing
        let steps = visibleSteps(for: tour)
        guard let submittedIndex = steps.firstIndex(where: { $0.id == "submitted" }) else { return }

        if activeTour != tour {
            // Only auto-surface for eligible (new) users; otherwise leave silent.
            guard eligibility(tour) || progress[tour] == .inProgress else { return }
            activeTour = tour
            if progress[tour] == nil {
                setStatus(.inProgress, for: tour, lastStep: steps.first?.id)
                analytics.tourStarted(tour)
            }
        }
        stepIndex = submittedIndex
        isStepDismissed = false
        setStatus(.inProgress, for: tour, lastStep: steps[submittedIndex].id)
        emitStepViewed()
    }

    /// `bothSignaturesPresent`: run the signature-lock teach. The `pdfReady`
    /// card follows it via `chainAfter`, never on top of it — and only once the
    /// finalized PDF actually exists (`context.pdfReady`).
    private func handleBothSignatures() {
        if eligibility(.signatureLock) {
            start(.signatureLock)
            return
        }
        // Lock teach already done (or still running): only surface the PDF card
        // when nothing else owns the screen.
        guard activeTour == nil, context.pdfReady, eligibility(.pdfReady) else { return }
        start(.pdfReady)
    }
}
