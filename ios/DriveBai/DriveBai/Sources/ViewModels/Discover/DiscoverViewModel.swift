import Foundation
import SwiftUI
import Combine

/// ViewModel for Driver Discover tab - fetches available listings from backend
@MainActor
final class DiscoverViewModel: ObservableObject {
    static let shared = DiscoverViewModel()

    // MARK: - Published Properties

    @Published var listings: [Car] = []
    @Published var filteredListings: [Car] = []
    @Published var isLoading: Bool = false
    @Published var error: String?
    @Published var searchText: String = ""
    @Published var selectedFilter: DiscoverFilter = .all
    /// Advanced filters driven by `DiscoverFilterSheet`. v1 applies these
    /// predicates client-side against the already-loaded listings.
    @Published var filters: DiscoverFilters = .empty

    // MARK: - Private Properties

    private let apiClient: APIClient
    private var cancellables = Set<AnyCancellable>()

    // MARK: - Init

    private init(apiClient: APIClient = .shared) {
        self.apiClient = apiClient
        setupSearchDebounce()
    }

    // MARK: - Setup

    private func setupSearchDebounce() {
        // Debounce search and filter changes. `filters` (advanced filter
        // sheet state) is folded in too so applying a Body/Fuel/Price cap
        // re-runs `applyFilters` without a manual refresh.
        Publishers.CombineLatest3($searchText, $selectedFilter, $filters)
            .debounce(for: .milliseconds(300), scheduler: RunLoop.main)
            .sink { [weak self] searchText, filter, _ in
                self?.applyFilters(searchText: searchText, filter: filter)
            }
            .store(in: &cancellables)
    }

    // MARK: - Public Methods

    /// Fetch listings from backend
    /// On error, preserves existing listings instead of clearing them
    func fetchListings() async {
        isLoading = true
        error = nil

        #if DEBUG
        print("[DiscoverViewModel] fetchListings called with filter: \(selectedFilter)")
        #endif

        do {
            let statusParam: String?
            switch selectedFilter {
            case .all:
                // Pass nil to get all statuses, but backend defaults to "available"
                // To truly get all, we'd need backend change or pass explicit param
                statusParam = nil
            case .available:
                statusParam = "available"
            case .nearby, .popular:
                // For now, treat these the same as available
                statusParam = "available"
            case .liked:
                // Liked filtering is done client-side, fetch all available listings
                statusParam = nil
            }

            #if DEBUG
            print("[DiscoverViewModel] Calling API with status: \(statusParam ?? "nil")")
            #endif

            let fetchedListings = try await apiClient.fetchListings(status: statusParam, search: nil)

            // Only update listings on successful fetch
            listings = fetchedListings
            applyFilters(searchText: searchText, filter: selectedFilter)

            #if DEBUG
            print("[DiscoverViewModel] Fetched \(listings.count) listings:")
            for listing in listings {
                let coverSlot = listing.photoSlots.first { $0.slotType == .coverFront }
                print("  - \(listing.id): '\(listing.title)' status=\(listing.status.rawValue) coverURL=\(coverSlot?.imageURL ?? "nil")")
            }
            print("[DiscoverViewModel] After filtering: \(filteredListings.count) listings")
            #endif
        } catch let apiError as APIError {
            // IMPORTANT: Do NOT clear existing listings on error
            // This preserves the user's view of data during transient network issues
            error = apiError.errorDescription
            #if DEBUG
            print("[DiscoverViewModel] Failed to fetch listings (keeping existing \(listings.count) listings): \(apiError)")
            #endif
        } catch {
            // IMPORTANT: Do NOT clear existing listings on error
            self.error = error.localizedDescription
            #if DEBUG
            print("[DiscoverViewModel] Failed to fetch listings (keeping existing \(listings.count) listings): \(error)")
            #endif
        }

        isLoading = false
    }

    /// Refresh listings (for pull-to-refresh)
    func refresh() async {
        await fetchListings()
    }

    /// Clear all cached data - called on logout to ensure next user gets fresh data
    func clearAll() {
        listings = []
        filteredListings = []
        searchText = ""
        selectedFilter = .all
        filters = .empty
        error = nil
        isLoading = false
    }

    /// Search listings locally
    func search(_ query: String) {
        searchText = query
    }

    /// Apply filter
    func applyFilter(_ filter: DiscoverFilter) {
        selectedFilter = filter
    }

    // MARK: - Private Methods

