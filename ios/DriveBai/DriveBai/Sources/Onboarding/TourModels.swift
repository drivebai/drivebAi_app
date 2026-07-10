import Foundation

// MARK: - ProductTour namespace
//
// Every symbol in the product-tour / coach-mark system lives in the
// **ProductTour** namespace to avoid colliding with the pre-existing signup
// vocabulary — `OnboardingTask` (a Today action card), the `onboarding_status`
// ENUM, `OnboardingStatus`, and `OnboardingResumeView`. Nothing here redefines
// any of those symbols. The only human word "onboarding" we keep is on the
// backend *table*/*route* (`user_onboarding_progress`, `/me/onboarding-progress`)
// and the persistence adapter name, which cannot collide with a Swift type.

// MARK: - Role

/// Which role a tour belongs to. `shared` tours are eligible in either role.
enum TourRole: String, Codable, CaseIterable {
    case driver
    case owner
    case shared
}

// MARK: - Tabs

/// Logical tab identity used by tab-bar spotlights. This is intentionally
/// NOT an `Int`-raw enum: driver `discover` and owner `myCars` both occupy the
/// same physical slot (index 1), which a raw-`Int` enum cannot express (two
/// cases can't share a raw value). `index` maps each case to its cell.
enum TourTab: Hashable {
    case today
    case discover
    case myCars
    case chats
    case profile

    /// Physical tab-cell index in the native 4-tab bar.
    /// Driver: today=0, discover=1, chats=2, profile=3.
    /// Owner:  today=0, myCars=1,   chats=2, profile=3.
    var index: Int {
        switch self {
        case .today:              return 0
        case .discover, .myCars:  return 1
        case .chats:              return 2
        case .profile:            return 3
        }
    }
}

// MARK: - Target IDs

/// Identifies an on-screen (or geometric) anchor a coach-mark can spotlight.
/// In-screen targets are tagged with `.onboardingTarget(_:)` and resolved via
/// anchor preferences; `.tab(_)` targets are resolved geometrically because the
/// native `UITabBar` lives outside SwiftUI's preference tree.
enum TourTargetID: Hashable {
    case tab(TourTab)

    // Discover
    case discoverSearch
    case discoverFirstCard

    // Car detail CTAs
    case requestLeaseCTA
    case buyThisCarCTA

    // Chat segments + location
    case chatMessagesSegment
    case chatRequestsSegment
    case getDirectionsRow

    // Today
    case todayFirstCard

    // Listing wizard steps
    case wizardVIN
    case wizardPricing
    case wizardPhotos
    case wizardDocuments
    case wizardReview

    // Purchase / Bill of Sale / inspection
    case bosFirstSection
    case bosSignedSection
    case finalizedPdfLink
    case inspectionCTA

    // My cars
    case myCarsEmpty
}

// MARK: - Presentation enums

/// Where the arrow on a coach card points. `none` is used for centered /
/// target-less cards and for steps whose anchor could not be resolved.
enum ArrowDirection {
    case up
    case down
    case none
}

/// Where the coach card floats relative to its target.
enum CardPlacement {
    case above
    case below
    case centered
}

// MARK: - Persisted status

/// Per-tour lifecycle status. Persisted (server + local cache) so a tour never
/// re-fires once the user has completed or skipped it.
enum TourStatus: String, Codable, CaseIterable {
    case inProgress = "in_progress"
    case completed
    case skipped

    /// A tour that reached a terminal state suppresses future auto-starts.
    var isTerminal: Bool { self == .completed || self == .skipped }
}

// MARK: - Tour keys

/// The catalogue of tours. The raw value is the durable persistence key that
/// mirrors the backend `tour_key` column and the local cache. Versioned (`_v1`)
/// so copy revisions can re-fire under a new key without a migration.
enum TourKey: String, Codable, CaseIterable {
    case sharedWelcome        = "shared_welcome_v1"
    case driverTabs           = "driver_tabs_v1"
    case ownerTabs            = "owner_tabs_v1"
    case driverFirstDiscover  = "driver_first_discover_v1"
    case driverCarDetail      = "driver_car_detail_v1"
    case driverPreRequest     = "driver_pre_request_v1"
    case driverPostRequest    = "driver_post_request_v1"
    case ownerFirstCar        = "owner_first_car_v1"
    case ownerFirstListing    = "owner_first_listing_v1"
    case chatSegments         = "chat_segments_v1"
    case firstTodayAction     = "first_today_action_v1"
    case roleSwitch           = "role_switch_v1"
    case purchaseIntro        = "purchase_intro_v1"
    case bosIntro             = "bos_intro_v1"
    case signatureLock        = "signature_lock_v1"
    case pdfReady             = "pdf_ready_v1"
    case inspectionIntro      = "inspection_intro_v1"

