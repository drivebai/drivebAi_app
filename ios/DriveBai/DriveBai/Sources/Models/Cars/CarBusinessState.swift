import Foundation

// MARK: - Canonical Car Business State
//
// THE single client-side derivation of "what state is this car in" (QA pt 3,
// design decision D9). Every screen that renders a car's status must derive
// it through `CarBusinessState.forCar(_:)` and display it with
// `CarStatusPill` — never by switching on `car.status` directly. This
// replaces the per-screen copies (`DisplayCarStatus` in OwnerMyCarsView and
// the raw `car.status.displayText` hero badge in CarDetailView), which
// wave-2 deletes.
//
// Precedence (highest first):
//   1. An attached active rental — authoritative. A paused-but-rented
//      listing still shows as rented: the pause toggle only blocks NEW
//      requests, not the one already running.
//   2. Sold (via `Car.isSold`; see that property for why it isn't a
//      `CarListingStatus` case).
//   3. Paused — from either the `is_paused` flag or a `paused` status row.
//   4. Pending review.
//   5. `status == "rented"` without a rental payload (non-owner fetches, or
//      list endpoints that don't join the lease) — rented with no details.
//   6. Available.

enum CarBusinessState: Equatable {
    case pendingReview
    case available
    case rented(ActiveRentalSummary?)
    case paused
    case sold

    static func forCar(_ car: Car) -> CarBusinessState {
        if let rental = car.activeRental { return .rented(rental) }   // active lease wins over paused
        if car.isSold { return .sold }
        if car.isPaused || car.status == .paused { return .paused }
        if car.status == .pending { return .pendingReview }
        if car.status == .rented { return .rented(nil) }
        return .available
    }
}