    private func applyFilters(searchText: String, filter: DiscoverFilter) {
        var result = listings

        #if DEBUG
        print("[DiscoverViewModel] applyFilters: input=\(listings.count) listings, filter=\(filter), searchText='\(searchText)'")
        for listing in listings {
            print("  - '\(listing.title)' status=\(listing.status.rawValue)")
        }
        #endif

        // Apply search filter
        if !searchText.isEmpty {
            let query = searchText.lowercased()
            result = result.filter { car in
                car.title.lowercased().contains(query) ||
                car.specs.make.lowercased().contains(query) ||
                car.specs.model.lowercased().contains(query) ||
                car.location.neighborhood.lowercased().contains(query)
            }
        }

        // Apply category filter
        switch filter {
        case .all:
            break
        case .available:
            result = result.filter { $0.status == .available }
        case .nearby:
            // TODO: Sort by distance when we have location
            result = result.filter { $0.status == .available }
        case .popular:
            // TODO: Sort by rating/rentals when we have that data
            result = result.filter { $0.status == .available }
                .sorted { $0.rentedWeeks > $1.rentedWeeks }
        case .liked:
            // Liked filtering is done in the view layer using LikedListingsStore
            // Here we just pass through all available listings
            break
        }

        // Apply advanced filter-sheet predicates (v1: client-side against the
        // already-loaded listings — see the spec's deferred follow-up for the
        // eventual server-side push).
        if let cap = filters.priceMaxWeekly {
            result = result.filter { car in
                guard let price = car.weeklyRentPrice?.amount else { return false }
                return price <= cap
            }
        }
        if let year = filters.minYear {
            result = result.filter { $0.specs.year >= year }
        }
        if let body = filters.bodyType {
            result = result.filter { $0.specs.bodyType == body }
        }
        if let fuel = filters.fuelType {
            result = result.filter { $0.specs.fuelType == fuel }
        }
        if let maxDeposit = filters.maxDeposit {
            result = result.filter { $0.requirements.depositAmount.amount <= maxDeposit }
        }

        #if DEBUG
        print("[DiscoverViewModel] applyFilters: output=\(result.count) listings")
        #endif

        filteredListings = result
    }

    // MARK: - Computed Properties

    var hasListings: Bool {
        !filteredListings.isEmpty
    }

    var isEmpty: Bool {
        !isLoading && filteredListings.isEmpty
    }

    var listingsCount: Int {
        filteredListings.count
    }
}

// MARK: - Mock Data for Preview/Fallback

extension DiscoverViewModel {
    static var mockListings: [Car] {
        [
            Car(
                title: "2023 Toyota Camry",
                description: "Reliable sedan in excellent condition",
                specs: CarSpecs(
                    bodyType: .sedan,
                    fuelType: .gas,
                    mileage: 15000,
                    year: 2023,
                    make: "Toyota",
                    model: "Camry"
                ),
                requirements: CarRequirements(
                    minYearsLicensedDriving: 2,
                    depositAmount: Money(amount: 500),
                    insuranceCoverage: .fullCoverage
                ),
                location: CarLocation(
                    neighborhood: "Downtown",
                    distanceMiles: 2.5
                ),
                owner: CarOwnerInfo(
                    name: "John D.",
                    rating: 4.8,
                    reviewCount: 24
                ),
                isForRent: true,
                weeklyRentPrice: Money(amount: 350),
                status: .available
            ),
            Car(
                title: "2022 Honda Accord",
                description: "Spacious and fuel-efficient",
                specs: CarSpecs(
                    bodyType: .sedan,
                    fuelType: .hybrid,
                    mileage: 22000,
                    year: 2022,
                    make: "Honda",
                    model: "Accord"
                ),
                requirements: CarRequirements(
                    minYearsLicensedDriving: 1,
                    depositAmount: Money(amount: 400),
                    insuranceCoverage: .fullCoverage
                ),
                location: CarLocation(
                    neighborhood: "Midtown",
                    distanceMiles: 1.8
                ),
                owner: CarOwnerInfo(
                    name: "Sarah M.",
                    rating: 4.9,
                    reviewCount: 56
                ),
                isForRent: true,
                weeklyRentPrice: Money(amount: 400),
                status: .available
            ),
            Car(
                title: "2021 Tesla Model 3",
                description: "Electric sedan with autopilot",
                specs: CarSpecs(
                    bodyType: .sedan,
                    fuelType: .electric,
                    mileage: 30000,
                    year: 2021,
                    make: "Tesla",
                    model: "Model 3"
                ),
                requirements: CarRequirements(
                    minYearsLicensedDriving: 3,
                    depositAmount: Money(amount: 1000),
                    insuranceCoverage: .fullCoverage
                ),
                location: CarLocation(
                    neighborhood: "Financial District",
                    distanceMiles: 3.2
                ),
                owner: CarOwnerInfo(
                    name: "Mike R.",
                    rating: 4.7,
                    reviewCount: 18
                ),
                isForRent: true,
                weeklyRentPrice: Money(amount: 650),
                status: .available
            )
        ]
    }
}
