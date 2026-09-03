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
//   4. Awaiting admin approval — the single publish gate (`is_approved`).
//   5. `status == "rented"` without a rental payload (non-owner fetches, or
//      list endpoints that don't join the lease) — rented with no details.
//   6. Available.

enum CarBusinessState: Equatable {
    /// A listing that admin approval hasn't cleared yet. Derived from
    /// `is_approved` (the single publish gate) so it disappears the moment
    /// the flag flips — see `forCar` for why this fixes the "approved but
    /// still shows Pending" confusion.
    case awaitingApproval
    /// Retained so screens outside this module (e.g. OwnerTodayView) keep
    /// compiling. `forCar` no longer produces it — every not-yet-live listing
    /// now resolves to `.awaitingApproval`.
    case pendingReview
    case available
    case rented(ActiveRentalSummary?)
    /// A committed lease that has not started running yet (item 5):
    /// accepted, payment_pending, or paid_awaiting_pickup. The car is
    /// reserved and hidden from Discover, so "Available now!" was a lie.
    case reserved(state: String, renterFirstName: String?)
    case paused
    case sold

    static func forCar(_ car: Car) -> CarBusinessState {
        // Sold is terminal and wins over everything, including a lingering
        // active-rental row. A car that was sold while still showing a rental
        // must read `.sold` (not `.rented`), or every screen that derives its
        // edit-lock / pill from this would re-expose the ex-owner's controls on
        // a car they no longer own.
        if car.isSold { return .sold }
        if let rental = car.activeRental { return .rented(rental) }   // active lease wins over paused
        // Pre-pickup committed lease (item 5): the car is spoken for even
        // though cars.status still says 'available'.
        if let rs = car.rentalState, rs != "picked_up" {
            return .reserved(state: rs, renterFirstName: car.renterFirstName)
        }
        if car.isPaused || car.status == .paused { return .paused }
        // Awaiting admin approval. `is_approved` is authoritative: an
        // unapproved listing always reads as awaiting, and once the flag
        // flips true the car LEAVES this state even if the server's `status`
        // still says `.pending` — which is exactly the "approved but still
        // shows Pending" confusion this replaces.
        if !car.isApproved { return .awaitingApproval }
        if car.status == .rented { return .rented(nil) }
        return .available
    }
}
