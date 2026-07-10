import Foundation

// MARK: - Tour Catalogue
//
// The single source of truth for every tour's steps and their FINAL copy.
// Copy rules (enforced here): one concept per card, ≤2 short sentences, no
// jargon. Never say "capture", "escrow", or "MV-912". Payment always reads
// "held on your card, not charged yet" / "your payment completes when you
// accept the vehicle".
//
// `steps(for:)` returns the *complete* list for a tour. Steps whose target is
// only conditionally present (`.buyThisCarCTA` on a for-sale car,
// `.getDirectionsRow` when a pickup location exists) are still listed here and
// filtered at runtime by `ProductTourCoordinator.visibleSteps(for:)` using the
// live `TourContext`, so the pure copy stays testable in isolation.

enum TourCatalogue {

    /// All steps for a tour, in order. Order is load-bearing (progress = index).
    static func steps(for tour: TourKey) -> [TourStep] {
        switch tour {

        // MARK: shared_welcome_v1
        case .sharedWelcome:
            return [
                TourStep(
                    id: "welcome",
                    tour: tour,
                    title: "Welcome to DrivaBai",
                    body: "Rent or list a car with someone real, nearby. This quick tour shows you around.",
                    target: nil,
                    arrow: .none,
                    cardPlacement: .centered,
                    primaryTitle: "Get started",
                    canSkip: true
                )
            ]

        // MARK: driver_tabs_v1
        case .driverTabs:
            return [
                TourStep(id: "today", tour: tour,
                         title: "Today",
                         body: "Your rentals and to-dos live here.",
                         target: .tab(.today), arrow: .down, cardPlacement: .above,
                         primaryTitle: "Next"),
                TourStep(id: "discover", tour: tour,
                         title: "Discover",
                         body: "Find a car to rent, right here.",
                         target: .tab(.discover), arrow: .down, cardPlacement: .above,
                         primaryTitle: "Next"),
                TourStep(id: "chats", tour: tour,
                         title: "Chats",
                         body: "Message owners and track requests.",
                         target: .tab(.chats), arrow: .down, cardPlacement: .above,
                         primaryTitle: "Next"),
                TourStep(id: "profile", tour: tour,
                         title: "Profile",
                         body: "Your documents, help and settings.",
                         target: .tab(.profile), arrow: .down, cardPlacement: .above,
                         primaryTitle: "Explore Discover")
            ]

        // MARK: owner_tabs_v1
        case .ownerTabs:
            return [
                TourStep(id: "today", tour: tour,
                         title: "Today",
                         body: "Requests and hand-offs to act on.",
                         target: .tab(.today), arrow: .down, cardPlacement: .above,
                         primaryTitle: "Next"),
                TourStep(id: "myCars", tour: tour,
                         title: "My cars",
                         body: "Every car you list shows up here.",
                         target: .tab(.myCars), arrow: .down, cardPlacement: .above,
                         primaryTitle: "Next"),
                TourStep(id: "chats", tour: tour,
                         title: "Chats",
                         body: "Talk to renters and buyers.",
                         target: .tab(.chats), arrow: .down, cardPlacement: .above,
                         primaryTitle: "Next"),
                TourStep(id: "profile", tour: tour,
                         title: "Profile",
                         body: "Settings, help and your account.",
                         target: .tab(.profile), arrow: .down, cardPlacement: .above,
                         primaryTitle: "List a car")
            ]

        // MARK: driver_first_discover_v1
        case .driverFirstDiscover:
            return [
                TourStep(id: "search", tour: tour,
                         title: "Search & filter",
                         body: "Find cars by name, price or location.",
                         target: .discoverSearch, arrow: .up, cardPlacement: .below,
                         primaryTitle: "Next"),
                TourStep(id: "card", tour: tour,
                         title: "Open a listing",
                         body: "Every car opens to its photos, price and terms.",
                         target: .discoverFirstCard, arrow: .up, cardPlacement: .below,
                         primaryTitle: "Got it")
            ]

        // MARK: driver_car_detail_v1
        case .driverCarDetail:
            return [
                TourStep(id: "requestLease", tour: tour,
                         title: "Request to rent",
                         body: "Send the owner a rental request. They'll review it.",
                         target: .requestLeaseCTA, arrow: .down, cardPlacement: .above,
                         primaryTitle: "Next"),
                // Only shown for for-sale cars (filtered by TourContext).
                TourStep(id: "buy", tour: tour,
                         title: "Or buy it",
                         body: "Some cars are for sale, too.",
                         target: .buyThisCarCTA, arrow: .down, cardPlacement: .above,
                         primaryTitle: "Got it")
            ]

        // MARK: driver_pre_request_v1  ("What happens next?")
        case .driverPreRequest:
            return [
                TourStep(id: "explain", tour: tour,
                         title: "What happens next?",
                         body: "You send a request, the owner accepts, then you pay on the Requests tab.",
                         target: nil, arrow: .none, cardPlacement: .centered,
                         primaryTitle: "Send request", canSkip: true)
            ]

        // MARK: driver_post_request_v1  (highest priority)
        //
        // Deliberately one card. The red dot and the Messages/Requests split are
        // taught once, by `chatSegments`, on the next chat the driver opens —
        // repeating them here meant a renting driver heard both twice.
        case .driverPostRequest:
            return [
                TourStep(id: "whereToPay", tour: tour,
                         title: "Your request is in",
                         body: "When the owner accepts, your Pay button appears here on Requests.",
                         target: .chatRequestsSegment, arrow: .up, cardPlacement: .below,
                         primaryTitle: "Got it")
            ]

        // MARK: owner_first_car_v1  (My Cars, zero listings)
        case .ownerFirstCar:
            return [
                TourStep(id: "first_car", tour: tour,
                         title: "List your first car",
                         body: "Add a vehicle to start receiving rental and purchase requests.",
                         target: .myCarsEmpty, arrow: .down, cardPlacement: .above,
                         primaryTitle: "Got it")
            ]

        // MARK: owner_first_listing_v1  (sheet-hosted, one card per wizard page)
        //
        // Screen-driven (see `TourKey.isScreenDriven`): each card explains the
        // page the user is on and is dismissed with "Got it". The wizard's own
        // navigation moves the tour along, so a card can never describe a page
        // the user isn't looking at.
        case .ownerFirstListing:
            return [
                TourStep(id: "vin", tour: tour,
                         title: "Start with the VIN",
                         body: "Search your VIN to auto-fill the details. It's optional.",
                         target: .wizardVIN, arrow: .up, cardPlacement: .below,
                         primaryTitle: "Got it"),
                TourStep(id: "pricing", tour: tour,
                         title: "Set your price",
                         body: "Choose weekly rent, sale price, or both. Selling adds a Title step later.",
                         target: .wizardPricing, arrow: .up, cardPlacement: .below,
                         primaryTitle: "Got it"),
                TourStep(id: "photos", tour: tour,
                         title: "Add 8 photos",
                         body: "We'll guide you through 8 angles. Better photos get more requests.",
                         target: .wizardPhotos, arrow: .up, cardPlacement: .below,
                         primaryTitle: "Got it"),
                TourStep(id: "documents", tour: tour,
                         title: "Required documents",
                         body: "Registration, inspection and insurance are needed for approval.",
                         target: .wizardDocuments, arrow: .up, cardPlacement: .below,
                         primaryTitle: "Got it"),
                TourStep(id: "review", tour: tour,
                         title: "Review & submit",
                         body: "Check everything, then submit for review.",
                         target: .wizardReview, arrow: .up, cardPlacement: .below,
                         primaryTitle: "Got it"),
                // Terminal confirmation — no skip.
                TourStep(id: "submitted", tour: tour,
                         title: "Submitted for review",
                         body: "A DrivaBai reviewer checks your listing, usually within a day. We'll notify you when it's live.",
                         target: nil, arrow: .none, cardPlacement: .centered,
                         primaryTitle: "Done", canSkip: false)
            ]

        // MARK: chat_segments_v1
        case .chatSegments:
            return [
                TourStep(id: "messages", tour: tour,
                         title: "Messages",
                         body: "Chat freely with the other person here.",
                         target: .chatMessagesSegment, arrow: .up, cardPlacement: .below,
                         primaryTitle: "Next"),
                TourStep(id: "requests", tour: tour,
                         title: "Requests",
                         body: "Rentals and sales — and their Pay/Accept buttons — live here.",
                         target: .chatRequestsSegment, arrow: .up, cardPlacement: .below,
                         primaryTitle: "Next"),
                TourStep(id: "dot", tour: tour,
                         title: "The red dot",
                         body: "It means something needs your action on Requests.",
                         target: .chatRequestsSegment, arrow: .up, cardPlacement: .below,
                         primaryTitle: "Got it")
            ]

        // MARK: first_today_action_v1
        case .firstTodayAction:
            return [
                TourStep(id: "card", tour: tour,
                         title: "Your next step",
                         body: "This is where your next step lives.",
                         target: .todayFirstCard, arrow: .none, cardPlacement: .below,
                         primaryTitle: "Next"),
                // Only shown when a pickup location exists (filtered by TourContext).
                TourStep(id: "directions", tour: tour,
                         title: "Get directions",
                         body: "Open the pickup spot in Apple or Google Maps.",
                         target: .getDirectionsRow, arrow: .none, cardPlacement: .below,
                         primaryTitle: "Got it")
            ]

        // MARK: role_switch_v1
        case .roleSwitch:
            return [
                TourStep(id: "switched", tour: tour,
                         title: "You switched modes",
                         body: "Your tabs and to-dos changed to match. Switch back anytime in Profile.",
                         target: nil, arrow: .none, cardPlacement: .centered,
                         primaryTitle: "Got it")
            ]

        // MARK: purchase_intro_v1
        case .purchaseIntro:
            return [
                TourStep(id: "overview", tour: tour,
                         title: "Buying a car",
                         body: "Make an offer. If the seller accepts, you'll sign a Bill of Sale together.",
                         target: .buyThisCarCTA, arrow: .down, cardPlacement: .above,
                         primaryTitle: "Next"),
                TourStep(id: "hold", tour: tour,
                         title: "How payment works",
                         body: "When it's time, the amount is held on your card, not charged yet.",
                         target: nil, arrow: .none, cardPlacement: .centered,
                         primaryTitle: "Got it")
            ]

        // MARK: bos_intro_v1  (sheet-hosted)
        case .bosIntro:
            return [
                TourStep(id: "fill", tour: tour,
                         title: "Fill in the details",
                         body: "Add the vehicle and terms. You and the other party each sign.",
                         target: .bosFirstSection, arrow: .up, cardPlacement: .below,
                         primaryTitle: "Next"),
                TourStep(id: "turns", tour: tour,
                         title: "Whose turn?",
                         body: "The banner at the top always tells you the next step and who's up.",
                         target: nil, arrow: .none, cardPlacement: .centered,
                         primaryTitle: "Got it")
            ]

        // MARK: signature_lock_v1  (sheet-hosted, no skip)
        case .signatureLock:
            return [
                TourStep(id: "locked", tour: tour,
                         title: "Signed and locked",
                         body: "Once you sign, these details lock so no one can change them.",
                         target: .bosSignedSection, arrow: .up, cardPlacement: .below,
                         primaryTitle: "Got it", canSkip: false)
            ]

        // MARK: pdf_ready_v1
        case .pdfReady:
            return [
                TourStep(id: "ready", tour: tour,
                         title: "Your signed Bill of Sale",
                         body: "Both signatures are in. Tap to view, then Share or Save to Files.",
                         target: .finalizedPdfLink, arrow: .up, cardPlacement: .below,
                         primaryTitle: "Got it")
            ]

        // MARK: inspection_intro_v1
        case .inspectionIntro:
            return [
                TourStep(id: "inspect", tour: tour,
                         title: "Inspect before you accept",
                         body: "Check the car over. Your payment completes when you accept the vehicle.",
                         target: .inspectionCTA, arrow: .up, cardPlacement: .below,
                         primaryTitle: "Got it")
            ]
        }
    }

    /// Tours triggered on a `.tab(_)` walk for a given role — used by
    /// "Replay app tour" to re-run welcome + the correct role tabs.
    static func tabsTour(for role: TourRole) -> TourKey? {
        switch role {
        case .driver: return .driverTabs
        case .owner:  return .ownerTabs
        case .shared: return nil
        }
    }
}