    /// The role that owns this tour, used for per-role scoping + eligibility.
    var role: TourRole {
        switch self {
        case .driverTabs, .driverFirstDiscover, .driverCarDetail,
             .driverPreRequest, .driverPostRequest:
            return .driver
        case .ownerTabs, .ownerFirstCar, .ownerFirstListing:
            return .owner
        case .sharedWelcome, .chatSegments, .firstTodayAction, .roleSwitch,
             .purchaseIntro, .bosIntro, .signatureLock, .pdfReady, .inspectionIntro:
            return .shared
        }
    }

    /// Priority for conflict resolution. `driverPostRequest` is the single
    /// highest-priority teach (where the driver pays) and can pre-empt an
    /// in-progress lower-priority tour.
    var priority: Int {
        self == .driverPostRequest ? 100 : 50
    }

    /// A tour whose step index is driven by the screen it teaches, not by the
    /// coach card's own button.
    ///
    /// The listing wizard is the only one: its cards explain the page the user
    /// is currently on, so tapping "Got it" must dismiss the card and hand the
    /// screen back — the wizard's own Continue button decides when the next
    /// card appears. Advancing the tour from the card instead would march
    /// through all six explanations while the wizard sat on page one, pointing
    /// at controls that aren't there.
    var isScreenDriven: Bool {
        self == .ownerFirstListing
    }
}

// MARK: - Tour step

/// One coach-mark card within a tour. Pure value type — no view state — so the
/// catalogue and the eligibility/transition logic can be unit-tested without
/// UIKit.
struct TourStep: Identifiable, Equatable {
    let id: String
    let tour: TourKey
    let title: String
    let body: String
    /// `nil` ⇒ a dim-only, centered card with no cutout or arrow.
    let target: TourTargetID?
    let arrow: ArrowDirection
    let cardPlacement: CardPlacement
    /// CTA label for the primary (advance) button, e.g. "Next", "Got it".
    let primaryTitle: String
    /// Whether the step exposes a top-right "Skip tour" affordance.
    let canSkip: Bool

    init(
        id: String,
        tour: TourKey,
        title: String,
        body: String,
        target: TourTargetID?,
        arrow: ArrowDirection = .none,
        cardPlacement: CardPlacement = .below,
        primaryTitle: String = "Next",
        canSkip: Bool = true
    ) {
        self.id = id
        self.tour = tour
        self.title = title
        self.body = body
        self.target = target
        self.arrow = arrow
        self.cardPlacement = cardPlacement
        self.primaryTitle = primaryTitle
        self.canSkip = canSkip
    }
}

// MARK: - Replay outcome

/// What happened when the user asked to replay a tour from Profile.
enum TourReplayOutcome: Equatable {
    /// The tour started right away on the current screen.
    case started
    /// The tour belongs to another screen and will start when the user opens it.
    case armedForLater
}

// MARK: - Events

/// Real app / domain events. Views raise these via `coordinator.handle(_:)`;
/// they never ask to "start tour X" directly — all policy lives in the
/// coordinator. There are **no timers**: every case is a concrete event.
///
/// Argument-less cases match the exact Wave-2 integration call sites (e.g.
/// `coordinator.handle(.leaseRequestCreated)`); contextual nuance (a car being
/// for-sale, a pickup location existing) is supplied separately through
/// `ProductTourCoordinator.updateContext(_:)` so call sites stay one-liners.
enum TourEvent: Equatable {
    case signupCompleted
    case roleActivated(TourRole)
    case discoverAppeared
    case carDetailOpened
    case willSendLeaseRequest
    case leaseRequestCreated
    case ownerCarCountZero
    case listingWizardOpened
    case listingSubmitted
    case chatWithRequestOpened
    case firstTodayActionPresent
    case roleSwitched(TourRole)
    case buyThisCarTapped
    case bosOpened
    case bothSignaturesPresent
    case inspectionAvailable
}

// MARK: - Small utilities

extension Collection {
    /// Bounds-checked subscript used to read `steps[safe: stepIndex]` without
    /// trapping when a tour has been advanced past its last card.
    subscript(safe index: Index) -> Element? {
        indices.contains(index) ? self[index] : nil
    }
}
