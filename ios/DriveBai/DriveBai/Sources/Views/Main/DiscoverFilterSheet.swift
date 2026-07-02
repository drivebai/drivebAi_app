import SwiftUI

// MARK: - Filter State
//
// Client-side filter shape used by the Discover tab. v1 applies these
// predicates against the already-loaded `listings` array (no server-side
// query params yet — see the deferred follow-up in the spec).

struct DiscoverFilters: Equatable {
    var priceMaxWeekly: Double? = nil     // in dollars
    var minYear: Int? = nil
    var bodyType: CarBodyType? = nil
    var fuelType: FuelType? = nil
    var maxDeposit: Double? = nil         // in dollars

    /// Number of active filters — drives the badge next to the toolbar button.
    var activeCount: Int {
        var n = 0
        if priceMaxWeekly != nil { n += 1 }
        if minYear != nil { n += 1 }
        if bodyType != nil { n += 1 }
        if fuelType != nil { n += 1 }
        if maxDeposit != nil { n += 1 }
        return n
    }

    static let empty = DiscoverFilters()
}

// MARK: - Filter Sheet

struct DiscoverFilterSheet: View {
    @ObservedObject var viewModel: DiscoverViewModel
    @Environment(\.dismiss) private var dismiss

    // Local draft state — only committed to the view model on Apply so the
    // user can back out with the swipe-down gesture without changing results.
    @State private var draft: DiscoverFilters

    // Toggles + numeric holders so the SwiftUI controls can bind cleanly.
    @State private var priceEnabled: Bool
    @State private var priceMax: Double
    @State private var yearEnabled: Bool
    @State private var minYear: Double
    @State private var depositEnabled: Bool
    @State private var depositMax: Double

    private let priceRange: ClosedRange<Double> = 50...2000
    private let depositRange: ClosedRange<Double> = 0...5000
    private let yearRange: ClosedRange<Double> = 2000...Double(Calendar.current.component(.year, from: Date()))

    init(viewModel: DiscoverViewModel) {
        self.viewModel = viewModel
        let current = viewModel.filters
        _draft = State(initialValue: current)
        _priceEnabled = State(initialValue: current.priceMaxWeekly != nil)
        _priceMax = State(initialValue: current.priceMaxWeekly ?? 500)
        _yearEnabled = State(initialValue: current.minYear != nil)
        _minYear = State(initialValue: Double(current.minYear ?? 2018))
        _depositEnabled = State(initialValue: current.maxDeposit != nil)
        _depositMax = State(initialValue: current.maxDeposit ?? 1000)
    }

    var body: some View {
        NavigationStack {
            Form {
                priceSection
                yearSection
                bodyTypeSection
                fuelTypeSection
                depositSection
            }
            .navigationTitle("Filters")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Reset") { resetFilters() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Apply") { applyFilters() }
                        .fontWeight(.semibold)
                }
            }
        }
    }

    // MARK: - Sections

    private var priceSection: some View {
        Section {
            Toggle("Max weekly price", isOn: $priceEnabled)
            if priceEnabled {
                HStack {
                    Text("Up to")
                        .foregroundColor(.secondary)
                    Spacer()
                    Text("$\(Int(priceMax))/wk")
                        .fontWeight(.semibold)
                }
                Slider(value: $priceMax, in: priceRange, step: 25)
            }
        } header: {
            Text("Price")
        }
    }

    private var yearSection: some View {
        Section {
            Toggle("Minimum year", isOn: $yearEnabled)
            if yearEnabled {
                HStack {
                    Text("From")
                        .foregroundColor(.secondary)
                    Spacer()
                    Text("\(Int(minYear)) or newer")
                        .fontWeight(.semibold)
                }
                Slider(value: $minYear, in: yearRange, step: 1)
            }
        } header: {
            Text("Year")
        }
    }

    private var bodyTypeSection: some View {
        Section {
            Picker("Body type", selection: $draft.bodyType) {
                Text("Any").tag(CarBodyType?.none)
                ForEach(CarBodyType.allCases) { type in
                    Text(type.displayText).tag(CarBodyType?.some(type))
                }
            }
        } header: {
            Text("Body type")
        }
    }

    private var fuelTypeSection: some View {
        Section {
            Picker("Fuel", selection: $draft.fuelType) {
                Text("Any").tag(FuelType?.none)
                ForEach(FuelType.allCases) { fuel in
                    Text(fuel.rawValue).tag(FuelType?.some(fuel))
                }
            }
        } header: {
            Text("Fuel")
        }
    }

    private var depositSection: some View {
        Section {
            Toggle("Max deposit", isOn: $depositEnabled)
            if depositEnabled {
                HStack {
                    Text("Up to")
                        .foregroundColor(.secondary)
                    Spacer()
                    Text("$\(Int(depositMax))")
                        .fontWeight(.semibold)
                }
                Slider(value: $depositMax, in: depositRange, step: 50)
            }
        } header: {
            Text("Deposit")
        }
    }

    // MARK: - Actions

    private func resetFilters() {
        priceEnabled = false
        yearEnabled = false
        depositEnabled = false
        draft = .empty
        viewModel.filters = .empty
        Task { await viewModel.fetchListings() }
        dismiss()
    }

    private func applyFilters() {
        draft.priceMaxWeekly = priceEnabled ? priceMax : nil
        draft.minYear = yearEnabled ? Int(minYear) : nil
        draft.maxDeposit = depositEnabled ? depositMax : nil
        viewModel.filters = draft
        Task { await viewModel.fetchListings() }
        dismiss()
    }
}
